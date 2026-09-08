package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/provider"
	"github.com/dtuit/rig/internal/provider/aws"
	"github.com/dtuit/rig/internal/provider/runpod"
	"github.com/dtuit/rig/internal/provider/vast"
)

// row returns the whitespace-collapsed fields of the first table line whose
// first column is typ — an empty cell disappears, which is exactly what the
// spot column must do on a cloud without spot capacity.
func row(t *testing.T, out, typ string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == typ {
			return f
		}
	}
	t.Fatalf("no row for %s in:\n%s", typ, out)
	return nil
}

// The human table names the cloud it is describing, because the same type
// token means nothing without it.
func TestTypesTableNamesTheProvider(t *testing.T) {
	prov := &fakeProvider{
		types: []provider.InstanceType{{Type: "1H100.80S.32V", GPU: "H100", MemoryGB: 80, PricePerHour: 2.4, SpotPrice: 1.2}},
		avail: map[string][]string{"FIN-01": {"1H100.80S.32V"}},
	}
	for _, args := range [][]string{{"types"}, {"types", "--available"}} {
		out := runRig(t, prov, args...)
		if !strings.HasPrefix(out, "provider: fake\nTYPE") {
			t.Errorf("rig %v does not open with the provider line:\n%s", args, out)
		}
		want := []string{"1H100.80S.32V", "H100", "80GB", "2.40", "1.20", "FIN-01"}
		if got := row(t, out, "1H100.80S.32V"); !slices.Equal(got, want) {
			t.Errorf("row = %q, want %q", got, want)
		}
	}
}

// A container cloud quotes no system RAM (RunPod's catalogue has none) and
// sells no spot capacity: MEM reads as a dash and the spot cell is empty
// rather than claiming a price of 0.00.
func TestTypesTableDashesUnknownMemoryAndBlanksSpot(t *testing.T) {
	prov := &fakeProvider{
		types: []provider.InstanceType{{Type: "1xA100-SXM", GPU: "A100", GPUMemoryGB: 80, PricePerHour: 1.74}},
		avail: map[string][]string{"EU-CZ-1": {"1xA100-SXM"}},
		caps:  &provider.Capabilities{Volumes: true, Balance: true, DockerImages: true},
	}
	out := runRig(t, prov, "types")
	want := []string{"1xA100-SXM", "A100", "-", "1.74", "EU-CZ-1"}
	if got := row(t, out, "1xA100-SXM"); !slices.Equal(got, want) {
		t.Errorf("row = %q, want %q (no MEM, no spot cell)", got, want)
	}
	if strings.Contains(out, "0.00") {
		t.Errorf("unsupported spot rendered as a price:\n%s", out)
	}
}

// On a Docker-image cloud the two rows are only rig's default and whatever
// the config names, so the table is followed by the standing offer; a cloud
// with a real image catalogue gets no such hint.
func TestImagesHintOnlyOnDockerImageProviders(t *testing.T) {
	const hint = "any image reference is accepted with --image / image:"
	docker := &fakeProvider{
		images: []provider.Image{{Name: "runpod/pytorch:1.2.0", Display: "runpod/pytorch:1.2.0", Default: true}},
		caps:   &provider.Capabilities{Volumes: true, Balance: true, DockerImages: true},
	}
	out := runRig(t, docker, "images")
	if !strings.HasPrefix(out, "provider: fake\nIMAGE") {
		t.Errorf("rig images does not open with the provider line:\n%s", out)
	}
	if !strings.Contains(out, hint) {
		t.Errorf("rig images lacks the hint:\n%s", out)
	}

	catalogue := &fakeProvider{
		images: []provider.Image{{Name: "ubuntu-24.04", Display: "Ubuntu 24.04", Default: true}},
	}
	if out := runRig(t, catalogue, "images"); strings.Contains(out, hint) {
		t.Errorf("a cloud with an image catalogue must not offer any reference:\n%s", out)
	}
}

// The example `rig config` prints is the only documentation of a provider
// block a user in trouble has to hand, so every key in it must be one the
// driver actually decodes — and the file must load.
func TestExampleConfigDecodesForEveryDriver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(exampleConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIG_CONFIG", path)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("the example config does not load: %v", err)
	}
	for _, name := range []string{"verda", "runpod", "vast", "aws"} {
		if _, ok := cfg.Providers[name]; !ok {
			t.Errorf("the example has no providers block for %s", name)
		}
	}

	var pod runpod.Options
	if err := cfg.ProviderConfig("runpod").Decode(&pod); err != nil {
		t.Fatalf("runpod block: %v", err)
	}
	if pod.Cloud != "secure" || pod.Image == "" || pod.DiskGB != 20 {
		t.Errorf("runpod keys not decoded: %+v", pod)
	}

	var vst vast.Config
	if err := cfg.ProviderConfig("vast").Decode(&vst); err != nil {
		t.Fatalf("vast block: %v", err)
	}
	if vst.MinReliability != 0.95 || vst.BidMarkup != 1.2 || vst.Image == "" || vst.DiskGB != 20 || vst.Filters == nil {
		t.Errorf("vast keys not decoded: %+v", vst)
	}

	var ec2 aws.Config
	if err := cfg.ProviderConfig("aws").Decode(&ec2); err != nil {
		t.Fatalf("aws block: %v", err)
	}
	if ec2.Region == "" || ec2.SecurityGroup != "rig" || ec2.VolumeType != "gp3" || len(ec2.Families) == 0 {
		t.Errorf("aws keys not decoded: %+v", ec2)
	}
}
