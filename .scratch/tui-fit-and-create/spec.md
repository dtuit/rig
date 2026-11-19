# Dashboard: fit the window, create from it, know its providers

Reported 2026-09-09: the dashboard's columns do not line up, a terminal too small for the
content shows it cut off with no way to scroll, an instance cannot be created from the TUI,
and the TUI is hardwired to one provider.

## Stories

1. **Aligned whatever the values are.** A long instance name, a marketplace type token or a
   container's `host:port` endpoint must not shift the columns after it.
2. **Fits the window.** Nothing runs off the side; anything taller than the window scrolls and
   the selection stays in view. Columns are squeezed and then given up in a fixed order.
3. **Create from the dashboard.** `n` launches an instance through the same picker `rig up`
   uses, and the dashboard comes back afterwards.
4. **Provider aware.** The dashboard covers every provider the config knows, names each row's
   cloud when more than one is in play, sums the burn rate across them, keeps one cloud's
   failure to its own line, and asks which cloud a new instance goes on.
   `rig --provider <name>` pins it to one, as it pins every other command.

## Non-goals

Switching provider from inside the dashboard (the aggregate view makes it moot), acting on
jobs from the dashboard, horizontal scrolling (fitting removes the need).
