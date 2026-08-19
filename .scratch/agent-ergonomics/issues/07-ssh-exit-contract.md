# 07 — decide and document the exit-status contract for `rig ssh -- <cmd>`

Status: resolved
Source: ../HANDOFF.md §07

## Problem

`rig ssh -- 'pkill -f something'` exits 255 because the remote command tears down its own process
tree and ssh reports that. Arguably correct, but undocumented — makes `rig ssh -- ... && next`
unusable without knowing to expect it.

## Not a `stop` bug

Originally recorded as a `rig stop` bug; it is not — `stop` returns cleanly
(`internal/cli/jobs.go:132–150`). Do not go looking for a bug in `stop`.

## Shape

Decide whether remote exit codes propagate (probably yes), then document it in the `ssh` command's
help text and README.
