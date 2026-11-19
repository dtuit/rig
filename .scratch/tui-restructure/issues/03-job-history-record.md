# 03 — Job history needs an append-only record

**Status:** resolved

`remote.StartJob` truncates the log and deletes the exit file on every start, so
re-running a job name destroys the previous run. `rig ps` and any job list can
therefore only ever show the latest run of each name, and a failed run is lost the
moment it is retried — which is precisely when its log matters most.

**What to build:** the job wrapper appends one line to `~/.rig/jobs.jsonl` at start
(name, argv, workdir, started) and one at exit (name, started, finished, exit code),
and keeps per-run logs at `~/.rig/logs/<name>.<started>.log` with `<name>.log` a
symlink to the current run. `remote.ListJobs` reads the record instead of globbing
exit files; `rig ps --all` and the TUI job list show history; `rig logs <name>`
keeps meaning the latest run, with `--run <n>` for an older one.

**Cost:** the footprint grows a file, and `rig adopt` must tolerate its absence — an
instance adopted from before this exists has no history and must say so rather than
appearing to have none.

This is worth doing whether or not the TUI is restructured: today's behaviour quietly
destroys evidence.

## Comments

2026-09-09 (agent): Decided from the prototype — build it. It is the foundation for
issue 01's job list, and independently it stops `rig run` destroying the evidence of a
run you are retrying.

2026-09-09 (agent): Landed. `internal/remote/jobs.go` is new — the job half of the
footprint moved out of `remote.go` (Job, StartJob, ListJobs, TailLog, StopJob) and grew
the record.

`RenderJobWrapper(name, workdir, command)` is `~/.rig/jobs/<name>.run.sh`, the script
tmux now runs: it stamps the run, points `<name>.log` at that run's own log, appends a
start line to `~/.rig/jobs.jsonl`, runs the unchanged `<name>.sh`, then writes
`<name>.exit` (still what the waiter polls, ADR-0001) and appends an exit line. The
wrapper exists rather than a longer tmux command line because the recording needs JSON
inside shell inside a tmux argument inside an ssh command; sent as a file over stdin,
none of those layers needs escaping — `json.Marshal` for the values, `shellq.Quote` for
the shell.

Two things came out of writing it. `date +%s` has one-second resolution, so two starts
inside one second shared a run id and truncated each other's log; the wrapper now bumps
the id past any existing log file, keeping `at` as the honest timestamp. And tmux is the
authority on whether a run is alive, not the record: a start with no exit whose session
is gone is `ExitUnknown` (`interrupted`), not running forever, and an *older* run of a
live name does not get to claim that session.

`ParseJobs` is pure and merges the three sources — tmux, the record, and the bare exit
files an instance adopted before the record existed. A torn record line (the box can be
killed mid-append) is skipped, not fatal. `Latest` keeps one run per name, which is what
`rig ps` shows without `--all`.

`rig ps` gains STATUS/STARTED/TOOK/COMMAND, `--all` (with a RUN column), and a footer
naming the jobs that predate the record so their missing history is not read as none.
`rig logs --run <id>` reaches an earlier run. `--json` gained fields, added none to any
existing one (ADR-0002).

Tests: `internal/remote/jobs_test.go`. Eleven over `ParseJobs`/`Latest`/the wrapper's
quoting, and two that run the real wrapper under bash in a temp HOME — because
everything the record promises is the script's doing, not Go's: two runs get distinct
ids, the failed run's log survives the retry, `<name>.log` follows the newest, and the
exit file still carries the latest code. The probe helper emits sessions the way the
probe's own sed leaves them, so a test cannot feed the parser something an instance
would never send (it caught exactly that mistake).

Not done: pruning old run logs. Instances are ephemeral by design, so growth is bounded
by the TTL, and silently deleting a user's logs is worse than the disk. Revisit if a
long-lived adopted box fills up.

Still unverified against a live instance: the tmux invocation and `rig ps` rendering on
a real box. The wrapper itself is covered by the bash tests, and the tmux line keeps the
exact quoting the pre-record version has been running with.
