# 01 — Instances dashboard and the drill-down

**Status:** resolved
**Blocked by:** 03 (the job list wants the record's fields)

- [x] Level 0 is rig's instances only: PROVIDER (when several), NAME, STATUS, TYPE,
      ENDPOINT, LOC, $/H, TTL, AGE, JOBS (`1 run, 2 done`). Volumes leave this screen.
- [x] TTL comes from the instance's deadline, as `rig status` reads it.
- [x] `enter` opens the instance: summary line, its storage, its jobs.
- [x] `enter` on a job opens the job: command, run facts, log tail, `f` follow, `s` stop.
- [x] `esc` walks back up; the header is a breadcrumb (`rig · instances › verda/trainer › fit-7b`).
- [x] Every level keeps the fitted table and the scrolling body from `tui-fit-and-create`.
- [x] `n` (create) and `d` (delete) stay on level 0.

From the UX review of the prototype (../spec.md, second pass):

- [x] Overview is five complete columns plus a dim second line per row (type, endpoint,
      location, age in full). Nothing an operator has to act on may be truncated.
- [x] `▌` focused row of the active pane, `│` remembered selection of an inactive one.
- [x] Footer is per level, names the object of each key, and always offers the way back.
- [x] The job log fills the height it has, with `● Following` / `❙❙ Paused` state; any
      scroll pauses, `f` resumes and jumps to the end.
- [x] Labels: `DURATION` + `+` while running (`elapsed`/`duration` spelt out on the
      detail), `failed · exit 1`, `ON RIG DOWN`, scoped money in the header, a stopped
      instance's rate parenthesised.
- [x] **Returning up a level restores both the selection and the scroll position** —
      the drill-down's one real cost is backtracking, and it is only tolerable if going
      back is free. The prototype keeps the cursor; it has no scrolling to keep.
- [x] Split is a mode, not the default, and offered only above ~120 columns; at the job
      level it drops the sidebar and gives the log the full width, wrapping not clipping.
- [x] Overview counts running, failed and done separately, urgency first.
- [x] The log pane is a snapshot and says so; `f` hands off to `rig logs -f` and returns,
      as `n` hands off to the picker (issue 05, decision 2).
- [x] Footer never clips a verb: navigation and `? actions` are guaranteed, the rest is
      in the help panel.
- [x] Focus is a *band* (`table.addBanded`, landed with the prototype), never a
      foreground repaint: a status colour must survive selection. Split names its panes
      and marks which has the keys.
- [x] Adopt the prototype's role palette — accent / muted / label / ok / warn / bad plus
      the two bands — replacing the flat colours in `dashboard.go`, with one place
      mapping state to role. Fixed mid-tones, not AdaptiveColor: detection fails
      silently and drew a dark band in a light terminal.
- [x] Every cell of a banded row sets a foreground. A background alone leaves unstyled
      cells on the terminal's own foreground, which is black on a light theme.
- [x] `tui.theme: auto|light|dark` in config, so the band is chosen and not sniffed.
- [x] The overview carries `SPENT` per instance (uptime x rate while running, the
      ledger's recorded cost once stopped) and totals it in the header. It outlives
      `$/H` in the drop order.
- [x] Instance names, mount paths and commands use the `mid` column (clipMid), which
      landed in `internal/tui/table.go` with the prototype.
- [x] Breadcrumb concessions: "instances", then the money, then middle-clip the
      instance; the leaf is never shortened.
- [x] JOBS is filled progressively: API-first paint with `…`, per-instance ssh only for
      running instances, `—` for the rest, and one slow instance must not hold up the
      others (issue 05, decision 1).
- [x] An unreachable instance shows everything the API knows and says why the rest is
      missing — `running · no ssh`, `expiry unreadable`, `? no ssh` mounts, the ssh error
      in the jobs pane with a `rig adopt` hint (issue 05, decision 4).
- [ ] A job list of thirty runs buries its failures — surface failed runs or offer a
      filter. Open; the stress case found it, nothing is designed yet.

## Notes

Layout decided by prototype: drill-down. See ../spec.md. The prototype
(`internal/tui/prototype.go`, `cmd/rig-proto`) is deleted, as this issue asked; the
screens it settled are in `git log` and in the spec's transcripts.

## Comments

2026-09-09 (agent): Landed. Two checklist lines above were stale by the time they were
implemented and were superseded, not skipped: the two-line instance row (issue 05
decision 3 chose one line) and the `● Following` / `❙❙ Paused` log state (decision 2
made the pane a snapshot). Both are ticked against the decision that replaced them.

**Structure.** `internal/tui/theme.go` is the role palette and the `theme` type that
enforces "every cell of a banded row has a foreground"; `dashboard.go` is the model,
the levels and the data commands; `screens.go` renders the three levels and holds the
cell-text rules. The prototype and `cmd/rig-proto` are deleted, as this issue asked.

**One ssh per instance, not two.** `rig status` reads the deadline over ssh and a job
list reads the jobs; at 0.8s each the dashboard cannot pay that twice per instance per
refresh. New `remote.InstanceSnapshot` / `ParseSnapshot` ask for both in one round trip
(`cat deadline; echo --PROBE--; <jobsProbe>`), and `core.App.Snapshot` is the seam
above it. `App.JobLog`, `App.WatchdogLog` and `App.History` are the other three readers
the detail screens use.

**Progressive fill.** The survey paints, then one command per *running* instance fills
EXPIRES and JOBS; a stopped one is never asked. `markAsking` keeps the previous answer
across a refresh so a tick does not blank the counts, and `reselect` keeps the cursor on
the instance it was on rather than on an index that moved. Verified live: the overview
showed `EXPIRES …  JOBS …` and then `1h44m` / `1 running · 4 failed · 8 done`.

**The ssh half is injectable.** `dashModel.ask` and `.readLog` are fields, defaulted to
the fleet-backed calls, for the same reason `Fleet.Open` is: asking, answered and
refused are the three states that matter here, and none of them can be exercised
against a fake cloud otherwise.

**`w` turns split on**, only at 120 columns or wider, and a window that shrinks under it
drops back to one pane rather than clipping. It was on the checklist but had no way in
until now.

**Two bugs the live run found, neither visible to any test I would have written.**
An unrecorded run rendered its command as a lone `—`, which reads as a rendering fault
rather than as "this instance predates the job record"; it now says so. And `f` handed
off correctly but ctrl-C killed `rig` itself (exit 130) instead of returning to the
dashboard, because an interrupt reaches the whole foreground process group. Fixed with
`signal.Notify` — deliberately not `signal.Ignore`, since SIG_IGN is inherited across
exec and would have left ctrl-C doing nothing at all. Re-verified: exit 0, and the
dashboard repaints after the follower.

Tests: `internal/tui/dashboard_test.go` rewritten — the overview is instances and not
volumes, progressive fill, a stopped instance is never asked, an unreachable one keeps
what the API knows and says why the rest is missing, failures counted apart, drilling in
and back with both cursor and scroll preserved, `n`/`f`/`q` handing back the right
result, delete going to the row's own provider, split gating, the unrecorded run, the
footer per level, no verb ever clipped, and every line fitting at ten window sizes
across all three levels.

Still open, as recorded: a job list of thirty runs buries its failures.
