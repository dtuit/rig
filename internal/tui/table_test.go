package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// cols is the shape the dashboard's instance table has, minus the marker:
// two columns that never go and three that go in drop order.
func cols() []column {
	return []column{
		{title: "NAME", min: 6},
		{title: "STATUS", min: 7},
		{title: "TYPE", min: 6, drop: 3},
		{title: "ENDPOINT", min: 9, drop: 4},
		{title: "LOC", min: 5, drop: 1},
	}
}

func row(vals ...string) []cell {
	out := make([]cell, len(vals))
	for i, v := range vals {
		out[i] = txt(v)
	}
	return out
}

// starts is the display column each cell of a rendered line begins at, found
// by walking the gaps — the property "the columns line up" is exactly that
// every line agrees on this list.
func starts(line string) []int {
	fields := []int{}
	at, inGap := 0, true
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		if runes[i] != ' ' {
			if inGap {
				fields = append(fields, at)
			}
			inGap = false
		} else if i+1 < len(runes) && runes[i+1] == ' ' {
			inGap = true
		}
		at += lipgloss.Width(string(runes[i]))
	}
	return fields
}

func TestColumnsLineUpWhateverTheValuesAre(t *testing.T) {
	tab := table{cols: cols()}
	tab.add(row("adhoc", "running", "1xA100-SXM", "10.0.0.1", "FIN-01")...)
	// the values that used to shift every column after them: a name past
	// the old %-14s, a type past %-16s, a container's host:port endpoint
	tab.add(row("a-very-long-instance-name", "provisioning", "8xH100-SXM-80GB-NVLINK", "213.192.2.102:40088", "ap-southeast-2b")...)
	tab.add(row("t", "running", "1xRTX-4090", "69.4.1.20", "US")...)

	lines := tab.lines(200)
	if len(lines) != 4 {
		t.Fatalf("want header + 3 rows, got %d lines:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	want := starts(stripANSI(lines[0]))
	if len(want) != 5 {
		t.Fatalf("header does not have 5 columns: %q -> %v", lines[0], want)
	}
	for i, line := range lines[1:] {
		if got := starts(stripANSI(line)); !equal(got, want) {
			t.Errorf("row %d starts at %v, header at %v\n%s", i, got, want, strings.Join(lines, "\n"))
		}
	}
}

func TestEveryLineFitsTheWidthItIsGiven(t *testing.T) {
	tab := table{cols: cols()}
	tab.add(row("adhoc", "running", "1xA100-SXM", "10.0.0.1", "FIN-01")...)
	tab.add(row("trainer-long-name", "provisioning", "8xH100-SXM-80GB", "213.192.2.102:40088", "ap-southeast-2b")...)

	for _, width := range []int{200, 100, 80, 60, 40, 24, 12, 6, 1} {
		for _, line := range tab.lines(width) {
			if w := lipgloss.Width(stripANSI(line)); w > width {
				t.Errorf("at width %d a line is %d wide: %q", width, w, line)
			}
		}
	}
}

func TestNarrowWindowSqueezesThenDropsInOrder(t *testing.T) {
	tab := table{cols: cols()}
	tab.add(row("adhoc", "running", "1xA100-SXM", "213.192.2.102:40088", "FIN-01")...)

	// Squeezing comes first: at 46 and even at 40 — the sum of the mins —
	// every column is still there, with ENDPOINT clipped. Below that LOC
	// (drop 1) is the first to go, then TYPE (3), then ENDPOINT (4); NAME
	// and STATUS have no drop order and stay to the end.
	for _, tc := range []struct {
		width int
		want  []string
		gone  []string
	}{
		{width: 200, want: []string{"NAME", "STATUS", "TYPE", "ENDPOINT", "LOC"}},
		{width: 46, want: []string{"NAME", "STATUS", "TYPE", "ENDPOINT", "LOC"}},
		{width: 40, want: []string{"NAME", "STATUS", "TYPE", "ENDPOINT", "LOC"}},
		{width: 39, want: []string{"NAME", "STATUS", "TYPE", "ENDPOINT"}, gone: []string{"LOC"}},
		{width: 32, want: []string{"NAME", "STATUS", "ENDPOINT"}, gone: []string{"LOC", "TYPE"}},
		{width: 24, want: []string{"NAME", "STATUS"}, gone: []string{"LOC", "TYPE", "ENDPOINT"}},
	} {
		head := stripANSI(tab.lines(tc.width)[0])
		for _, title := range tc.want {
			if !strings.Contains(head, title) {
				t.Errorf("width %d: %q lost %s", tc.width, head, title)
			}
		}
		for _, title := range tc.gone {
			if strings.Contains(head, title) {
				t.Errorf("width %d: %q still shows %s", tc.width, head, title)
			}
		}
	}
}

func TestDroppingAColumnHandsItsRoomToTheSurvivors(t *testing.T) {
	tab := table{cols: cols()}
	tab.add(row("adhoc", "running", "1xA100-SXM", "213.192.2.102:40088", "FIN-01")...)

	// At 40 — the sum of the mins — every column is on screen and ENDPOINT
	// is squeezed to keep LOC there. At 39 LOC goes, and the room it gives
	// up has to reach ENDPOINT: otherwise the table would be both a column
	// short and needlessly clipped.
	kept := fields(stripANSI(tab.lines(40)[1]))
	dropped := fields(stripANSI(tab.lines(39)[1]))
	if len(kept) != 5 || len(dropped) != 4 {
		t.Fatalf("want 5 cells at 40 and 4 at 39, got %v and %v", kept, dropped)
	}
	if lipgloss.Width(dropped[3]) <= lipgloss.Width(kept[3]) {
		t.Errorf("ENDPOINT is %q with LOC on screen and only %q without it",
			kept[3], dropped[3])
	}
}

func TestASqueezedValueIsMarkedAsCut(t *testing.T) {
	tab := table{cols: cols()}
	tab.add(row("a-very-long-instance-name", "running", "1xA100-SXM", "10.0.0.1", "FIN-01")...)
	line := stripANSI(tab.lines(20)[1])
	if !strings.Contains(line, "…") {
		t.Errorf("a clipped row does not say so: %q", line)
	}
	if strings.Contains(line, "instance-name") {
		t.Errorf("row was not clipped at all: %q", line)
	}
}

func TestAnEmptyTableRendersNothing(t *testing.T) {
	if lines := (table{cols: cols()}).lines(80); lines != nil {
		t.Errorf("want no lines for no rows, got %q", lines)
	}
}

func TestClipCountsDisplayColumns(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"short", "short"},
		{"exactly-8", "exactly…"},
	} {
		if got := clip(tc.in, 8); got != tc.want {
			t.Errorf("clip(%q, 8) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := clip("anything", 0); got != "" {
		t.Errorf("clip to 0 = %q, want empty", got)
	}
	// a double-width rune counts as the two columns it occupies, so the
	// clip lands under the budget rather than one column over it
	if got := lipgloss.Width(clip("日本語です", 4)); got > 4 {
		t.Errorf("clip of wide runes to 4 renders %d columns", got)
	}
}

func TestPadFillsAndAligns(t *testing.T) {
	if got := pad("x", 4, false); got != "x   " {
		t.Errorf("left-aligned pad = %q", got)
	}
	if got := pad("x", 4, true); got != "   x" {
		t.Errorf("right-aligned pad = %q", got)
	}
}

// stripANSI removes the styling so a test can measure and read the text.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// fields splits a rendered line into its cells, on the gap between columns.
func fields(line string) []string {
	var out []string
	for _, f := range strings.Split(line, strings.Repeat(" ", gap)) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Names, mount paths and long commands differ at the end, so a column that
// clips the tail leaves rows that look identical. clipMid keeps both ends.
func TestClipMidKeepsBothEnds(t *testing.T) {
	const name = "verda/finetune-qwen3-32b-experiment-a-rerun"
	got := clipMid(name, 30)
	if lipgloss.Width(got) > 30 {
		t.Errorf("clipMid = %q, %d wide", got, lipgloss.Width(got))
	}
	if !strings.HasPrefix(got, "verda/") || !strings.HasSuffix(got, "-a-rerun") {
		t.Errorf("clipMid = %q, want both ends kept", got)
	}
	// two names differing only at the end must stay distinguishable
	a := clipMid("finetune-qwen3-32b-experiment-a", 20)
	b := clipMid("finetune-qwen3-32b-experiment-b", 20)
	if a == b {
		t.Errorf("both names clip to %q", a)
	}
	if got := clipMid("short", 30); got != "short" {
		t.Errorf("a value that fits was changed: %q", got)
	}
	// too narrow to keep both ends falls back to a tail clip
	if got := clipMid("abcdefgh", 4); lipgloss.Width(got) > 4 {
		t.Errorf("clipMid to 4 = %q", got)
	}
}

func TestMidColumnClipsFromTheMiddle(t *testing.T) {
	tab := table{cols: []column{{title: "NAME", min: 6, mid: true}, {title: "STATUS", min: 7}}}
	tab.add(txt("finetune-qwen3-32b-experiment-a-rerun"), txt("running"))
	line := stripANSI(tab.lines(28)[1])
	if !strings.Contains(line, "-a-rerun") {
		t.Errorf("a mid column clipped its tail: %q", line)
	}
}

// A band is a background, so it must not disturb the text or the widths —
// selection that shifted a column would be worse than no selection at all.
func TestBandedRowKeepsItsTextAndWidth(t *testing.T) {
	plain := table{cols: cols()}
	plain.add(row("adhoc", "running", "1xA100-SXM", "10.0.0.1", "FIN-01")...)

	banded := table{cols: cols()}
	banded.addBanded(lipgloss.NewStyle().Background(lipgloss.Color("237")),
		row("adhoc", "running", "1xA100-SXM", "10.0.0.1", "FIN-01")...)

	want := stripANSI(plain.lines(80)[1])
	got := strings.TrimRight(stripANSI(banded.lines(80)[1]), " ")
	if got != strings.TrimRight(want, " ") {
		t.Errorf("banded row reads %q, plain reads %q", got, want)
	}
	if !equal(starts(got), starts(want)) {
		t.Errorf("banded row shifted the columns: %v vs %v", starts(got), starts(want))
	}
	// the band runs the full width so the selection does not stop mid-screen
	if w := lipgloss.Width(stripANSI(banded.lines(80)[1])); w != 80 {
		t.Errorf("banded row is %d wide, want the full 80", w)
	}
}
