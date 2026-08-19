# 01 — `rig wait`: block until a job finishes

Status: resolved
Source: ../HANDOFF.md §01

## Problem

There is no way to block until a job finishes. Driving an 8-hour matrix cost ~40 polling
round-trips of `until rig ssh -- 'test -f ~/.rig/logs/x.exit'; do sleep 180; done` — each one a
shell out, an ssh handshake, and an agent turn.

## Shape

- `rig wait <job> [--timeout D] [-i inst]` — blocks until the job leaves the running state, then
  exits with the job's own exit code.
- `rig run --wait` — start it and tell me when it's done.

## Where

- `internal/cli/jobs.go` (commands)
- `internal/remote/remote.go:135` — the `Job` struct already carries `ExitCode`; `remote.Jobs()`
  parses it at `remote.go:188`. Only the blocking loop and exit-code propagation are missing.
- Poll server-side over one ssh session rather than reconnecting per tick.

## Done when

- A job that exits 0 makes `rig wait` exit 0.
- A job that exits non-zero propagates that code.
- `--timeout` exits distinguishably (reserved code, e.g. 124, documented).
- `rig run --wait` is equivalent to `rig run` followed by `rig wait`.

## Notes

- Handoff suggests writing this test-first (`tdd` skill) — the contract is crisp.
- CLI-contract change → deserves an ADR.
