package remote

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dtuit/rig/internal/shellq"
)

// Runner executes a command on an instance; sshx.Target satisfies it.
type Runner interface {
	Run(cmd string) (string, error)
}

// ErrWaitTimeout is returned when WaitConfig.Timeout expires before the job
// finishes. The CLI maps it to exit code 124.
var ErrWaitTimeout = errors.New("timed out waiting for job")

// ErrNoMatch is returned by WaitForLine when the job ends before any log line
// matches the pattern. The CLI maps it to exit code 125 (ADR-0005).
var ErrNoMatch = errors.New("job ended before a log line matched")

type WaitConfig struct {
	Timeout time.Duration // give up after this long; 0 waits forever
	Poll    time.Duration // server-side poll interval (default 5s)
	Chunk   time.Duration // max length of one remote poll session (default 5m)

	sleep func(time.Duration) // test seam for the transport-retry pause
}

// WaitJob blocks until the named job leaves the running state and returns its
// exit code. Polling runs server-side in bounded chunks (ADR-0001): each
// remote session watches for the job's exit file for up to Chunk, printing
// EXIT <code>, NOJOB, or TIMEOUT on stdout; transient ssh failures are
// retried.
func WaitJob(r Runner, name string, cfg WaitConfig) (int, error) {
	res, err := waitLoop(r, name, "", cfg)
	if err != nil {
		return 0, err
	}
	return res.code, nil
}

// WaitForLine blocks until a line of the job's log matches pattern (an
// extended regex, grep -E) and returns that line (issue 12). The job ending
// first is ErrNoMatch; the poll otherwise behaves exactly like WaitJob.
func WaitForLine(r Runner, name, pattern string, cfg WaitConfig) (string, error) {
	if pattern == "" {
		return "", fmt.Errorf("empty log pattern")
	}
	res, err := waitLoop(r, name, pattern, cfg)
	if err != nil {
		return "", err
	}
	if res.state != "MATCH" {
		return "", fmt.Errorf("%w (job %q exited %d)", ErrNoMatch, name, res.code)
	}
	return res.line, nil
}

// waitResult is one parsed poll outcome: state EXIT (code), MATCH (line),
// NOJOB, or TIMEOUT.
type waitResult struct {
	state string
	code  int
	line  string
}

// waitLoop drives the chunked remote poll until it yields EXIT or MATCH.
func waitLoop(r Runner, name, pattern string, cfg WaitConfig) (waitResult, error) {
	if !nameRe.MatchString(name) {
		return waitResult{}, fmt.Errorf("invalid job name %q", name)
	}
	if cfg.Poll <= 0 {
		cfg.Poll = 5 * time.Second
	}
	if cfg.Chunk <= 0 {
		cfg.Chunk = 5 * time.Minute
	}
	if cfg.sleep == nil {
		cfg.sleep = time.Sleep
	}
	var deadline time.Time
	if cfg.Timeout > 0 {
		deadline = time.Now().Add(cfg.Timeout)
	}
	failures := 0
	for {
		chunk := cfg.Chunk
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return waitResult{}, ErrWaitTimeout
			}
			if remaining < chunk {
				chunk = remaining
			}
		}
		out, err := r.Run(waitScript(name, pattern, cfg.Poll, chunk))
		if err != nil {
			failures++
			if failures >= maxWaitFailures {
				return waitResult{}, fmt.Errorf("waiting for job %q: ssh failed %d times in a row: %v: %s",
					name, failures, err, strings.TrimSpace(out))
			}
			cfg.sleep(waitRetryPause)
			continue
		}
		failures = 0
		res := parseWaitOutput(out)
		switch res.state {
		case "EXIT", "MATCH":
			return res, nil
		case "NOJOB":
			return waitResult{}, fmt.Errorf("no job named %q (rig ps to list)", name)
		case "TIMEOUT":
			continue // next iteration re-checks the deadline
		default:
			return waitResult{}, fmt.Errorf("unexpected wait output %q", strings.TrimSpace(out))
		}
	}
}

const (
	maxWaitFailures = 5
	waitRetryPause  = 10 * time.Second
)

// waitScript polls for the job's exit file server-side. It always exits 0
// and reports through stdout — EXIT <code>, NOJOB, TIMEOUT, or (with a
// pattern) MATCH <line> — so ssh's own exit status stays reserved for
// transport failure (ADR-0001). The tmux target uses the exact-match form
// =rig-<name> so a sibling like rig-<name>2 is not mistaken for the job
// (issue 11). POSIX sh: the remote login shell is not guaranteed to be bash.
func waitScript(name, pattern string, poll, chunk time.Duration) string {
	exitFile := fmt.Sprintf(`"$HOME"/.rig/logs/%s.exit`, name)
	logFile := fmt.Sprintf(`"$HOME"/.rig/logs/%s.log`, name)
	pollSecs := int(poll / time.Second)
	if pollSecs < 1 {
		pollSecs = 1
	}
	chunkSecs := int(chunk / time.Second)
	if chunkSecs < 1 {
		chunkSecs = 1
	}
	// match is re-run right before reporting EXIT/NOJOB so a line written in
	// the job's last moments is not lost to the poll interval.
	// printf, not echo: dash's echo interprets backslash escapes, so a log line
	// holding \t or a Windows path would come back altered or split. -a keeps
	// grep from answering "Binary file matches" on a log with progress bytes.
	match := ""
	if pattern != "" {
		match = fmt.Sprintf(`l=$(grep -a -m1 -E -e %s %s 2>/dev/null) && { printf 'MATCH %%s\n' "$l"; exit 0; }; `,
			shellq.Quote(pattern), logFile)
	}
	return fmt.Sprintf(
		`end=$(( $(date +%%s) + %[1]d )); while :; do %[5]s`+
			`if [ -f %[2]s ]; then %[5]secho "EXIT $(cat %[2]s)"; exit 0; fi; `+
			`if ! tmux has-session -t =rig-%[3]s 2>/dev/null; then sleep 1; %[5]s`+
			`if [ -f %[2]s ]; then echo "EXIT $(cat %[2]s)"; else echo NOJOB; fi; exit 0; fi; `+
			`if [ "$(date +%%s)" -ge "$end" ]; then echo TIMEOUT; exit 0; fi; `+
			`sleep %[4]d; done`,
		chunkSecs, exitFile, name, pollSecs, match)
}

// parseWaitOutput finds the result token in the poll output, scanning from
// the last line so stray remote noise (motd, warnings) is ignored. A MATCH
// line carries the matched log line verbatim after one space.
func parseWaitOutput(out string) waitResult {
	lines := strings.Split(strings.TrimRight(out, "\r\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(lines[i], "\r")
		if line == "MATCH" || strings.HasPrefix(line, "MATCH ") {
			return waitResult{state: "MATCH", line: strings.TrimPrefix(strings.TrimPrefix(line, "MATCH"), " ")}
		}
		f := strings.Fields(line)
		switch {
		case len(f) == 2 && f[0] == "EXIT":
			if n, err := strconv.Atoi(f[1]); err == nil {
				return waitResult{state: "EXIT", code: n}
			}
		case len(f) == 1 && (f[0] == "NOJOB" || f[0] == "TIMEOUT"):
			return waitResult{state: f[0]}
		}
	}
	return waitResult{}
}
