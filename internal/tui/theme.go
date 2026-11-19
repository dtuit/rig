package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The palette is roles, not colours, and each role is one hue: accent for
// identity and focus, ok/warn/bad for what a status means, muted for
// context, label for column headings. Two rules, both learned by getting
// them wrong in the prototype:
//
// It does not use lipgloss.AdaptiveColor. That asks the terminal for its
// background colour and silently guesses when the terminal does not answer,
// which drew a dark selection band in a light-themed terminal. Every role
// here is a mid-tone that reads on white and on black, so legibility never
// depends on the guess; the band, which cannot be neutral, is a setting
// (config tui.theme) rather than a sniff.
//
// And a banded row sets a foreground on every cell. A background alone
// leaves unstyled cells on the terminal's own foreground — black on a light
// theme, invisible against a dark band. The theme type below is the
// enforcement: it hands out the styles for a row and has no way to produce
// a cell without one.
var (
	cAccent = lipgloss.Color("170") // magenta, mid-tone
	cMuted  = lipgloss.Color("245")
	cLabel  = lipgloss.Color("244")
	cOK     = lipgloss.Color("35")
	cWarn   = lipgloss.Color("172")
	cBad    = lipgloss.Color("167")

	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	dimStyle    = lipgloss.NewStyle().Foreground(cMuted)
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(cLabel)
	okStyle     = lipgloss.NewStyle().Foreground(cOK)
	warnStyle   = lipgloss.NewStyle().Foreground(cWarn)
	errStyle    = lipgloss.NewStyle().Foreground(cBad)
	paneOnStyle = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
)

// band is one selection palette: the background, and the foregrounds that
// read against it.
type band struct {
	bg, fg, dim, ok, warn, bad lipgloss.Color
}

var (
	darkBand = band{
		bg: lipgloss.Color("24"), fg: lipgloss.Color("255"), dim: lipgloss.Color("252"),
		ok: lipgloss.Color("120"), warn: lipgloss.Color("222"), bad: lipgloss.Color("210"),
	}
	lightBand = band{
		bg: lipgloss.Color("189"), fg: lipgloss.Color("235"), dim: lipgloss.Color("240"),
		ok: lipgloss.Color("22"), warn: lipgloss.Color("94"), bad: lipgloss.Color("124"),
	}
)

// selectionBand answers the band for a config theme setting. Anything other
// than "light" gets the dark band: it is the one that reads against most
// terminals, and an unrecognised value must not leave the UI unpainted.
func selectionBand(theme string) band {
	if strings.EqualFold(strings.TrimSpace(theme), "light") {
		return lightBand
	}
	return darkBand
}

// theme is the palette one row is drawn with: the plain one, or a
// selection band's. Every cell of a row comes from here.
type theme struct {
	rowStyle *lipgloss.Style // the band behind the row, nil for an ordinary one
	plain    lipgloss.Style
	dim      lipgloss.Style
	ok       lipgloss.Style
	warn     lipgloss.Style
	bad      lipgloss.Style
}

// plainTheme is an unselected row: it keeps the terminal's own foreground,
// which is legible on the terminal's own background by definition.
func plainTheme() theme {
	return theme{
		plain: lipgloss.NewStyle(), dim: dimStyle,
		ok: okStyle, warn: warnStyle, bad: errStyle,
	}
}

// bandTheme is a selected row: focused rows get the band, and the row an
// unfocused pane still remembers gets a quieter one, so the list always
// says which instance the detail belongs to.
func bandTheme(b band, focused bool) theme {
	bg := b.bg
	if !focused {
		bg = lipgloss.Color("236")
		if b.bg == lightBand.bg {
			bg = lipgloss.Color("254")
		}
	}
	on := func(c lipgloss.Color) lipgloss.Style {
		return lipgloss.NewStyle().Background(bg).Foreground(c)
	}
	row := on(b.fg)
	return theme{
		rowStyle: &row,
		plain:    on(b.fg), dim: on(b.dim),
		ok: on(b.ok), warn: on(b.warn), bad: on(b.bad),
	}
}

// txt is a plain cell in this row's palette — never an unstyled one.
func (t theme) txt(s string) cell { return painted(s, t.plain) }

func (t theme) dimmed(s string) cell { return painted(s, t.dim) }

// state paints a status by what it means, so green, amber and red say the
// same thing on every screen, banded or not.
func (t theme) state(s string) cell {
	switch {
	case strings.HasPrefix(s, "running"), s == "done", s == "kept":
		return painted(s, t.ok)
	case strings.HasPrefix(s, "failed"), s == "interrupted", strings.Contains(s, "no ssh"):
		return painted(s, t.bad)
	default:
		return painted(s, t.warn)
	}
}

// addRow adds a row in its own theme: the focus marker, the cells, and the
// band behind all of it when the row is selected. `▌` is the focused row of
// the pane taking keys and `│` the remembered row of one that is not —
// reinforced by the band, because a single glyph was lost among the borders.
func addRow(t *table, th theme, selected, focused bool, cells ...cell) {
	marker := txt(" ")
	switch {
	case selected && focused:
		marker = painted("▌", th.plain.Bold(true))
	case selected:
		marker = painted("│", th.dim)
	}
	all := append([]cell{marker}, cells...)
	if th.rowStyle != nil {
		t.addBanded(*th.rowStyle, all...)
		return
	}
	t.add(all...)
}
