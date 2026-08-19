package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/core"
	"github.com/dtuit/rig/internal/provider"
)

// fakeProvider serves canned inventory; methods rig's read paths don't hit
// just fail loudly.
type fakeProvider struct {
	instances []provider.Instance
	types     []provider.InstanceType
	volumes   []provider.Volume
	avail     map[string][]string
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Locations(context.Context) ([]provider.Location, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeProvider) InstanceTypes(context.Context) ([]provider.InstanceType, error) {
	return f.types, nil
}
func (f *fakeProvider) Availability(context.Context, bool) (map[string][]string, error) {
	return f.avail, nil
}
func (f *fakeProvider) Images(context.Context, string) ([]provider.Image, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeProvider) ListInstances(context.Context) ([]provider.Instance, error) {
	return f.instances, nil
}
func (f *fakeProvider) GetInstance(context.Context, string) (*provider.Instance, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeProvider) CreateInstance(context.Context, provider.CreateInstanceRequest) (*provider.Instance, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeProvider) DeleteInstance(context.Context, string, []string) error {
	return fmt.Errorf("not implemented")
}
func (f *fakeProvider) ShutdownInstance(context.Context, string) error {
	return fmt.Errorf("not implemented")
}
func (f *fakeProvider) StartInstance(context.Context, string) error {
	return fmt.Errorf("not implemented")
}
func (f *fakeProvider) ListVolumes(context.Context) ([]provider.Volume, error) {
	return f.volumes, nil
}
func (f *fakeProvider) GetVolume(context.Context, string) (*provider.Volume, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeProvider) CreateVolume(context.Context, provider.CreateVolumeRequest) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (f *fakeProvider) AttachVolume(context.Context, string, string) error {
	return fmt.Errorf("not implemented")
}
func (f *fakeProvider) DetachVolume(context.Context, string, string) error {
	return fmt.Errorf("not implemented")
}
func (f *fakeProvider) DeleteVolume(context.Context, string) error {
	return fmt.Errorf("not implemented")
}
func (f *fakeProvider) EnsureSSHKey(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (f *fakeProvider) Balance(context.Context) (float64, string, error) {
	return 0, "", fmt.Errorf("not implemented")
}

// runRig executes the CLI with a fake provider installed and returns stdout.
func runRig(t *testing.T, prov provider.Provider, args ...string) string {
	t.Helper()
	prevApp := app
	app = &core.App{Cfg: &config.Config{}, Prov: prov}
	defer func() { app = prevApp; jsonFlag = false }()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prevStdout := os.Stdout
	os.Stdout = w
	root := NewRoot()
	root.SetArgs(args)
	execErr := root.Execute()
	w.Close()
	os.Stdout = prevStdout
	var buf [1 << 16]byte
	n, _ := r.Read(buf[:])
	r.Close()
	if execErr != nil {
		t.Fatalf("rig %v: %v", args, execErr)
	}
	return string(buf[:n])
}

func TestStatusJSON(t *testing.T) {
	prov := &fakeProvider{instances: []provider.Instance{{
		ID: "abc123", Name: "rig-train", Type: "1H100.80S.32V", Status: "provisioning",
		Location: "FIN-01", PricePerHour: 2.4, CreatedAt: time.Now(),
	}}}
	out := runRig(t, prov, "status", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0]["ttl"] != "-" {
		t.Errorf(`ttl = %v, want "-"`, rows[0]["ttl"])
	}
	if rows[0]["name"] != "rig-train" {
		t.Errorf(`name = %v, want "rig-train"`, rows[0]["name"])
	}
	if rows[0]["price_per_hour"] != 2.4 {
		t.Errorf(`price_per_hour = %v, want 2.4`, rows[0]["price_per_hour"])
	}
}

func TestStatusJSONEmptyList(t *testing.T) {
	out := runRig(t, &fakeProvider{}, "status", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || rows == nil {
		t.Fatalf("want empty JSON array, got %q (err %v)", out, err)
	}
}

func TestTypesJSON(t *testing.T) {
	prov := &fakeProvider{
		types: []provider.InstanceType{{Type: "1H100.80S.32V", GPU: "H100", MemoryGB: 80, PricePerHour: 2.4, SpotPrice: 1.2}},
		avail: map[string][]string{"FIN-01": {"1H100.80S.32V"}},
	}
	out := runRig(t, prov, "types", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	av, ok := rows[0]["available"].([]any)
	if !ok || len(av) != 1 || av[0] != "FIN-01" {
		t.Errorf(`available = %v, want ["FIN-01"]`, rows[0]["available"])
	}
	if rows[0]["spot_price"] != 1.2 {
		t.Errorf(`spot_price = %v, want 1.2`, rows[0]["spot_price"])
	}
}

func TestVolumesJSON(t *testing.T) {
	prov := &fakeProvider{volumes: []provider.Volume{{ID: "v1", Name: "rig-data", SizeGB: 500, Location: "FIN-01"}}}
	out := runRig(t, prov, "volumes", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(rows) != 1 || rows[0]["size_gb"] != float64(500) {
		t.Fatalf(`rows = %v, want one with size_gb 500`, rows)
	}
}

func TestStatusTableUnchangedWithoutJSON(t *testing.T) {
	out := runRig(t, &fakeProvider{}, "status")
	if out != "no rig instances\n" {
		t.Errorf("table output changed: %q", out)
	}
}
