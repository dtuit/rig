# 05 — What the prototype hid

**Status:** resolved

The layout is settled and the wording is settled. What is not settled is everything the
prototype got for free, because its data was instant, complete and always available.
Three of those matter enough to decide before writing the real screens.

## 1. The overview's JOBS column changes the dashboard's cost model

Measured 2026-09-09 against a live Verda instance: **0.8s per per-instance ssh call**
even with ControlMaster multiplexing warm (1.08s cold), and a 10s `ConnectTimeout` when
the instance cannot be reached at all.

Today's dashboard is provider-API only: one `ListInstances` per cloud, in parallel, and
it works on stopped instances. `JOBS` — the column that justifies drilling in — needs
`remote.ListJobs`, which is one ssh per instance. On four instances that is ~0.8s in
parallel and 3.2s serial, on a 15-second refresh, and a single unreachable box costs the
full 10s timeout.

Options:

- **API-first paint, progressive job counts.** Render from the survey immediately with
  `JOBS` showing `…`, then fill each row as its ssh returns. Only ask instances that are
  Running — a stopped box cannot answer and must show `—`, not a 10s stall. First paint
  stays as fast as today.
- **Lazy: no ssh on the overview at all.** `JOBS` shows `—` until you open an instance.
  Cheapest and simplest; loses the signpost that was the reason for the column.
- **Cache counts in instcache** with a TTL and refresh in the background. Survives across
  invocations, but adds a staleness question to a file that is already advisory.

## 2. Log following cannot work the way the prototype implies

`sshx.Stream` attaches `os.Stdout` and `os.Stderr` directly (`sshx.go`), so it cannot run
inside a bubbletea alt-screen at all. The prototype's `● Live` pill is a static slice of
fake data; nothing behind it exists.

Options:

- **Poll a tail.** `tail -n N` every ~2s through the existing `Run`. No new plumbing, the
  "N new lines" count falls out of comparing snapshots, and 0.8s per poll is affordable.
  Latency is a second or two and it costs an ssh round trip while the pane is open.
- **Hand off to `rig logs -f`.** `f` leaves the TUI and runs the real follower, exactly
  as `n` already leaves it and runs the picker, returning on ctrl-C. No streaming code
  inside the model, no goroutine to leak, and the follow experience is the real one. The
  in-pane log becomes explicitly a snapshot, which changes what the `Live` /
  `Viewing history` pill means.
- **True streaming.** A new `sshx` method returning an `io.ReadCloser` plus cancel, read
  in a goroutine feeding `tea.Msg`s. Best latency, most risk: process lifecycle on esc,
  on quit, on switching instance.

## 3. The unreachable instance has no designed state

Every pane on the instance detail needs ssh: jobs, storage mount points, the watchdog
log. The prototype has no instance that refuses. Stopped is common (a TTL'd box), and so
is running-but-unreachable (a container whose mapped port moved, ADR-0006). "No jobs" and
"unknown, could not ask" must not look the same — the first invites `n`, the second
invites `rig adopt`.

## Smaller, still open

- **One line or two per instance row.** Asked for single-line, but the same message
  praised the two-line version; single-line is what the prototype does now. Confirm.
- **The light band.** `go run ./cmd/rig-proto -light` needs judging in an actual
  light-themed terminal — dark blue band with light text, or pale band with dark text.
- **Thirty runs bury four failures** (issue 01): surface failed runs, or filter.
- **Storage mount points** (issue 02): parse `Volume.MountCommand` or ask the instance.

## Answers (2026-09-09)

**1. API-first paint, job counts filled progressively.** The overview renders from the
provider survey as fast as today's dashboard, with `JOBS` showing `…`; each running
instance's count arrives as its ssh returns. A stopped or unreachable instance shows `—`
and is never asked, so nothing waits out a 10s `ConnectTimeout`. Consequences for
issue 01: the model needs per-instance job state (asking / known / not asked), the
refresh has to fan out and merge rather than replace, and a slow instance must not hold
up the others.

**2. Following hands off to `rig logs -f`,** as creating hands off to the picker. No
streaming inside the model, no goroutine lifecycle, and the follow is the real one. The
in-pane log is explicitly a snapshot and now says so —
`LOG  snapshot · last 60 of 184,223 lines · f follows live · r refreshes` — because
calling it Live would have been a lie about what the pane does. The `Live` /
`Viewing history` pill from the second pass is withdrawn; it described a mechanism that
cannot exist behind `sshx.Stream`.

**3. One line per instance row.** Confirmed against both variants side by side.

**4. The unreachable instance now has a designed state,** which was the one gap with no
answer. The rule: **everything the provider API knows still shows; only what needs ssh
says why it is missing.** So for an instance the cloud calls running and ssh cannot
reach —

- `STATUS` reads `running · no ssh`, because the cloud saying running and ssh
  disagreeing *is* the state, and it belongs where state is read.
- `EXPIRES` is `?` on the overview and `expiry unreadable, no ssh` on the detail: the
  deadline lives on the instance, so an unreachable one has none to read — itself worth
  knowing, since it means the watchdog cannot be confirmed.
- Storage still lists every volume with its name, size, type and fate, all of which come
  from the API. Only `MOUNTED` is unknown, and it says `? no ssh`.
- The jobs pane carries the ssh error verbatim, wrapped, plus
  `rig adopt <name> repairs the footprint`. "No jobs" invites `n`; this invites adopt,
  and the two must never look alike.

`go run ./cmd/rig-proto -print` ends with both new screens: the first paint with counts
still arriving, and the unreachable instance's detail.
