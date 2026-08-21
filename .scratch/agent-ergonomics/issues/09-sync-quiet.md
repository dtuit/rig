# 09 — `rig sync --quiet`: suppress rsync progress output

Status: needs-triage
Source: target-user feedback, 2026-08-21 (agent session driving a 27-run training matrix)

## Problem

`Rsync` always passes `--info=progress2`, so every `rig sync` streams progress to stdout. In an
agent session that output twice reached 50–95KB per sync — enough to blow the agent's context
budget and force spilling to a file. Progress bars are for humans watching a terminal; an agent
only needs "what synced, did it succeed".

## Shape

- `rig sync --quiet` (or `-q`) — drop `--info=progress2` (keep the one-line
  `sync <src> -> <inst>:<dest>` header and errors).
- Consider auto-quiet when stdout is not a TTY, the same instinct as keepalives-for-agents:
  progress output is useless piped, so the flag would rarely need typing. Explicit `--quiet`
  wins either way; a `--progress` escape hatch could force it back on.

## Where

- `internal/sshx/sshx.go` (`Rsync` — take a progress/quiet knob)
- `internal/cli/access.go` (`newSyncCmd` flag; `fetch` likely wants the same treatment —
  check `internal/cli/fetch.go`)
