package remote

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner scripts successive Run results and records the commands sent.
type fakeRunner struct {
	results []fakeResult
	cmds    []string
}

type fakeResult struct {
	out   string
	err   error
	delay time.Duration
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	if len(f.results) == 0 {
		return "", nil
	}
	r := f.results[0]
	f.results = f.results[1:]
	time.Sleep(r.delay)
	return r.out, r.err
}

// TestWaitScriptUnderSh runs the generated poll script in a real POSIX shell
// against a fake $HOME, covering the shell syntax the fake runner can't.
func TestWaitScriptUnderSh(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".rig", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(name string) string {
		cmd := exec.Command("sh", "-c", waitScript(name, "", time.Second, time.Second))
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("script failed: %v", err)
		}
		return string(out)
	}

	if err := os.WriteFile(filepath.Join(home, ".rig", "logs", "train.exit"), []byte("7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := parseWaitOutput(run("train")); res.state != "EXIT" || res.code != 7 {
		t.Errorf("finished job: got %s %d, want EXIT 7", res.state, res.code)
	}
	if res := parseWaitOutput(run("ghost")); res.state != "NOJOB" {
		t.Errorf("unknown job: got %s, want NOJOB", res.state)
	}
}

// TestWaitScriptForPatternUnderSh covers the --for branch in a real shell: a
// matching log line wins over the exit file; a finished job with no match
// reports EXIT (issue 12).
func TestWaitScriptForPatternUnderSh(t *testing.T) {
	home := t.TempDir()
	logs := filepath.Join(home, ".rig", "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(name, pattern string) waitResult {
		cmd := exec.Command("sh", "-c", waitScript(name, pattern, time.Second, time.Second))
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("script failed: %v", err)
		}
		return parseWaitOutput(string(out))
	}
	log := "stage: fetch\nit's step 10/500 loss=0.42\nstage: train\n"
	if err := os.WriteFile(filepath.Join(logs, "train.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "train.exit"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := run("train", "^stage: (train|bench)$"); res.state != "MATCH" || res.line != "stage: train" {
		t.Errorf("match: got %+v, want MATCH stage: train", res)
	}
	if res := run("train", "^it's step [0-9]+/500 "); res.state != "MATCH" || !strings.HasPrefix(res.line, "it's step") {
		t.Errorf("pattern with quote/regex chars: got %+v", res)
	}
	if res := run("train", "never appears"); res.state != "EXIT" || res.code != 0 {
		t.Errorf("finished without match: got %+v, want EXIT 0", res)
	}
	// a line with backslashes and a NUL byte elsewhere in the log: dash's echo
	// would rewrite \t and grep without -a would say "Binary file matches"
	odd := "path C:\\new\\tmp done\nbinary \x00 bytes here\n"
	if err := os.WriteFile(filepath.Join(logs, "train.log"), []byte(odd), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := run("train", "^path "); res.state != "MATCH" || res.line != "path C:\\new\\tmp done" {
		t.Errorf("backslashes in the matched line: got %+v", res)
	}
	if res := run("train", "bytes here"); res.state != "MATCH" || !strings.HasSuffix(res.line, "bytes here") {
		t.Errorf("binary log: got %+v, want a MATCH not 'Binary file'", res)
	}
}

// TestWaitScriptTargetsExactSession is issue 11: has-session -t rig-x would
// also match rig-x2 by prefix; =rig-x forces an exact match.
func TestWaitScriptTargetsExactSession(t *testing.T) {
	s := waitScript("pipeline", "", time.Second, time.Second)
	if !strings.Contains(s, "has-session -t =rig-pipeline ") {
		t.Errorf("script lacks exact-match tmux target: %s", s)
	}
}

func TestWaitScriptQuotesPattern(t *testing.T) {
	s := waitScript("train", "loss=[0-9.]+ it's", time.Second, time.Second)
	if !strings.Contains(s, `grep -a -m1 -E -e 'loss=[0-9.]+ it'\''s' "$HOME"/.rig/logs/train.log`) || !strings.Contains(s, `printf 'MATCH %s\n' "$l"`) {
		t.Errorf("script lacks the quoted grep: %s", s)
	}
	if strings.Contains(waitScript("train", "", time.Second, time.Second), "grep") {
		t.Error("script without a pattern should not grep")
	}
}

func TestParseWaitOutput(t *testing.T) {
	cases := []struct {
		in   string
		want waitResult
	}{
		{"EXIT 7\n", waitResult{state: "EXIT", code: 7}},
		{"Welcome to Ubuntu\n\nEXIT 0\n", waitResult{state: "EXIT"}},
		{"NOJOB\n", waitResult{state: "NOJOB"}},
		{"TIMEOUT\r\n", waitResult{state: "TIMEOUT"}},
		{"MATCH stage: train\n", waitResult{state: "MATCH", line: "stage: train"}},
		{"motd noise\nMATCH step 10/500  loss=0.42 EXIT 3\n", waitResult{state: "MATCH", line: "step 10/500  loss=0.42 EXIT 3"}},
		{"MATCH\n", waitResult{state: "MATCH"}},
		{"garbage\n", waitResult{}},
	}
	for _, c := range cases {
		if got := parseWaitOutput(c.in); got != c.want {
			t.Errorf("parseWaitOutput(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestWaitForLineReturnsMatch(t *testing.T) {
	r := &fakeRunner{results: []fakeResult{{out: "TIMEOUT\n"}, {out: "MATCH stage: train\n"}}}
	line, err := WaitForLine(r, "train", "^stage: train$", WaitConfig{})
	if err != nil || line != "stage: train" {
		t.Fatalf("WaitForLine = %q, %v; want stage: train, nil", line, err)
	}
	if len(r.cmds) != 2 || !strings.Contains(r.cmds[0], "'^stage: train$'") {
		t.Errorf("polls = %q, want 2 carrying the quoted pattern", r.cmds)
	}
}

func TestWaitForLineJobEndedIsNoMatch(t *testing.T) {
	r := &fakeRunner{results: []fakeResult{{out: "EXIT 3\n"}}}
	_, err := WaitForLine(r, "train", "never", WaitConfig{})
	if !errors.Is(err, ErrNoMatch) || !strings.Contains(err.Error(), "exited 3") {
		t.Fatalf("err = %v, want ErrNoMatch naming the exit code", err)
	}
	r = &fakeRunner{results: []fakeResult{{out: "TIMEOUT\n", delay: 30 * time.Millisecond}}}
	if _, err := WaitForLine(r, "train", "never", WaitConfig{Timeout: 10 * time.Millisecond}); !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("err = %v, want ErrWaitTimeout", err)
	}
}

func TestWaitJobTimesOut(t *testing.T) {
	r := &fakeRunner{results: []fakeResult{{out: "TIMEOUT\n", delay: 30 * time.Millisecond}}}
	_, err := WaitJob(r, "train", WaitConfig{Timeout: 10 * time.Millisecond})
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("err = %v, want ErrWaitTimeout", err)
	}
}

func TestWaitJobRetriesTransportFailure(t *testing.T) {
	var paused int
	r := &fakeRunner{results: []fakeResult{
		{out: "", err: errors.New("ssh: connect refused")},
		{out: "EXIT 7\n"},
	}}
	code, err := WaitJob(r, "train", WaitConfig{sleep: func(time.Duration) { paused++ }})
	if err != nil || code != 7 {
		t.Fatalf("WaitJob = %d, %v; want 7, nil", code, err)
	}
	if paused != 1 {
		t.Errorf("retry pauses = %d, want 1", paused)
	}
}

func TestWaitJobGivesUpAfterRepeatedTransportFailure(t *testing.T) {
	r := &fakeRunner{}
	for i := 0; i < 10; i++ {
		r.results = append(r.results, fakeResult{err: errors.New("ssh: unreachable")})
	}
	_, err := WaitJob(r, "train", WaitConfig{sleep: func(time.Duration) {}})
	if err == nil || errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("err = %v, want transport error", err)
	}
	if len(r.cmds) >= 10 {
		t.Errorf("polls = %d, want bounded retries", len(r.cmds))
	}
}

func TestWaitJobResumesAfterChunkTimeout(t *testing.T) {
	r := &fakeRunner{results: []fakeResult{{out: "TIMEOUT\n"}, {out: "EXIT 0\n"}}}
	code, err := WaitJob(r, "train", WaitConfig{})
	if err != nil || code != 0 {
		t.Fatalf("WaitJob = %d, %v; want 0, nil", code, err)
	}
	if len(r.cmds) != 2 {
		t.Fatalf("remote polls = %d, want 2", len(r.cmds))
	}
	for _, cmd := range r.cmds {
		if !strings.Contains(cmd, "train") {
			t.Errorf("poll command does not reference the job: %q", cmd)
		}
	}
}

func TestWaitJobUnknownJobErrors(t *testing.T) {
	r := &fakeRunner{results: []fakeResult{{out: "NOJOB\n"}}}
	_, err := WaitJob(r, "ghost", WaitConfig{})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v, want error naming the job", err)
	}
}

func TestWaitJobPropagatesExitCode(t *testing.T) {
	for _, want := range []int{0, 3} {
		r := &fakeRunner{results: []fakeResult{{out: "EXIT 3\n"}}}
		if want == 0 {
			r.results = []fakeResult{{out: "EXIT 0\n"}}
		}
		code, err := WaitJob(r, "train", WaitConfig{})
		if err != nil {
			t.Fatalf("WaitJob: %v", err)
		}
		if code != want {
			t.Errorf("code = %d, want %d", code, want)
		}
	}
}
