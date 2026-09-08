package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/instcache"
	"github.com/dtuit/rig/internal/provider"
	"github.com/dtuit/rig/internal/provider/fake"
)

// newApp wires a fake provider into an App whose local state (instance cache,
// ledger) and ssh key live under a temp dir, so nothing of the developer's
// is read or written.
func newApp(t *testing.T, prov *fake.Provider) (*App, *[]string) {
	t.Helper()
	return newAppIn(t, t.TempDir(), prov)
}

// newAppIn is newApp with the local-state directory chosen by the caller, so
// two Apps driving different providers can share one cache and ledger the
// way two rig invocations on one workstation do.
func newAppIn(t *testing.T, dir string, prov *fake.Provider) (*App, *[]string) {
	t.Helper()
	t.Setenv("RIG_CONFIG", filepath.Join(dir, "config.yml"))
	pub := filepath.Join(dir, "id_ed25519.pub")
	if err := os.WriteFile(pub, []byte("ssh-ed25519 AAAAtest rig@test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs []string
	app := &App{
		Cfg:  &config.Config{SSH: config.SSH{PublicKey: pub}},
		Prov: prov,
		Log:  func(format string, a ...any) { logs = append(logs, strings.TrimSpace(fmt.Sprintf(format, a...))) },
	}
	return app, &logs
}

func TestPickOrdersTypesThenVolumeLocationsThenAlphabetical(t *testing.T) {
	prov := fake.New()
	prov.Avail = map[string][]string{
		"FIN-01": {"1H100.80S.30V", "1A100.40S.22V"},
		"FIN-02": {"1H100.80S.30V"},
		"ICE-01": {"1A100.40S.22V"},
	}
	prov.Volumes = []provider.Volume{{ID: "vol-1", Name: "data", Location: "FIN-02"}}
	app, _ := newApp(t, prov)

	got, err := app.Pick(context.Background(), UpSpec{
		Types:  []string{"1H100.80S.30V", "1A100.40S.22V", "8H100.80S.176V"},
		Volume: &config.VolumeSpec{Name: "data"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Candidate{
		{Type: "1H100.80S.30V", Location: "FIN-02"}, // holds the volume: wins the tie
		{Type: "1H100.80S.30V", Location: "FIN-01"},
		{Type: "1A100.40S.22V", Location: "FIN-01"},
		{Type: "1A100.40S.22V", Location: "ICE-01"},
	}
	if len(got) != len(want) {
		t.Fatalf("Pick = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Pick[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestPickMatchesTypeTokensCaseInsensitively(t *testing.T) {
	// marketplace tokens (1xRTX-4090) are typed by hand; the provider's
	// spelling is what the create request must carry
	prov := fake.New()
	prov.Avail = map[string][]string{"US-TX-3": {"1xRTX-4090"}}
	app, _ := newApp(t, prov)

	got, err := app.Pick(context.Background(), UpSpec{Types: []string{"1xrtx-4090"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (Candidate{Type: "1xRTX-4090", Location: "US-TX-3"}) {
		t.Errorf("Pick = %v, want the provider's spelling in US-TX-3", got)
	}
}

func TestPickHonoursLocationPin(t *testing.T) {
	prov := fake.New()
	prov.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}, "ICE-01": {"1H100.80S.30V"}}
	app, _ := newApp(t, prov)

	got, err := app.Pick(context.Background(), UpSpec{Types: []string{"1H100.80S.30V"}, Location: "ICE-01"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (Candidate{Type: "1H100.80S.30V", Location: "ICE-01"}) {
		t.Errorf("Pick = %v, want just ICE-01", got)
	}

	_, err = app.Pick(context.Background(), UpSpec{Types: []string{"1H100.80S.30V"}, Location: "FIN-02", Spot: true})
	want := "none of 1H100.80S.30V available spot in FIN-02 right now (rig types --available to see what is)"
	if err == nil || err.Error() != want {
		t.Errorf("Pick(unavailable) = %v, want %q", err, want)
	}
}

func TestProvisionVerdaShapedHappyPath(t *testing.T) {
	prov := fake.New()
	prov.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}}
	// waitRunning sleeps 10s after a non-terminal status, so the scripted
	// instance is Running on its first poll to keep the test fast.
	prov.Statuses["fake-1"] = []provider.Status{provider.Running}
	app, logs := newApp(t, prov)

	in, err := app.Provision(context.Background(), UpSpec{Name: "train", Types: []string{"1H100.80S.30V"}, OSGB: 100})
	if err != nil {
		t.Fatal(err)
	}
	if in.ID != "fake-1" || in.Name != "rig-train" || in.Status != provider.Running || in.IP != "10.0.0.1" {
		t.Errorf("Provision = %+v", in)
	}
	if len(prov.Requests) != 1 {
		t.Fatalf("CreateInstance calls = %d, want 1", len(prov.Requests))
	}
	req := prov.Requests[0]
	if req.Type != "1H100.80S.30V" || req.Location != "FIN-01" || req.Name != "rig-train" ||
		req.Image != "fake-image" || req.OSVolumeGB != 100 || len(req.SSHKeyIDs) != 1 || req.SSHKeyIDs[0] != "key-1" {
		t.Errorf("CreateInstanceRequest = %+v", req)
	}
	if len(prov.Deleted) != 0 {
		t.Errorf("nothing should be deleted on the happy path, got %v", prov.Deleted)
	}
	wantLog := "selected 1H100.80S.30V in FIN-01 (spot=false)"
	if !contains(*logs, wantLog) {
		t.Errorf("logs %q lack %q", *logs, wantLog)
	}
}

func TestProvisionStripsHostPrefixFromName(t *testing.T) {
	prov := fake.New()
	prov.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}}
	app, _ := newApp(t, prov)

	in, err := app.Provision(context.Background(), UpSpec{Name: "rig-x", Types: []string{"1H100.80S.30V"}})
	if err != nil {
		t.Fatal(err)
	}
	if in.Name != "rig-x" {
		t.Errorf("Name = %q, want rig-x (not rig-rig-x)", in.Name)
	}
}

func TestProvisionRefusesExistingName(t *testing.T) {
	prov := fake.New()
	prov.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}}
	app, _ := newApp(t, prov)
	ctx := context.Background()
	if _, err := app.Provision(ctx, UpSpec{Name: "train", Types: []string{"1H100.80S.30V"}}); err != nil {
		t.Fatal(err)
	}

	_, err := app.Provision(ctx, UpSpec{Name: "train", Types: []string{"1H100.80S.30V"}})
	want := "instance rig-train already exists (running); pick another --name"
	if err == nil || err.Error() != want {
		t.Errorf("second Provision = %v, want %q", err, want)
	}
}

func TestProvisionWalksCandidatesOnNoCapacityAtCreate(t *testing.T) {
	prov := fake.New()
	prov.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}, "FIN-02": {"1H100.80S.30V"}, "ICE-01": {"1A100.40S.22V"}}
	prov.Creates = []error{
		errors.New("verda: no capacity in FIN-01: " + provider.ErrNoCapacity.Error()), // opaque text: not the sentinel
	}
	app, _ := newApp(t, prov)
	spec := UpSpec{Name: "train", Types: []string{"1H100.80S.30V", "1A100.40S.22V"}}

	// a plain error aborts, however it is worded
	_, err := app.Provision(context.Background(), spec)
	if err == nil || !strings.HasPrefix(err.Error(), "create failed: verda: no capacity in FIN-01") {
		t.Fatalf("plain create error should abort, got %v", err)
	}

	prov = fake.New()
	prov.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}, "FIN-02": {"1H100.80S.30V"}, "ICE-01": {"1A100.40S.22V"}}
	prov.Creates = []error{
		noCapacity("FIN-01"),
		noCapacity("FIN-02"),
		nil,
	}
	app, logs := newApp(t, prov)

	in, err := app.Provision(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if in.Type != "1A100.40S.22V" || in.Location != "ICE-01" {
		t.Errorf("landed on %s in %s, want 1A100.40S.22V in ICE-01", in.Type, in.Location)
	}
	got := make([]Candidate, 0, len(prov.Requests))
	for _, r := range prov.Requests {
		got = append(got, Candidate{Type: r.Type, Location: r.Location})
	}
	want := []Candidate{
		{Type: "1H100.80S.30V", Location: "FIN-01"},
		{Type: "1H100.80S.30V", Location: "FIN-02"},
		{Type: "1A100.40S.22V", Location: "ICE-01"},
	}
	if len(got) != len(want) {
		t.Fatalf("create attempts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("attempt %d = %v, want %v", i, got[i], want[i])
		}
	}
	for _, l := range []string{
		"no capacity for 1H100.80S.30V in FIN-01, trying next",
		"no capacity for 1H100.80S.30V in FIN-02, trying next",
	} {
		if !contains(*logs, l) {
			t.Errorf("logs %q lack %q", *logs, l)
		}
	}
}

func TestProvisionDeletesInstanceThatReportsNoCapacity(t *testing.T) {
	prov := fake.New()
	prov.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}, "FIN-02": {"1H100.80S.30V"}}
	prov.Statuses["fake-1"] = []provider.Status{provider.NoCapacity}
	prov.Statuses["fake-2"] = []provider.Status{provider.Running}
	app, logs := newApp(t, prov)

	in, err := app.Provision(context.Background(), UpSpec{Name: "train", Types: []string{"1H100.80S.30V"}})
	if err != nil {
		t.Fatal(err)
	}
	if in.ID != "fake-2" || in.Location != "FIN-02" {
		t.Errorf("landed on %s in %s, want fake-2 in FIN-02", in.ID, in.Location)
	}
	if len(prov.Deleted) != 1 || prov.Deleted[0].ID != "fake-1" {
		t.Fatalf("Deleted = %v, want just fake-1", prov.Deleted)
	}
	if v := prov.Deleted[0].VolumeIDs; len(v) != 1 || v[0] != "os-1" {
		t.Errorf("deleted volumes = %v, want the OS volume os-1 only", v)
	}
	if !contains(*logs, "no capacity for 1H100.80S.30V in FIN-01, trying next") {
		t.Errorf("logs %q lack the retry line", *logs)
	}
}

func TestProvisionExhaustedCandidatesIsNoneAvailable(t *testing.T) {
	prov := fake.New()
	prov.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}}
	prov.Creates = []error{noCapacity("FIN-01")}
	app, _ := newApp(t, prov)

	_, err := app.Provision(context.Background(), UpSpec{Name: "train", Types: []string{"1H100.80S.30V"}})
	want := "none of 1H100.80S.30V available on-demand right now (rig types --available to see what is)"
	if err == nil || err.Error() != want {
		t.Errorf("Provision = %v, want %q", err, want)
	}
}

func TestProvisionAbortsOnOtherTerminalStatus(t *testing.T) {
	prov := fake.New()
	prov.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}, "FIN-02": {"1H100.80S.30V"}}
	prov.Statuses["fake-1"] = []provider.Status{provider.Error}
	app, _ := newApp(t, prov)

	_, err := app.Provision(context.Background(), UpSpec{Name: "train", Types: []string{"1H100.80S.30V"}})
	if err == nil || err.Error() != `instance entered status "error"` {
		t.Errorf("Provision = %v, want the error status to abort", err)
	}
	if len(prov.Requests) != 1 {
		t.Errorf("an error status must not trigger the next candidate; creates = %d", len(prov.Requests))
	}
}

func TestProvisionRefusesUnsupportedSpotAndVolume(t *testing.T) {
	prov := fake.New()
	prov.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}}
	prov.Caps = provider.Capabilities{Spot: false, Volumes: false, Balance: true}
	app, _ := newApp(t, prov)
	ctx := context.Background()

	_, err := app.Provision(ctx, UpSpec{Name: "train", Types: []string{"1H100.80S.30V"}, Spot: true})
	if !errors.Is(err, provider.ErrUnsupported) || err.Error() != "fake does not offer spot instances: unsupported by provider" {
		t.Errorf("spot refusal = %v", err)
	}

	_, err = app.Provision(ctx, UpSpec{Name: "train", Types: []string{"1H100.80S.30V"}, Volume: &config.VolumeSpec{Name: "data"}})
	if !errors.Is(err, provider.ErrUnsupported) || err.Error() != "fake does not offer data volumes: unsupported by provider" {
		t.Errorf("volume refusal = %v", err)
	}
	if len(prov.Requests) != 0 {
		t.Errorf("a refusal must happen before any create; creates = %d", len(prov.Requests))
	}
}

func TestProvisionFindsVolumeInCandidateLocation(t *testing.T) {
	prov := fake.New()
	prov.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}, "FIN-02": {"1H100.80S.30V"}}
	prov.Volumes = []provider.Volume{{ID: "vol-9", Name: "data", Location: "FIN-02", SizeGB: 100}}
	app, _ := newApp(t, prov)

	in, err := app.Provision(context.Background(), UpSpec{
		Name: "train", Types: []string{"1H100.80S.30V"}, Volume: &config.VolumeSpec{Name: "data"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if in.Location != "FIN-02" {
		t.Errorf("Location = %s, want FIN-02 (where the volume lives)", in.Location)
	}
	if v := prov.Requests[0].ExistingVolumes; len(v) != 1 || v[0] != "vol-9" {
		t.Errorf("ExistingVolumes = %v, want [vol-9]", v)
	}
	if len(prov.Volumes) != 1 {
		t.Errorf("an existing volume must be reused, not recreated: %v", prov.Volumes)
	}
}

func TestDownDeletesOSVolumeAndKeepsData(t *testing.T) {
	prov := fake.New()
	prov.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}}
	app, _ := newApp(t, prov)
	ctx := context.Background()
	in, err := app.Provision(ctx, UpSpec{Name: "train", Types: []string{"1H100.80S.30V"}, Volume: &config.VolumeSpec{Name: "data"}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := app.Down(ctx, in, false); err != nil {
		t.Fatal(err)
	}
	if len(prov.Deleted) != 1 || prov.Deleted[0].ID != "fake-1" {
		t.Fatalf("Deleted = %v", prov.Deleted)
	}
	if v := prov.Deleted[0].VolumeIDs; len(v) != 1 || v[0] != "os-1" {
		t.Errorf("Down(keep) deleted volumes %v, want [os-1]", v)
	}

	in2, err := app.Provision(ctx, UpSpec{Name: "train", Types: []string{"1H100.80S.30V"}, Volume: &config.VolumeSpec{Name: "data"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Down(ctx, in2, true); err != nil {
		t.Fatal(err)
	}
	if v := prov.Deleted[1].VolumeIDs; len(v) != 2 || v[0] != "os-2" || v[1] != "vol-1" {
		t.Errorf("Down(--delete-volumes) deleted volumes %v, want [os-2 vol-1]", v)
	}
}

func TestResolveFreshPicksTheSoleRunningInstance(t *testing.T) {
	prov := fake.New()
	prov.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}}
	app, _ := newApp(t, prov)
	ctx := context.Background()

	if _, err := app.ResolveFresh(ctx, ""); err == nil || err.Error() != "no running rig instances (rig up to create one)" {
		t.Errorf("ResolveFresh(empty fleet) = %v", err)
	}
	if _, err := app.Provision(ctx, UpSpec{Name: "a", Types: []string{"1H100.80S.30V"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Provision(ctx, UpSpec{Name: "b", Types: []string{"1H100.80S.30V"}}); err != nil {
		t.Fatal(err)
	}
	if err := prov.ShutdownInstance(ctx, "fake-2"); err != nil {
		t.Fatal(err)
	}

	in, err := app.Resolve(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if in.Name != "rig-a" {
		t.Errorf("Resolve(\"\") = %s, want rig-a (the only running one)", in.Name)
	}
	if in, err := app.Resolve(ctx, "b"); err != nil || in.ID != "fake-2" {
		t.Errorf("Resolve(b) = %v, %v", in, err)
	}
}

// noCapacity is what a driver returns when the cloud has no hardware: a
// message for the user wrapping the sentinel.
func noCapacity(loc string) error {
	return fmt.Errorf("no GPUs left in %s: %w", loc, provider.ErrNoCapacity)
}

func contains(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// Target connects to the endpoint the provider reports, not cfg.ssh.user@IP:22:
// a container's mapped port and a non-root image user come through, config
// ssh.user overrides the provider's user only when set, and a driver that
// fills no endpoint still yields root@IP.
func TestTargetUsesProviderEndpoint(t *testing.T) {
	app, _ := newApp(t, fake.New())
	container := &provider.Instance{IP: "1.2.3.4", SSH: provider.Endpoint{Host: "1.2.3.4", Port: 10341, User: "root"}}
	proxied := &provider.Instance{IP: "1.2.3.4", SSH: provider.Endpoint{Host: "ssh5.vast.ai", Port: 12345, User: "root"}}
	dlami := &provider.Instance{IP: "3.4.5.6", SSH: provider.Endpoint{Host: "3.4.5.6", Port: 22, User: "ubuntu"}}
	bare := &provider.Instance{IP: "10.0.0.9"}

	if got := app.Target(container); got.Host != "1.2.3.4" || got.Port != 10341 || got.User != "root" {
		t.Errorf("container target = %s@%s:%d, want root@1.2.3.4:10341", got.User, got.Host, got.Port)
	}
	if got := app.Target(proxied); got.Host != "ssh5.vast.ai" || got.Port != 12345 {
		t.Errorf("proxied target = %s:%d, want ssh5.vast.ai:12345", got.Host, got.Port)
	}
	if got := app.Target(dlami); got.User != "ubuntu" {
		t.Errorf("provider user = %q, want ubuntu when ssh.user is unset", got.User)
	}
	if got := app.Target(bare); got.User != "root" || got.Host != "10.0.0.9" || got.Port != 0 {
		t.Errorf("bare target = %s@%s:%d, want root@10.0.0.9 on the default port", got.User, got.Host, got.Port)
	}

	app.Cfg.SSH.User = "ops"
	if got := app.Target(dlami); got.User != "ops" {
		t.Errorf("explicit ssh.user = %q, want ops to override the provider's ubuntu", got.User)
	}
}

// A fresh resolve sees the endpoint as it is now; the cache entry must follow
// (a RunPod restart remaps the port), so the next cached resolve — rig ssh,
// rig run — connects to the new port instead of failing once.
func TestResolveFreshRefreshesCachedEndpoint(t *testing.T) {
	prov := fake.New()
	prov.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}}
	app, _ := newApp(t, prov)
	ctx := context.Background()
	in, err := app.Provision(ctx, UpSpec{Name: "train", Types: []string{"1H100.80S.30V"}})
	if err != nil {
		t.Fatal(err)
	}
	app.Remember(in)
	if cached, err := app.Resolve(ctx, "train"); err != nil || cached.SSH.Port != 22 {
		t.Fatalf("Resolve(train) after up = %+v, %v; want the cached port-22 endpoint", cached, err)
	}

	prov.SSH["fake-1"] = provider.Endpoint{Host: "10.0.0.1", Port: 10341, User: "root"}
	fresh, err := app.ResolveFresh(ctx, "train")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.SSH.Port != 10341 {
		t.Fatalf("ResolveFresh(train).SSH = %+v, want the remapped port 10341", fresh.SSH)
	}
	entries, err := instcache.Read(instcache.Path(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].SSH != (provider.Endpoint{Host: "10.0.0.1", Port: 10341, User: "root"}) {
		t.Errorf("cache after ResolveFresh = %+v, want one entry on port 10341", entries)
	}
	if cached, err := app.Resolve(ctx, "train"); err != nil || cached.SSH.Port != 10341 {
		t.Errorf("cached Resolve(train) = %+v, %v; want the refreshed endpoint", cached, err)
	}
}

// Every instance core hands out is tagged with the provider it came from —
// the driver does not know rig's name for itself — and the tag rides into
// the cache entry and the ledger events.
func TestManagedStampsProvider(t *testing.T) {
	prov := fake.Named("runpod")
	prov.Avail = map[string][]string{"US-TX-3": {"1xA100-SXM"}}
	app, _ := newApp(t, prov)
	ctx := context.Background()

	in, err := app.Provision(ctx, UpSpec{Name: "pod", Types: []string{"1xA100-SXM"}})
	if err != nil {
		t.Fatal(err)
	}
	if in.Provider != "runpod" {
		t.Errorf("Provision().Provider = %q, want runpod", in.Provider)
	}
	listed, err := app.Managed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Provider != "runpod" {
		t.Errorf("Managed = %+v, want one instance tagged runpod", listed)
	}
	app.Remember(in)
	cached, err := instcache.Read(instcache.Path(), time.Now())
	if err != nil || len(cached) != 1 || cached[0].Provider != "runpod" {
		t.Errorf("cache = %+v, %v; want one runpod entry", cached, err)
	}
	ev, err := app.Down(ctx, in, false)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Provider != "runpod" {
		t.Errorf("down event Provider = %q, want runpod", ev.Provider)
	}
}

// Two clouds may both hold a rig-train. A cached entry belongs to the
// provider that wrote it: the other provider's Resolve must not answer from
// it, and the other provider's fresh listing must not drop it.
func TestResolveMatchesOnlyTheActiveProvidersCache(t *testing.T) {
	dir := t.TempDir()
	podProv := fake.Named("runpod")
	podProv.Avail = map[string][]string{"US-TX-3": {"1xA100-SXM"}}
	pods, _ := newAppIn(t, dir, podProv)
	vmProv := fake.Named("verda")
	vmProv.Avail = map[string][]string{"FIN-01": {"1H100.80S.30V"}}
	vms, _ := newAppIn(t, dir, vmProv)
	ctx := context.Background()

	pod, err := pods.Provision(ctx, UpSpec{Name: "train", Types: []string{"1xA100-SXM"}})
	if err != nil {
		t.Fatal(err)
	}
	pods.Remember(pod)

	// verda has no rig-train: the runpod entry must not stand in for one
	if in, err := vms.Resolve(ctx, "train"); err == nil {
		t.Fatalf("verda Resolve(train) = %+v, want a miss (the cached entry is runpod's)", in)
	} else if err.Error() != `no rig instance matching "train"` {
		t.Errorf("verda Resolve(train) err = %v", err)
	}
	// ... and that fresh (empty) verda listing must have left it alone
	if cached, _ := instcache.Read(instcache.Path(), time.Now()); len(cached) != 1 || cached[0].Provider != "runpod" {
		t.Fatalf("cache after verda's empty listing = %+v, want the runpod entry untouched", cached)
	}

	vm, err := vms.Provision(ctx, UpSpec{Name: "train", Types: []string{"1H100.80S.30V"}})
	if err != nil {
		t.Fatal(err)
	}
	vms.Remember(vm)
	if cached, _ := instcache.Read(instcache.Path(), time.Now()); len(cached) != 2 {
		t.Fatalf("cache = %+v, want one rig-train per provider", cached)
	}
	got, err := vms.Resolve(ctx, "train")
	if err != nil || got.Provider != "verda" || got.ID != vm.ID {
		t.Errorf("verda Resolve(train) = %+v, %v; want verda's %s", got, err, vm.ID)
	}
	got, err = pods.Resolve(ctx, "train")
	if err != nil || got.Provider != "runpod" || got.ID != pod.ID {
		t.Errorf("runpod Resolve(train) = %+v, %v; want runpod's %s", got, err, pod.ID)
	}
}
