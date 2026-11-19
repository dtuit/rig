# 03 — Create an instance from the dashboard

**Status:** resolved

- [x] `n` on the dashboard launches an instance through the picker `rig up` uses.
- [x] The launch log and the closing summary land on a plain terminal, not under the alt screen.
- [x] The dashboard reopens afterwards; a cancelled picker goes straight back.

## Comments

2026-09-09 (agent): The dashboard reports an intent rather than launching anything itself:
`tui.Dashboard` now returns `(Outcome, error)`, `Quit` or `Create`. `internal/cli/root.go`'s
`dashboard(ctx)` loops — open the UI, and on `Create` run `createFromDashboard` (provider
question, `tui.Picker`, `App.Up`, `printUp`) before opening it again. The loop lives in cli
because a launch needs the plain terminal huh and the launch log want, and because the closing
summary is cli's own output: `printUp` in `internal/cli/lifecycle.go` is now shared by `rig up`
and this path, byte-identical. Nesting a huh form inside the running bubbletea program was the
alternative and was not worth the rewrite of the picker's provider round-trips into async
model steps.

A cancelled picker leaves nothing to read and reopens immediately; a launch or a failure pauses
on "press enter" first, so the output is not painted over.

Verified live against Verda on 2026-09-09 in a pty: `n` leaves the dashboard and the picker's
"Pricing on verda" question appears; ctrl+C in the picker returns to the dashboard, which
refetches; `q` exits 0. Nothing was created.
