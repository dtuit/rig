# 12 — `rig wait --for <regex>`: block until a job log line matches

Status: resolved
Source: agent session d301490d (training run on one H100, 2026-09-06); code read against ec9471b

## Problem

`rig wait` reports only the job's exit code. A multi-stage job (manifest, fetch, bench, train,
bench) emits stage-transition lines and training progress lines that an agent needs mid-run: to
confirm the pipeline entered training, to read the step rate once, to pull partial results when one
stage ends. `rig logs -f` streams forever and cannot stop on a condition.

In the session the agent wrote eight hand-rolled watchers, each an ssh loop doing tail + grep +
tmux liveness, and used rig for none of it. Every one carried the identity file and four ssh
options.

## Shape

`rig wait <job> --for '<regex>' [--timeout D]`: block until a line of `~/.rig/logs/<job>.log`
matches; print that line on stdout; exit 0. Exit non-zero (distinct code, documented) if the job
ends first without a match; 124 on timeout, as today. Poll server-side in bounded chunks like the
existing wait (ADR-0001), e.g. `grep -m1 -E` over the log tail plus the exit-file check.

Optional: `--since-line N` or "only lines after the previous match" so repeated calls can walk a
log forward. Keep `--for` composable with `rig run --wait`.

## Where

`internal/remote/wait.go` (`waitScript`, `parseWaitOutput` gain a MATCH token),
`internal/cli/wait.go` (flag), README agent section (show the Monitor-friendly pattern).

## Comments

**Implemented (2026-09-06, agent):** `rig wait <job> --for '<regex>'` and
`rig run --wait --for '<regex>'`. Exit contract (ADR-0005, `docs/adr/0005-wait-for-log-pattern.md`):
0 with the matched line on stdout; 124 on `--timeout`; 125 when the job ends before any line
matches (stderr names the job's exit code). Server-side: `waitScript` in
`internal/remote/wait.go` takes a pattern and, per poll, runs `grep -m1 -E -e '<pattern>'`
over `~/.rig/logs/<job>.log`, printing `MATCH <line>`; the grep is repeated just before
`EXIT`/`NOJOB` is reported so a last-moment line is not lost. The pattern is single-quoted
with `shellq.Quote` (issue 10's helper). `parseWaitOutput` returns a `waitResult` and reads
`MATCH` lines by prefix so spaces in the line survive; motd noise is still skipped. New
`remote.WaitForLine` and `remote.ErrNoMatch`; `WaitJob` keeps its signature. CLI:
`internal/cli/wait.go` adds the flag and `waitForOutcome` (125 mapping); `internal/cli/jobs.go`
passes `--for` through `waitJob` (requires `--wait`). Tests: sh-executed script with
matching/non-matching patterns and quote/regex characters, quoted-grep presence,
`parseWaitOutput` table, `WaitForLine` match/no-match/timeout, CLI outcome codes. Not done:
the optional `--since-line`; README lines are recommended to the human rather than edited
(README is being edited elsewhere).

**Review fix (2026-09-07, agent, /code-review vs ec9471b):** both axes found that the remote
script printed the matched line with `echo`, which under dash interprets backslash escapes, so a
line holding `\t` or a Windows path came back altered or split past the `MATCH` token. Now
`printf 'MATCH %s\n' "$l"`, and `grep -a` so a log with progress-bar bytes matches instead of
answering "Binary file matches". The CLI no longer double-wraps the `ErrNoMatch` message. Tests
cover a backslash line and a NUL byte in the log. Still open by design: `--since-line`; `--for` on
a job that never existed exits 1 (NOJOB), not 125.
