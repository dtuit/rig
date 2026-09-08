package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/core"
	"github.com/dtuit/rig/internal/provider"
	"github.com/dtuit/rig/internal/provider/fake"
)

// runRigAsking executes the CLI with a fake provider installed and an empty
// stdin, so a confirmation prompt is declined, and returns stdout with the
// command error — the shape a prompt assertion needs.
func runRigAsking(t *testing.T, prov provider.Provider, args ...string) (string, error) {
	t.Helper()
	t.Setenv("RIG_CONFIG", filepath.Join(t.TempDir(), "config.yml"))
	prevApp := app
	app = &core.App{Cfg: &config.Config{}, Prov: prov}
	defer func() { app = prevApp; instanceFlag = ""; jsonFlag = false }()

	in, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	inW.Close() // nothing to read: confirm declines
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prevStdin, prevStdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = in, w
	root := NewRoot()
	root.SetArgs(args)
	execErr := root.Execute()
	w.Close()
	os.Stdin, os.Stdout = prevStdin, prevStdout
	in.Close()
	var buf [1 << 16]byte
	n, _ := r.Read(buf[:])
	r.Close()
	return string(buf[:n]), execErr
}

// A self-stopped instance still bills its disk, so a bare `rig down` must
// find it with no -i — and say it is stopped before destroying it, so nobody
// tears down the wrong thing.
func TestDownSelectsAStoppedInstanceAndSaysSo(t *testing.T) {
	prov := fake.New()
	if _, err := prov.CreateInstance(context.Background(), provider.CreateInstanceRequest{
		Name: "rig-smoke", Type: "1H100.80S.30V", Location: "FIN-01",
	}); err != nil {
		t.Fatal(err)
	}
	if err := prov.SetStatus("fake-1", provider.Stopped); err != nil {
		t.Fatal(err)
	}

	out, err := runRigAsking(t, prov, "down")
	if err == nil || err.Error() != "aborted" {
		t.Fatalf("rig down = %v, want the declined prompt to abort", err)
	}
	if want := "delete instance smoke (stopped)?"; !strings.Contains(out, want) {
		t.Errorf("prompt = %q, want it to contain %q", out, want)
	}
	if len(prov.Deleted) != 0 {
		t.Errorf("a declined prompt deleted %v", prov.Deleted)
	}
}
