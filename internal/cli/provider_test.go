package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/provider"
)

// opened counts how often the test driver's Factory ran; a selection that
// reaches it proves the name resolved to this driver.
var opened int

func init() {
	provider.Register("testfake", func(config.ProviderConfig) (provider.Provider, error) {
		opened++
		return &fakeProvider{}, nil
	})
}

// runSelecting executes the CLI with no App pre-installed, so newApp goes
// through the registry, and returns rig's stderr and the command error.
func runSelecting(t *testing.T, cfgText string, args ...string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(cfgText), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIG_CONFIG", path)
	prevApp, prevOpened := app, opened
	app = nil
	defer func() { app = prevApp; providerFlag = ""; jsonFlag = false }()

	r, w, _ := os.Pipe()
	prevStderr := os.Stderr
	os.Stderr = w
	root := NewRoot()
	root.SetArgs(args)
	execErr := root.Execute()
	w.Close()
	os.Stderr = prevStderr
	var buf [1 << 16]byte
	n, _ := r.Read(buf[:])
	r.Close()
	if opened != prevOpened+1 {
		t.Errorf("rig %v: test driver opened %d times, want once", args, opened-prevOpened)
	}
	return string(buf[:n]), execErr
}

func TestProviderFlagSelectsDriver(t *testing.T) {
	if _, err := runSelecting(t, "", "--provider", "testfake", "status", "--json"); err != nil {
		t.Fatal(err)
	}
}

func TestConfigProviderSelectsDriver(t *testing.T) {
	if _, err := runSelecting(t, "provider: testfake\n", "status", "--json"); err != nil {
		t.Fatal(err)
	}
}

func TestProfilePinSelectsDriver(t *testing.T) {
	// up fails later against the inventory-only fake; the pin has already
	// chosen the driver by then.
	runSelecting(t, "provider: nope\nprofiles:\n  p:\n    provider: testfake\n    types: [t]\n", "up", "p")
}

func TestProviderFlagOverridesProfilePinWithWarning(t *testing.T) {
	stderr, _ := runSelecting(t, "profiles:\n  p:\n    provider: nope\n    types: [t]\n", "--provider", "testfake", "up", "p")
	if !strings.Contains(stderr, "rig: --provider testfake overrides profile pin nope\n") {
		t.Errorf("stderr = %q, want the override warning", stderr)
	}
}

func TestUnknownProviderNamesRegistered(t *testing.T) {
	prevApp := app
	app = nil
	t.Setenv("RIG_CONFIG", filepath.Join(t.TempDir(), "none.yml"))
	defer func() { app = prevApp; providerFlag = "" }()
	root := NewRoot()
	root.SetArgs([]string{"--provider", "nope", "status"})
	err := root.Execute()
	want := `unknown provider "nope" (have: aws, runpod, testfake, vast, verda)`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
}
