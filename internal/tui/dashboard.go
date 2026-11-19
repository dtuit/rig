package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dtuit/rig/internal/core"
	"github.com/dtuit/rig/internal/ledger"
	"github.com/dtuit/rig/internal/provider"
	"github.com/dtuit/rig/internal/remote"
)

// Outcome is what the user asked for on the way out. The dashboard never
// launches or follows anything itself: both need the plain terminal — the
// picker's forms, and `rig logs -f`, whose ssh attaches this process's
// stdout — so it reports the intent and its caller runs the flow and calls
// back in.
type Outcome int

const (
	// Quit: the user is done.
	Quit Outcome = iota
	// Create: run the launch flow, then reopen.
	Create
	// FollowLog: follow the log named by the Result, then reopen.
	FollowLog
)

// Result is the outcome plus what it applies to.
type Result struct {
	Outcome Outcome
	// Instance and Job name the log a FollowLog outcome wants followed.
	Instance *provider.Instance
	Job      remote.Job
}

// level is how deep the drill-down is: the estate, one instance, one run.
type level int

const (
	levelInstances level = iota
	levelInstance
	levelJob
)

// dashData is one round of provider answers — the API half, and what paints
// first. The ssh half arrives per instance afterwards.
type dashData struct {
	surveys   []core.Survey
	instances []provider.Instance
	volumes   map[string][]provider.Volume // provider -> that cloud's volumes
	loaded    bool
}

// footprint is what one instance's ssh round trip answered, or why it could
// not. asking is true from the API paint until the answer arrives, so the
// screen can say "…" rather than "none".
type footprint struct {
	asking bool
	snap   remote.Snapshot
	err    error
}

// logPane is one run's log as read. key guards against a stale read landing
// on a different run than the one now on screen.
type logPane struct {
	key    string
	asking bool
	lines  []string
	err    error
}

type dashModel struct {
	ctx     context.Context
	fleet   *core.Fleet
	several bool
	band    band
	split   bool // the wide-terminal two-pane mode

	// ask and readLog are how the dashboard reaches an instance. They are
	// fields, not calls, for the same reason Fleet.Open is: the ssh half
	// cannot be exercised against a fake cloud otherwise, and every state
	// that matters here — asking, answered, refused — is a state of these
	// two answering.
	ask     func(provider.Instance) (remote.Snapshot, error)
	readLog func(provider.Instance, remote.Job) ([]string, error)

	data  dashData
	feet  map[string]footprint // instance key -> its ssh answer
	log   logPane
	hist  []ledger.Event
	histK string

	level   level
	inst    int
	job     int
	tops    [3]int // saved viewport offset per level, so going back is free
	confirm string
	flash   string
	help    bool
	loading bool
	result  Result

	width, height int
	vp            viewport.Model
	ready         bool
}

// instKey identifies an instance across refreshes: two clouds can hold the
// same name, and an index moves as instances come and go.
func instKey(in provider.Instance) string { return in.Provider + "/" + in.Name }

type tickMsg time.Time

// footprintMsg is one instance's ssh answer, delivered on its own so a slow
// instance never holds up the others (issue 05, decision 1).
type footprintMsg struct {
	key  string
	snap remote.Snapshot
	err  error
}

type logMsg struct {
	key   string
	lines []string
	err   error
}

func fetch(ctx context.Context, f *core.Fleet) tea.Cmd {
	return func() tea.Msg { return newDashData(f.Survey(ctx)) }
}

func newDashData(surveys []core.Survey) dashData {
	d := dashData{surveys: surveys, volumes: map[string][]provider.Volume{}, loaded: true}
	for _, s := range surveys {
		d.instances = append(d.instances, s.Instances...)
		d.volumes[s.Provider] = s.Volumes
	}
	slices.SortFunc(d.instances, func(x, y provider.Instance) int {
		return y.CreatedAt.Compare(x.CreatedAt)
	})
	return d
}

// askFootprints asks every instance worth asking, one command each. A
// stopped instance is never asked: it cannot answer, and waiting out its
// connect timeout is worse than showing nothing.
func (m dashModel) askFootprints() tea.Cmd {
	var cmds []tea.Cmd
	for _, in := range m.data.instances {
		if in.Status != provider.Running {
			continue
		}
		in, key, ask := in, instKey(in), m.ask
		cmds = append(cmds, func() tea.Msg {
			snap, err := ask(in)
			return footprintMsg{key: key, snap: snap, err: err}
		})
	}
	return tea.Batch(cmds...)
}

func (m dashModel) askLog() tea.Cmd {
	in, j, ok := m.currentJob()
	if !ok {
		return nil
	}
	key, read := logKey(in, j), m.readLog
	return func() tea.Msg {
		lines, err := read(in, j)
		return logMsg{key: key, lines: lines, err: err}
	}
}

func logKey(in provider.Instance, j remote.Job) string {
	return fmt.Sprintf("%s/%s/%d", instKey(in), j.Name, j.Run)
}

func tick() tea.Cmd {
	return tea.Tick(15*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m dashModel) Init() tea.Cmd {
	return tea.Batch(fetch(m.ctx, m.fleet), tick())
}

// here is the selected instance, if there is one.
func (m dashModel) here() (provider.Instance, bool) {
	if m.inst < 0 || m.inst >= len(m.data.instances) {
		return provider.Instance{}, false
	}
	return m.data.instances[m.inst], true
}

func (m dashModel) foot() footprint {
	in, ok := m.here()
	if !ok {
		return footprint{}
	}
	return m.feet[instKey(in)]
}

// jobs is the selected instance's runs, newest first.
func (m dashModel) jobs() []remote.Job { return m.foot().snap.Jobs }

func (m dashModel) currentJob() (provider.Instance, remote.Job, bool) {
	in, ok := m.here()
	if !ok {
		return in, remote.Job{}, false
	}
	jobs := m.jobs()
	if m.job < 0 || m.job >= len(jobs) {
		return in, remote.Job{}, false
	}
	return in, jobs[m.job], true
}

func (m dashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height, m.ready = msg.Width, msg.Height, true
		if m.split && m.width < splitMinWidth {
			m.split, m.flash = false, "window too narrow for split panes"
		}
		return m.sync(true), nil

	case dashData:
		m.data = msg
		m.loading = false
		// keep the cursor on the instance it was on, not on the index
		m.inst = m.reselect(msg.instances)
		m.feet = m.markAsking(msg.instances)
		if m.job >= len(m.jobs()) {
			m.job = max(0, len(m.jobs())-1)
		}
		return m.sync(false), m.askFootprints()

	case footprintMsg:
		m.feet[msg.key] = footprint{snap: msg.snap, err: msg.err}
		if m.job >= len(m.jobs()) {
			m.job = max(0, len(m.jobs())-1)
		}
		return m.sync(false), nil

	case logMsg:
		if msg.key == m.log.key {
			m.log = logPane{key: msg.key, lines: msg.lines, err: msg.err}
		}
		return m.sync(false), nil

	case tickMsg:
		return m, tea.Batch(fetch(m.ctx, m.fleet), tick())

	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// reselect finds the instance the cursor was on in a fresh listing, so a
// refresh does not move the selection under the user.
func (m dashModel) reselect(fresh []provider.Instance) int {
	in, ok := m.here()
	if !ok {
		return 0
	}
	if i := slices.IndexFunc(fresh, func(x provider.Instance) bool {
		return instKey(x) == instKey(in)
	}); i >= 0 {
		return i
	}
	return min(m.inst, max(0, len(fresh)-1))
}

// markAsking keeps the answers a refresh does not invalidate and marks the
// running instances as being asked again, so the screen shows the last known
// counts rather than blanking on every tick.
func (m dashModel) markAsking(fresh []provider.Instance) map[string]footprint {
	next := make(map[string]footprint, len(fresh))
	for _, in := range fresh {
		key := instKey(in)
		prev, had := m.feet[key]
		switch {
		case in.Status != provider.Running:
			next[key] = footprint{} // never asked: it cannot answer
		case had:
			prev.asking = true
			next[key] = prev
		default:
			next[key] = footprint{asking: true}
		}
	}
	return next
}

func (m dashModel) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.result = Result{Outcome: Quit}
		return m, tea.Quit
	case "?":
		m.help = !m.help
		return m.sync(false), nil
	}
	if m.help { // any other key closes it
		m.help = false
		return m.sync(false), nil
	}
	if m.confirm != "" {
		switch msg.String() {
		case "y", "Y":
			action := m.confirm
			m.confirm, m.loading = "", true
			return m.sync(false), m.doAction(action)
		default:
			m.confirm, m.flash = "", "cancelled"
		}
		return m.sync(false), nil
	}

	switch msg.String() {
	case "up", "k":
		return m.move(-1), nil
	case "down", "j":
		return m.move(1), nil
	case "enter", "l", "right":
		return m.deeper()
	case "esc", "h", "left", "backspace":
		return m.shallower(), nil
	case "tab":
		// panes only mean something in split, and only where there are two
		if m.split && m.level < levelJob {
			if m.level == levelInstances && len(m.jobs()) > 0 {
				return m.enter(levelInstance), nil
			}
			return m.enter(levelInstances), nil
		}
	case "r":
		m.loading = true
		if m.level == levelJob {
			m.log.asking = true
			return m.sync(false), tea.Batch(fetch(m.ctx, m.fleet), m.askLog())
		}
		return m.sync(false), fetch(m.ctx, m.fleet)
	case "n":
		if m.level == levelInstances {
			m.result = Result{Outcome: Create}
			return m, tea.Quit
		}
	case "d":
		if m.level == levelInstances && len(m.data.instances) > 0 {
			m.confirm = "down"
			return m.sync(false), nil
		}
	case "f":
		if m.level == levelJob {
			if in, j, ok := m.currentJob(); ok {
				m.result = Result{Outcome: FollowLog, Instance: &in, Job: j}
				return m, tea.Quit
			}
		}
	case "s":
		if m.level == levelJob {
			if _, j, ok := m.currentJob(); ok {
				if !j.Running {
					m.flash = "that run has already finished"
					return m.sync(false), nil
				}
				m.confirm = "stop"
			}
			return m.sync(false), nil
		}
	case "w":
		if m.level >= levelJob {
			break
		}
		switch {
		case m.split:
			m.split, m.flash = false, "single pane"
		case m.width >= splitMinWidth:
			m.split, m.flash = true, "split panes · tab switches"
		default:
			m.flash = fmt.Sprintf("split needs %d columns; this window has %d", splitMinWidth, m.width)
		}
		return m.sync(true), nil
	case "g", "home":
		m.vp.GotoTop()
		return m, nil
	case "G", "end":
		m.vp.GotoBottom()
		return m, nil
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// move walks the list this level owns; the job level has no list, so the
// same keys scroll its log.
func (m dashModel) move(by int) dashModel {
	switch m.level {
	case levelInstances:
		next := m.inst + by
		if next >= 0 && next < len(m.data.instances) {
			m.inst, m.job = next, 0
		}
	case levelInstance:
		if n := len(m.jobs()); n > 0 {
			m.job = min(max(0, m.job+by), n-1)
		}
	case levelJob:
		if by > 0 {
			m.vp.LineDown(1)
		} else {
			m.vp.LineUp(1)
		}
		return m
	}
	return m.sync(true)
}

func (m dashModel) deeper() (tea.Model, tea.Cmd) {
	switch m.level {
	case levelInstances:
		if _, ok := m.here(); ok {
			return m.enter(levelInstance), nil
		}
	case levelInstance:
		if _, _, ok := m.currentJob(); ok {
			next := m.enter(levelJob)
			in, j, _ := next.currentJob()
			next.log = logPane{key: logKey(in, j), asking: true}
			return next.sync(false), next.askLog()
		}
	}
	return m, nil
}

func (m dashModel) shallower() dashModel {
	if m.level == levelInstances {
		return m
	}
	return m.enter(m.level - 1)
}

// enter changes level, remembering where this level was scrolled to and
// restoring where the next one was: the drill-down's one real cost is
// backtracking, and it is only tolerable if going back is free.
func (m dashModel) enter(to level) dashModel {
	m.tops[m.level] = m.vp.YOffset
	m.level, m.flash = to, ""
	m = m.sync(false)
	m.vp.SetYOffset(m.tops[to])
	return m
}

func (m dashModel) doAction(action string) tea.Cmd {
	fleet, ctx := m.fleet, m.ctx
	switch action {
	case "down":
		in, ok := m.here()
		if !ok {
			return nil
		}
		return func() tea.Msg {
			app, err := fleet.FindApp(in)
			if err == nil {
				_, err = app.Down(ctx, &in, false)
			}
			surveys := fleet.Survey(ctx)
			if err != nil {
				// the failure belongs to the cloud that refused it
				for i := range surveys {
					if surveys[i].Provider == in.Provider {
						surveys[i].Err = err
					}
				}
			}
			return newDashData(surveys)
		}
	case "stop":
		in, j, ok := m.currentJob()
		if !ok {
			return nil
		}
		key := instKey(in)
		return func() tea.Msg {
			app, err := fleet.FindApp(in)
			if err == nil {
				err = remote.StopJob(app.Target(&in), j.Name)
			}
			if err != nil {
				return footprintMsg{key: key, err: err}
			}
			snap, err := app.Snapshot(&in)
			return footprintMsg{key: key, snap: snap, err: err}
		}
	}
	return nil
}

// sync rebuilds the scrolling body for the current level and window. It runs
// in Update, not View, because the viewport has to be told its content and
// size before it renders. follow scrolls the selected row back into view,
// and only a cursor move asks for it: a refresh that yanked the body back to
// the cursor would undo the user's own scrolling under them.
func (m dashModel) sync(follow bool) dashModel {
	m.vp.Width = max(1, m.width)
	m.vp.Height = max(1, m.height-chromeHeight)
	body, cursorLine := m.body()
	m.vp.SetContent(strings.Join(body, "\n"))
	if follow && cursorLine >= 0 {
		m.vp = show(m.vp, cursorLine)
	}
	return m
}

// splitMinWidth is the narrowest window split is offered at. Below it the
// sidebar takes a quarter of the width from the columns that carry the
// content, and the prototype showed COMMAND disappearing entirely.
const splitMinWidth = 120

// chromeHeight is the fixed part of the screen the body does not get: the
// header, the blank line under it, and the footer.
const chromeHeight = 3

// show scrolls the viewport the least it has to for line to be visible.
func show(vp viewport.Model, line int) viewport.Model {
	switch {
	case line < vp.YOffset:
		vp.SetYOffset(line)
	case line >= vp.YOffset+vp.Height:
		vp.SetYOffset(line - vp.Height + 1)
	}
	return vp
}

func (m dashModel) View() string {
	if !m.ready {
		return "\n  starting ...\n"
	}
	out := append([]string{m.header(), ""}, strings.Split(m.vp.View(), "\n")...)
	out = append(out, m.footer())
	// A window too short for even the chrome keeps the top of what it has.
	if len(out) > m.height {
		out = out[:max(1, m.height)]
	}
	return strings.Join(out, "\n")
}

// Dashboard runs the overview and reports what the user asked for on the way
// out. Create and FollowLog are the caller's to act on (see Outcome).
func Dashboard(ctx context.Context, f *core.Fleet) (Result, error) {
	out, err := tea.NewProgram(newDashModel(ctx, f), tea.WithAltScreen()).Run()
	if err != nil {
		return Result{}, err
	}
	final, ok := out.(dashModel)
	if !ok {
		return Result{}, nil
	}
	return final.result, nil
}

func newDashModel(ctx context.Context, f *core.Fleet) dashModel {
	vp := viewport.New(80, 20)
	// The dashboard owns the keys that move the selection; the viewport is
	// left the ones that only move the window.
	vp.KeyMap.Up = key.NewBinding()
	vp.KeyMap.Down = key.NewBinding()
	vp.KeyMap.Left = key.NewBinding()
	vp.KeyMap.Right = key.NewBinding()
	vp.KeyMap.HalfPageUp = key.NewBinding(key.WithKeys("ctrl+u"))
	vp.KeyMap.HalfPageDown = key.NewBinding(key.WithKeys("ctrl+d"))
	themeName := ""
	if f.Cfg != nil {
		themeName = f.Cfg.TUI.Theme
	}
	return dashModel{
		ctx:     ctx,
		fleet:   f,
		several: len(f.Names()) > 1,
		band:    selectionBand(themeName),
		loading: true,
		feet:    map[string]footprint{},
		vp:      vp,
		ask: func(in provider.Instance) (remote.Snapshot, error) {
			app, err := f.FindApp(in)
			if err != nil {
				return remote.Snapshot{}, err
			}
			return app.Snapshot(&in)
		},
		readLog: func(in provider.Instance, j remote.Job) ([]string, error) {
			app, err := f.FindApp(in)
			if err != nil {
				return nil, err
			}
			return app.JobLog(&in, j, 500)
		},
	}
}
