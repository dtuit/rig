# 03 — `rig sync --exclude`

Status: resolved
Source: ../HANDOFF.md §03

## Problem

Needed to sync a dataset without its 1.2 GB `images/` subdirectory; had to build a pruned
hardlink tree by hand.

## Shape

Repeatable `--exclude PATTERN`, passed straight through to rsync.

## Where

`internal/cli/access.go:90` (flag) → `internal/sshx/sshx.go:154` (rsync argv). Commit `c126e34`
already excludes `.env` by default — this generalises that mechanism. Keep the default exclusions
on top of any user patterns.
