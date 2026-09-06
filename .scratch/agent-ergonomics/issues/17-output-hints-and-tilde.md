# 17 — small output fixes: `~` has two meanings, `rig run` hint, spend footer

Status: resolved
Source: agent session d301490d (training run on one H100, 2026-09-06); code read against ec9471b

## Problem

Three cosmetic issues seen in one session, grouped because each is a one-line change:

1. `rig down` prints `(lived 4.4h, ~$14.30)` where `~` means "approximately", while `rig spend`
   uses `~` to mark "estimated, no down event". The down figure is exact (lifetime x price), so the
   tilde is misleading there.
2. `rig spend` always prints the footer `(~ = estimated: no down event, likely TTL teardown)`
   (`internal/cli/spend.go:51`), even when no row carries the mark.
3. `rig run` prints the hint `rig logs <job> -f` after starting a job. For an agent the useful
   next command is `rig wait <job>` (or issue 12's `--for`); show both, wait first.

## Shape

Drop the tilde from the down line; print the spend footer only when some line has
`Estimated`; add the wait hint to `newRunCmd`.

## Where

`internal/cli/lifecycle.go` (down output), `internal/cli/spend.go:51`, `internal/cli/jobs.go`
(run output).

## Comments

**Down tilde done (2026-09-06, agent):** item 1 only — `rig down` now prints
`<short> deleted (lived 4.4h, $14.30)` (`internal/cli/lifecycle.go`); the figure is exact
(lifetime x price from the ledger event), so `~` stays reserved for `rig spend`'s "estimated"
mark. Items 2 (spend footer) and 3 (run hint) live in `spend.go`/`jobs.go` and are untouched
here.
**spend.go and jobs.go items (2026-09-06, agent):** items 2 and 3 done. `internal/cli/spend.go` appends the
`(~ = estimated: ...)` legend to the total line only when some row has `Estimated` set.
`internal/cli/jobs.go` prints the hints after `rig run` as `rig wait <job>` then
`rig logs <job> -f`. Item 1 (the `rig down` tilde in `internal/cli/lifecycle.go`) is left
for the lifecycle change set; status stays open until then.
