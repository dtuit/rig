package tunnel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeWorld fakes the manager's process seams: Launch scripts what the
// supervisor would do to the state file, Alive/Stop track fake pids.
type fakeWorld struct {
	t        *testing.T
	dir      string
	alive    map[int]bool
	stopped  []int
	launched []State
	// onLaunch simulates the spawned supervisor; nil means "write nothing"
	// (a supervisor that never comes up).
	onLaunch func(st State) int
}

func (w *fakeWorld) manager() *Manager {
	return &Manager{
		Dir: w.dir,
		Launch: func(st State) (int, error) {
			w.launched = append(w.launched, st)
			if w.onLaunch == nil {
				return 999, nil
			}
			return w.onLaunch(st), nil
		},
		Alive: func(pid int) bool { return w.alive[pid] },
		Stop: func(pid int) error {
			w.stopped = append(w.stopped, pid)
			w.alive[pid] = false
			return nil
		},
		Timeout: 50 * time.Millisecond,
		poll:    time.Millisecond,
	}
}

// connectedSupervisor scripts the happy path: the supervisor writes its
// state file with phase connected and pid.
func (w *fakeWorld) connectedSupervisor(pid int) func(State) int {
	return func(st State) int {
		st.PID = pid
		st.Phase = PhaseConnected
		if err := writeState(w.dir, st); err != nil {
			w.t.Fatal(err)
		}
		w.alive[pid] = true
		return pid
	}
}

func newWorld(t *testing.T) *fakeWorld {
	return &fakeWorld{t: t, dir: t.TempDir(), alive: map[int]bool{}}
}

// seed writes a state file directly, as if a supervisor were already running.
func (w *fakeWorld) seed(st State) {
	if err := writeState(w.dir, st); err != nil {
		w.t.Fatal(err)
	}
}

var ep = Endpoint{Instance: "rig-train", ID: "id-1", IP: "10.0.0.5"}

func fwd(pairs ...[2]int) []Forward {
	var out []Forward
	for _, p := range pairs {
		out = append(out, Forward{Local: p[0], Remote: p[1]})
	}
	return out
}

func TestEnsureStartsSupervisor(t *testing.T) {
	w := newWorld(t)
	w.onLaunch = w.connectedSupervisor(42)

	st, already, err := w.manager().Ensure(ep, fwd([2]int{8000, 8000}))
	if err != nil {
		t.Fatal(err)
	}
	if already {
		t.Error("fresh ensure reported already-up")
	}
	if st.Phase != PhaseConnected || st.PID != 42 {
		t.Errorf("state = %+v, want connected pid 42", st)
	}
	if len(w.launched) != 1 || w.launched[0].Endpoint != ep {
		t.Errorf("launched = %+v, want one launch for %+v", w.launched, ep)
	}
}

func TestEnsureIdempotent(t *testing.T) {
	w := newWorld(t)
	w.alive[42] = true
	w.seed(State{Endpoint: ep, Forwards: fwd([2]int{8000, 8000}), PID: 42, Phase: PhaseConnected})

	st, already, err := w.manager().Ensure(ep, fwd([2]int{8000, 8000}))
	if err != nil {
		t.Fatal(err)
	}
	if !already {
		t.Error("covered ensure did not report already-up")
	}
	if st.PID != 42 {
		t.Errorf("state = %+v, want the existing pid-42 tunnel", st)
	}
	if len(w.launched) != 0 {
		t.Errorf("covered ensure launched %+v", w.launched)
	}
}

func TestEnsureAdditionalPortsLaunchesOnlyMissing(t *testing.T) {
	w := newWorld(t)
	w.alive[42] = true
	w.seed(State{Endpoint: ep, Forwards: fwd([2]int{8000, 8000}), PID: 42, Phase: PhaseConnected})
	w.onLaunch = w.connectedSupervisor(43)

	_, already, err := w.manager().Ensure(ep, fwd([2]int{8000, 8000}, [2]int{9090, 9090}))
	if err != nil {
		t.Fatal(err)
	}
	if already {
		t.Error("partially covered ensure reported already-up")
	}
	if len(w.launched) != 1 || len(w.launched[0].Forwards) != 1 || w.launched[0].Forwards[0].Local != 9090 {
		t.Errorf("launched = %+v, want one launch forwarding only 9090", w.launched)
	}
}

func TestEnsureRefusesConflictingLocalPort(t *testing.T) {
	other := Endpoint{Instance: "rig-other", ID: "id-2", IP: "10.0.0.6"}
	for name, holder := range map[string]State{
		"other instance":        {Endpoint: other, Forwards: fwd([2]int{8000, 8000}), PID: 42, Phase: PhaseConnected},
		"different remote port": {Endpoint: ep, Forwards: fwd([2]int{8000, 8080}), PID: 42, Phase: PhaseConnected},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.alive[42] = true
			w.seed(holder)

			_, _, err := w.manager().Ensure(ep, fwd([2]int{8000, 8000}))
			if err == nil {
				t.Fatal("conflicting ensure succeeded")
			}
			if !strings.Contains(err.Error(), "8000") || !strings.Contains(err.Error(), holder.Instance) {
				t.Errorf("error %q should name port 8000 and holder %s", err, holder.Instance)
			}
			if len(w.launched) != 0 {
				t.Errorf("conflicting ensure launched %+v", w.launched)
			}
		})
	}
}

func TestEnsureStartupTimeout(t *testing.T) {
	w := newWorld(t)
	// onLaunch nil: the supervisor never writes a state file.

	_, _, err := w.manager().Ensure(ep, fwd([2]int{8000, 8000}))
	if err == nil {
		t.Fatal("ensure succeeded with no supervisor state")
	}
	if !strings.Contains(err.Error(), "rig-train") {
		t.Errorf("timeout error %q should name the instance", err)
	}
	if len(w.stopped) != 1 || w.stopped[0] != 999 {
		t.Errorf("stopped = %v, want the launched supervisor (999) killed", w.stopped)
	}
	if live, _ := w.manager().List(); len(live) != 0 {
		t.Errorf("timed-out ensure left state behind: %+v", live)
	}
}

func TestEnsureReportsSupervisorFailure(t *testing.T) {
	w := newWorld(t)
	w.onLaunch = func(st State) int {
		st.PID = 42
		st.Phase = PhaseGaveUp
		st.LastError = "bind: address already in use"
		if err := writeState(w.dir, st); err != nil {
			t.Fatal(err)
		}
		return 42
	}

	_, _, err := w.manager().Ensure(ep, fwd([2]int{8000, 8000}))
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("err = %v, want the supervisor's recorded error", err)
	}
	if live, _ := w.manager().List(); len(live) != 0 {
		t.Errorf("failed ensure left state behind: %+v", live)
	}
}

func TestListReapsDeadSupervisors(t *testing.T) {
	w := newWorld(t)
	w.alive[42] = true
	w.seed(State{Endpoint: ep, Forwards: fwd([2]int{8000, 8000}), PID: 42, Phase: PhaseConnected})
	dead := Endpoint{Instance: "rig-old", ID: "id-9", IP: "10.0.0.9"}
	w.seed(State{Endpoint: dead, Forwards: fwd([2]int{7000, 7000}), PID: 41, Phase: PhaseConnected})

	live, err := w.manager().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Instance != "rig-train" {
		t.Errorf("live = %+v, want only rig-train", live)
	}
	// a dead tunnel must not show as live to a later ensure either
	w.onLaunch = w.connectedSupervisor(43)
	if _, already, _ := w.manager().Ensure(dead, fwd([2]int{7000, 7000})); already {
		t.Error("ensure trusted a dead supervisor's state")
	}
}

func TestStopInstanceStopsAllItsTunnels(t *testing.T) {
	w := newWorld(t)
	w.alive[42], w.alive[43], w.alive[44] = true, true, true
	w.seed(State{Endpoint: ep, Forwards: fwd([2]int{8000, 8000}), PID: 42, Phase: PhaseConnected})
	w.seed(State{Endpoint: ep, Forwards: fwd([2]int{9090, 9090}), PID: 43, Phase: PhaseConnected})
	other := Endpoint{Instance: "rig-other", ID: "id-2", IP: "10.0.0.6"}
	w.seed(State{Endpoint: other, Forwards: fwd([2]int{7000, 7000}), PID: 44, Phase: PhaseConnected})

	stopped, err := w.manager().StopInstance("rig-train", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 2 {
		t.Errorf("stopped %d tunnels, want 2: %+v", len(stopped), stopped)
	}
	if len(w.stopped) != 2 {
		t.Errorf("Stop called for %v, want pids 42 and 43", w.stopped)
	}
	live, _ := w.manager().List()
	if len(live) != 1 || live[0].Instance != "rig-other" {
		t.Errorf("live = %+v, want only rig-other untouched", live)
	}
}

func TestStopInstancePortsStopsOnlyMatching(t *testing.T) {
	w := newWorld(t)
	w.alive[42], w.alive[43] = true, true
	w.seed(State{Endpoint: ep, Forwards: fwd([2]int{8000, 8000}), PID: 42, Phase: PhaseConnected})
	w.seed(State{Endpoint: ep, Forwards: fwd([2]int{9090, 9090}), PID: 43, Phase: PhaseConnected})

	stopped, err := w.manager().StopInstance("rig-train", []int{9090})
	if err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 1 || stopped[0].PID != 43 {
		t.Errorf("stopped = %+v, want only the 9090 supervisor (43)", stopped)
	}
	live, _ := w.manager().List()
	if len(live) != 1 || live[0].Forwards[0].Local != 8000 {
		t.Errorf("live = %+v, want the 8000 tunnel still up", live)
	}
}

// Stopping what is already stopped is reached-desired-state: success.
func TestStopInstanceNothingRunning(t *testing.T) {
	w := newWorld(t)
	stopped, err := w.manager().StopInstance("rig-train", nil)
	if err != nil || len(stopped) != 0 {
		t.Errorf("stopped, err = %+v, %v; want none, nil", stopped, err)
	}
	// unknown port on a live tunnel's instance is equally already-stopped
	w.alive[42] = true
	w.seed(State{Endpoint: ep, Forwards: fwd([2]int{8000, 8000}), PID: 42, Phase: PhaseConnected})
	stopped, err = w.manager().StopInstance("rig-train", []int{5555})
	if err != nil || len(stopped) != 0 {
		t.Errorf("stopped, err = %+v, %v; want none, nil", stopped, err)
	}
	if live, _ := w.manager().List(); len(live) != 1 {
		t.Errorf("live = %+v, want the 8000 tunnel untouched", live)
	}
}

// A supervisor that outlives the SIGTERM wait is a failure to reach the
// desired state — stop must say so, not report success.
func TestStopInstanceReportsUnkillableSupervisor(t *testing.T) {
	w := newWorld(t)
	w.seed(State{Endpoint: ep, Forwards: fwd([2]int{8000, 8000}), PID: 42, Phase: PhaseConnected})
	m := w.manager()
	m.Alive = func(pid int) bool { return pid == 42 } // ignores SIGTERM
	m.Stop = func(pid int) error { w.stopped = append(w.stopped, pid); return nil }

	if _, err := m.StopInstance("rig-train", nil); err == nil || !strings.Contains(err.Error(), "42") {
		t.Errorf("err = %v, want a failure naming supervisor 42", err)
	}
}

// The stale supervisor's ssh child releases its ports only when it dies —
// launching the replacement while it is still alive loses the bind race.
func TestEnsureWaitsForStaleSupervisorDeath(t *testing.T) {
	w := newWorld(t)
	stale := Endpoint{Instance: "rig-train", ID: "id-old", IP: "10.0.0.99"}
	w.seed(State{Endpoint: stale, Forwards: fwd([2]int{8000, 8000}), PID: 42, Phase: PhaseConnected})

	m := w.manager()
	aliveChecks := 0
	m.Alive = func(pid int) bool {
		if pid == 42 {
			aliveChecks++
			return aliveChecks <= 3 // SIGTERM lands, but death takes a few checks
		}
		return w.alive[pid]
	}
	m.Stop = func(pid int) error { w.stopped = append(w.stopped, pid); return nil }
	w.onLaunch = func(st State) int {
		if m.Alive(42) {
			t.Error("replacement launched while the stale supervisor was still alive")
		}
		return w.connectedSupervisor(43)(st)
	}

	if _, _, err := m.Ensure(ep, fwd([2]int{8000, 8000})); err != nil {
		t.Fatal(err)
	}
	if len(w.stopped) != 1 || w.stopped[0] != 42 {
		t.Errorf("stopped = %v, want the stale supervisor (42)", w.stopped)
	}
}

// Accepted invariant (2026-08-21): a live tunnel recorded under this
// instance's name but with a stale ID/IP (name reuse after an incomplete
// down) is replaced, not reported already-up — it forwards to a recycled
// address.
func TestEnsureReplacesTunnelWithStaleEndpoint(t *testing.T) {
	w := newWorld(t)
	w.alive[42] = true
	stale := Endpoint{Instance: "rig-train", ID: "id-old", IP: "10.0.0.99"}
	w.seed(State{Endpoint: stale, Forwards: fwd([2]int{8000, 8000}), PID: 42, Phase: PhaseConnected})
	w.onLaunch = w.connectedSupervisor(43)

	st, already, err := w.manager().Ensure(ep, fwd([2]int{8000, 8000}))
	if err != nil {
		t.Fatal(err)
	}
	if already {
		t.Error("ensure reported a stale-endpoint tunnel as already-up")
	}
	if len(w.stopped) != 1 || w.stopped[0] != 42 {
		t.Errorf("stopped = %v, want the stale supervisor (42)", w.stopped)
	}
	if st.PID != 43 || st.IP != "10.0.0.5" {
		t.Errorf("state = %+v, want a fresh pid-43 tunnel to 10.0.0.5", st)
	}
	live, _ := w.manager().List()
	if len(live) != 1 || live[0].IP != "10.0.0.5" {
		t.Errorf("live = %+v, want only the replacement", live)
	}
}

// A container's mapped sshd port is part of the endpoint: it must round-trip
// through the state file so the supervisor dials it, and a live tunnel
// recorded on the old port after a restart remapped it is stale — replaced,
// not reported already-up.
func TestEnsureRecordsPortAndReplacesRemappedTunnel(t *testing.T) {
	w := newWorld(t)
	w.alive[42] = true
	old := Endpoint{Instance: "rig-pod", ID: "id-7", IP: "1.2.3.4", Port: 10341}
	w.seed(State{Endpoint: old, Forwards: fwd([2]int{8000, 8000}), PID: 42, Phase: PhaseConnected})
	live, err := w.manager().List()
	if err != nil || len(live) != 1 || live[0].Port != 10341 {
		t.Fatalf("List = %+v, %v; want the seeded tunnel with port 10341", live, err)
	}

	remapped := Endpoint{Instance: "rig-pod", ID: "id-7", IP: "1.2.3.4", Port: 20517}
	w.onLaunch = w.connectedSupervisor(43)
	st, already, err := w.manager().Ensure(remapped, fwd([2]int{8000, 8000}))
	if err != nil {
		t.Fatal(err)
	}
	if already {
		t.Error("ensure reported a tunnel to the old port as already-up")
	}
	if len(w.stopped) != 1 || w.stopped[0] != 42 {
		t.Errorf("stopped = %v, want the old-port supervisor (42)", w.stopped)
	}
	if st.PID != 43 || st.Port != 20517 {
		t.Errorf("state = %+v, want a fresh pid-43 tunnel on port 20517", st)
	}
	if len(w.launched) != 1 || w.launched[0].Port != 20517 {
		t.Errorf("launched = %+v, want the supervisor handed port 20517", w.launched)
	}
}

// A VM on port 22 is recorded exactly as before this field existed: no port
// key in the file, so old state files and new ones read the same.
func TestStateOmitsDefaultPort(t *testing.T) {
	w := newWorld(t)
	w.alive[42] = true
	w.seed(State{Endpoint: ep, Forwards: fwd([2]int{8000, 8000}), PID: 42, Phase: PhaseConnected})
	files, _ := filepath.Glob(filepath.Join(w.dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("state files = %v", files)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"port"`) {
		t.Errorf("default-port state must not carry a port key: %s", b)
	}
}

// A non-root login user (an AWS image's ubuntu) is part of the endpoint: it
// must round-trip through the state file so the supervisor logs in as that
// user, while a root endpoint is recorded exactly as before the field
// existed — no user key — so old state files and new ones read the same.
func TestEnsureRecordsUser(t *testing.T) {
	w := newWorld(t)
	w.onLaunch = w.connectedSupervisor(42)
	dlami := Endpoint{Instance: "rig-aws", ID: "i-1", IP: "3.4.5.6", User: "ubuntu"}
	st, _, err := w.manager().Ensure(dlami, fwd([2]int{8000, 8000}))
	if err != nil {
		t.Fatal(err)
	}
	if st.User != "ubuntu" || len(w.launched) != 1 || w.launched[0].User != "ubuntu" {
		t.Errorf("state = %+v, launched = %+v; want the supervisor handed user ubuntu", st, w.launched)
	}
	live, err := w.manager().List()
	if err != nil || len(live) != 1 || live[0].User != "ubuntu" {
		t.Errorf("List = %+v, %v; want the tunnel with user ubuntu", live, err)
	}

	w.seed(State{Endpoint: ep, Forwards: fwd([2]int{9090, 9090}), PID: 43, Phase: PhaseConnected})
	b, err := os.ReadFile(State{Endpoint: ep, Forwards: fwd([2]int{9090, 9090})}.statePath(w.dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"user"`) {
		t.Errorf("root-user state must not carry a user key: %s", b)
	}
}
