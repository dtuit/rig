// Package tunnel manages supervised ssh port-forward tunnels: ensuring one
// exists to an instance, listing the live ones, and the state files the
// ensure side and the supervisor process share. State holds connection
// metadata only — never credentials (ADR-0003 spirit).
package tunnel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dtuit/rig/internal/config"
)

// Forward is one local-to-instance port forward.
type Forward struct {
	Local  int `json:"local"`
	Remote int `json:"remote"`
}

func (f Forward) pair() [2]int { return [2]int{f.Local, f.Remote} }

// Endpoint identifies the instance a tunnel connects to, pinned at ensure
// time. Ensure treats a recorded Endpoint whose ID, host, port or user no
// longer matches the resolved instance as a tunnel to a recycled endpoint (or
// a container whose mapped port moved) and replaces it. User is the ssh
// login the ensure side resolved (provider default or config override);
// empty means the supervisor falls back to config and root, as it did before
// the field existed. Host keeps the on-disk key "ip" it was born with, so
// state files written before the field was renamed still load.
type Endpoint struct {
	Instance string `json:"instance"` // full rig- hostname
	ID       string `json:"id"`       // provider instance ID
	Host     string `json:"ip"`       // ssh host: the instance IP, or a proxy host
	Port     int    `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
}

// Phase is a tunnel's lifecycle stage. Issue 05 adds "reconnecting" between
// connected and gave-up.
type Phase string

const (
	PhaseStarting  Phase = "starting"
	PhaseConnected Phase = "connected"
	PhaseGaveUp    Phase = "gave-up"
)

// State is one supervised tunnel as recorded in its state file. The
// supervisor owns the file: it writes on phase change and removes it on
// clean stop; readers treat a dead PID as stale and reap it.
type State struct {
	Endpoint
	Forwards  []Forward `json:"forwards"`
	PID       int       `json:"pid"` // supervisor pid
	StartedAt time.Time `json:"started_at"`
	Phase     Phase     `json:"phase"`
	LastError string    `json:"last_error,omitempty"`
	ChangedAt time.Time `json:"changed_at"`
}

// Dir returns the tunnel state directory, beside the config file (the
// ledger precedent).
func Dir() string {
	return filepath.Join(filepath.Dir(config.DefaultPath()), "tunnels")
}

// fileBase names a tunnel's state and log files: instance plus its local
// ports, so supervisors for the same instance's different port sets coexist.
func (s State) fileBase() string {
	parts := []string{s.Instance}
	for _, f := range s.Forwards {
		parts = append(parts, fmt.Sprint(f.Local))
	}
	return strings.Join(parts, "-")
}

func (s State) statePath(dir string) string { return filepath.Join(dir, s.fileBase()+".json") }
func (s State) logPath(dir string) string   { return filepath.Join(dir, s.fileBase()+".log") }

func readState(path string) (State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return s, nil
}

// writeState replaces the state file atomically so a polling reader never
// sees a partial write.
func writeState(dir string, s State) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	s.ChangedAt = time.Now()
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.statePath(dir))
}
