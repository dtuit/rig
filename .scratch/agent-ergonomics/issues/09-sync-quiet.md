# 09 — `rig sync --quiet`: suppress rsync progress output

Status: resolved
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

## Comments

**--quiet, auto-quiet, --progress (2026-09-06, agent):** `sshx.Rsync` takes a `progress` bool
(`rsyncArgs` is the pure builder, tested in `sshx_test.go`); `rig sync` decides it with
`showProgress` in `internal/cli/access.go`: `--quiet/-q` always wins, `--progress` forces it on,
otherwise progress shows only when stdout is a terminal (`os.Stdout.Stat()` char-device check —
`x/term` is not a dependency) AND `--jobs` is 1, since parallel bars interleave (issue 14). The
`sync src -> inst:dest` header and rsync's own stderr are unaffected. Table-tested in
`sync_test.go`. `fetch` was not touched (not this change's file); it does not use `Rsync`.
