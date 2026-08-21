package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dtuit/rig/internal/tunnel"
)

// tunnelDir points tunnel.Dir() at a temp directory for the test.
func tunnelDir(t *testing.T) string {
	t.Helper()
	t.Setenv("RIG_CONFIG", filepath.Join(t.TempDir(), "config.yml"))
	return tunnel.Dir()
}

// seedTunnel writes a state file as a supervisor would. List reads any
// *.json in the directory, so the file name only needs to be unique.
func seedTunnel(t *testing.T, st tunnel.State) {
	t.Helper()
	dir := tunnel.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("%s-%d.json", st.Instance, st.Forwards[0].Local)
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeSupervisor spawns a real process to stand in for a supervisor, so the
// production Stop (SIGTERM) and Alive (pid probe) act on something real.
func fakeSupervisor(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait() // reap on death, so pid-liveness goes false
	t.Cleanup(func() { cmd.Process.Kill() })
	return cmd.Process.Pid
}

func connectedState(instance, id, ip string, pid, local, remote int) tunnel.State {
	return tunnel.State{
		Endpoint:  tunnel.Endpoint{Instance: instance, ID: id, IP: ip},
		Forwards:  []tunnel.Forward{{Local: local, Remote: remote}},
		PID:       pid,
		StartedAt: time.Now(),
		Phase:     tunnel.PhaseConnected,
	}
}

func TestTunnelStatusJSON(t *testing.T) {
	tunnelDir(t)
	self := os.Getpid()
	seedTunnel(t, connectedState("rig-train", "id-1", "10.0.0.5", self, 8000, 8000))
	seedTunnel(t, connectedState("rig-other", "id-2", "10.0.0.6", self, 7000, 80))

	out := runRig(t, &fakeProvider{}, "tunnel", "status", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	byName := map[string]map[string]any{}
	for _, r := range rows {
		byName[r["instance"].(string)] = r
	}
	train := byName["rig-train"]
	if train == nil || train["phase"] != "connected" || train["pid"] != float64(self) {
		t.Errorf("rig-train row = %v, want phase connected pid %d", train, self)
	}
	fwds, ok := byName["rig-other"]["forwards"].([]any)
	if !ok || len(fwds) != 1 || fwds[0].(map[string]any)["remote"] != float64(80) {
		t.Errorf(`forwards = %v, want [{"local":7000,"remote":80}]`, byName["rig-other"]["forwards"])
	}
}

func TestTunnelStatusJSONEmpty(t *testing.T) {
	tunnelDir(t)
	out := runRig(t, &fakeProvider{}, "tunnel", "status", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || rows == nil {
		t.Fatalf("want empty JSON array, got %q (err %v)", out, err)
	}
}

func TestTunnelStatusTable(t *testing.T) {
	tunnelDir(t)
	seedTunnel(t, connectedState("rig-train", "id-1", "10.0.0.5", os.Getpid(), 8000, 8000))
	seedTunnel(t, connectedState("rig-other", "id-2", "10.0.0.6", os.Getpid(), 7000, 80))

	out := runRig(t, &fakeProvider{}, "tunnel", "status")
	for _, want := range []string{"INSTANCE", "train", "other", "8000", "7000->80", "connected", "10.0.0.5"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestTunnelStatusTableEmpty(t *testing.T) {
	tunnelDir(t)
	out := runRig(t, &fakeProvider{}, "tunnel", "status")
	if out != "no detached tunnels\n" {
		t.Errorf("empty status = %q", out)
	}
}

func TestTunnelStatusReapsDeadSupervisor(t *testing.T) {
	dir := tunnelDir(t)
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	seedTunnel(t, connectedState("rig-train", "id-1", "10.0.0.5", dead.Process.Pid, 8000, 8000))

	out := runRig(t, &fakeProvider{}, "tunnel", "status")
	if out != "no detached tunnels\n" {
		t.Errorf("dead supervisor still shown: %q", out)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) != 0 {
		t.Errorf("stale state not reaped: %v", files)
	}
}

func TestTunnelStopAll(t *testing.T) {
	dir := tunnelDir(t)
	trainA, trainB, other := fakeSupervisor(t), fakeSupervisor(t), fakeSupervisor(t)
	seedTunnel(t, connectedState("rig-train", "id-1", "10.0.0.5", trainA, 8000, 8000))
	seedTunnel(t, connectedState("rig-train", "id-1", "10.0.0.5", trainB, 9090, 9090))
	seedTunnel(t, connectedState("rig-other", "id-2", "10.0.0.6", other, 7000, 7000))

	out := runRig(t, &fakeProvider{}, "tunnel", "stop", "-i", "train")
	if !strings.Contains(out, "stopped") {
		t.Errorf("stop said %q, want a stopped report", out)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 || !strings.Contains(files[0], "rig-other") {
		t.Errorf("state files after stop = %v, want only rig-other's", files)
	}
}

func TestTunnelStopPort(t *testing.T) {
	dir := tunnelDir(t)
	seedTunnel(t, connectedState("rig-train", "id-1", "10.0.0.5", fakeSupervisor(t), 8000, 8000))
	seedTunnel(t, connectedState("rig-train", "id-1", "10.0.0.5", fakeSupervisor(t), 9090, 9090))

	runRig(t, &fakeProvider{}, "tunnel", "stop", "-i", "train", "9090")
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 || !strings.Contains(files[0], "8000") {
		t.Errorf("state files after stop 9090 = %v, want only the 8000 tunnel", files)
	}
}

// Stopping when nothing runs exits 0 — runRig fails the test on any error.
func TestTunnelStopNothingRunning(t *testing.T) {
	tunnelDir(t)
	out := runRig(t, &fakeProvider{}, "tunnel", "stop", "-i", "train")
	if !strings.Contains(out, "no detached tunnels") {
		t.Errorf("stop-nothing said %q", out)
	}
}

func TestResolveTunnelInstance(t *testing.T) {
	states := []tunnel.State{
		connectedState("rig-train", "id-1", "10.0.0.5", 1, 8000, 8000),
		connectedState("rig-other", "id-2", "10.0.0.6", 2, 7000, 7000),
	}
	if got, err := resolveTunnelInstance(states, "train"); err != nil || got != "rig-train" {
		t.Errorf(`resolve "train" = %q, %v; want rig-train`, got, err)
	}
	if got, err := resolveTunnelInstance(states, "rig-other"); err != nil || got != "rig-other" {
		t.Errorf(`resolve "rig-other" = %q, %v`, got, err)
	}
	// -i is documented as "name or ID prefix" — an ID prefix must find the
	// tunnel, not silently no-op against a nonexistent rig-<id> name
	if got, err := resolveTunnelInstance(states, "id-2"); err != nil || got != "rig-other" {
		t.Errorf(`resolve by ID prefix "id-2" = %q, %v; want rig-other`, got, err)
	}
	// a key with no matching tunnel is already-stopped: a no-op, not an error
	if got, err := resolveTunnelInstance(states, "ghost"); err != nil || got != "rig-ghost" {
		t.Errorf(`resolve "ghost" = %q, %v; want rig-ghost no-op`, got, err)
	}
	if _, err := resolveTunnelInstance(states, ""); err == nil || !strings.Contains(err.Error(), "-i") {
		t.Errorf("ambiguous resolve err = %v, want a pick-one-with--i error", err)
	}
	if got, err := resolveTunnelInstance(states[:1], ""); err != nil || got != "rig-train" {
		t.Errorf("sole-instance resolve = %q, %v; want rig-train", got, err)
	}
	if got, err := resolveTunnelInstance(nil, ""); err != nil || got != "" {
		t.Errorf("no-tunnels resolve = %q, %v; want empty success", got, err)
	}
}
