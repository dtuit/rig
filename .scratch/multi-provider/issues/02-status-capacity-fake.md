# 02 — Prefactor: normalised status, capacity sentinel, create retry, fake provider

**What to build:** Core stops comparing Verda's status strings, learns a typed "no capacity"
answer from `CreateInstance`, and walks the candidate (type, location) list when it gets one.
A scriptable in-memory provider makes all of this testable without a cloud.

**Blocked by:** None — can start immediately (independent of 01).

**Status:** resolved

- [x] `provider.Status` with constants `Provisioning`, `Running`, `Stopped`, `Error`, `Gone`,
      `NoCapacity`; `Instance.Status` uses it and `Instance.RawStatus` keeps the provider's text.
      Verda maps: running→Running; offline→Stopped; provisioning/ordered→Provisioning;
      discontinued/deleting→Gone; error→Error; no_capacity→NoCapacity.
- [x] Every literal status comparison in core, tui, cli, instcache uses the constants; `--json`
      output of `status` keeps emitting the lowercase word (ADR-0002: the struct is the schema;
      document the value set in the field comment).
- [x] `provider.ErrNoCapacity` and `provider.ErrUnsupported` sentinels (wrap with `%w`).
- [x] `core.Pick` returns an ordered candidate list (same ordering rules as today); `Up` tries
      `CreateInstance` per candidate and continues only on `ErrNoCapacity`, logging each miss;
      any other error aborts. A `NoCapacity` status seen in `waitRunning` is treated the same
      way after deleting the failed instance.
- [x] `Up` refuses `spot` or `volume` requests up front when the driver reports them
      unsupported (a `Capabilities()` method or the sentinel from the call — pick one and record
      it in the issue comments).
- [x] `internal/provider/fake`: in-memory `Provider` with scriptable availability, per-call
      create outcomes, status sequences. Core tests cover candidate walk, refusal, Verda-shaped
      happy path.

## Notes

Spec: ../spec.md — "Normalised status", "Capacity sentinel and create retry", "Feature gaps are
typed". Verda's `isNotFound` text heuristic in core stays; drivers may additionally return a
typed `ErrNotFound` later, but that is not required here.

## Comments

2026-09-08 (agent): Landed. `internal/provider/provider.go`: `type Status string` with
`Provisioning/Running/Stopped/Error/Gone/NoCapacity` (lowercase values, so `--json` is unchanged
and the value set is documented on the field), `Instance.RawStatus` (`raw_status,omitempty`),
`ErrNoCapacity`, `ErrUnsupported`, and a `Capabilities() Capabilities{Spot, Volumes, Balance}`
method on the interface — chosen over the call-time sentinel so `up` can refuse before spending a
create; Verda reports all three true. `internal/provider/verda/verda.go`: `fromSDKStatus` maps the
Verda words as listed above (unknown words pass through as `Status(raw)`), `RawStatus` keeps the
text. `internal/core/core.go`: `Candidate{Type, Location}`; `Pick` returns `[]Candidate` in the old
ranking (type preference, volume-holding locations, alphabetical); the provider-only half of `Up`
is extracted as exported `App.Provision(ctx, spec) (*provider.Instance, error)` (Up calls the same
unexported `provision`, which also hands back the data-volume IDs for the mount step). Provision
refuses `spot`/`volume` against `Capabilities` wrapping `ErrUnsupported` with the provider name,
then walks the candidates: `ErrNoCapacity` from `CreateInstance` or a `NoCapacity` status in
`waitRunning` logs `no capacity for <type> in <loc>, trying next` (the latter after deleting the
instance with its OS volume, as Down does — shared `volumesToDelete`); exhaustion yields the same
"none of … available" error as an empty pick. `Error`/`Gone` still abort. Literal comparisons in
core, cli/lifecycle.go, tui/dashboard.go, instcache replaced by constants (`down --all` now skips
`Gone`, which covers both discontinued and deleting). `internal/provider/fake/fake.go`: scriptable
`Avail`, `Volumes`, `Creates []error` (per call), `Statuses map[id][]Status` (ids `fake-1`, …),
records `Requests` and `Deleted`. `internal/core/core_test.go`: 13 tests through Pick, Provision,
Down, Resolve/ResolveFresh. Deviations: `internal/cli/json_test.go`'s `fakeProvider` gained a
`Capabilities()` method (interface grew; no assertion changed); the Verda status mapping is not
unit-tested because `fromSDKInstance` is reachable only through the SDK client. Follow-up: when a
data volume is created for a candidate that then has no capacity, the volume is left in that
location (data volumes survive by contract) and the next candidate finds-or-creates its own.
