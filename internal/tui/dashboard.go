package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dtuit/rig/internal/core"
	"github.com/dtuit/rig/internal/provider"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("240"))
	selStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	runStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	offStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).MarginTop(1)
)

type dashData struct {
	instances []provider.Instance
	volumes   []provider.Volume
	balance   string
	err       error
}

type dashModel struct {
	app      *core.App
	ctx      context.Context
	data     dashData
	cursor   int
	confirm  string // pending action: "down" or ""
	flash    string
	loading  bool
}

type tickMsg time.Time

func fetch(ctx context.Context, a *core.App) tea.Cmd {
	return func() tea.Msg {
		var d dashData
		d.instances, d.err = a.Managed(ctx)
		if d.err != nil {
			return d
		}
		d.volumes, _ = a.Prov.ListVolumes(ctx)
		if amt, cur, err := a.Prov.Balance(ctx); err == nil {
			d.balance = fmt.Sprintf("%.2f %s", amt, cur)
		}
		return d
	}
}

func tick() tea.Cmd {
	return tea.Tick(15*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m dashModel) Init() tea.Cmd {
	return tea.Batch(fetch(m.ctx, m.app), tick())
}

func (m dashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case dashData:
		m.data = msg
		m.loading = false
		if m.cursor >= len(m.data.instances) {
			m.cursor = max(0, len(m.data.instances)-1)
		}
		return m, nil
	case tickMsg:
		return m, tea.Batch(fetch(m.ctx, m.app), tick())
	case tea.KeyMsg:
		if m.confirm != "" {
			switch msg.String() {
			case "y", "Y":
				action := m.confirm
				m.confirm = ""
				return m, m.doAction(action)
			default:
				m.confirm = ""
				m.flash = "cancelled"
			}
			return m, nil
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.data.instances)-1 {
				m.cursor++
			}
		case "r":
			m.loading = true
			return m, fetch(m.ctx, m.app)
		case "d":
			if len(m.data.instances) > 0 {
				m.confirm = "down"
			}
		}
	}
	return m, nil
}

func (m dashModel) doAction(action string) tea.Cmd {
	in := m.data.instances[m.cursor]
	app := m.app
	ctx := m.ctx
	return func() tea.Msg {
		switch action {
		case "down":
			if err := app.Down(ctx, &in, false); err != nil {
				return dashData{err: err}
			}
		}
		var d dashData
		d.instances, d.err = app.Managed(ctx)
		d.volumes, _ = app.Prov.ListVolumes(ctx)
		return d
	}
}

func (m dashModel) View() string {
	var b strings.Builder
	title := "rig"
	if m.data.balance != "" {
		title += dimStyle.Render("  balance " + m.data.balance)
	}
	if m.loading {
		title += dimStyle.Render("  refreshing...")
	}
	b.WriteString(titleStyle.Render("rig") + dimStyle.Render(strings.TrimPrefix(title, "rig")) + "\n\n")

	if m.data.err != nil {
		b.WriteString(errStyle.Render("error: "+m.data.err.Error()) + "\n")
	}

	b.WriteString(headerStyle.Render(fmt.Sprintf("  %-16s %-12s %-18s %-15s %-7s %-9s %s",
		"NAME", "STATUS", "TYPE", "IP", "LOC", "$/H", "AGE")) + "\n")
	if len(m.data.instances) == 0 {
		b.WriteString(dimStyle.Render("  no rig instances — rig up to create one") + "\n")
	}
	var burn float64
	for i, in := range m.data.instances {
		cursor := "  "
		if i == m.cursor {
			cursor = selStyle.Render("> ")
		}
		status := in.Status
		switch in.Status {
		case "running":
			status = runStyle.Render(fmt.Sprintf("%-12s", status))
			burn += in.PricePerHour
		case "offline", "provisioning", "ordered":
			status = offStyle.Render(fmt.Sprintf("%-12s", status))
		default:
			status = fmt.Sprintf("%-12s", status)
		}
		price := fmt.Sprintf("%.2f", in.PricePerHour)
		if in.Spot {
			price += " spot"
		}
		b.WriteString(fmt.Sprintf("%s%-16s %s %-18s %-15s %-7s %-9s %s\n",
			cursor, strings.TrimPrefix(in.Name, core.HostPrefix), status, in.Type,
			in.IP, in.Location, price, ageShort(in.CreatedAt)))
	}
	if burn > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("\n  burn rate $%.2f/h ($%.0f/day)", burn, burn*24)) + "\n")
	}

	if len(m.data.volumes) > 0 {
		b.WriteString("\n" + headerStyle.Render(fmt.Sprintf("  %-16s %-8s %-12s %-7s %s",
			"VOLUME", "SIZE", "TYPE", "LOC", "ATTACHED")) + "\n")
		for _, v := range m.data.volumes {
			att := "-"
			if v.IsAttached() {
				att = "yes"
			}
			b.WriteString(fmt.Sprintf("  %-16s %-8s %-12s %-7s %s\n",
				v.Name, fmt.Sprintf("%dGB", v.SizeGB), v.Type, v.Location, att))
		}
	}

	if m.confirm != "" {
		in := m.data.instances[m.cursor]
		b.WriteString("\n" + errStyle.Render(fmt.Sprintf("delete %s? volumes are kept. [y/N]", in.Name)))
	} else {
		if m.flash != "" {
			b.WriteString("\n" + dimStyle.Render(m.flash))
		}
		b.WriteString(helpStyle.Render("\n  ↑/↓ select · d delete · r refresh · q quit"))
	}
	return b.String()
}

func ageShort(t time.Time) string {
	d := time.Since(t)
	switch {
	case d > 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d > time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

// Dashboard runs the full-screen overview UI.
func Dashboard(ctx context.Context, a *core.App) error {
	m := dashModel{app: a, ctx: ctx, loading: true}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
