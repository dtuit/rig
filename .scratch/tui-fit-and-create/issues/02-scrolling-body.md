# 02 — A body that scrolls instead of running off the screen

**Status:** resolved

- [x] The dashboard tracks the terminal size (`tea.WindowSizeMsg`).
- [x] Everything between the header and the footer scrolls; the selection is kept in view.
- [x] The footer says the body scrolls and how far down it is.
- [x] A window too short for even the chrome shows the top of what it has.

## Comments

2026-09-09 (agent): `internal/tui/dashboard.go` puts the instance table, the per-provider
error lines, the burn rate and the volume table in a `bubbles/viewport` (promoted to a direct
dependency; it was already indirect through huh). `sync(follow bool)` rebuilds the content and
size in `Update` — never in `View`, which cannot mutate — and only a cursor move asks to
follow: a refresh every fifteen seconds that yanked the body back to the cursor would undo the
user's own scrolling under them. The viewport keeps `pgup`/`pgdn`/`f`/`b`/space and gains
`g`/`G`/home/end; its `j`/`k`/`u`/`d` bindings are cleared because the dashboard owns those
(`d` is delete). `View` trims to the window height, so a three-line terminal keeps the header
and what rows fit.

Tests: `internal/tui/dashboard_test.go` drives the model as bubbletea does — messages in,
`View()` out. Every line fits and the view is no taller than the window at twelve sizes from
200x60 down to 4x1; 40 rows in a 12-line window scroll to the selected row after 39 `j`
presses and the footer says so; `end` reaches the bottom.
