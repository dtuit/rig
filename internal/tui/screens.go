package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/dtuit/rig/internal/core"
	"github.com/dtuit/rig/internal/provider"
	"github.com/dtuit/rig/internal/remote"
)

// body renders the current level and says which body line the cursor is on,
// so sync can scroll it into view. -1 means this level has no cursor.
func (m dashModel) body() ([]string, int) {
	switch {
	case m.help:
		return m.helpLines(), -1
	case m.split && m.level < levelJob:
		return m.splitBody()
	case m.level == levelInstance:
		return m.instanceBody(m.width)
	case m.level == levelJob:
		return m.jobBody(m.width), -1
	}
	return m.instancesBody(m.width, true)
}

// instancesBody is the estate: one line per instance, five complete columns.
// Type, endpoint and location are in the instance detail — an operator
// cannot act on a truncated endpoint, so nothing here is truncated to make
// room for them.
func (m dashModel) instancesBody(width int, focused bool) ([]string, int) {
	t := table{cols: []column{
		{title: "", min: 1},
		{title: "INSTANCE", min: 8, mid: true},
		{title: "STATUS", min: 7},
		{title: "$/H", min: 5, drop: 2, right: true},
		{title: "SPENT", min: 5, drop: 5, right: true},
		{title: "EXPIRES", min: 5, drop: 3, right: true},
		{title: "JOBS", min: 4, drop: 4},
	}}
	for i, in := range m.data.instances {
		f := m.feet[instKey(in)]
		th := m.themeFor(i == m.inst, focused)
		addRow(&t, th, i == m.inst, focused,
			th.txt(shortName(in, m.several)),
			th.state(statusText(in, f)),
			th.txt(rateText(in)),
			th.txt(spentText(in, m.spentOf(in))),
			th.txt(expiryText(in, f)),
			th.txt(jobsText(in, f)))
	}
	lines := t.lines(width)
	if len(lines) == 0 {
		if !m.data.loaded {
			return []string{dimStyle.Render("  asking " + strings.Join(m.fleet.Names(), ", ") + " ...")}, -1
		}
		lines = []string{dimStyle.Render(clip("  no rig instances — n to create one", width))}
	}
	// A cloud that could not be asked says so on its own line and leaves the
	// others' rows alone.
	for _, s := range m.data.surveys {
		if s.Err != nil {
			lines = append(lines, errStyle.Render(clip(fmt.Sprintf("  %s · %v", s.Provider, s.Err), width)))
		}
	}
	cursor := -1
	if len(m.data.instances) > 0 {
		cursor = 1 + m.inst // the rows start under their header
	}
	return lines, cursor
}

// instanceBody is one instance: what it is, its storage, and its runs.
func (m dashModel) instanceBody(width int) ([]string, int) {
	in, ok := m.here()
	if !ok {
		return []string{dimStyle.Render("  no instance selected")}, -1
	}
	out := m.instanceFacts(in, width)
	out = append(out, "")
	out = append(out, m.storageLines(in, width)...)
	out = append(out, "", headerStyle.Render("  JOBS"))
	jobs, cursor := m.jobLines(in, width, true)
	return append(out, jobs...), cursor + len(out)
}

// instanceFacts is everything about an instance that needs no ssh, which is
// why an unreachable instance still shows it all.
func (m dashModel) instanceFacts(in provider.Instance, width int) []string {
	f := m.feet[instKey(in)]
	price := "on-demand"
	if in.Spot {
		price = "spot"
	}
	first := fmt.Sprintf("  %s · %s · %s · %s", in.Type, price, in.Endpoint().String(), in.Location)
	second := fmt.Sprintf("  image %s · age %s · spent %s · %s",
		orDash(in.Image), ageShort(in.CreatedAt), spentText(in, m.spentOf(in)), expiryPhrase(in, f))
	out := wrap(first, width, "    ")
	for i := range out {
		out[i] = dimStyle.Render(out[i])
	}
	return append(out, dimStyle.Render(clip(second, width)))
}

// storageLines is the instance's own volumes. Name, size, type and fate all
// come from the provider API, so they show whether or not ssh works; the
// mount point needs the instance and lands with issue 02.
func (m dashModel) storageLines(in provider.Instance, width int) []string {
	t := table{cols: []column{
		{title: "", min: 1},
		{title: "STORAGE", min: 6, mid: true},
		{title: "SIZE", min: 4, drop: 3, right: true},
		{title: "TYPE", min: 4, drop: 2},
		// the header names the action, so "kept" is kept from what
		{title: "IF INSTANCE DELETED", min: 4, drop: 1},
	}}
	th := plainTheme()
	for _, v := range m.data.volumes[in.Provider] {
		if !slices.Contains(v.InstanceIDs, in.ID) && v.ID != in.OSVolumeID {
			continue
		}
		kind := v.Type
		fate := th.state("kept")
		if v.ID == in.OSVolumeID {
			kind += " (OS)"
			fate = th.state("deleted")
		}
		t.add(txt(" "), txt(v.Name), txt(fmt.Sprintf("%dGB", v.SizeGB)), txt(kind), fate)
	}
	if len(t.rows) == 0 {
		return []string{dimStyle.Render(clip("   no storage attached", width))}
	}
	return t.lines(width)
}

// jobLines lists the runs, and says why it cannot when it cannot: an
// instance that could not be asked must not read as one with nothing
// running. "No jobs" invites n; a failure invites rig adopt.
func (m dashModel) jobLines(in provider.Instance, width int, focused bool) ([]string, int) {
	f := m.feet[instKey(in)]
	switch {
	case f.err != nil:
		out := wrap("   could not ask this instance: "+f.err.Error(), width, "     ")
		for i := range out {
			out[i] = errStyle.Render(out[i])
		}
		short := strings.TrimPrefix(in.Name, core.HostPrefix)
		return append(out, dimStyle.Render(clip("   rig adopt "+short+" repairs the footprint", width))), -1
	case f.asking && len(f.snap.Jobs) == 0:
		return []string{dimStyle.Render(clip("   asking the instance ...", width))}, -1
	case in.Status != provider.Running:
		return []string{dimStyle.Render(clip("   "+string(in.Status)+", so its jobs cannot be read", width))}, -1
	case len(f.snap.Jobs) == 0:
		return []string{dimStyle.Render(clip("   no jobs yet — rig run starts one", width))}, -1
	}

	t := table{cols: []column{
		{title: "", min: 1},
		{title: "RUN", min: 11, drop: 4},
		{title: "JOB", min: 6},
		{title: "STATUS", min: 7},
		{title: "DURATION", min: 5, drop: 3, right: true},
		{title: "COMMAND", min: 12, drop: 1, mid: true},
	}}
	now := time.Now()
	for i, j := range f.snap.Jobs {
		th := m.themeFor(i == m.job, focused)
		addRow(&t, th, i == m.job, focused,
			th.txt(runLabel(j)), th.txt(j.Name), th.state(jobState(j)),
			th.txt(durationText(j, now)), th.txt(j.Command))
	}
	return t.lines(width), 1 + m.job
}

// jobBody is one run: what it was, how it went, and its log. The log gets
// every line left over, and wraps rather than clips — a clipped warning
// hides what the job is waiting on, which is the reason to be reading it.
func (m dashModel) jobBody(width int) []string {
	_, j, ok := m.currentJob()
	if !ok {
		return []string{dimStyle.Render("  no run selected")}
	}
	elapsed := "duration"
	if j.Running {
		elapsed = "elapsed"
	}
	var out []string
	if j.Command == "" {
		// a lone dash reads as a rendering fault; an instance adopted
		// before the job record kept one genuinely does not know
		out = wrap(dimStyle.Render("  command not recorded — this run predates the instance's job log"), width, "    ")
	} else {
		out = wrap("  "+j.Command, width, "    ")
	}
	facts := fmt.Sprintf("  %s · started %s · %s %s",
		jobState(j), startedText(j), elapsed, strings.TrimSuffix(durationText(j, time.Now()), " +"))
	if !j.Recorded() {
		facts = "  " + jobState(j) + " · no start or duration recorded"
	}
	out = append(out, dimStyle.Render(clip(facts, width)))
	if j.Workdir != "" {
		out = append(out, dimStyle.Render(clip("  workdir "+j.Workdir, width)))
	}
	out = append(out, "")

	// The pane is a snapshot, not a stream: sshx.Stream attaches this
	// process's stdout and cannot run inside the alt screen, so following
	// hands off to `rig logs -f` (issue 05, decision 2). Calling this
	// "Live" would be a lie about what it does.
	head := headerStyle.Render("  LOG  ")
	switch {
	case m.log.asking && len(m.log.lines) == 0:
		out = append(out, head+dimStyle.Render("reading ..."))
	case m.log.err != nil:
		out = append(out, head+errStyle.Render("could not be read"))
		out = append(out, wrap("  "+m.log.err.Error(), width, "    ")...)
		return out
	default:
		note := fmt.Sprintf("snapshot · last %d lines · r refreshes", len(m.log.lines))
		if j.Running {
			note = fmt.Sprintf("snapshot · last %d lines · f follows live · r refreshes", len(m.log.lines))
		}
		if lipgloss.Width("  LOG  "+note) > width {
			note = "snapshot · f follows live"
		}
		out = append(out, head+dimStyle.Render(note))
	}
	if len(m.log.lines) == 0 && !m.log.asking {
		out = append(out, dimStyle.Render(clip("  the log is empty", width)))
	}
	for _, l := range m.log.lines {
		out = append(out, wrap("  "+l, width, "     ")...)
	}
	return out
}

// splitBody keeps instance switching to hand on a wide terminal. The list is
// deliberately narrow and complete rather than wide and clipped.
func (m dashModel) splitBody() ([]string, int) {
	left := max(24, m.width/4)
	right := m.width - left - 3
	listing := table{cols: []column{
		{title: "", min: 1},
		{title: "INSTANCE", min: 8, mid: true},
		{title: "STATUS", min: 7, drop: 1},
	}}
	for i, in := range m.data.instances {
		f := m.feet[instKey(in)]
		th := m.themeFor(i == m.inst, m.level == levelInstances)
		addRow(&listing, th, i == m.inst, m.level == levelInstances,
			th.txt(shortName(in, m.several)), th.state(statusText(in, f)))
	}
	l := append([]string{paneTitle("INSTANCES", m.level == levelInstances, left)}, listing.lines(left)...)
	r := []string{paneTitle("DETAIL", m.level == levelInstance, right)}
	if in, ok := m.here(); ok {
		r = append(r, m.instanceFacts(in, right)...)
		r = append(r, "")
		r = append(r, m.storageLines(in, right)...)
		r = append(r, "", headerStyle.Render("  JOBS"))
		jobs, _ := m.jobLines(in, right, m.level == levelInstance)
		r = append(r, jobs...)
	}
	rule := dimStyle.Render("│")
	var out []string
	for i := range max(max(len(l), len(r)), m.height-chromeHeight) {
		lc, rc := "", ""
		if i < len(l) {
			lc = l[i]
		}
		if i < len(r) {
			rc = r[i]
		}
		out = append(out, pad(lc, left, false)+" "+rule+" "+rc)
	}
	// the panes scroll as one surface; the cursor is in the left one
	cursor := -1
	if m.level == levelInstances && len(m.data.instances) > 0 {
		cursor = 2 + m.inst
	}
	return out, cursor
}

// paneTitle marks which pane has the keys, since one glyph on one row was
// too quiet to carry it.
func paneTitle(name string, active bool, width int) string {
	if active {
		return paneOnStyle.Render(pad(" "+name+" ◂ keys", width, false))
	}
	return dimStyle.Render(pad(" "+name, width, false))
}

// header carries the location, and the money scoped to it: the estate's burn
// at the top level, this instance's rate once inside one. A bare rate on a
// job screen says nothing about the job, the instance or the estate.
func (m dashModel) header() string {
	head := titleStyle.Render("rig")
	crumbs, money := m.crumbs()
	room := m.width - lipgloss.Width(head) - 3
	fits := func(c []string, money string) bool {
		return lipgloss.Width(strings.Join(c, " › ")+money) <= room
	}
	// The concessions, in order: the word "instances" says least, then the
	// money, then the instance name is middle-clipped so it still reads.
	// The leaf, which is what you are looking at, is never shortened.
	try := append([]string{}, crumbs...)
	if !fits(try, money) && len(try) > 1 {
		try[0] = "…"
	}
	if !fits(try, money) {
		money = ""
	}
	if !fits(try, money) && len(try) > 2 {
		spare := room - lipgloss.Width(try[0]+" › "+try[2]) - 6
		try[1] = clipMid(try[1], max(6, spare))
	}
	trail := strings.Join(try, " › ") + money
	if m.loading {
		trail += " · refreshing"
	}
	return head + dimStyle.Render(clip(" · "+trail, m.width-lipgloss.Width(head)))
}

func (m dashModel) crumbs() ([]string, string) {
	in, ok := m.here()
	if !ok || m.level == levelInstances {
		running, burn, spent := 0, 0.0, 0.0
		for _, x := range m.data.instances {
			if x.Status == provider.Running {
				running++
				burn += x.PricePerHour
			}
			spent += m.spentOf(x)
		}
		head := fmt.Sprintf("instances · burn $%.2f/h", burn)
		if spent > 0 {
			head += fmt.Sprintf(" · spent $%s", commas(int(spent+0.5)))
		}
		return []string{head + fmt.Sprintf(" · %d of %d running", running, len(m.data.instances))}, ""
	}
	money := fmt.Sprintf(" · $%.2f/h this instance", in.PricePerHour)
	name := shortName(in, true)
	if m.level == levelJob {
		if _, j, ok := m.currentJob(); ok {
			return []string{"instances", name, fmt.Sprintf("%s (%s)", j.Name, runLabel(j))}, money
		}
	}
	return []string{"instances", name}, money
}

// footer names the object of every key, changes per level, and always offers
// the way back. Navigation and `? actions` are guaranteed room; the rarer
// verbs move into the help panel rather than being clipped to `d delet…`.
func (m dashModel) footer() string {
	if m.help {
		return dimStyle.Render(clip("  ? or any key closes this", m.width))
	}
	if m.confirm != "" {
		return errStyle.Render(clip("  "+m.confirmPrompt(), m.width))
	}
	var nav, extra string
	switch m.level {
	case levelInstances:
		nav = "↑/↓ instance · enter open · q quit"
		extra = "n new instance · d delete instance · r refresh"
	case levelInstance:
		nav = "↑/↓ run · enter open run · esc instances · q quit"
		extra = "d delete instance · r refresh"
	case levelJob:
		nav = "↑/↓ scroll · f follow live · esc runs · q quit"
		extra = "s stop run · r refresh"
	}
	if m.split && m.level < levelJob {
		nav = "tab pane · " + nav
	}
	line := "  " + nav + " · " + extra + " · ? actions"
	if lipgloss.Width(line) > m.width {
		line = "  " + nav + " · ? actions"
	}
	if m.flash != "" {
		line = "  " + m.flash + " ·" + strings.TrimPrefix(line, " ")
	}
	return dimStyle.Render(clip(line, m.width))
}

func (m dashModel) confirmPrompt() string {
	switch m.confirm {
	case "down":
		in, ok := m.here()
		if !ok {
			return ""
		}
		where := ""
		if m.several {
			where = " on " + in.Provider
		}
		return fmt.Sprintf("delete %s%s? data volumes are kept. [y/N]", in.Name, where)
	case "stop":
		if _, j, ok := m.currentJob(); ok {
			return fmt.Sprintf("stop run %s? its log is kept. [y/N]", j.Name)
		}
	}
	return ""
}

func (m dashModel) helpLines() []string {
	groups := [][2]string{
		{"NAVIGATE", "↑/↓ move · enter open · esc back · pgup/pgdn and g/G scroll · w split panes (needs 120 columns) · q quit"},
		{"INSTANCE", "n new instance · d delete instance (data volumes are kept) · r refresh"},
		{"RUN", "enter open · f follow the log live (leaves the dashboard, ctrl-C returns) · s stop"},
		{"READ", "$/H is what the instance charges while running, — when it is not; SPENT is what it has cost so far; EXPIRES is its self-destruct deadline, read from the instance"},
	}
	var out []string
	for _, g := range groups {
		out = append(out, headerStyle.Render("  "+g[0]))
		out = append(out, wrap("  "+g[1], m.width, "    ")...)
		out = append(out, "")
	}
	return out
}

// ---- cell text -------------------------------------------------------------

func shortName(in provider.Instance, withProvider bool) string {
	name := strings.TrimPrefix(in.Name, core.HostPrefix)
	if withProvider {
		return in.Provider + "/" + name
	}
	return name
}

// statusText carries the pricing mode, so the $/H column can be a plain
// number, and says when the cloud calls an instance running and ssh
// disagrees — that disagreement is the state, and belongs where state is.
func statusText(in provider.Instance, f footprint) string {
	s := string(in.Status)
	switch {
	case f.err != nil:
		return s + " · no ssh"
	case in.Status == provider.Running && in.Spot:
		return s + " · spot"
	}
	return s
}

// rateText is a dash for an instance that is not running: it is not charging
// that rate now, and a number invited being added to the burn.
func rateText(in provider.Instance) string {
	if in.Status != provider.Running {
		return "—"
	}
	return fmt.Sprintf("%.2f", in.PricePerHour)
}

// spentOf is what an instance has cost: rate times uptime while it runs, and
// for one that has stopped the figure the ledger recorded, because
// multiplying an age that includes hours it was not billing overstates it.
func (m dashModel) spentOf(in provider.Instance) float64 {
	if in.Status == provider.Running {
		if in.CreatedAt.IsZero() {
			return 0
		}
		return in.PricePerHour * time.Since(in.CreatedAt).Hours()
	}
	for _, e := range m.hist {
		if e.Event == "down" && e.Name == in.Name {
			return e.Cost
		}
	}
	return 0
}

func spentText(in provider.Instance, v float64) string {
	switch {
	case v == 0:
		return "—"
	case v < 10:
		return fmt.Sprintf("$%.2f", v)
	}
	return "$" + commas(int(v+0.5))
}

// expiryText is the self-destruct deadline as a column. The deadline lives
// on the instance, so an unreachable one has none to read — which is itself
// worth knowing, because it means the watchdog cannot be confirmed.
func expiryText(in provider.Instance, f footprint) string {
	switch {
	case in.Status != provider.Running:
		return "—"
	case f.err != nil:
		return "?"
	case f.snap.HasDeadline():
		return shortDur(time.Until(f.snap.Deadline))
	case f.asking:
		return "…"
	}
	return "none"
}

func expiryPhrase(in provider.Instance, f footprint) string {
	switch {
	case in.Status != provider.Running:
		return "no watchdog while stopped"
	case f.err != nil:
		return "expiry unreadable, no ssh"
	case f.snap.HasDeadline():
		return "expires in " + shortDur(time.Until(f.snap.Deadline))
	case f.asking:
		return "reading the deadline ..."
	}
	return "no deadline set — it will not self-destruct"
}

// jobsText counts running, failed and done separately and in that order.
// "1 running, 2 done" counted a failed run as done, which hid the failure
// from exactly the glance this column exists for; urgency first means a
// squeezed column loses "done" rather than the bad news.
func jobsText(in provider.Instance, f footprint) string {
	switch {
	case f.err != nil, in.Status != provider.Running:
		return "—"
	case f.asking && len(f.snap.Jobs) == 0:
		return "…"
	case len(f.snap.Jobs) == 0:
		return "no jobs"
	}
	var run, fail, done int
	for _, j := range f.snap.Jobs {
		switch {
		case j.Running:
			run++
		case j.ExitCode != 0:
			fail++
		default:
			done++
		}
	}
	var parts []string
	if run > 0 {
		parts = append(parts, fmt.Sprintf("%d running", run))
	}
	if fail > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", fail))
	}
	if done > 0 {
		parts = append(parts, fmt.Sprintf("%d done", done))
	}
	return strings.Join(parts, " · ")
}

// jobState reads as a verdict, not a number: "failed · exit 1" says what the
// 1 meant, which "exited 1" left to the reader.
func jobState(j remote.Job) string {
	switch {
	case j.Running:
		return "running"
	case j.ExitCode == remote.ExitUnknown:
		return "interrupted"
	case j.ExitCode == 0:
		return "done"
	default:
		return fmt.Sprintf("failed · exit %d", j.ExitCode)
	}
}

// runLabel identifies which run this is — two runs of one name are
// otherwise indistinguishable rows.
func runLabel(j remote.Job) string {
	if !j.Recorded() {
		return "—"
	}
	return j.Started.Local().Format("01-02 15:04")
}

func startedText(j remote.Job) string {
	if j.Started.IsZero() {
		return "at an unrecorded time"
	}
	return ageShort(j.Started) + " ago"
}

// durationText is elapsed-so-far for a running job and final for a finished
// one; the + tells them apart in a column whose heading cannot change per
// row (the job screen spells out "elapsed" or "duration" instead).
func durationText(j remote.Job, now time.Time) string {
	d, ok := j.Took(now)
	if !ok {
		return "—"
	}
	s := shortDur(d)
	if j.Running {
		s += " +"
	}
	return s
}

func shortDur(d time.Duration) string {
	if d < 0 {
		return "overdue"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func ageShort(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
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

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// commas groups a number so 184223 reads at a glance.
func commas(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// wrap breaks a line to width on spaces, indenting continuations, so a long
// command or log line is readable instead of cut.
func wrap(s string, width int, indent string) []string {
	if lipgloss.Width(s) <= width {
		return []string{s}
	}
	// Fields drops the leading whitespace, which is the line's own indent.
	lead := s[:len(s)-len(strings.TrimLeft(s, " "))]
	var out []string
	cur := lead
	for _, w := range strings.Fields(s) {
		next := cur + w
		if strings.TrimSpace(cur) != "" {
			next = cur + " " + w
		}
		if lipgloss.Width(next) > width && strings.TrimSpace(cur) != "" {
			out = append(out, cur)
			cur = indent + w
			continue
		}
		cur = next
	}
	if strings.TrimSpace(cur) != "" {
		out = append(out, cur)
	}
	for i, l := range out {
		out[i] = clip(l, width)
	}
	return out
}

// themeFor is the palette a row is drawn with, in this dashboard's band.
func (m dashModel) themeFor(selected, focused bool) theme {
	if !selected {
		return plainTheme()
	}
	return bandTheme(m.band, focused)
}
