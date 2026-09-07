# 18 — placement: let the data volume's location outrank type order

Status: needs-triage
Source: review of agent session d301490d (2026-09-06) and the 2026-09-04 lab run, both of which re-uploaded 7.9 GB of datasets and rebuilt the environment; code read against 01b90d0

## Problem

`core.Pick` (`internal/core/core.go:204`) walks the type preference list first and only sorts
candidate datacenters by volume presence within one type (`volLocs`, `:218`). With
`--type 1H100.80S.32V,1A100.22V` and a filled `rig-data` volume in FIN-03 next to an available
A100, rig still takes the H100 in FIN-02 and `EnsureVolume` quietly creates a second, empty
`rig-data` there. Volumes are datacenter-bound, so a profile accumulates one per location, each
billed while detached, and the warm one is never reused unless the user pins `--location`.

The two training runs landed in FIN-03 (A100) and FIN-02 (H100). Had the first created a volume,
the second would not have found it.

## Shape

- A placement mode, per profile and as a flag: `volume_first: true` / `rig up --follow-volume`.
  Order candidates as (has volume, type rank) instead of (type rank, has volume): any type from the
  list in a location that already holds the volume beats a better-ranked type elsewhere. Fall back
  to today's order when no location holds the volume.
- Always tell the user what happened: `rig: volume rig-data exists in FIN-03 (500GB) but the
  chosen type is only available in FIN-02; creating a new volume there` on stderr, so the second
  volume is never a surprise. This line is worth adding even if the mode is rejected.
- `rig volumes` should show the same-named volumes grouped, and `rig up` should print which one
  was attached.

## Where

`internal/core/core.go` `Pick` and `EnsureVolume`, `internal/config/config.go` (profile field),
`internal/cli/lifecycle.go` (flag), `internal/tui/picker.go` if the picker exposes it.
