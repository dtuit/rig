package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// gap is the blank space between two columns.
const gap = 2

// column describes one column of a fitted table. Width is not fixed: it is
// the widest value the column actually holds, squeezed towards min and then
// dropped altogether if the terminal is too narrow for it.
type column struct {
	title string
	// min is the narrowest the column is worth showing at; it is truncated
	// down to min before being considered for dropping.
	min int
	// drop is the order the column is given up in when the window is too
	// narrow for every column: 1 goes first, then 2, and so on. 0 never
	// goes — the table is unreadable without it.
	drop int
	// right right-aligns the column, for the numbers it reads best on.
	right bool
	// mid clips out of the middle rather than the end, for values whose
	// distinguishing part is at the end: two instances called
	// finetune-qwen3-32b-experiment-a and -b, a mount path under a long
	// parent, a torchrun line whose arguments are what differ. Clipping the
	// tail off those leaves rows that look identical.
	mid bool
}

// cell is one value plus how to paint it. style is applied after the value
// is padded or truncated, so colour never changes a column's width.
type cell struct {
	text  string
	style *lipgloss.Style
}

func txt(s string) cell { return cell{text: s} }

func painted(s string, st lipgloss.Style) cell { return cell{text: s, style: &st} }

// render sizes the cell and paints it: its own style over the row's band,
// so a banded row keeps each cell's meaning-carrying colour and gains only
// the background. Inherit copies only what the cell leaves unset.
func (c cell) render(w int, right, mid bool, band *lipgloss.Style) string {
	text := c.text
	if mid {
		text = clipMid(text, w)
	}
	s := pad(text, w, right)
	switch {
	case c.style != nil && band != nil:
		return c.style.Inherit(*band).Render(s)
	case c.style != nil:
		return c.style.Render(s)
	case band != nil:
		return band.Render(s)
	}
	return s
}

// table lays rows out under headers so that the columns line up whatever
// the values are and whatever the terminal is: every cell is padded or
// truncated to its column's width, which is why one long instance name
// cannot shift the columns after it, and the whole table is fitted to the
// width it is given rather than running off the side of the window.
type table struct {
	cols []column
	rows [][]cell
	// bands paints whole rows, gaps included, by row index. Selection has
	// to be a background: painting the row's foreground instead destroys
	// the colours the row is carrying — the red on a failed job, the green
	// on a running one — on precisely the row being looked at.
	bands map[int]lipgloss.Style
}

func (t *table) add(cells ...cell) { t.rows = append(t.rows, cells) }

// addBanded adds a row painted with band behind it.
func (t *table) addBanded(band lipgloss.Style, cells ...cell) {
	if t.bands == nil {
		t.bands = map[int]lipgloss.Style{}
	}
	t.bands[len(t.rows)] = band
	t.rows = append(t.rows, cells)
}

// lines renders the header followed by one line per row, each at most width
// display columns wide. An empty table renders as nothing at all — not even
// a header, since a header over no rows says less than the caller's own
// "nothing here" line does.
func (t table) lines(width int) []string {
	if len(t.rows) == 0 {
		return nil
	}
	w := t.widths(width)
	out := make([]string, 0, len(t.rows)+1)
	head := make([]cell, len(t.cols))
	for i, c := range t.cols {
		head[i] = txt(c.title)
	}
	out = append(out, headerStyle.Render(t.line(head, w, false, nil)))
	for i, r := range t.rows {
		var band *lipgloss.Style
		if b, ok := t.bands[i]; ok {
			band = &b
		}
		ln := t.line(r, w, true, band)
		if band != nil {
			// carry the band to the full width, or a selection stops
			// mid-screen and reads as unfinished
			ln += band.Render(strings.Repeat(" ", max(0, width-lipgloss.Width(ln))))
		}
		out = append(out, ln)
	}
	return out
}

// line renders one row at the given widths, dropped columns skipped. paint
// is false for the header, which is styled as a whole.
//
// Nothing is clipped here: every cell is already sized to its column, and
// clipping a painted line would cut an escape sequence in half. Fitting the
// window is widths' job, and it always answers with widths that fit.
func (t table) line(cells []cell, widths []int, paint bool, band *lipgloss.Style) string {
	var b strings.Builder
	first := true
	for i, w := range widths {
		if w < 0 || i >= len(cells) {
			continue
		}
		if !first {
			// the gaps are part of the band, or it would stripe
			if band != nil {
				b.WriteString(band.Render(strings.Repeat(" ", gap)))
			} else {
				b.WriteString(strings.Repeat(" ", gap))
			}
		}
		first = false
		c := cells[i]
		if !paint {
			c.style = nil
		}
		b.WriteString(c.render(w, t.cols[i].right, t.cols[i].mid, band))
	}
	if band != nil {
		return b.String() // trailing pad carries the band; do not trim it
	}
	return strings.TrimRight(b.String(), " ")
}

// widths sizes every column to fit total display width, returning -1 for a
// column that has to go. The concessions are made in the order that keeps
// the most readable table: every column at the width its values want if
// they fit, else the columns squeezed towards their min (a clipped endpoint
// still identifies an instance), and only when even that will not do is a
// column given up — the one furthest down the drop order. Every drop hands
// its room back to the survivors, so losing LOC un-clips ENDPOINT rather
// than leaving both narrow.
func (t table) widths(total int) []int {
	gone := make([]bool, len(t.cols))
	for {
		w := t.natural(gone)
		squeezeToward(t.cols, w, total, true)
		if lineWidth(w) <= total {
			return w
		}
		if i := droppable(t.cols, w); i >= 0 {
			gone[i] = true
			continue
		}
		// Narrower than the mins of the columns that never drop. Give up
		// those mins first, and only if a single character each still will
		// not fit, give up the columns themselves from the right — so the
		// answer is always something that fits, however absurd the window.
		squeezeToward(t.cols, w, total, false)
		if lineWidth(w) <= total {
			return w
		}
		if i := lastKept(w); i >= 0 && kept(w) > 1 {
			gone[i] = true
			continue
		}
		return w
	}
}

// natural is each surviving column at the width its widest value wants.
func (t table) natural(gone []bool) []int {
	w := make([]int, len(t.cols))
	for i, c := range t.cols {
		if gone[i] {
			w[i] = -1
			continue
		}
		w[i] = lipgloss.Width(c.title)
		for _, r := range t.rows {
			if i < len(r) {
				w[i] = max(w[i], lipgloss.Width(r[i].text))
			}
		}
	}
	return w
}

// squeezeToward narrows the widest column one at a time until the line fits
// total. respectMin stops at each column's min; without it the floor is a
// single character.
func squeezeToward(cols []column, w []int, total int, respectMin bool) {
	for lineWidth(w) > total {
		widest := -1
		for i := range cols {
			floor := 1
			if respectMin {
				floor = max(cols[i].min, 1)
			}
			if w[i] < 0 || w[i] <= floor {
				continue
			}
			if widest < 0 || w[i] > w[widest] {
				widest = i
			}
		}
		if widest < 0 {
			return
		}
		w[widest]--
	}
}

// lineWidth is what widths w render to, gaps included.
func lineWidth(w []int) int {
	total, shown := 0, 0
	for _, x := range w {
		if x < 0 {
			continue
		}
		total += x
		shown++
	}
	if shown > 1 {
		total += (shown - 1) * gap
	}
	return total
}

// droppable is the kept column next in line to go: the lowest drop order
// still on screen. -1 when only columns that never go are left.
func droppable(cols []column, w []int) int {
	next := -1
	for i, c := range cols {
		if w[i] < 0 || c.drop == 0 {
			continue
		}
		if next < 0 || c.drop < cols[next].drop {
			next = i
		}
	}
	return next
}

func kept(w []int) int {
	n := 0
	for _, x := range w {
		if x >= 0 {
			n++
		}
	}
	return n
}

func lastKept(w []int) int {
	for i := len(w) - 1; i >= 0; i-- {
		if w[i] >= 0 {
			return i
		}
	}
	return -1
}

// clip shortens s to at most n display columns, marking the cut with an
// ellipsis so a truncated value cannot be mistaken for a short one.
func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > n {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// clipMid shortens s to n display columns by removing the middle, keeping
// both ends: the head says what kind of thing it is, the tail is usually
// what distinguishes it from its neighbours.
func clipMid(s string, n int) string {
	if n <= 0 || lipgloss.Width(s) <= n {
		return s
	}
	if n < 5 {
		return clip(s, n)
	}
	r := []rune(s)
	keep := n - 1 // the ellipsis
	tail := keep / 2
	head := keep - tail
	for lipgloss.Width(string(r[:head]))+lipgloss.Width(string(r[len(r)-tail:]))+1 > n {
		if head > tail {
			head--
		} else {
			tail--
		}
	}
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// pad renders s in exactly n display columns: clipped when too long, spaced
// out when too short, on whichever side the alignment calls for.
func pad(s string, n int, right bool) string {
	s = clip(s, n)
	fill := n - lipgloss.Width(s)
	if fill <= 0 {
		return s
	}
	if right {
		return strings.Repeat(" ", fill) + s
	}
	return s + strings.Repeat(" ", fill)
}
