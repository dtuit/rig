package tunnel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Manager ensures, lists, and stops supervised tunnels. The process seams
// (Launch, Alive, Stop) are injectable so the whole ensure protocol is
// testable without real ssh — the remote.Runner precedent. Zero values get
// production defaults.
type Manager struct {
	Dir     string                   // state directory (Dir() in production)
	Launch  func(State) (int, error) // spawn a detached supervisor; returns its pid
	Alive   func(pid int) bool       // is this supervisor still running?
	Stop    func(pid int) error      // terminate a supervisor
	Timeout time.Duration            // startup window before ensure gives up (default 30s)

	poll  time.Duration       // state-file poll interval
	sleep func(time.Duration) // test seam
}

func (m *Manager) init() {
	if m.Launch == nil {
		m.Launch = Launcher(m.Dir)
	}
	if m.Alive == nil {
		m.Alive = pidAlive
	}
	if m.Stop == nil {
		m.Stop = stopSupervisor
	}
	if m.Timeout <= 0 {
		m.Timeout = 30 * time.Second
	}
	if m.poll <= 0 {
		m.poll = 200 * time.Millisecond
	}
	if m.sleep == nil {
		m.sleep = time.Sleep
	}
}

// List returns the live tunnels, reaping state files whose supervisor is
// dead (killed process, machine reboot) or unreadable.
func (m *Manager) List() ([]State, error) {
	m.init()
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []State
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(m.Dir, e.Name())
		st, err := readState(path)
		if err != nil || !m.Alive(st.PID) {
			os.Remove(path)
			continue
		}
		out = append(out, st)
	}
	return out, nil
}

// Ensure guarantees a live tunnel to ep covering forwards. Exit conditions
// honor the agent contract: (state, true, nil) when an equivalent tunnel
// already covers everything; (state, false, nil) only once freshly started
// forwards are actually usable; an error otherwise — a conflicting local
// port, a supervisor failure, or the startup window expiring.
func (m *Manager) Ensure(ep Endpoint, forwards []Forward) (State, bool, error) {
	m.init()
	if len(forwards) == 0 {
		return State{}, false, fmt.Errorf("no forwards requested")
	}
	live, err := m.List()
	if err != nil {
		return State{}, false, err
	}

	// Name reuse after an incomplete down: a tunnel recorded under this
	// instance's name whose ID/IP no longer match forwards to a recycled
	// address — replace it, never trust it. Wait out the stale supervisor's
	// death (bounded): its ssh child releases the local ports only then, and
	// the replacement must not lose that bind race.
	kept := live[:0]
	for _, st := range live {
		if st.Instance == ep.Instance && st.Endpoint != ep {
			_ = m.Stop(st.PID)
			for i := 0; m.Alive(st.PID) && i < 50; i++ {
				m.sleep(m.poll)
			}
			os.Remove(st.statePath(m.Dir))
			continue
		}
		kept = append(kept, st)
	}
	live = kept

	type owner struct {
		st     State
		remote int
	}
	owners := map[int]owner{}
	for _, st := range live {
		for _, f := range st.Forwards {
			owners[f.Local] = owner{st, f.Remote}
		}
	}

	var missing []Forward
	var covering State
	for _, f := range forwards {
		o, ok := owners[f.Local]
		if !ok {
			missing = append(missing, f)
			continue
		}
		if o.st.ID != ep.ID || o.remote != f.Remote {
			return State{}, false, fmt.Errorf("local port %d is already forwarded to %s:%d for %s",
				f.Local, o.st.IP, o.remote, o.st.Instance)
		}
		covering = o.st
	}
	if len(missing) == 0 {
		return covering, true, nil
	}

	st := State{Endpoint: ep, Forwards: missing, Phase: PhaseStarting, StartedAt: time.Now()}
	pid, err := m.Launch(st)
	if err != nil {
		return State{}, false, fmt.Errorf("starting tunnel supervisor: %w", err)
	}
	path := st.statePath(m.Dir)
	deadline := time.Now().Add(m.Timeout)
	for {
		if cur, err := readState(path); err == nil {
			switch cur.Phase {
			case PhaseConnected:
				return cur, false, nil
			case PhaseGaveUp:
				os.Remove(path)
				return State{}, false, fmt.Errorf("tunnel to %s failed: %s", ep.Instance, cur.LastError)
			}
		}
		if time.Now().After(deadline) {
			_ = m.Stop(pid)
			os.Remove(path)
			return State{}, false, fmt.Errorf("tunnel to %s not usable after %s (supervisor log: %s)",
				ep.Instance, m.Timeout, st.logPath(m.Dir))
		}
		m.sleep(m.poll)
	}
}
