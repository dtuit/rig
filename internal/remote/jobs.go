package remote

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dtuit/rig/internal/shellq"
	"github.com/dtuit/rig/internal/sshx"
)

// recordPath is the append-only record of every run of every job on the
// instance. It exists because the files around it cannot answer "what
// happened last time": a job's log and exit file are named after the job,
// so starting it again overwrites them, and the run being overwritten is
// exactly the failed one whose evidence is wanted. The record is written by
// the wrapper (RenderJobWrapper), never by rig over ssh, so a run is
// recorded whether or not anyone is connected when it ends.
const recordPath = "$HOME/.rig/jobs.jsonl"

// ExitUnknown is the exit code of a run the record never saw finish: the
// instance stopped, or was stopped by its own watchdog, while the job was
// running. It is not a job that exited -1.
const ExitUnknown = -1

// Job is one run of a named job. Name and Run identify it — the same name
// can have many runs, newest first out of ListJobs. Running is what tmux
// says, not what the record says, so a run whose wrapper died with the
// instance reads as finished with ExitUnknown rather than running forever.
// The fields after ExitCode come from the record and are empty for a job
// started before the instance had one (see ListJobs).
type Job struct {
	Name     string    `json:"name"`
	Running  bool      `json:"running"`
	ExitCode int       `json:"exit_code"` // valid when !Running; ExitUnknown if never recorded
	Run      int64     `json:"run,omitempty"`
	Started  time.Time `json:"started,omitempty"`
	Finished time.Time `json:"finished,omitempty"`
	Command  string    `json:"command,omitempty"`
	Workdir  string    `json:"workdir,omitempty"`
}

// Recorded reports whether this run came from the record rather than from
// the bare log and exit files a pre-record footprint leaves. A caller that
// shows history has to say which rows cannot have any.
func (j Job) Recorded() bool { return j.Run != 0 }

// Took is how long the run lasted, and whether that is known: a running job
// is measured to now, and a run the record never saw finish has no duration.
func (j Job) Took(now time.Time) (time.Duration, bool) {
	if j.Started.IsZero() {
		return 0, false
	}
	switch {
	case j.Running:
		return now.Sub(j.Started), true
	case j.Finished.IsZero():
		return 0, false
	default:
		return j.Finished.Sub(j.Started), true
	}
}

// LogPath is where this run's output lives on the instance. Every run gets
// its own file so an earlier run's log survives the next start;
// <name>.log is a symlink to the newest, which is what `rig logs` and the
// waiter read.
func (j Job) LogPath() string {
	if j.Run == 0 {
		return fmt.Sprintf("~/.rig/logs/%s.log", j.Name)
	}
	return fmt.Sprintf("~/.rig/logs/%s.%d.log", j.Name, j.Run)
}

// RenderJobWrapper is ~/.rig/jobs/<name>.run.sh: the script tmux actually
// runs. It stamps the run, points <name>.log at this run's own log, records
// the start, runs the job script, then records the exit code — writing
// <name>.exit as well, which is what the waiter polls (ADR-0001).
//
// The wrapper exists rather than a longer tmux command line because the
// recording needs JSON inside shell inside a tmux argument inside an ssh
// command; sent as a file over stdin, none of those layers has to be
// escaped, and the quoting is testable without a shell.
func RenderJobWrapper(name, workdir, command string) string {
	// json.Marshal for the values, shellq.Quote for the shell: a command
	// like `echo 'hi'` holds the quote that would otherwise end the word.
	j := func(v string) string {
		b, _ := json.Marshal(v)
		return shellq.Quote(string(b))
	}
	return "#!/bin/bash\n" +
		"# rig job wrapper — written by rig, do not edit.\n" +
		"NAME=" + shellq.Quote(name) + "\n" +
		"REC=\"" + recordPath + "\"\n" +
		"AT=$(date +%s)\n" +
		"RUN=$AT\n" +
		// date has one-second resolution, so two starts inside one second
		// would share a run id and truncate each other's log. The id is
		// only ever an identifier, so bumping it is free; AT stays the
		// honest timestamp.
		"while [ -e \"$HOME/.rig/logs/$NAME.$RUN.log\" ]; do RUN=$((RUN+1)); done\n" +
		"LOG=\"$HOME/.rig/logs/$NAME.$RUN.log\"\n" +
		": > \"$LOG\"\n" +
		"ln -sf \"$LOG\" \"$HOME/.rig/logs/$NAME.log\"\n" +
		"printf '{\"event\":\"start\",\"name\":%s,\"run\":%s,\"at\":%s,\"workdir\":%s,\"argv\":%s}\\n' " +
		j(name) + " \"$RUN\" \"$AT\" " + j(workdir) + " " + j(command) + " >> \"$REC\"\n" +
		"\"$HOME/.rig/jobs/$NAME.sh\" >> \"$LOG\" 2>&1\n" +
		"code=$?\n" +
		"echo \"$code\" > \"$HOME/.rig/logs/$NAME.exit\"\n" +
		"printf '{\"event\":\"exit\",\"name\":%s,\"run\":%s,\"at\":%s,\"code\":%s}\\n' " +
		j(name) + " \"$RUN\" \"$(date +%s)\" \"$code\" >> \"$REC\"\n" +
		"exit \"$code\"\n"
}

// StartJob runs command detached in tmux session rig-<name>, through the
// wrapper that records the run. The job's own script keeps its old home and
// shape (~/.rig/jobs/<name>.sh, env files sourced, argv as typed), so what
// runs is unchanged; what is new is that the run is written down.
func StartJob(t sshx.Target, name, workdir, command string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("job name must match %s", nameRe)
	}
	script := "#!/bin/bash\n"
	script += "export PATH=\"$HOME/.local/bin:$PATH\"\n"
	if workdir != "" {
		script += fmt.Sprintf("cd %q || exit 1\n", workdir)
	}
	script += "set -a; for f in \"$HOME\"/.rig/env/*; do [ -f \"$f\" ] && . \"$f\"; done; set +a\n"
	script += command + "\n"
	if out, err := t.RunWithStdin(script,
		fmt.Sprintf("cat > ~/.rig/jobs/%s.sh && chmod +x ~/.rig/jobs/%s.sh", name, name)); err != nil {
		return fmt.Errorf("writing job script: %w: %s", err, out)
	}
	if out, err := t.RunWithStdin(RenderJobWrapper(name, workdir, command),
		fmt.Sprintf("cat > ~/.rig/jobs/%s.run.sh && chmod +x ~/.rig/jobs/%s.run.sh", name, name)); err != nil {
		return fmt.Errorf("writing job wrapper: %w: %s", err, out)
	}
	// The stale exit file goes before tmux starts, not inside the wrapper:
	// a waiter attached in between would otherwise read the previous run's
	// code as this one's.
	// Single quotes around the tmux argument, as the pre-record version had:
	// $HOME reaches tmux unexpanded and tmux's own sh expands it. Double
	// quotes with \$ would also work, but this spelling is the one that has
	// been running on instances.
	start := fmt.Sprintf(
		`rm -f ~/.rig/logs/%[1]s.exit && tmux new-session -d -s rig-%[1]s '"$HOME"/.rig/jobs/%[1]s.run.sh'`,
		name)
	if out, err := t.Run(start); err != nil {
		if strings.Contains(out, "duplicate session") {
			return fmt.Errorf("job %q is already running", name)
		}
		return fmt.Errorf("starting job: %w: %s", err, out)
	}
	return nil
}

// Snapshot is everything one ssh round trip tells a caller about an
// instance's footprint: when it is due to self-destruct, and what it has
// run. The two used to be separate calls — `rig status` reads the deadline,
// a job list reads the jobs — and at 0.8s each that is a round trip the
// dashboard cannot afford twice per instance per refresh.
type Snapshot struct {
	// Deadline is zero when the instance has none readable: no footprint,
	// or a container restart took ~/.rig with it.
	Deadline time.Time
	Jobs     []Job
}

// HasDeadline reports whether the watchdog has a deadline on this instance.
// No deadline means no self-destruct, which is worth showing as such rather
// than as a time.
func (s Snapshot) HasDeadline() bool { return !s.Deadline.IsZero() }

// snapshotProbe reads the deadline and the job sources in one command. The
// deadline comes first and is terminated by a marker, so a missing file is
// an empty section rather than a shifted parse.
const snapshotProbe = `cat "$HOME"/.rig/deadline 2>/dev/null; echo '--PROBE--'; ` + jobsProbe

// InstanceSnapshot asks one instance for its deadline and its jobs together.
func InstanceSnapshot(r Runner) (Snapshot, error) {
	out, err := r.Run(snapshotProbe)
	if err != nil {
		return Snapshot{}, fmt.Errorf("asking the instance: %w: %s", err, out)
	}
	return ParseSnapshot(out), nil
}

// ParseSnapshot splits a snapshotProbe answer. Pure, so the parse is tested
// without an instance; a deadline that will not parse is simply absent,
// because a dashboard must not fail over one unreadable file.
func ParseSnapshot(out string) Snapshot {
	head, rest, found := strings.Cut(out, "--PROBE--")
	if !found {
		return Snapshot{Jobs: ParseJobs(out)}
	}
	var s Snapshot
	if n, err := strconv.ParseInt(strings.TrimSpace(head), 10, 64); err == nil && n > 0 {
		s.Deadline = time.Unix(n, 0)
	}
	s.Jobs = ParseJobs(rest)
	return s
}

// JobLogTail is the last lines of a run's log, as text rather than as a
// stream: TailLog attaches this process's stdout, which a full-screen UI
// cannot allow, so the dashboard reads a snapshot and hands off to
// `rig logs -f` for real following.
func JobLogTail(t sshx.Target, j Job, lines int) ([]string, error) {
	if !nameRe.MatchString(j.Name) {
		return nil, fmt.Errorf("invalid job name %q", j.Name)
	}
	if lines <= 0 {
		lines = 200
	}
	out, err := t.Run(fmt.Sprintf("tail -n %d %s 2>/dev/null || true", lines, j.LogPath()))
	if err != nil {
		return nil, fmt.Errorf("reading the job log: %w: %s", err, out)
	}
	var kept []string
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		kept = append(kept, strings.TrimRight(l, "\r"))
	}
	if len(kept) == 1 && kept[0] == "" {
		return nil, nil
	}
	return kept, nil
}

// jobsProbe collects everything a job listing needs in one round trip: the
// live tmux sessions, the record, and the bare exit files an instance
// adopted before the record existed still has.
const jobsProbe = `tmux ls -F '#{session_name}' 2>/dev/null | grep '^rig-' | sed 's/^rig-/SESSION /'; ` +
	`echo '--RECORD--'; cat "$HOME"/.rig/jobs.jsonl 2>/dev/null; ` +
	`echo '--LEGACY--'; for f in "$HOME"/.rig/logs/*.exit; do [ -f "$f" ] || continue; ` +
	`echo "EXIT $(basename "$f" .exit) $(cat "$f")"; done; true`

// recordLine is one line of ~/.rig/jobs.jsonl.
type recordLine struct {
	Event   string `json:"event"`
	Name    string `json:"name"`
	Run     int64  `json:"run"`
	At      int64  `json:"at"`
	Code    int    `json:"code"`
	Argv    string `json:"argv"`
	Workdir string `json:"workdir"`
}

// ParseJobs turns a jobsProbe answer into runs, newest first. It is pure so
// the merge — which is the whole difficulty, three sources that disagree —
// can be tested without an instance.
//
// The sources are trusted in this order: tmux for whether a run is alive,
// the record for what a run was and when, and the exit files only for jobs
// the record has never heard of. A record line that will not parse is
// skipped rather than failing the listing: the file is appended to by a
// shell on a box that may have been killed mid-write.
func ParseJobs(out string) []Job {
	const (
		sessions = iota
		record
		legacy
	)
	section := sessions
	live := map[string]bool{}
	runs := map[string]*Job{} // name\x00run -> job
	var order []string
	exits := map[string]int{}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch strings.TrimSpace(line) {
		case "--RECORD--":
			section = record
			continue
		case "--LEGACY--":
			section = legacy
			continue
		}
		switch section {
		case sessions:
			if f := strings.Fields(line); len(f) == 2 && f[0] == "SESSION" {
				live[f[1]] = true
			}
		case record:
			if strings.TrimSpace(line) == "" {
				continue
			}
			var r recordLine
			if json.Unmarshal([]byte(line), &r) != nil || r.Name == "" || r.Run == 0 {
				continue
			}
			key := r.Name + "\x00" + strconv.FormatInt(r.Run, 10)
			j, ok := runs[key]
			if !ok {
				j = &Job{Name: r.Name, Run: r.Run, ExitCode: ExitUnknown}
				runs[key], order = j, append(order, key)
			}
			switch r.Event {
			case "start":
				j.Started = time.Unix(r.At, 0)
				j.Command, j.Workdir = r.Argv, r.Workdir
			case "exit":
				j.Finished, j.ExitCode = time.Unix(r.At, 0), r.Code
			}
		case legacy:
			if f := strings.Fields(line); len(f) == 3 && f[0] == "EXIT" {
				code, _ := strconv.Atoi(f[2])
				exits[f[1]] = code
			}
		}
	}

	// Newest run of each name first, so "is it alive" can be asked of the
	// newest alone: an older run of a live name is finished by definition.
	jobs := make([]Job, 0, len(order)+len(exits))
	for _, key := range order {
		jobs = append(jobs, *runs[key])
	}
	slices.SortStableFunc(jobs, func(a, b Job) int { return int(b.Run - a.Run) })
	newest := map[string]bool{}
	for i := range jobs {
		if !newest[jobs[i].Name] {
			newest[jobs[i].Name] = true
			jobs[i].Running = live[jobs[i].Name] && jobs[i].Finished.IsZero()
		}
		if jobs[i].Running {
			jobs[i].ExitCode = 0
		}
	}
	// Jobs the record never saw: an instance adopted before it existed, or
	// one whose record was lost with a container restart. The exit files and
	// tmux are all there is, which is exactly what rig used to show.
	for name, code := range exits {
		if newest[name] {
			continue
		}
		jobs = append(jobs, Job{Name: name, ExitCode: code})
		newest[name] = true
	}
	for name := range live {
		if !newest[name] {
			jobs = append(jobs, Job{Name: name, Running: true})
		}
	}
	// legacy rows carry no run stamp, so they order by name after the rest
	slices.SortStableFunc(jobs, func(a, b Job) int {
		if (a.Run == 0) != (b.Run == 0) {
			if a.Run == 0 {
				return 1
			}
			return -1
		}
		if a.Run != b.Run {
			return int(b.Run - a.Run)
		}
		return strings.Compare(a.Name, b.Name)
	})
	return jobs
}

// ListJobs returns every recorded run on the instance, newest first.
func ListJobs(t sshx.Target) ([]Job, error) {
	return listJobs(t)
}

func listJobs(r Runner) ([]Job, error) {
	out, err := r.Run(jobsProbe)
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %w: %s", err, out)
	}
	return ParseJobs(out), nil
}

// Latest keeps only the newest run of each name — what `rig ps` shows
// without --all, and what rig showed before there was any history.
func Latest(jobs []Job) []Job {
	seen := map[string]bool{}
	out := jobs[:0:0]
	for _, j := range jobs {
		if seen[j.Name] {
			continue
		}
		seen[j.Name] = true
		out = append(out, j)
	}
	return out
}

func TailLog(t sshx.Target, name string, follow bool, lines int) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid job name %q", name)
	}
	cmd := fmt.Sprintf("tail -n %d ~/.rig/logs/%s.log", lines, name)
	if follow {
		cmd += " -f"
	}
	return t.Stream(cmd)
}

// TailRunLog is TailLog for one run of a job rather than the current one.
func TailRunLog(t sshx.Target, j Job, follow bool, lines int) error {
	if !nameRe.MatchString(j.Name) {
		return fmt.Errorf("invalid job name %q", j.Name)
	}
	cmd := fmt.Sprintf("tail -n %d %s", lines, j.LogPath())
	if follow {
		cmd += " -f"
	}
	return t.Stream(cmd)
}

func StopJob(t sshx.Target, name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid job name %q", name)
	}
	// =rig-<name> is tmux's exact-match target; without it rig-<name> would
	// also match a prefix sibling like rig-<name>2 (issue 11).
	out, err := t.Run(fmt.Sprintf("tmux kill-session -t =rig-%s", name))
	if err != nil {
		return fmt.Errorf("stopping job %s: %v: %s", name, err, out)
	}
	return nil
}
