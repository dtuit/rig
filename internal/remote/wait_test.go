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
		cmd := exec.Command("sh", "-c", waitScript(name, time.Second, time.Second))
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
	if state, code := parseWaitOutput(run("train")); state != "EXIT" || code != 7 {
		t.Errorf("finished job: got %s %d, want EXIT 7", state, code)
	}
	if state, _ := parseWaitOutput(run("ghost")); state != "NOJOB" {
		t.Errorf("unknown job: got %s, want NOJOB", state)
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
