# 04 — The dashboard covers every provider

**Status:** resolved

- [x] `rig` surveys every provider the config knows, in parallel.
- [x] Rows name their cloud when more than one is in play; volumes too.
- [x] The burn rate sums across clouds; each cloud's balance is named in the header.
- [x] A cloud that cannot be reached gets one error line and leaves the others alone.
- [x] Delete acts on the provider the row is on.
- [x] Create asks which cloud (one provider needs no question).
- [x] `rig --provider <name>` pins the dashboard to one cloud.

## Comments

2026-09-09 (agent): New `internal/core/fleet.go`. `Fleet` is every provider one config drives:
`ProviderNames(cfg)` (the default first, then the other `providers:` blocks alphabetically —
the default is included even with no block of its own, which is how a single-provider rig
configures Verda), `App(name)` opening each driver lazily and once, `Survey(ctx)` asking them
all in parallel, and `FindApp(in)` following an instance's own `Provider` stamp. `Survey`
answers a `Survey` per provider in display order; only the instance listing can fail one, and
volumes and balance stay decoration, so a driver answering `ErrUnsupported` contributes none
rather than spoiling the row. `Fleet.Open` is the injection seam for tests, because the
registry cannot hold a fake (only cli imports the real drivers).

`App.Managed` is split into `managed` (no cache write) and `reconcile`: the instance cache is
one file rewritten whole, so `Survey` fans the provider calls out but reconciles serially —
two goroutines doing read-modify-write on it would lose one another's updates. A test seeds
two providers' stale entries and checks both come back fresh.

Dashboard: the header is `rig · <provider> · <balance>` for one cloud and
`rig · N providers · verda … · runpod …` for several; `PROVIDER` is a column (drop order 6, so
it outlives TYPE and ENDPOINT — a row whose cloud is unnamed cannot be acted on with any
confidence) on the instance and volume tables; the delete confirmation names the cloud; a
failed delete is reported against the cloud that refused it.

Two things came out of looking at the rendering rather than the code: spot was a `" spot"`
suffix on the price and was the first thing a narrow window clipped (`25.92 sp…`), so it is now
its own `SPOT` column, present only when something on screen is spot-priced, and `$/H` has
min 5 so a price never clips; and the help line was clipped mid-word on a 56-column window, so
it has a short form (`↑/↓ · n new · d del · r · q · 42%`) for windows the spelt-out one does
not fit.

`internal/cli/root.go`: `dashboardProviders(cfg)` is the rule — `--provider` or every
configured provider — and it is what `rig` builds its Fleet from. The dashboard now also opens
when no credentials resolve at all: the provider that cannot be opened is an error line in the
table instead of a failure before the UI starts.

Verified live against Verda on 2026-09-09 in a pty (100x20): `rig · verda · 355.97 usd`, the
volume table aligned, and the 15 volumes scrolling 0% → 100% on `G`.
