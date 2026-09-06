# 14 — `rig sync` cannot sync a single file, a per-path destination, or in parallel

Status: resolved
Source: agent session d301490d (training run on one H100, 2026-09-06); code read against ec9471b

## Problem

The session needed three transfers and used raw rsync for all of them:

1. A code tree with anchored excludes (`/outputs`, `/data`) to `/root/project/`. `--exclude` and
   `--dest` exist, so this one was possible; the agent did not trust it (issue 09 noise, and the
   two below).
2. One secrets file to one exact path. `rig sync` appends `/` to the source
   (`internal/cli/access.go:363`), so a file source becomes `file/` and rsync fails. Single files
   are impossible.
3. Eight dataset folders, 7.9 GB, to one parent. One `--dest` applies to every path, and paths sync
   sequentially. Four parallel rsync streams took 10 minutes; one stream is about 25.

## Shape

- Detect a file source (`os.Stat`) and omit the trailing slash; default dest `~/<basename>`.
- Accept `src:dest` pairs (or repeatable `--to src=dest`) so one command can place several paths.
- `--jobs N` runs the rsyncs concurrently (errgroup), default 1; document that 3 to 4 streams
  saturate the dev box link.
- Pair with issue 09 (`--quiet` / auto-quiet when not a TTY): parallel progress bars are unreadable.

## Where

`internal/cli/access.go` `newSyncCmd`, `internal/sshx/sshx.go` `Rsync` (source-is-file flag),
`sync_test.go`.

## Comments

**Files, per-path dest, --jobs (2026-09-06, agent):** all three in `internal/cli/access.go`
`newSyncCmd`, with the pure parts factored out and tested in `sync_test.go` (no rsync in
tests):

- `planSync` stats each source: a directory keeps the `src/ -> dest/` form, a file is passed
  as-is with dest defaulting to `~/<basename>` (a `dest/` still means "into that directory",
  rsync's own rule).
- `splitSyncArg` accepts `src:dest` per argument, splitting on the LAST colon only when the
  tail starts with `/` or `~` — `C:\Users\...` and `odd:name` stay sources. `--dest` remains the
  default for arguments without a pair.
- `--jobs N` (default 1) fans out with a stdlib semaphore + WaitGroup (`x/sync` is only a
  transitive module, so no errgroup); every failure is collected and returned via `errors.Join`,
  prefixed `sync <src>:`. The one-line `sync src -> inst:dest` header is kept per transfer.
- `sshx.Rsync` gained a `progress` bool (see issue 09) and leaves src/dest untouched, so the
  file-vs-dir decision lives in one place, the CLI.

Suggested README note: three to four streams saturate a typical link.

**Own connections for parallel streams (2026-09-06, agent, merge pass):** with `--jobs` above 1 the
rsyncs run with `ControlMaster=no` (`sshx.Target.NoMultiplex`), because channels multiplexed over the
shared master sit in one TCP window and would not deliver the 4x the session measured with four
independent connections. Single-stream sync keeps the shared master. Tested in `sshx_test.go`.
