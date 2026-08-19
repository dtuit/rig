package remote

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Runner executes a command on an instance; sshx.Target satisfies it.
type Runner interface {
	Run(cmd string) (string, error)
}

// ErrWaitTimeout is returned when WaitConfig.Timeout expires before the job
// finishes. The CLI maps it to exit code 124.
var ErrWaitTimeout = errors.New("timed out waiting for job")

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
	if !nameRe.MatchString(name) {
		return 0, fmt.Errorf("invalid job name %q", name)
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
				return 0, ErrWaitTimeout
			}
			if remaining < chunk {
				chunk = remaining
			}
		}
		out, err := r.Run(waitScript(name, cfg.Poll, chunk))
		if err != nil {
			failures++
			if failures >= maxWaitFailures {
				return 0, fmt.Errorf("waiting for job %q: ssh failed %d times in a row: %v: %s",
					name, failures, err, strings.TrimSpace(out))
			}
			cfg.sleep(waitRetryPause)
			continue
		}
		failures = 0
		state, code := parseWaitOutput(out)
		switch state {
		case "EXIT":
			return code, nil
		case "NOJOB":
			return 0, fmt.Errorf("no job named %q (rig ps to list)", name)
		case "TIMEOUT":
			continue // next iteration re-checks the deadline
		default:
			return 0, fmt.Errorf("unexpected wait output %q", strings.TrimSpace(out))
		}
	}
}

const (
	maxWaitFailures = 5
	waitRetryPause  = 10 * time.Second
)

// waitScript polls for the job's exit file server-side. It always exits 0
// and reports through stdout — EXIT <code>, NOJOB, or TIMEOUT — so ssh's own
// exit status stays reserved for transport failure (ADR-0001). POSIX sh: the
// remote login shell is not guaranteed to be bash.
func waitScript(name string, poll, chunk time.Duration) string {
	exitFile := fmt.Sprintf(`"$HOME"/.rig/logs/%s.exit`, name)
	pollSecs := int(poll / time.Second)
	if pollSecs < 1 {
		pollSecs = 1
	}
	chunkSecs := int(chunk / time.Second)
	if chunkSecs < 1 {
		chunkSecs = 1
	}
	return fmt.Sprintf(
		`end=$(( $(date +%%s) + %[1]d )); while :; do `+
			`if [ -f %[2]s ]; then echo "EXIT $(cat %[2]s)"; exit 0; fi; `+
			`if ! tmux has-session -t rig-%[3]s 2>/dev/null; then sleep 1; `+
			`if [ -f %[2]s ]; then echo "EXIT $(cat %[2]s)"; else echo NOJOB; fi; exit 0; fi; `+
			`if [ "$(date +%%s)" -ge "$end" ]; then echo TIMEOUT; exit 0; fi; `+
			`sleep %[4]d; done`,
		chunkSecs, exitFile, name, pollSecs)
}

// parseWaitOutput finds the result token in the poll output, scanning from
// the last line so stray remote noise (motd, warnings) is ignored.
func parseWaitOutput(out string) (state string, code int) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		f := strings.Fields(lines[i])
		switch {
		case len(f) == 2 && f[0] == "EXIT":
			if n, err := strconv.Atoi(f[1]); err == nil {
				return "EXIT", n
			}
		case len(f) == 1 && (f[0] == "NOJOB" || f[0] == "TIMEOUT"):
			return f[0], 0
		}
	}
	return "", 0
}
