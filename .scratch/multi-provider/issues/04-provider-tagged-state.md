# 04 — Prefactor: ledger and instance cache carry the provider

**What to build:** Local state knows which cloud each record came from, so switching providers
never mixes up instances with the same name or adds a RunPod pod's cost to a Verda total.
Existing files keep working: a record without a provider is Verda's.

**Blocked by:** 01 — provider selection must exist for there to be a "current provider".

**Status:** resolved

- [x] `ledger.Event.Provider` and `instcache.Entry` instance `Provider` fields (json `provider`,
      `omitempty` on read defaulting to `verda`).
- [x] `Managed()` reconciles the cache against the provider listing only for entries of the
      active provider; other providers' entries are untouched.
- [x] `Resolve` by name only matches cached entries of the active provider.
- [x] `rig spend` groups totals per provider when more than one appears in the window; the
      single-provider output is byte-identical to today.
- [x] `rig status --json` includes `provider`; human table adds it only when the config has more
      than one provider block.
- [x] Tests: old-format ledger and cache files load with provider `verda`.

## Notes

Spec: ../spec.md — "Local state". ADR-0004 and ADR-0006 hold unchanged; note the field in 11.

## Comments

2026-09-08 (agent): Landed. `internal/provider/provider.go`: `Instance.Provider string`
(`provider,omitempty`), stamped by core, never by drivers. `internal/core/core.go`: `Managed`
stamps `a.Prov.Name()` on every listed instance and calls
`instcache.Reconcile(path, a.Prov.Name(), out, now)`; `provision` stamps the created instance
after `waitRunning`; `Down` stamps the instance it is handed so the down event carries the
cloud; `cached`/`refreshCached` read through new `cachedInstances()`, which keeps only this
provider's entries. `internal/instcache/instcache.go`: `DefaultProvider = "verda"`; `load`
fills an empty provider so `Read` never returns ""; `Put` replaces only the same provider's
entry by ID/name (two clouds' `rig-train` coexist); `Reconcile(path, prov, live, now)` — new
signature — leaves other providers' entries untouched and tags kept entries with `prov`.
`internal/ledger/ledger.go`: `DefaultProvider`, `Event.Provider` (`provider,omitempty`, filled
from the instance in `UpEvent`/`ExtendEvent`/`DownEvent`), `Read` fills "" → verda,
`Line.Provider` (`provider,omitempty`) for grouping. `internal/cli/spend.go`: when the window's
lines span more than one provider the table gains a `PROVIDER` column after `NAME` and
per-provider `name: $x.xx` subtotals (sorted) print between the blank line and `total:`;
single-provider output is byte-identical. `internal/cli/lifecycle.go`: `status --json` carries
`provider` via the struct; the human table adds `PROVIDER` after `NAME` only when
`len(cfg.Providers) > 1`. Tunnel user (flagged by issue 03): `tunnel.Endpoint.User`
(`user,omitempty`), recorded from `Target(in).User` on `--detach`, used by the supervisor's
rebuilt `Instance.SSH.User` (empty → today's config/root fallback). `internal/provider/fake`:
`Named(name)`; `New()` = `Named("fake")`. Tests: `instcache_test.go` (old entry reads as verda;
`Reconcile` touches only the active provider; same-named entries per provider),
`ledger_test.go` (old event reads as verda; events and lines carry the provider),
`core_test.go` (`Managed`/`Provision`/`Down` stamp the provider into instance, cache and event;
`Resolve` ignores another provider's cached entry and an empty listing from one provider leaves
the other's entry alone — two `fake.Named` providers sharing one state dir via new `newAppIn`),
`cli/spend_test.go` (single-provider unchanged; grouping, subtotals, `--json` provider),
`cli/json_test.go` (`provider` in `status --json`; PROVIDER column only with two provider
blocks, via new `runRigCfg`), `tunnel/manager_test.go` (user round-trips; root omits the key).
Existing test changed: `instcache_test.go` `TestReconcile` passes the new `"verda"` argument
(signature change only). Deviations/follow-ups: `instcache.Remove(path, key)` is still
provider-agnostic — `Down` on one cloud's `rig-train` also forgets the other cloud's same-named
entry (advisory: one extra provider call later); `Put` scoping went beyond the checklist so the
two-provider cache test is meaningful; `spend --json` lines gain `provider`; the dashboard
provider display is issue 10, ADR wording issue 11.
