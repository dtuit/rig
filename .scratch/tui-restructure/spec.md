# TUI restructure: instances, then drill in

Asked 2026-09-09: the dashboard should list rig's instances; selecting one shows its
detail — attached storage, jobs — and selecting a job shows that job's detail (command,
logs). The global volume list leaves the top level: it is not interesting there.

## Prototype

`internal/tui/prototype.go` + `cmd/rig-proto` (both throwaway — delete once decided).

    go run ./cmd/rig-proto          # interactive; 1/2/3 switch layout live
    go run ./cmd/rig-proto -print   # dump every layout and level as text

Three layouts over identical fake data:

| # | layout | keeps the estate in view | width for commands and logs |
| - | ------ | ------------------------ | --------------------------- |
| 1 | drill-down: one level fills the screen, breadcrumb header, esc goes up | no | full |
| 2 | split: instance list left, selected instance's detail right | yes | two thirds, minus the rule |
| 3 | accordion: one list, the selected instance expands in place | yes | full, less the indent |

**Verdict: drill-down at ordinary widths, split available when there is room.**
Confirmed twice against the mockup. At 74–78 columns drill-down is the only layout that
keeps the COMMAND column and unclipped identifiers; split spends a quarter of the width
on context the breadcrumb already carries, and the accordion has the worst scaling
problem of the three — every expansion consumes the space needed to navigate elsewhere,
and at the job level one open log pushes the sibling jobs *and* the other instances off
screen. Split earns its keep only on a wide terminal, so it is a mode the user can turn
on above roughly 120 columns, not the default. The accordion is not being built.

## What the mockup changed about the design

First pass:

- The instances list gains **TTL** (`rig status` has it, the dashboard never did) and
  **JOBS** (`1 running, 2 done`) — the second being the signpost that there is anything
  to drill into at all.
- Per-instance **storage** beats the global volume list because it can answer the
  question the global one cannot: what happens to this disk when the instance goes.
- The job list wants RUN / JOB / STATUS / DURATION / COMMAND.

Second pass, from UX review of the printed screens:

- **Show fewer complete fields.** Eleven columns truncated nearly every identifier,
  headings included; `8xH10…` and `65.108.3…` cannot be acted on. The overview is now
  five columns — INSTANCE, STATUS, $/H, TTL, JOBS — with the type, endpoint, location
  and age on a second, dim line per row, in full. The vertical space was idle anyway.
- **Selection, focus and expansion are three things.** `▌` is the focused row of the
  pane taking keys, `│` the quieter remembered selection of a pane that is not, `▸`/`▾`
  expansion. Split levels 0 and 1 were indistinguishable before this despite different
  keyboard behaviour.
- **The footer is per level and names its object**: `d delete instance`, `s stop job`,
  `n new job`. It always offers the way back, and the job screen no longer advertises a
  selection it does not have.
- **The log gets the height** left after four lines of facts, and says which state it is
  in — `● Following · any scroll pauses` or `❙❙ Paused · line 184,219 of 184,223 · f
  resumes`. `f follow` never revealed which state you were in. Any scroll pauses;
  `f` resumes and jumps to the end.
- **Labels that needed interpretation**: `DURATION` with a `+` while still running (a
  column heading cannot change per row, but the detail screen says `elapsed` or
  `duration` outright); `failed · exit 1`, not `failed 1`; `ON RIG DOWN`, naming the
  action; the money scoped — `burn $27.51/h · 2 of 3 running` at level 0 and
  `$25.92/h this instance` once inside one, since a bare `$27.51/h` on a job screen said
  nothing about the job, the instance or the estate. A stopped instance's rate is
  parenthesised, `(1.05)`, so it cannot be read into the burn.
- **★ is gone.** It marked runs needing a job record; the record is built (issue 03), so
  runs are ordinary rows and a RUN column tells two runs of one name apart.
- **Expanded detail sits under its source**, not after the whole list.

Third pass, from review of the printed screens:

- **One line per instance row.** The appended second line was harder to parse than it
  was worth; the type, endpoint, location and age live in the instance detail, which is
  where "move the remainder into details" pointed in the first place.
- **The overview no longer hides a failure.** `1 running, 2 done` counted a failed job
  as done. It is now `1 running · 1 failed · 1 done`, urgency first, so a squeezed
  column loses "done" and keeps the bad news.
- **`Live` / `Viewing history`,** not Following/Paused — "Paused" read as though the
  *job* were paused. The history state says `24 new lines · f jump to latest`, so it
  explains both what is happening and what resuming will do, and it sheds the line
  numbers before it sheds the action.
- **The footer guarantees navigation and `? actions`.** It is the interaction guide, so
  it may not clip to `d delet…`; the rarer verbs live in a help panel.
- **Focus is a painted row**, not one glyph — `▌` against `│` was lost among the
  borders. Split names its panes and marks which has the keys.
- **Split gives the detail the full width at the job level** (the sidebar goes, so no
  pane hint can go missing) and log lines wrap rather than clip: a clipped warning hides
  what the job is waiting on, which is the reason to be reading it.
- **Wording**: spot moved into STATUS so `25.92*` needs no key; a stopped instance shows
  `—` rather than a parenthesised rate that invited being added to the burn; `age`, not
  `up`, which contradicted `stopped`; `IF INSTANCE DELETED`, naming the action; and the
  breadcrumb keeps the leaf whole, conceding the word "instances", then the money, then
  middle-clipping the instance name.

### Colour

The third pass made colour worse before it made it better: painting the focused row in
the accent colour destroyed the colours that row was carrying, so `failed · exit 1`
stopped being red on precisely the row being looked at. Two fixes.

**Selection is a background, not a foreground.** `table` gained `addBanded` (in
`internal/tui/table.go`, tested): a row style painted behind the row, gaps included and
carried to the full width so a selection does not stop mid-screen. `cell.render`
inherits it, so each cell keeps its own colour and gains only the band. Verified from
the real escape codes in a pty — the status cell on the selected row emits
`38;5;78;48;5;237`, green over the band, rather than being repainted. A second, fainter
band marks the row an inactive pane still remembers.

**The palette is roles, one hue each**: accent (identity, focus marker, active pane),
muted (context), label (column headings), and ok / warn / bad for what a status *means*
— green running and done, amber stopped, red failed and interrupted. `theme.status`
maps state to role in one place, so green means the same thing everywhere.

Two rules learned by getting them wrong, both worth carrying into the real TUI:

**A band must set a foreground on every cell.** Setting only a background leaves
unstyled cells using the terminal's own foreground — black on a light theme, invisible
on a dark band. Reported from a light-themed terminal, and it is a real bug, not a
preference: `theme` now hands out the styles for a row and has no way to produce a cell
without a foreground. Verified from the escape codes in a pty: all seven styled
segments of a banded row set both.

**Do not use AdaptiveColor for anything legibility depends on.** It asks the terminal
for its background colour and silently guesses when the terminal does not answer, which
is how a light-themed terminal drew a dark band in the first place. Every role is now a
fixed mid-tone that reads on white and on black, and the band is a dark blue with light
text — the choice editors make, legible either way — rather than a grey block that
depends on the guess being right. The real TUI should expose this as a setting
(`tui.theme: auto|light|dark`) rather than sniffing; `go run ./cmd/rig-proto -light`
previews the pale-band variant (bg 189 with dark text) to be judged in a light
terminal.

The shipped dashboard still uses the flat colours in `dashboard.go`. Adopting these
roles is on issue 01.

### Cost on the row

`$/H` alone did not answer the question actually being asked, which is what an instance
has cost. A `SPENT` column carries the total — rate times uptime for a running
instance, and for a stopped one the figure the ledger already recorded, rather than
multiplying an age that includes hours it was not billing. Whole dollars above $10
(`$156`), cents below (`$1.06`), and the header totals it: `burn $27.51/h · spent $171`.
The drop order was rebalanced so `SPENT` outlives `$/H`: a rate can be read off the
detail screen, a total cannot be recomputed from what is left on the row.

## What the stress case found

`go run ./cmd/rig-proto -stress` — realistic names, torchrun-length commands, 30 runs.
The review's prediction held: it revealed more than another layout round would have.

- **Tail-clipping destroys identity.** `verda/finetune-qwen3-32b-experim…`,
  `/mnt/rig-data-qwen3-checkp…` and three different commands all clipped to the same
  prefix. Names, mount paths and commands differ at the *end*. New `clipMid` and a
  `mid` column option in `internal/tui/table.go` (tested) clip out of the middle, so
  the overview reads `verda/finetune-q…periment-a-rerun` and the commands are told
  apart by `--seq-len 8192` vs `--batch-size 16` vs `--exclude '*.tmp'`.
- **The prototype listed runs oldest-first**, which real `ListJobs` does not. Fixed;
  worth knowing the mockup can lie about ordering.
- **Returning up a level does keep both cursor and scroll.** Driven in a pty: twelve
  `j` presses moved the window from `3–8 of 30` to `8–13 of 30`, enter opened the run,
  and esc came back still at `8–13`. That was the drill-down's one real cost, and it
  holds.
- **Still open:** 30 near-identical runs bury the four failures on page one of five.
  A job list this long wants either failures surfaced or a filter — recorded on
  issue 01, not designed here.

## Decisions (2026-09-09, from the prototype)

1. **Layout: drill-down**, with split as an opt-in mode on a wide terminal. The
   accordion is dropped.
2. **Detached volumes: both.** An "unattached storage" section closes the instance
   list — those are the volumes costing money for nothing, so they belong on the screen
   opened most — and `v` opens the full cross-cloud storage screen for an audit.
3. **Job history: build the record.** See `issues/03`. Retrying a job currently
   destroys its predecessor's evidence, so this is a fix as much as a feature.

## What job data exists today

Buildable now, from files the footprint already writes, in one ssh round trip:
name, running (tmux), exit code (`~/.rig/logs/<name>.exit`), the command
(`~/.rig/jobs/<name>.sh`), log size and tail, and start/finish from those files' mtimes.

Not buildable: **more than one run per job name.** `remote.StartJob` does
`rm -f ~/.rig/logs/<name>.exit && : > ~/.rig/logs/<name>.log` on every start, so
re-running a name destroys its predecessor's log and exit code. The mockup marks such
rows ★. Real history needs one new append-only record — `~/.rig/jobs.jsonl`, appended
by the job wrapper at start and at exit.
