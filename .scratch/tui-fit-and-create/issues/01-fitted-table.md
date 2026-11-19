# 01 — Columns that line up and fit the window

**Status:** resolved

- [x] Column widths come from the data, not from hand-counted `%-14s` formats.
- [x] Every cell is padded or truncated to its column, so no value shifts the columns after it.
- [x] A clipped value says so (ellipsis).
- [x] Columns are squeezed towards a min, then dropped in a declared order, so the widest row
      fits the terminal at any width.
- [x] Styling is applied after sizing, so colour never changes a width.

## Comments

2026-09-09 (agent): Landed as `internal/tui/table.go`: `column{title, min, drop, right}`,
`cell{text, style}` and `table.lines(width)`. `drop` is the order a column is given up in (1
first, 0 never); `widths` sizes from the values, squeezes the widest column towards its min,
then drops the next column in the drop order — and **re-sizes from natural after each drop**,
so losing LOC un-clips ENDPOINT instead of leaving both narrow. Below the mins of the columns
that never drop it squeezes past them, and past that it gives up mandatory columns from the
right, so the answer always fits. `clip`/`pad` count display columns (`lipgloss.Width`), so a
double-width rune costs two. The old `clip` in `picker.go` (rune-counting, no ellipsis budget)
is gone; the picker uses this one.

Tests: `internal/tui/table_test.go` — the columns of every row start at the same display
column as the header's for values that used to break the old layout; every line fits at nine
widths from 200 down to 1; the drop order is asserted at six widths (squeeze first: all five
columns survive at 46 and at 40, the sum of the mins); a drop hands its room to the survivors;
a clipped value carries an ellipsis; wide runes and both alignments.
