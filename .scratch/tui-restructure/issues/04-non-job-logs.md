# 04 — What else the TUI should show besides job logs

**Status:** 1 and 2 resolved; 3 and 4 open

Asked 2026-09-09: are there non-job logs worth exposing in the TUI? Four sources exist.
Only one is worth building, and building it means *writing* the log, not surfacing it.

## 1. The TTL watchdog — the log that matters, and it is empty

`remote.ttlScript` records almost nothing. Its only output is one line, at the moment it
fires: `logger -t rig-ttl "deadline reached, shutting down"`. Every deferral — the
interesting event — writes the new epoch into `$DIR/deadline` and prints nothing. So:

- On a systemd VM (Verda, AWS) the single line goes to journald and `~/.rig/ttl.log`
  does not exist at all.
- In a container (RunPod, Vast) `~/.rig/ttl.log` exists, because `ttlStartLine`
  redirects `ttl.sh` into it, and it is empty — the deferrals do not print, and
  `logger` in a container usually goes nowhere.

Surfacing `ttl.log` today would show a blank pane. The real gap is that nothing answers
**"why is this box still alive three hours past its TTL?"**, whose answer is always
"it deferred, because job X was running" or "because `busy.d/keepalive` exited 0".

**What to build:** `ttl.sh` appends one line per decision to `$DIR/ttl.log` —
`<iso8601> defer 1h (job fit-7b running)`, `<iso8601> defer 1h (busy.d/keepalive)`,
`<iso8601> fire (deadline reached)`, `<iso8601> armed (deadline 2026-09-10T04:00Z)` —
and writes to that file directly rather than to stdout, so a container and a systemd VM
produce the same file in the same place and the reader needs no init-system branch.
Then the instance detail carries a LIFECYCLE section: armed, deferred N times, next
check. Cheap, and it makes the TTL column in the overview corroborable instead of
merely asserted.

Note the log must be capped (`tail -c` on append, or trim past N lines) — the watchdog
runs for the life of the instance.

## 2. This instance's ledger events — free, local, worth it

`~/.config/rig/ledger.jsonl` already records up / extend / down per instance with the
price and, on down, the computed cost. Filtering it to the selected instance costs
nothing (no ssh) and answers "when was this extended, and what has it cost" on the
detail screen — which is also where the new SPENT column's number comes from.

## 3. The tunnel supervisor log — exists, already half-surfaced

`internal/tunnel/supervise.go` writes a real log per tunnel
(`<dir>/<instance>-<port>.log`), and `rig tunnel status` already prints its last line
via `lastLogLine`. Worth a pane on the instance detail *only when that instance has a
tunnel*, answering "why does my forward keep dropping". Low effort, narrow audience.

## 4. Provider console / boot output — not a TUI decision

The case it serves is "the instance says running but ssh never answered", where the
footprint is not installed and there is nothing local to read. AWS could answer it
(`GetConsoleOutput`); RunPod and Vast have container-log endpoints; Verda has nothing
implemented. It needs a new optional `Provider` method and a capability flag, and the
TUI is the wrong place to decide that — see ADR-0007 on typed sentinels for features a
cloud lacks. Left out deliberately.

## Recommendation

Build 1 and 2 with the restructure (1 is the only one that changes behaviour on the
instance, and it is small). Add 3 if a tunnel pane falls out cheaply. Leave 4 to its own
spec.

## Comments

2026-09-09 (agent): 1 and 2 built, on the decision to do so.

**1. The watchdog now records its decisions.** `remote.ttlScript` gained a `ttl_log`
function and logs `watchdog started`, `armed for <time>`, `deferred 1h to <time>
(jobs running: fit-7b)`, `deferred 1h to <time> (busy.d/keepalive said busy)` and
`deadline reached, stopping the instance`. It writes to `$DIR/ttl.log` directly rather
than to stdout, so a container (nohup) and a systemd VM (whose stdout goes to the
journal) leave the same record in the same place and the reader needs no init-system
branch. The file is capped at 400 lines, trimmed to 200, because the watchdog runs for
the life of the instance. `ttlStartLine` now appends (`>>`) instead of truncating, or a
container restart would erase the record it is about to add to.

`remote.WatchdogLog(t, lines)` reads it; an absent file is an empty answer, not an
error, because an instance adopted before this existed has none. Surfaced as
`rig logs --watchdog`, which takes no job name (the argument rules are tested, and they
run before any provider is opened).

The script also honours `RIG_TTL_POLL` so it can be tested at 1s instead of 60s. That
is the only reason the two new tests are possible: they run the real `ttl.sh` under bash
against a fake `RIG_DIR`, with `exit 0` standing in for the self-stop line, and assert
that a busy hook produces a deferral with its name in it *and* moves the deadline, and
that an idle instance records firing. Shell is only really tested by running it.

**2. One instance's ledger timeline.** `ledger.EventsFor(events, name)` filters the
ledger to one instance, oldest first, accepting the name with or without the `rig-`
prefix as `-i` does. It matches on name rather than ID deliberately: an instance
relaunched onto the same volume under the same name is one piece of work to its
operator, and its history should read as one story. Surfaced as `rig spend -i <name>` —
same ledger, different question, still no provider call — printing up / extend / down
with the type, price, TTL, new deadlines and final cost, and saying "still open since X"
when there is no down event, which is the normal state of anything the watchdog tore
down.

Both are the readers the TUI's instance detail will use (issue 01): the LIFECYCLE
section is `WatchdogLog` plus `EventsFor`, and neither costs a cloud call beyond the one
ssh the watchdog log needs.

3 (tunnel log pane) and 4 (provider console output) remain open as written above.
