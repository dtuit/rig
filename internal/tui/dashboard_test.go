package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/core"
	"github.com/dtuit/rig/internal/provider"
	"github.com/dtuit/rig/internal/provider/fake"
	"github.com/dtuit/rig/internal/remote"
)

// The dashboard is driven here as bubbletea drives it — messages in, View()
// out — because that is the only way to see what a terminal of a given size
// actually shows. The provider half comes from fakes through the Fleet; the
// ssh half from the model's own ask/readLog seams.

func newFleet(t *testing.T, provs ...*fake.Provider) *core.Fleet {
	t.Helper()
	t.Setenv("RIG_CONFIG", filepath.Join(t.TempDir(), "config.yml"))
	byName := map[string]provider.Provider{}
	names := make([]string, 0, len(provs))
	for _, p := range provs {
		byName[p.Name()] = p
		names = append(names, p.Name())
	}
	f := core.NewFleet(&config.Config{}, func(string, ...any) {}, names...)
	f.Open = func(name string) (provider.Provider, error) {
		p, ok := byName[name]
		if !ok {
			return nil, errors.New("no such provider")
		}
		return p, nil
	}
	return f
}

// snapshots scripts the ssh half by instance name: a Snapshot, or an error
// for an instance that cannot be reached.
type snapshots map[string]struct {
	snap remote.Snapshot
	err  error
}

// open builds the model, sizes it, lets the API survey land and then every
// scripted ssh answer, as bubbletea would deliver them.
func open(t *testing.T, f *core.Fleet, width, height int, ss snapshots) dashModel {
	t.Helper()
	m := newDashModel(context.Background(), f)
	m.ask = func(in provider.Instance) (remote.Snapshot, error) {
		a, ok := ss[strings.TrimPrefix(in.Name, core.HostPrefix)]
		if !ok {
			return remote.Snapshot{}, errors.New("not scripted")
		}
		return a.snap, a.err
	}
	m.readLog = func(_ provider.Instance, j remote.Job) ([]string, error) {
		return []string{"line one of " + j.Name, "line two of " + j.Name}, nil
	}
	m = send(m, tea.WindowSizeMsg{Width: width, Height: height})
	return settle(m, f)
}

// paint delivers only the API survey — the first paint, before any ssh has
// answered.
func paint(t *testing.T, f *core.Fleet, width, height int, ss snapshots) dashModel {
	t.Helper()
	m := newDashModel(context.Background(), f)
	m.ask = func(in provider.Instance) (remote.Snapshot, error) {
		a := ss[strings.TrimPrefix(in.Name, core.HostPrefix)]
		return a.snap, a.err
	}
	m = send(m, tea.WindowSizeMsg{Width: width, Height: height})
	return send(m, newDashData(f.Survey(context.Background())))
}

// settle delivers the survey and then drains the per-instance answers.
func settle(m dashModel, f *core.Fleet) dashModel {
	m = send(m, newDashData(f.Survey(context.Background())))
	for _, in := range m.data.instances {
		if in.Status != provider.Running {
			continue
		}
		snap, err := m.ask(in)
		m = send(m, footprintMsg{key: instKey(in), snap: snap, err: err})
	}
	return m
}

func send(m dashModel, msg tea.Msg) dashModel {
	out, _ := m.Update(msg)
	return out.(dashModel)
}

func keys(m dashModel, presses ...string) dashModel {
	for _, k := range presses {
		m = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	}
	return m
}

func press(m dashModel, k tea.KeyType) dashModel {
	return send(m, tea.KeyMsg{Type: k})
}

// dive presses enter and delivers what the model asked for as a result,
// which at the run level is its log — the read is a command, as it is in
// the real thing.
func dive(m dashModel) dashModel {
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(dashModel)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m = send(m, msg)
		}
	}
	return m
}

// lines is what the terminal shows, styling removed.
func lines(m dashModel) []string {
	out := strings.Split(m.View(), "\n")
	for i := range out {
		out[i] = stripANSI(out[i])
	}
	return out
}

func screen(m dashModel) string { return strings.Join(lines(m), "\n") }

func instance(id, name, typ, loc string, price float64) provider.Instance {
	return provider.Instance{
		ID: id, Name: core.HostPrefix + name, Type: typ, Location: loc,
		Status: provider.Running, IP: "10.0.0.1", PricePerHour: price,
		SSH:       provider.Endpoint{Host: "10.0.0.1", Port: 22},
		CreatedAt: time.Now().Add(-2 * time.Hour),
		Image:     "ubuntu-24.04",
	}
}

// job builds a run; run doubles as its id and as how many minutes ago it
// started, so the newest run has the smallest number.
func job(name string, run int64, running bool, exit int, cmd string) remote.Job {
	started := time.Now().Add(-time.Duration(run) * time.Minute)
	j := remote.Job{
		Name: name, Run: run, Running: running, ExitCode: exit,
		Command: cmd, Started: started,
	}
	if !running {
		j.Finished = started.Add(20 * time.Minute)
	}
	return j
}

func withJobs(jobs ...remote.Job) struct {
	snap remote.Snapshot
	err  error
} {
	return struct {
		snap remote.Snapshot
		err  error
	}{snap: remote.Snapshot{Deadline: time.Now().Add(3 * time.Hour), Jobs: jobs}}
}

func unreachable(msg string) struct {
	snap remote.Snapshot
	err  error
} {
	return struct {
		snap remote.Snapshot
		err  error
	}{err: errors.New(msg)}
}

// ---- level 0 ---------------------------------------------------------------

func TestOverviewIsInstancesAndNotVolumes(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	verda.Volumes = []provider.Volume{{
		ID: "v1", Name: "rig-data", SizeGB: 500, Type: "NVMe", Location: "FIN-02",
		InstanceIDs: []string{"a"},
	}}
	f := newFleet(t, verda)

	view := screen(open(t, f, 110, 20, snapshots{"trainer": withJobs()}))
	for _, want := range []string{"INSTANCE", "STATUS", "$/H", "SPENT", "EXPIRES", "JOBS", "trainer"} {
		if !strings.Contains(view, want) {
			t.Errorf("overview lacks %q:\n%s", want, view)
		}
	}
	// the global volume table is gone from this screen
	if strings.Contains(view, "VOLUME") || strings.Contains(view, "rig-data") {
		t.Errorf("volumes are still on the overview:\n%s", view)
	}
}

// The API paints first and the ssh answers arrive after, so the columns that
// need an instance say "…" rather than claiming there is nothing.
func TestJobCountsAndExpiryFillProgressively(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	f := newFleet(t, verda)
	ss := snapshots{"trainer": withJobs(job("fit", 1000, true, 0, "bash train.sh"))}

	first := screen(paint(t, f, 110, 20, ss))
	if !strings.Contains(first, "…") {
		t.Errorf("first paint does not say it is still asking:\n%s", first)
	}
	if strings.Contains(first, "no jobs") {
		t.Errorf("first paint claims there are no jobs:\n%s", first)
	}

	full := screen(open(t, f, 110, 20, ss))
	if !strings.Contains(full, "1 running") {
		t.Errorf("the answer never landed:\n%s", full)
	}
}

// A stopped instance is never asked: it cannot answer, and waiting out its
// connect timeout is worse than showing nothing.
func TestAStoppedInstanceIsNeverAsked(t *testing.T) {
	verda := fake.Named("verda")
	in := instance("a", "old", "1xA100", "FIN-01", 1.29)
	in.Status = provider.Stopped
	verda.Put(in)
	f := newFleet(t, verda)

	asked := 0
	m := newDashModel(context.Background(), f)
	m.ask = func(provider.Instance) (remote.Snapshot, error) {
		asked++
		return remote.Snapshot{}, nil
	}
	m = send(m, tea.WindowSizeMsg{Width: 110, Height: 20})
	m = send(m, newDashData(f.Survey(context.Background())))
	// draining the batch would call ask; assert the command list is empty
	if cmd := m.askFootprints(); cmd != nil {
		if msg := cmd(); msg != nil {
			t.Errorf("a stopped instance was asked: %+v", msg)
		}
	}
	if asked != 0 {
		t.Errorf("ask was called %d times for a stopped instance", asked)
	}
	view := screen(m)
	if !strings.Contains(view, "stopped") {
		t.Errorf("view does not show the stopped instance:\n%s", view)
	}
}

// The cloud saying running while ssh disagrees is itself the state, and
// everything the API knows still shows.
func TestAnUnreachableInstanceSaysSoAndKeepsWhatItKnows(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "stale", "1H100.80S.32V", "FIN-03", 3.25))
	f := newFleet(t, verda)
	ss := snapshots{"stale": unreachable("ssh: connect to host 10.0.0.1 port 22: connection timed out")}

	m := open(t, f, 110, 20, ss)
	view := screen(m)
	if !strings.Contains(view, "no ssh") {
		t.Errorf("an unreachable instance does not say so:\n%s", view)
	}
	if !strings.Contains(view, "1H100.80S.32V") && !strings.Contains(view, "stale") {
		t.Errorf("the API's facts vanished with the ssh failure:\n%s", view)
	}
	// the expiry cannot be read, and says so rather than claiming none
	if !strings.Contains(view, "?") {
		t.Errorf("expiry should be unknown, not absent:\n%s", view)
	}

	detail := screen(press(m, tea.KeyEnter))
	for _, want := range []string{
		"could not ask this instance",
		"connection timed out",
		"rig adopt stale",
		"expiry unreadable",
		"1H100.80S.32V", // still shown: the API knew it
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("unreachable detail lacks %q:\n%s", want, detail)
		}
	}
}

// A failed run counted as done hid the failure from the glance the column
// exists for.
func TestOverviewCountsFailuresSeparately(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	f := newFleet(t, verda)

	view := screen(open(t, f, 130, 20, snapshots{"trainer": withJobs(
		job("fit", 3000, true, 0, "bash train.sh"),
		job("prep", 2000, false, 0, "python prep.py"),
		job("fit", 1000, false, 1, "bash train.sh"),
	)}))
	if !strings.Contains(view, "1 running · 1 failed · 1 done") {
		t.Errorf("failures are not counted apart:\n%s", view)
	}
}

// ---- drilling in -----------------------------------------------------------

func TestEnterOpensTheInstanceThenTheRun(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	verda.Volumes = []provider.Volume{
		{ID: "v1", Name: "rig-data", SizeGB: 500, Type: "NVMe", InstanceIDs: []string{"a"}},
		{ID: "os-a", Name: "OS-trainer", SizeGB: 100, Type: "NVMe"},
	}
	in := instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92)
	in.OSVolumeID = "os-a"
	verda.Put(in)
	f := newFleet(t, verda)
	m := open(t, f, 120, 24, snapshots{"trainer": withJobs(
		job("fit-7b", 3000, true, 0, `bash train.sh 700 32 "A D E"`),
	)})

	detail := press(m, tea.KeyEnter)
	view := screen(detail)
	for _, want := range []string{
		"instances › verda/trainer", // breadcrumb
		"8xH100-SXM", "FIN-02",      // summary
		"STORAGE", "rig-data", "IF INSTANCE DELETED", "kept",
		"OS-trainer", "deleted", // the OS volume goes with the instance
		"JOBS", "RUN", "DURATION", "fit-7b",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("instance detail lacks %q:\n%s", want, view)
		}
	}

	run := dive(detail)
	rv := screen(run)
	for _, want := range []string{
		"› fit-7b", `bash train.sh 700 32 "A D E"`, "running", "elapsed",
		"LOG", "snapshot", "line one of fit-7b",
	} {
		if !strings.Contains(rv, want) {
			t.Errorf("run detail lacks %q:\n%s", want, rv)
		}
	}
	if run.level != levelJob {
		t.Errorf("level = %v, want the job level", run.level)
	}
}

// The drill-down's one real cost is backtracking, so going back must be
// free: both the selection and the scroll position survive it.
func TestGoingBackRestoresSelectionAndScroll(t *testing.T) {
	verda := fake.Named("verda")
	for i := range 30 {
		in := instance(string(rune('a'+i)), "box"+string(rune('a'+i)), "1xA100", "FIN-01", 1)
		in.CreatedAt = time.Now().Add(-time.Duration(i) * time.Hour)
		verda.Put(in)
	}
	f := newFleet(t, verda)
	ss := snapshots{}
	for i := range 30 {
		ss["box"+string(rune('a'+i))] = withJobs(job("j", 1000, true, 0, "sleep 1"))
	}
	m := open(t, f, 110, 12, ss)

	for range 20 {
		m = keys(m, "j")
	}
	wantInst, wantTop := m.inst, m.vp.YOffset
	if wantTop == 0 {
		t.Fatal("20 rows in a 12-line window should have scrolled")
	}

	m = press(m, tea.KeyEnter) // into the instance
	m = press(m, tea.KeyEsc)   // and back

	if m.inst != wantInst {
		t.Errorf("selection moved: %d, want %d", m.inst, wantInst)
	}
	if m.vp.YOffset != wantTop {
		t.Errorf("scroll moved: %d, want %d", m.vp.YOffset, wantTop)
	}
}

func TestEscFromTheRunReturnsToItsInstance(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	f := newFleet(t, verda)
	m := open(t, f, 120, 24, snapshots{"trainer": withJobs(
		job("fit", 3000, true, 0, "a"), job("prep", 2000, false, 0, "b"),
	)})

	m = press(m, tea.KeyEnter) // instance
	m = keys(m, "j")           // second run
	m = dive(m)                // run
	if m.level != levelJob {
		t.Fatalf("level = %v", m.level)
	}
	m = press(m, tea.KeyEsc)
	if m.level != levelInstance {
		t.Errorf("esc left level %v, want the instance", m.level)
	}
	if m.job != 1 {
		t.Errorf("the run cursor moved to %d, want 1", m.job)
	}
	m = press(m, tea.KeyEsc)
	if m.level != levelInstances {
		t.Errorf("esc left level %v, want the instances", m.level)
	}
}

// ---- handing off -----------------------------------------------------------

func TestNAsksForALaunchAndOnlyAtTheTop(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	f := newFleet(t, verda)
	m := open(t, f, 120, 24, snapshots{"trainer": withJobs()})

	if got := keys(m, "n").result.Outcome; got != Create {
		t.Errorf("n at the top gave %v, want Create", got)
	}
	// inside an instance, n is not a launch: creating is a level-0 action
	inside := keys(press(m, tea.KeyEnter), "n")
	if inside.result.Outcome != Quit || inside.level != levelInstance {
		t.Errorf("n inside an instance did something: %+v at level %v", inside.result, inside.level)
	}
}

// Following cannot happen inside the alt screen, so f reports the run it
// wants followed and the caller runs it.
func TestFHandsBackTheRunToFollow(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	f := newFleet(t, verda)
	m := open(t, f, 120, 24, snapshots{"trainer": withJobs(
		job("fit-7b", 3000, true, 0, "bash train.sh"),
	)})

	m = press(m, tea.KeyEnter)
	m = dive(m)
	got := keys(m, "f").result
	if got.Outcome != FollowLog {
		t.Fatalf("f gave %v, want FollowLog", got.Outcome)
	}
	if got.Job.Name != "fit-7b" || got.Instance == nil || got.Instance.Name != "rig-trainer" {
		t.Errorf("f handed back %+v on %+v", got.Job, got.Instance)
	}
}

func TestQuitLeavesTheEstateUntouched(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	f := newFleet(t, verda)
	m := keys(open(t, f, 120, 24, snapshots{"trainer": withJobs()}), "q")

	if m.result.Outcome != Quit {
		t.Errorf("q gave %v", m.result.Outcome)
	}
	if len(verda.Deleted) != 0 || len(verda.Requests) != 0 {
		t.Error("the dashboard changed something on its own")
	}
}

func TestDeleteGoesToTheProviderTheRowIsOn(t *testing.T) {
	verda, runpod := fake.Named("verda"), fake.Named("runpod")
	old := instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92)
	old.CreatedAt = time.Now().Add(-10 * time.Hour)
	verda.Put(old)
	runpod.Put(instance("b", "infer", "1xA100-SXM", "EU-RO-1", 1.59))
	f := newFleet(t, verda, runpod)
	m := open(t, f, 130, 24, snapshots{"trainer": withJobs(), "infer": withJobs()})

	// newest first, so the RunPod instance is under the cursor
	if in, _ := m.here(); in.Provider != "runpod" {
		t.Fatalf("expected the runpod row first, got %+v", in)
	}
	m = keys(m, "d")
	if !strings.Contains(screen(m), "delete rig-infer on runpod?") {
		t.Errorf("the confirmation does not name the cloud:\n%s", screen(m))
	}
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatal("y did not act")
	}
	m = send(out.(dashModel), cmd())
	if len(runpod.Deleted) != 1 || runpod.Deleted[0].ID != "b" {
		t.Errorf("runpod recorded %+v", runpod.Deleted)
	}
	if len(verda.Deleted) != 0 {
		t.Errorf("verda was asked to delete %+v", verda.Deleted)
	}
}

// ---- chrome ----------------------------------------------------------------

func TestEverythingFitsTheWindowAtEveryLevel(t *testing.T) {
	verda, runpod := fake.Named("verda"), fake.Named("runpod")
	long := instance("a", "finetune-qwen3-32b-experiment-a-rerun", "8xH200-SXM5-141GB", "ICE-01", 61.44)
	long.Spot = true
	verda.Put(long)
	verda.Volumes = []provider.Volume{{
		ID: "v1", Name: "rig-data-qwen3-checkpoints", SizeGB: 4000,
		Type: "NVMe_Shared", InstanceIDs: []string{"a"},
	}}
	runpod.Put(instance("b", "infer", "1xA100-SXM", "EU-RO-1", 1.59))
	f := newFleet(t, verda, runpod)
	ss := snapshots{
		"finetune-qwen3-32b-experiment-a-rerun": withJobs(
			job("sweep-lr3-bs16", 3000, true, 0,
				"torchrun --nnodes 1 --nproc_per_node 8 train.py --config configs/qwen3-32b-sft.yaml --seq-len 8192"),
			job("sweep-lr4-bs32", 2000, false, 1, "python -m evals.harness --tasks mmlu,gsm8k"),
		),
		"infer": withJobs(),
	}

	for _, size := range []struct{ w, h int }{
		{200, 60}, {120, 40}, {100, 30}, {80, 24}, {60, 20}, {40, 12}, {30, 8}, {20, 6}, {16, 4}, {10, 3},
	} {
		m := open(t, f, size.w, size.h, ss)
		for _, lvl := range []string{"instances", "instance", "run"} {
			got := lines(m)
			if len(got) > size.h {
				t.Errorf("%dx%d %s: view is %d lines tall", size.w, size.h, lvl, len(got))
			}
			for i, line := range got {
				if w := lipgloss.Width(line); w > size.w {
					t.Errorf("%dx%d %s: line %d is %d wide: %q", size.w, size.h, lvl, i, w, line)
				}
			}
			m = dive(m)
		}
	}
}

func TestFooterNamesItsObjectPerLevel(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	f := newFleet(t, verda)
	m := open(t, f, 130, 24, snapshots{"trainer": withJobs(job("fit", 3000, true, 0, "x"))})

	for _, tc := range []struct {
		level string
		want  []string
		gone  []string
	}{
		{"instances", []string{"↑/↓ instance", "n new instance", "d delete instance", "? actions"}, []string{"esc"}},
		{"instance", []string{"↑/↓ run", "enter open run", "esc instances"}, []string{"n new instance"}},
		{"run", []string{"↑/↓ scroll", "f follow live", "esc runs", "s stop run"}, []string{"enter open"}},
	} {
		foot := lines(m)[len(lines(m))-1]
		for _, want := range tc.want {
			if !strings.Contains(foot, want) {
				t.Errorf("%s footer lacks %q: %q", tc.level, want, foot)
			}
		}
		for _, gone := range tc.gone {
			if strings.Contains(foot, gone) {
				t.Errorf("%s footer still offers %q: %q", tc.level, gone, foot)
			}
		}
		m = dive(m)
	}
}

// The footer is the interaction guide, so a narrow window drops the rarer
// verbs into the help panel rather than clipping one mid-word.
func TestFooterNeverClipsAVerb(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	f := newFleet(t, verda)

	for _, width := range []int{130, 90, 70, 50} {
		m := open(t, f, width, 20, snapshots{"trainer": withJobs()})
		foot := lines(m)[len(lines(m))-1]
		if strings.Contains(foot, "delet…") || strings.Contains(foot, "instan…") {
			t.Errorf("width %d clipped a verb: %q", width, foot)
		}
		if !strings.Contains(foot, "? actions") && width >= 50 {
			t.Errorf("width %d lost the way into the help panel: %q", width, foot)
		}
	}
}

func TestHelpPanelExplainsTheColumns(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	f := newFleet(t, verda)
	m := keys(open(t, f, 120, 24, snapshots{"trainer": withJobs()}), "?")

	view := screen(m)
	for _, want := range []string{"NAVIGATE", "INSTANCE", "RUN", "READ", "SPENT", "EXPIRES"} {
		if !strings.Contains(view, want) {
			t.Errorf("help lacks %q:\n%s", want, view)
		}
	}
	if got := keys(m, "j"); got.help {
		t.Error("any key should close the help panel")
	}
}

func TestOneCloudNeedsNoProviderPrefix(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	verda.Bal, verda.Cur = 412.90, "USD"
	f := newFleet(t, verda)

	got := lines(open(t, f, 120, 20, snapshots{"trainer": withJobs()}))
	if strings.Contains(got[2], "verda/trainer") {
		t.Errorf("a single-cloud row prefixes the provider: %q", got[2])
	}
	if !strings.Contains(got[0], "burn $25.92/h") || !strings.Contains(got[0], "1 of 1 running") {
		t.Errorf("header = %q", got[0])
	}
}

func TestOneCloudsFailureLeavesTheOthersRowsAlone(t *testing.T) {
	verda, aws := fake.Named("verda"), fake.Named("aws")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	aws.ListErr = errors.New("expired SSO token")
	f := newFleet(t, verda, aws)

	view := screen(open(t, f, 120, 20, snapshots{"trainer": withJobs()}))
	if !strings.Contains(view, "aws · expired SSO token") {
		t.Errorf("the failing cloud is not reported:\n%s", view)
	}
	if !strings.Contains(view, "trainer") {
		t.Errorf("the working cloud lost its rows:\n%s", view)
	}
}

func TestAnEmptyEstatePointsAtCreating(t *testing.T) {
	view := screen(open(t, newFleet(t, fake.Named("verda")), 100, 20, snapshots{}))
	if !strings.Contains(view, "no rig instances — n to create one") {
		t.Errorf("empty dashboard says nothing useful:\n%s", view)
	}
}

// Split is a mode, not the default, and only where it fits: below the
// threshold the sidebar takes a quarter of the width from the columns that
// carry the content.
func TestSplitIsOfferedOnlyWhereItFits(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	f := newFleet(t, verda)
	ss := snapshots{"trainer": withJobs(job("fit", 3000, true, 0, "bash train.sh"))}

	wide := keys(open(t, f, 140, 24, ss), "w")
	if !wide.split {
		t.Error("w did not turn split on at 140 columns")
	}
	if view := screen(wide); !strings.Contains(view, "INSTANCES") || !strings.Contains(view, "DETAIL") {
		t.Errorf("split does not name its panes:\n%s", view)
	}
	// tab moves the keys between panes, and the pane with them says so
	tabbed := press(wide, tea.KeyTab)
	if tabbed.level != levelInstance {
		t.Errorf("tab did not move focus: level %v", tabbed.level)
	}
	if !strings.Contains(screen(tabbed), "DETAIL ◂ keys") {
		t.Errorf("the focused pane is unmarked:\n%s", screen(tabbed))
	}

	narrow := keys(open(t, f, 90, 24, ss), "w")
	if narrow.split {
		t.Error("split was allowed at 90 columns")
	}
	if !strings.Contains(screen(narrow), "split needs 120 columns") {
		t.Errorf("refusing split says nothing:\n%s", screen(narrow))
	}

	// and a window that shrinks under it drops back rather than clipping
	shrunk := send(wide, tea.WindowSizeMsg{Width: 80, Height: 24})
	if shrunk.split {
		t.Error("split survived a window it no longer fits")
	}
}

// An instance adopted before the job record has runs with no command and no
// times. A lone dash reads as a rendering fault, so it says what it is.
func TestAnUnrecordedRunSaysWhatItDoesNotKnow(t *testing.T) {
	verda := fake.Named("verda")
	verda.Put(instance("a", "trainer", "8xH100-SXM", "FIN-02", 25.92))
	f := newFleet(t, verda)
	legacy := remote.Job{Name: "census", Running: true} // no Run, no Started, no Command
	m := dive(press(open(t, f, 120, 24, snapshots{"trainer": withJobs(legacy)}), tea.KeyEnter))

	view := screen(m)
	for _, want := range []string{"command not recorded", "no start or duration recorded"} {
		if !strings.Contains(view, want) {
			t.Errorf("run detail lacks %q:\n%s", want, view)
		}
	}
}
