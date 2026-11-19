package remote

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// probe builds a jobsProbe answer: live tmux sessions, record lines, and the
// bare exit files a pre-record footprint leaves. Sessions are named as tmux
// has them (rig-<job>) and emitted as the probe's own sed leaves them, so a
// test cannot pass the parser something the instance would never send.
func probe(sessions []string, record []string, exits []string) string {
	var b strings.Builder
	for _, s := range sessions {
		b.WriteString("SESSION " + strings.TrimPrefix(s, "rig-") + "\n")
	}
	b.WriteString("--RECORD--\n")
	for _, r := range record {
		b.WriteString(r + "\n")
	}
	b.WriteString("--LEGACY--\n")
	for _, e := range exits {
		b.WriteString("EXIT " + e + "\n")
	}
	return b.String()
}

func start(name string, run int64, argv string) string {
	return `{"event":"start","name":"` + name + `","run":` + itoa(run) + `,"at":` + itoa(run) +
		`,"workdir":"","argv":"` + argv + `"}`
}

func exit(name string, run, at int64, code int) string {
	return `{"event":"exit","name":"` + name + `","run":` + itoa(run) + `,"at":` + itoa(at) +
		`,"code":` + itoa(int64(code)) + `}`
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func find(t *testing.T, jobs []Job, name string, run int64) Job {
	t.Helper()
	for _, j := range jobs {
		if j.Name == name && j.Run == run {
			return j
		}
	}
	t.Fatalf("no run %d of %s in %+v", run, name, jobs)
	return Job{}
}

// The whole point of the record: a name can have more than one run, and
// retrying a job no longer erases the run being retried.
func TestParseJobsKeepsEveryRunOfAName(t *testing.T) {
	out := probe(
		[]string{"rig-fit"},
		[]string{
			start("fit", 1000, "bash train.sh 64"),
			exit("fit", 1000, 1600, 1),
			start("fit", 2000, "bash train.sh 32"),
		},
		nil)

	jobs := ParseJobs(out)
	if len(jobs) != 2 {
		t.Fatalf("want both runs, got %+v", jobs)
	}
	// newest first
	if jobs[0].Run != 2000 || jobs[1].Run != 1000 {
		t.Errorf("runs out of order: %d then %d", jobs[0].Run, jobs[1].Run)
	}
	if !jobs[0].Running {
		t.Errorf("the live run reads as finished: %+v", jobs[0])
	}
	if jobs[0].Command != "bash train.sh 32" {
		t.Errorf("command = %q", jobs[0].Command)
	}
	old := jobs[1]
	if old.Running || old.ExitCode != 1 {
		t.Errorf("the failed run should be finished with code 1: %+v", old)
	}
	if d, ok := old.Took(time.Now()); !ok || d != 600*time.Second {
		t.Errorf("took = %v (known=%v), want 10m", d, ok)
	}
}

// tmux is the authority on "alive", not the record: an instance stopped
// mid-run leaves a start with no exit, and that run is over.
func TestParseJobsCallsAnUnfinishedRunOfADeadJobUnknown(t *testing.T) {
	out := probe(nil, []string{start("fit", 1000, "bash train.sh")}, nil)

	j := find(t, ParseJobs(out), "fit", 1000)
	if j.Running {
		t.Errorf("no tmux session, so the run is not running: %+v", j)
	}
	if j.ExitCode != ExitUnknown {
		t.Errorf("exit code = %d, want ExitUnknown", j.ExitCode)
	}
	if _, ok := j.Took(time.Now()); ok {
		t.Error("a run that was never seen to finish has no duration")
	}
}

// An older run of a live name is finished even though tmux has a session of
// that name: the session belongs to the newest run.
func TestParseJobsDoesNotCallAnOldRunRunningBecauseTheNameIsLive(t *testing.T) {
	out := probe(
		[]string{"rig-fit"},
		[]string{
			start("fit", 1000, "old"),
			start("fit", 2000, "new"),
		},
		nil)

	jobs := ParseJobs(out)
	if !find(t, jobs, "fit", 2000).Running {
		t.Error("the newest run should be the live one")
	}
	if find(t, jobs, "fit", 1000).Running {
		t.Error("the older run must not claim the session")
	}
}

// An instance adopted before the record existed still has to list its jobs.
func TestParseJobsFallsBackToExitFilesWithNoRecord(t *testing.T) {
	out := probe([]string{"rig-serve"}, nil, []string{"prep 0", "eval 2"})

	jobs := ParseJobs(out)
	if len(jobs) != 3 {
		t.Fatalf("want serve, prep and eval, got %+v", jobs)
	}
	for _, tc := range []struct {
		name string
		run  bool
		code int
	}{{"serve", true, 0}, {"prep", false, 0}, {"eval", false, 2}} {
		var got Job
		for _, j := range jobs {
			if j.Name == tc.name {
				got = j
			}
		}
		if got.Name == "" {
			t.Fatalf("%s missing from %+v", tc.name, jobs)
		}
		if got.Running != tc.run || (!tc.run && got.ExitCode != tc.code) {
			t.Errorf("%s = %+v, want running=%v code=%d", tc.name, got, tc.run, tc.code)
		}
		if got.Recorded() {
			t.Errorf("%s claims to be recorded", tc.name)
		}
	}
}

// The record wins over the exit file for a name in both: the exit file is
// only ever the current run, which the record already has.
func TestParseJobsPrefersTheRecordOverTheExitFile(t *testing.T) {
	out := probe(nil,
		[]string{start("fit", 1000, "bash train.sh"), exit("fit", 1000, 1600, 0)},
		[]string{"fit 0"})

	jobs := ParseJobs(out)
	if len(jobs) != 1 {
		t.Fatalf("the exit file duplicated the recorded run: %+v", jobs)
	}
	if !jobs[0].Recorded() {
		t.Errorf("kept the unrecorded row: %+v", jobs[0])
	}
}

// The record is appended by a shell on a box that can be killed mid-write.
func TestParseJobsSkipsALineItCannotRead(t *testing.T) {
	out := probe(nil,
		[]string{
			`{"event":"start","name":"fit","run":10`, // truncated
			``,
			`not json at all`,
			start("fit", 2000, "bash train.sh"),
			exit("fit", 2000, 2100, 0),
		},
		nil)

	jobs := ParseJobs(out)
	if len(jobs) != 1 || jobs[0].Run != 2000 {
		t.Fatalf("a torn line took the listing with it: %+v", jobs)
	}
}

func TestParseJobsOfAnEmptyInstance(t *testing.T) {
	if jobs := ParseJobs(probe(nil, nil, nil)); len(jobs) != 0 {
		t.Errorf("want no jobs, got %+v", jobs)
	}
}

func TestLatestKeepsOneRunPerName(t *testing.T) {
	out := probe([]string{"rig-fit"}, []string{
		start("fit", 1000, "old"), exit("fit", 1000, 1100, 1),
		start("fit", 2000, "new"),
		start("prep", 1500, "prep"), exit("prep", 1500, 1550, 0),
	}, nil)

	got := Latest(ParseJobs(out))
	if len(got) != 2 {
		t.Fatalf("want one row per name, got %+v", got)
	}
	if got[0].Name != "fit" || got[0].Run != 2000 {
		t.Errorf("kept the wrong run of fit: %+v", got[0])
	}
}

// The wrapper is the only place a run gets written down, so its quoting has
// to survive a command holding the quote that ends a shell word.
func TestJobWrapperQuotesACommandWithQuotesInIt(t *testing.T) {
	got := RenderJobWrapper("fit-7b", "/mnt/rig-data", `bash train.sh 700 32 "A D E" 'x'`)

	for _, want := range []string{
		`NAME=fit-7b`,
		`"$HOME/.rig/jobs/$NAME.sh" >> "$LOG" 2>&1`,
		`ln -sf "$LOG" "$HOME/.rig/logs/$NAME.log"`,
		`echo "$code" > "$HOME/.rig/logs/$NAME.exit"`,
		`"event":"start"`,
		`"event":"exit"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("wrapper missing %q:\n%s", want, got)
		}
	}
	// the argv reaches the record as one JSON string, shell-quoted whole
	if !strings.Contains(got, `'"bash train.sh 700 32 \"A D E\" '\''x'\''"'`) {
		t.Errorf("argv is not safely quoted:\n%s", got)
	}
	if strings.Count(got, "printf") != 2 {
		t.Errorf("want exactly a start and an exit record:\n%s", got)
	}
}

// A run's log is its own file; the symlink is what the waiter and rig logs
// read, which is why an earlier run's log now survives the next start.
func TestJobLogPathIsPerRun(t *testing.T) {
	if got := (Job{Name: "fit", Run: 1757}).LogPath(); got != "~/.rig/logs/fit.1757.log" {
		t.Errorf("LogPath = %q", got)
	}
	if got := (Job{Name: "fit"}).LogPath(); got != "~/.rig/logs/fit.log" {
		t.Errorf("an unrecorded job has only the one log: %q", got)
	}
}

func TestListJobsAsksForEverythingInOneRoundTrip(t *testing.T) {
	r := &fakeRunner{results: []fakeResult{{
		out: probe([]string{"rig-fit"}, []string{start("fit", 1000, "x")}, nil),
	}}}
	jobs, err := listJobs(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || !jobs[0].Running {
		t.Errorf("jobs = %+v", jobs)
	}
	if len(r.cmds) != 1 {
		t.Fatalf("want one round trip, got %d: %v", len(r.cmds), r.cmds)
	}
	for _, want := range []string{"tmux ls", "jobs.jsonl", ".exit"} {
		if !strings.Contains(r.cmds[0], want) {
			t.Errorf("probe does not ask for %q: %s", want, r.cmds[0])
		}
	}
}

// The wrapper is shell, and shell is only really tested by running it. This
// drives the real script under bash in a temp HOME — no instance, no ssh —
// because everything the record promises (a run written down, a log kept
// per run, the symlink moved) is the script's doing, not Go's.
func runWrapper(t *testing.T, home, name, command string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	for _, d := range []string{".rig/logs", ".rig/jobs"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	job := filepath.Join(home, ".rig/jobs", name+".sh")
	if err := os.WriteFile(job, []byte("#!/bin/bash\n"+command+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(home, ".rig/jobs", name+".run.sh")
	if err := os.WriteFile(wrapper, []byte(RenderJobWrapper(name, "", command)), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", wrapper)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, _ := cmd.CombinedOutput() // a failing job is a valid case here
	if testing.Verbose() {
		t.Logf("wrapper output: %s", out)
	}
}

func TestWrapperRecordsARunAndKeepsThePreviousLog(t *testing.T) {
	home := t.TempDir()
	runWrapper(t, home, "fit", `echo "first run"; exit 3`)
	runWrapper(t, home, "fit", `echo "second run"`)

	rec, err := os.ReadFile(filepath.Join(home, ".rig/jobs.jsonl"))
	if err != nil {
		t.Fatalf("the wrapper wrote no record: %v", err)
	}
	jobs := ParseJobs(probe(nil, strings.Split(strings.TrimSpace(string(rec)), "\n"), nil))
	if len(jobs) != 2 {
		t.Fatalf("want two runs on the record, got %+v\n%s", jobs, rec)
	}
	newest, oldest := jobs[0], jobs[1]
	if newest.Run == oldest.Run {
		t.Errorf("both runs share the id %d, so they share a log", newest.Run)
	}
	if newest.ExitCode != 0 || oldest.ExitCode != 3 {
		t.Errorf("exit codes = %d and %d, want 0 and 3", newest.ExitCode, oldest.ExitCode)
	}
	if newest.Command != `echo "second run"` {
		t.Errorf("command = %q", newest.Command)
	}
	// the failed run's log has to survive the retry — the whole reason the
	// record exists
	old, err := os.ReadFile(filepath.Join(home, ".rig/logs", "fit."+itoa(oldest.Run)+".log"))
	if err != nil || !strings.Contains(string(old), "first run") {
		t.Errorf("the first run's log did not survive: %v %q", err, old)
	}
	// <name>.log is the current run, which is what rig logs and the waiter read
	cur, err := os.ReadFile(filepath.Join(home, ".rig/logs", "fit.log"))
	if err != nil || !strings.Contains(string(cur), "second run") {
		t.Errorf("fit.log is not the newest run: %v %q", err, cur)
	}
	// and the exit file the waiter polls still carries the latest code
	code, err := os.ReadFile(filepath.Join(home, ".rig/logs", "fit.exit"))
	if err != nil || strings.TrimSpace(string(code)) != "0" {
		t.Errorf("exit file = %q (%v), want 0", code, err)
	}
}

// A command carrying the quote that ends a shell word must reach the job
// intact and be recorded verbatim.
func TestWrapperSurvivesAwkwardQuoting(t *testing.T) {
	home := t.TempDir()
	command := `echo 'single' "double" $HOME`
	runWrapper(t, home, "quoting", command)

	rec, err := os.ReadFile(filepath.Join(home, ".rig/jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := ParseJobs(probe(nil, strings.Split(strings.TrimSpace(string(rec)), "\n"), nil))
	if len(jobs) != 1 {
		t.Fatalf("record = %s", rec)
	}
	if jobs[0].Command != command {
		t.Errorf("recorded command = %q, want %q", jobs[0].Command, command)
	}
	if jobs[0].ExitCode != 0 {
		t.Errorf("the job did not run cleanly: %+v\n%s", jobs[0], rec)
	}
}
