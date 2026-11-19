// Package core ties config, provider, and remote together: instance
// selection, the up/down flows, and target resolution for ssh-based commands.
package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/instcache"
	"github.com/dtuit/rig/internal/ledger"
	"github.com/dtuit/rig/internal/provider"
	"github.com/dtuit/rig/internal/remote"
	"github.com/dtuit/rig/internal/sshx"
)

const HostPrefix = "rig-"

type App struct {
	Cfg  *config.Config
	Prov provider.Provider
	Log  func(format string, a ...any)
}

// Provider is the name of the cloud this App drives.
func (a *App) Provider() string { return a.Prov.Name() }

func (a *App) logf(format string, args ...any) {
	if a.Log != nil {
		a.Log(format, args...)
	}
}

// Managed returns rig-managed instances (hostname prefix), newest first,
// each tagged with this App's provider, and reconciles the instance cache
// against the listing.
func (a *App) Managed(ctx context.Context) ([]provider.Instance, error) {
	out, err := a.managed(ctx)
	if err != nil {
		return nil, err
	}
	a.reconcile(out)
	return out, nil
}

// managed is Managed without the cache write, so a caller listing several
// providers at once can fan the provider calls out in parallel and reconcile
// serially afterwards: instcache saves the whole file, and two goroutines
// doing read-modify-write on it would lose one another's updates.
func (a *App) managed(ctx context.Context) ([]provider.Instance, error) {
	all, err := a.Prov.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	var out []provider.Instance
	for _, in := range all {
		if strings.HasPrefix(in.Name, HostPrefix) {
			a.stamp(&in)
			out = append(out, in)
		}
	}
	slices.SortFunc(out, func(x, y provider.Instance) int {
		return y.CreatedAt.Compare(x.CreatedAt)
	})
	return out, nil
}

// reconcile tells the cache what this provider's listing just said. A fresh
// listing is the one moment stale entries can be caught — this provider's
// entries; the listing says nothing about other clouds'.
func (a *App) reconcile(instances []provider.Instance) {
	if err := instcache.Reconcile(instcache.Path(), a.Provider(), instances, time.Now()); err != nil {
		a.logf("warning: instance cache: %v", err)
	}
}

// stamp tags in with the provider it came from. Drivers do not know rig's
// name for them, so core does this wherever an instance enters: listings,
// creates, and the instance a caller hands to Down.
func (a *App) stamp(in *provider.Instance) {
	in.Provider = a.Provider()
}

// reach is what a resolve requires of the instance it answers with. The
// ssh-based commands need one they can dial (reachable); the
// teardown-and-inspect commands have work to do on an instance that merely
// exists (existing) — a self-stopped container still bills its disk, so
// tearing it down must not need -i.
type reach int

const (
	// reachable admits a Running instance only: ssh, run, logs, sync, tunnel.
	reachable reach = iota
	// existing admits every instance the provider still has, Gone aside:
	// down, extend, adopt.
	existing
)

// Resolve picks an instance to connect to by name/ID prefix; with empty key
// it returns the sole running rig instance, erroring if that is ambiguous or
// if the one instance there is cannot be dialled. A named key is answered
// from the local instance cache when it can be (ADR-0006), so the ssh-heavy
// commands skip the provider round-trip; the cached copy carries the status
// and IP recorded at up/adopt time, not the live ones. Commands whose work
// does not need a reachable instance use ResolveFresh.
func (a *App) Resolve(ctx context.Context, key string) (*provider.Instance, error) {
	if key != "" {
		if in := a.cached(key); in != nil {
			return in, nil
		}
	}
	return a.resolve(ctx, key, reachable)
}

// cached answers key from the instance cache, or nil on a miss. Cache
// trouble is a warning, never an error: the provider path still works.
func (a *App) cached(key string) *provider.Instance {
	return match(a.cachedInstances(), key)
}

// cachedInstances is this provider's slice of the instance cache: another
// cloud's rig-train is not the one this App can reach. Cache trouble is a
// warning and an empty answer.
func (a *App) cachedInstances() []provider.Instance {
	instances, err := instcache.Read(instcache.Path(), time.Now())
	if err != nil {
		a.logf("warning: instance cache: %v", err)
		return nil
	}
	mine := instances[:0]
	for _, in := range instances {
		if in.Provider == a.Provider() {
			mine = append(mine, in)
		}
	}
	return mine
}

// match honours -i's contract: full hostname, short name, or ID prefix.
func match(instances []provider.Instance, key string) *provider.Instance {
	for i := range instances {
		in := &instances[i]
		if in.Name == key || in.Name == HostPrefix+key || strings.HasPrefix(in.ID, key) {
			return in
		}
	}
	return nil
}

// ResolveFresh is Resolve against the provider only, and it answers with a
// stopped instance as readily as a running one — for the commands whose
// answer must reflect the instance as it is now, not as it was cached, and
// whose work does not need to reach it (down, extend, adopt). A single
// self-stopped instance is exactly what a container provider's TTL watchdog
// leaves behind, and it goes on billing its disk, so a bare rig down must
// select it rather than send the user hunting for -i.
func (a *App) ResolveFresh(ctx context.Context, key string) (*provider.Instance, error) {
	return a.resolve(ctx, key, existing)
}

// resolve is the provider half of both resolvers, admitting only what want
// asks for.
func (a *App) resolve(ctx context.Context, key string, want reach) (*provider.Instance, error) {
	instances, err := a.Managed(ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return sole(instances, want)
	}
	in := match(instances, key)
	if in == nil {
		return nil, fmt.Errorf("no rig instance matching %q", key)
	}
	if want == reachable && in.Status != provider.Running {
		return nil, notRunning(in)
	}
	a.refreshCached(in)
	return in, nil
}

// sole answers a keyless resolve: the one instance want admits, or an error
// that names the alternatives with their statuses so -i is the obvious next
// step. An instance the provider has already discarded (Gone) is nobody's
// candidate and nobody's ambiguity.
func sole(instances []provider.Instance, want reach) (*provider.Instance, error) {
	var live, candidates []provider.Instance
	for _, in := range instances {
		if in.Status == provider.Gone {
			continue
		}
		live = append(live, in)
		if want == existing || in.Status == provider.Running {
			candidates = append(candidates, in)
		}
	}
	switch len(candidates) {
	case 1:
		return &candidates[0], nil
	case 0:
		switch len(live) {
		case 0:
			return nil, fmt.Errorf("no running rig instances (rig up to create one)")
		case 1:
			return nil, notRunning(&live[0])
		default:
			return nil, fmt.Errorf("no running rig instances (%s); rig down deletes one, rig up creates another",
				strings.Join(withStatus(live), ", "))
		}
	default:
		if want == reachable {
			return nil, fmt.Errorf("multiple running instances (%s); pick one with -i", strings.Join(shortNames(candidates), ", "))
		}
		return nil, fmt.Errorf("multiple rig instances (%s); pick one with -i", strings.Join(withStatus(candidates), ", "))
	}
}

// notRunning refuses an instance an ssh-based command cannot dial, saying
// which state it is in instead of pretending there is nothing there: a
// stopped instance is still costing money, and deleting it is the fix.
func notRunning(in *provider.Instance) error {
	short := shortName(in.Name)
	if in.Status == provider.Stopped {
		return fmt.Errorf("instance %s is stopped, so there is nothing to connect to (rig down %s deletes it; a stopped instance still bills its disk)", short, short)
	}
	return fmt.Errorf("instance %s is %s, not running (rig status to watch it)", short, in.Status)
}

// shortName is the name the user typed: the rig- prefix is rig's, not theirs.
func shortName(name string) string { return strings.TrimPrefix(name, HostPrefix) }

func shortNames(instances []provider.Instance) []string {
	out := make([]string, 0, len(instances))
	for _, in := range instances {
		out = append(out, shortName(in.Name))
	}
	return out
}

// withStatus labels instances for an error the user has to choose from:
// "train running, smoke stopped".
func withStatus(instances []provider.Instance) []string {
	out := make([]string, 0, len(instances))
	for _, in := range instances {
		out = append(out, shortName(in.Name)+" "+string(in.Status))
	}
	return out
}

// refreshCached rewrites in's cache entry when the endpoint it recorded is
// no longer the one the provider reports — a container's host-mapped port
// moves on restart — so the next cached resolve connects rather than failing
// once. Managed's reconcile normally does this already; this covers the
// entry it could not. Best-effort, like every cache write.
func (a *App) refreshCached(in *provider.Instance) {
	if old := match(a.cachedInstances(), in.Name); old != nil && old.SSH != in.SSH {
		a.Remember(in)
	}
}

// Target is the ssh endpoint for in: the host and port the provider reports
// (falling back to the IP on the default port for a driver that fills none),
// as the provider's login user unless config ssh.user overrides it — and
// root when neither says. A static host's user was configured for that host
// by hand, so it wins over ssh.user, which is about the clouds' images.
func (a *App) Target(in *provider.Instance) sshx.Target {
	user := orDefault(a.Cfg.SSH.User, orDefault(in.SSH.User, "root"))
	if a.Prov.Capabilities().Static && in.SSH.User != "" {
		user = in.SSH.User
	}
	t := sshx.Target{
		User:         user,
		Host:         orDefault(in.SSH.Host, in.IP),
		Port:         in.SSH.Port,
		ForwardAgent: a.Cfg.ForwardAgent(),
		IdentityFile: a.Cfg.IdentityFile(),
	}
	if ag := a.Cfg.SSH.Agent; ag != nil && ag.Socket != "" {
		sock := config.ExpandHome(ag.Socket)
		keys := make([]string, 0, len(ag.Keys))
		for _, k := range ag.Keys {
			keys = append(keys, config.ExpandHome(k))
		}
		if err := sshx.EnsureAgent(sock, keys); err != nil {
			a.logf("warning: rig agent: %v (falling back to ambient SSH_AUTH_SOCK)", err)
		} else {
			t.AgentSocket = sock
		}
	}
	return t
}

// UpSpec is a fully resolved launch request (from a profile or the TUI).
type UpSpec struct {
	Name     string // instance name without the rig- prefix
	Types    []string
	Spot     bool
	Location string // optional pin
	Image    string // empty = provider default for the chosen type
	Volume   *config.VolumeSpec
	TTL      time.Duration
	EnvFiles []string
	OSGB     int
}

func SpecFromProfile(name string, p config.Profile) (UpSpec, error) {
	ttl, err := p.TTLDuration()
	if err != nil {
		return UpSpec{}, fmt.Errorf("profile %s: bad ttl: %w", name, err)
	}
	if len(p.Types) == 0 {
		return UpSpec{}, fmt.Errorf("profile %s has no types", name)
	}
	return UpSpec{
		Name:     name,
		Types:    p.Types,
		Spot:     p.Spot,
		Location: p.Location,
		Image:    p.Image,
		Volume:   p.Volume,
		TTL:      ttl,
		EnvFiles: p.EnvFiles,
		OSGB:     p.OSGB,
	}, nil
}

// Candidate is one (type, location) pair Up may try, in preference order.
type Candidate struct {
	Type     string
	Location string
}

// Pick ranks (type, location) pairs from the preference list against live
// availability: types in profile order, then within a type the locations
// already holding the profile's volume, then alphabetically. Up walks the
// list in that order, moving on only when the provider reports no capacity.
func (a *App) Pick(ctx context.Context, spec UpSpec) ([]Candidate, error) {
	avail, err := a.Prov.Availability(ctx, spec.Spot)
	if err != nil {
		return nil, fmt.Errorf("fetching availability: %w", err)
	}
	volLocs := map[string]bool{}
	if spec.Volume != nil {
		vols, err := a.Prov.ListVolumes(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range vols {
			if v.Name == spec.Volume.Name {
				volLocs[v.Location] = true
			}
		}
	}
	var out []Candidate
	for _, want := range spec.Types {
		// tokens are typed by hand (1xrtx-4090); the provider's spelling is
		// what the create request must carry
		t := want
		var locs []string
		for l, types := range avail {
			if spec.Location != "" && l != spec.Location {
				continue
			}
			if i := slices.IndexFunc(types, func(x string) bool { return strings.EqualFold(x, want) }); i >= 0 {
				t = types[i]
				locs = append(locs, l)
			}
		}
		slices.SortFunc(locs, func(x, y string) int {
			if volLocs[x] != volLocs[y] {
				if volLocs[x] {
					return -1
				}
				return 1
			}
			return strings.Compare(x, y)
		})
		for _, l := range locs {
			out = append(out, Candidate{Type: t, Location: l})
		}
	}
	if len(out) == 0 {
		return nil, noneAvailable(spec, nil)
	}
	return out, nil
}

// noneAvailable is the launch-failed answer when no candidate was offered or
// every one turned out to have no capacity. The drivers' own reasons ride
// along, because from here "the cloud is full" and "rig asked the wrong
// question" look identical: a location-filter bug made every Vast create
// answer ErrNoCapacity, and dropping the reason hid it for a release.
func noneAvailable(spec UpSpec, reasons []string) error {
	mode := "on-demand"
	if spec.Spot {
		mode = "spot"
	}
	return fmt.Errorf("none of %s available %s%s right now (rig types --available to see what is)%s",
		strings.Join(spec.Types, ", "), mode, pinSuffix(spec.Location), because(reasons))
}

// because renders the collected reasons, capped so a long candidate walk
// cannot turn one error into a wall of text.
func because(reasons []string) string {
	const most = 3
	if len(reasons) == 0 {
		return ""
	}
	if len(reasons) > most {
		reasons = append(reasons[:most:most], fmt.Sprintf("(%d more)", len(reasons)-most))
	}
	return "; the provider said: " + strings.Join(reasons, "; ")
}

// addReason collects a driver's no-capacity text for the final error,
// keeping each distinct reason once: a whole candidate list rejected for the
// same reason is one fact about the request, not ten about the stock.
func addReason(reasons []string, err error) []string {
	r := strings.TrimSpace(err.Error())
	if r == "" || slices.Contains(reasons, r) {
		return reasons
	}
	return append(reasons, r)
}

func pinSuffix(loc string) string {
	if loc == "" {
		return ""
	}
	return " in " + loc
}

// EnsureVolume finds or creates the named volume in loc and returns its ID.
func (a *App) EnsureVolume(ctx context.Context, spec *config.VolumeSpec, loc string) (string, error) {
	vols, err := a.Prov.ListVolumes(ctx)
	if err != nil {
		return "", err
	}
	for _, v := range vols {
		if v.Name == spec.Name && v.Location == loc {
			if len(v.InstanceIDs) > 0 {
				return "", fmt.Errorf("volume %s in %s is attached to another instance", spec.Name, loc)
			}
			a.logf("volume %s found in %s (%dGB)", spec.Name, loc, v.SizeGB)
			return v.ID, nil
		}
	}
	size := spec.SizeGB
	if size == 0 {
		size = 100
	}
	a.logf("creating volume %s in %s (%dGB %s)", spec.Name, loc, size, orDefault(spec.Type, "NVMe_Shared"))
	return a.Prov.CreateVolume(ctx, provider.CreateVolumeRequest{
		Name:     spec.Name,
		SizeGB:   size,
		Type:     spec.Type,
		Location: loc,
	})
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Provision is the provider half of Up: it refuses what the driver cannot
// do, walks the candidate list until an instance is running with an IP, and
// returns it untouched — no ssh, no footprint, no ledger or cache entry.
// It is exported as the seam the core tests drive (issue 02): everything Up
// does that a fake provider can answer, with nothing that needs an
// instance to ssh into. Production callers use Up; keep it exported.
func (a *App) Provision(ctx context.Context, spec UpSpec) (*provider.Instance, error) {
	in, _, err := a.provision(ctx, spec)
	return in, err
}

// provision also returns the data volume IDs attached at create so Up can
// mount them; the instance's own VolumeIDs are provider-shaped and may
// include the OS volume.
func (a *App) provision(ctx context.Context, spec UpSpec) (*provider.Instance, []string, error) {
	caps := a.Prov.Capabilities()
	if caps.Static {
		return nil, nil, ErrStatic(a.Provider(), "created")
	}
	if spec.Spot && !caps.Spot {
		return nil, nil, fmt.Errorf("%s does not offer spot instances: %w", a.Provider(), provider.ErrUnsupported)
	}
	if spec.Volume != nil && !caps.Volumes {
		return nil, nil, fmt.Errorf("%s does not offer data volumes: %w", a.Provider(), provider.ErrUnsupported)
	}

	// --name rig-x would otherwise become rig-rig-x (issue 15)
	if strings.HasPrefix(spec.Name, HostPrefix) {
		spec.Name = strings.TrimPrefix(spec.Name, HostPrefix)
		a.logf("--name already carries the %s prefix; using %s", HostPrefix, spec.Name)
	}
	hostname := HostPrefix + spec.Name
	existing, err := a.Managed(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, in := range existing {
		if in.Name == hostname && in.Status != provider.Gone {
			return nil, nil, fmt.Errorf("instance %s already exists (%s); pick another --name", hostname, in.Status)
		}
	}

	candidates, err := a.Pick(ctx, spec)
	if err != nil {
		return nil, nil, err
	}

	keyPath, pubkey, err := a.Cfg.PublicKey()
	if err != nil {
		return nil, nil, err
	}
	user := os.Getenv("USER")
	keyID, err := a.Prov.EnsureSSHKey(ctx, fmt.Sprintf("rig-%s-%s", user, filepath.Base(keyPath)), pubkey)
	if err != nil {
		return nil, nil, err
	}

	images := map[string]string{} // default image per type, looked up once
	var reasons []string          // the drivers' own no-capacity words, deduped
	for _, c := range candidates {
		a.logf("selected %s in %s (spot=%v)", c.Type, c.Location, spec.Spot)

		image, err := a.imageFor(ctx, spec, c.Type, images)
		if err != nil {
			return nil, nil, err
		}
		a.logf("image: %s", image)

		var volumes []string
		if spec.Volume != nil {
			volID, err := a.EnsureVolume(ctx, spec.Volume, c.Location)
			if err != nil {
				return nil, nil, err
			}
			volumes = append(volumes, volID)
		}

		a.logf("creating instance %s ...", hostname)
		in, err := a.Prov.CreateInstance(ctx, provider.CreateInstanceRequest{
			Type:            c.Type,
			Image:           image,
			Name:            hostname,
			Description:     "managed by rig",
			Location:        c.Location,
			Spot:            spec.Spot,
			SSHKeyIDs:       []string{keyID},
			PublicKey:       pubkey,
			ExistingVolumes: volumes,
			OSVolumeGB:      spec.OSGB,
		})
		if err != nil {
			if errors.Is(err, provider.ErrNoCapacity) {
				a.logf("no capacity for %s in %s, trying next (%v)", c.Type, c.Location, err)
				reasons = addReason(reasons, err)
				continue
			}
			return nil, nil, fmt.Errorf("create failed: %w", err)
		}

		in, err = a.waitRunning(ctx, in.ID, 20*time.Minute)
		if err != nil {
			if errors.Is(err, provider.ErrNoCapacity) {
				a.logf("no capacity for %s in %s, trying next (%v)", c.Type, c.Location, err)
				reasons = addReason(reasons, err)
				a.discard(ctx, in)
				continue
			}
			return nil, nil, err
		}
		a.stamp(in)
		return in, volumes, nil
	}
	return nil, nil, noneAvailable(spec, reasons)
}

// imageFor answers spec.Image, or the provider's default image for typ,
// remembering the lookup in cache across the candidate walk.
func (a *App) imageFor(ctx context.Context, spec UpSpec, typ string, cache map[string]string) (string, error) {
	if spec.Image != "" {
		return spec.Image, nil
	}
	if image, ok := cache[typ]; ok {
		return image, nil
	}
	imgs, err := a.Prov.Images(ctx, typ)
	if err != nil {
		return "", fmt.Errorf("listing images for %s: %w", typ, err)
	}
	image := ""
	for _, im := range imgs {
		if im.Default {
			image = im.Name
			break
		}
	}
	if image == "" && len(imgs) > 0 {
		image = imgs[0].Name
	}
	if image == "" {
		return "", fmt.Errorf("no image available for %s; set image in config", typ)
	}
	cache[typ] = image
	return image, nil
}

// discard removes an instance that never became usable, taking its OS
// volume with it as Down does. Best effort: the candidate walk goes on
// whatever the provider says.
func (a *App) discard(ctx context.Context, in *provider.Instance) {
	if err := a.Prov.DeleteInstance(ctx, in.ID, volumesToDelete(in, false)); err != nil {
		a.logf("warning: deleting %s (%s): %v", in.Name, in.ID, err)
	}
}

// Footprint is what remote.Init installs for this provider: the TTL and the
// driver's self-stop line, warnings routed to the App log. Exported for the
// caller that installs it itself (rig adopt); Up uses it too.
func (a *App) Footprint(ttl time.Duration) remote.Footprint {
	caps := a.Prov.Capabilities()
	return remote.Footprint{TTL: ttl, SelfStop: caps.SelfStop, NoWatchdog: caps.Static, Log: a.logf}
}

// Static reports whether this App's instances are machines rig is lent
// (provider.Capabilities.Static): no up, down, extend or watchdog.
func (a *App) Static() bool { return a.Prov.Capabilities().Static }

// ErrStatic is the refusal for a lifecycle verb on a static host.
func ErrStatic(prov, verb string) error {
	return fmt.Errorf("%s hosts are configured in providers.%s.hosts, not %s by rig (rig adopt sets one up): %w",
		prov, prov, verb, provider.ErrUnsupported)
}

// Up runs the whole launch flow and returns the running, initialized instance.
func (a *App) Up(ctx context.Context, spec UpSpec) (*provider.Instance, error) {
	in, volumes, err := a.provision(ctx, spec)
	if err != nil {
		return nil, err
	}
	a.logf("instance running at %s, waiting for ssh ...", in.IP)

	t := a.Target(in)
	if err := t.WaitForSSH(10 * time.Minute); err != nil {
		return in, err
	}

	a.logf("installing TTL watchdog (%s)", spec.TTL)
	if err := remote.Init(t, a.Footprint(spec.TTL)); err != nil {
		return in, fmt.Errorf("remote init: %w", err)
	}

	for _, f := range spec.EnvFiles {
		path := config.ExpandHome(f)
		b, err := os.ReadFile(path)
		if err != nil {
			return in, fmt.Errorf("env file %s: %w", f, err)
		}
		if err := remote.PushEnvFile(t, string(b), filepath.Base(path)); err != nil {
			return in, err
		}
		a.logf("pushed env file %s", filepath.Base(path))
	}

	if len(volumes) > 0 {
		if err := a.MountVolume(ctx, t, volumes[0]); err != nil {
			a.logf("warning: volume mount: %v", err)
		}
	}
	if err := ledger.Append(ledger.Path(), ledger.UpEvent(in, spec.TTL, time.Now())); err != nil {
		a.logf("warning: spend ledger: %v", err)
	}
	a.Remember(in)
	return in, nil
}

// Remember records in for the cached Resolve path (issue 13); up and adopt
// call it once the footprint is installed. Best-effort, like the ledger.
func (a *App) Remember(in *provider.Instance) {
	if err := instcache.Put(instcache.Path(), *in, time.Now()); err != nil {
		a.logf("warning: instance cache: %v", err)
	}
}

// Forget drops in from the instance cache once the provider no longer has it.
func (a *App) Forget(in *provider.Instance) {
	if err := instcache.Remove(instcache.Path(), in.Name); err != nil {
		a.logf("warning: instance cache: %v", err)
	}
}

// isNotFound spots a provider "already gone" answer. Provider errors reach
// core as opaque text (the SDK renders "API error 404: ..."), so this is a
// heuristic on that text, kept narrow to avoid matching an ID.
func isNotFound(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not found") || strings.Contains(s, "error 404")
}

func (a *App) waitRunning(ctx context.Context, id string, timeout time.Duration) (*provider.Instance, error) {
	deadline := time.Now().Add(timeout)
	var last provider.Status
	for time.Now().Before(deadline) {
		in, err := a.Prov.GetInstance(ctx, id)
		if err == nil {
			if in.Status != last {
				a.logf("status: %s", in.Status)
				last = in.Status
			}
			switch in.Status {
			case provider.Running:
				if in.IP != "" {
					return in, nil
				}
			case provider.NoCapacity:
				return in, fmt.Errorf("instance entered status %q: %w", in.Status, provider.ErrNoCapacity)
			case provider.Error, provider.Gone:
				return in, fmt.Errorf("instance entered status %q", in.Status)
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	return nil, fmt.Errorf("instance %s not running after %s", id, timeout)
}

// MountVolume runs the provider-supplied mkdir/mount commands best-effort.
func (a *App) MountVolume(ctx context.Context, t sshx.Target, volID string) error {
	v, err := a.Prov.GetVolume(ctx, volID)
	if err != nil {
		return err
	}
	for _, cmd := range []string{v.MkdirCommand, v.MountCommand} {
		if cmd == "" {
			continue
		}
		if out, err := t.Run(cmd); err != nil {
			if strings.Contains(out, "already mounted") {
				continue
			}
			return fmt.Errorf("%q: %v: %s", cmd, err, strings.TrimSpace(out))
		}
	}
	a.logf("volume %s mounted", v.Name)
	return nil
}

// Down deletes the instance and its OS volume (worthless without the
// instance, but billed if left behind). Data volumes are kept unless
// deleteVolumes. The returned event is the ledger record — lifetime and
// cost — so callers report the same numbers the ledger keeps.
// Snapshot asks one instance for its deadline and its jobs — the ssh half
// of what the dashboard shows, in a single round trip. Only the caller
// knows whether an instance is worth asking: a stopped one cannot answer,
// and waiting out its connect timeout is worse than showing nothing.
func (a *App) Snapshot(in *provider.Instance) (remote.Snapshot, error) {
	return remote.InstanceSnapshot(a.Target(in))
}

// JobLog is the tail of one run's log, as text.
func (a *App) JobLog(in *provider.Instance, j remote.Job, lines int) ([]string, error) {
	return remote.JobLogTail(a.Target(in), j, lines)
}

// WatchdogLog is what the TTL watchdog has written down on the instance.
func (a *App) WatchdogLog(in *provider.Instance, lines int) ([]string, error) {
	return remote.WatchdogLog(a.Target(in), lines)
}

// History is one instance's ledger events, oldest first. Local only — no
// provider call and no ssh — so it is the one part of an instance's detail
// that is there even when the instance is not.
func (a *App) History(in *provider.Instance) []ledger.Event {
	events, err := ledger.Read(ledger.Path())
	if err != nil {
		a.logf("warning: spend ledger: %v", err)
		return nil
	}
	return ledger.EventsFor(events, in.Name)
}

func (a *App) Down(ctx context.Context, in *provider.Instance, deleteVolumes bool) (ledger.Event, error) {
	a.stamp(in) // the ledger event must say which cloud billed this
	if a.Static() {
		return ledger.Event{}, ErrStatic(a.Provider(), "deleted")
	}
	if err := a.Prov.DeleteInstance(ctx, in.ID, volumesToDelete(in, deleteVolumes)); err != nil {
		if isNotFound(err) {
			a.Forget(in) // the provider lost it first; the cache must not keep resolving to it
		}
		return ledger.Event{}, err
	}
	a.Forget(in)
	ev := ledger.DownEvent(in, time.Now())
	if err := ledger.Append(ledger.Path(), ev); err != nil {
		a.logf("warning: spend ledger: %v", err)
	}
	return ev, nil
}

// volumesToDelete lists what goes down with in: always its OS volume, plus
// its data volumes when deleteVolumes.
func volumesToDelete(in *provider.Instance, deleteVolumes bool) []string {
	volIDs := []string{}
	if in.OSVolumeID != "" {
		volIDs = append(volIDs, in.OSVolumeID)
	}
	if deleteVolumes {
		for _, id := range in.VolumeIDs {
			if id != in.OSVolumeID {
				volIDs = append(volIDs, id)
			}
		}
	}
	return volIDs
}
