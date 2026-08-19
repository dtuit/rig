// Package core ties config, provider, and remote together: instance
// selection, the up/down flows, and target resolution for ssh-based commands.
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dtuit/rig/internal/config"
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

func (a *App) logf(format string, args ...any) {
	if a.Log != nil {
		a.Log(format, args...)
	}
}

// Managed returns rig-managed instances (hostname prefix), newest first.
func (a *App) Managed(ctx context.Context) ([]provider.Instance, error) {
	all, err := a.Prov.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	var out []provider.Instance
	for _, in := range all {
		if strings.HasPrefix(in.Name, HostPrefix) {
			out = append(out, in)
		}
	}
	slices.SortFunc(out, func(x, y provider.Instance) int {
		return y.CreatedAt.Compare(x.CreatedAt)
	})
	return out, nil
}

// Resolve picks an instance by name/ID prefix; with empty key it returns the
// sole running rig instance, erroring if that is ambiguous.
func (a *App) Resolve(ctx context.Context, key string) (*provider.Instance, error) {
	instances, err := a.Managed(ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		var running []provider.Instance
		for _, in := range instances {
			if in.Status == "running" {
				running = append(running, in)
			}
		}
		switch len(running) {
		case 1:
			return &running[0], nil
		case 0:
			return nil, fmt.Errorf("no running rig instances (rig up to create one)")
		default:
			var names []string
			for _, in := range running {
				names = append(names, strings.TrimPrefix(in.Name, HostPrefix))
			}
			return nil, fmt.Errorf("multiple running instances (%s); pick one with -i", strings.Join(names, ", "))
		}
	}
	for _, in := range instances {
		if in.Name == key || in.Name == HostPrefix+key || strings.HasPrefix(in.ID, key) {
			return &in, nil
		}
	}
	return nil, fmt.Errorf("no rig instance matching %q", key)
}

func (a *App) Target(in *provider.Instance) sshx.Target {
	t := sshx.Target{
		User:         a.Cfg.SSH.User,
		Host:         in.IP,
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

// Pick chooses (type, location) from the preference list against live
// availability. Locations already holding the profile's volume win ties.
func (a *App) Pick(ctx context.Context, spec UpSpec) (typ, loc string, err error) {
	avail, err := a.Prov.Availability(ctx, spec.Spot)
	if err != nil {
		return "", "", fmt.Errorf("fetching availability: %w", err)
	}
	volLocs := map[string]bool{}
	if spec.Volume != nil {
		vols, err := a.Prov.ListVolumes(ctx)
		if err != nil {
			return "", "", err
		}
		for _, v := range vols {
			if v.Name == spec.Volume.Name {
				volLocs[v.Location] = true
			}
		}
	}
	for _, t := range spec.Types {
		var candidates []string
		for l, types := range avail {
			if spec.Location != "" && l != spec.Location {
				continue
			}
			if slices.Contains(types, t) {
				candidates = append(candidates, l)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		slices.SortFunc(candidates, func(x, y string) int {
			if volLocs[x] != volLocs[y] {
				if volLocs[x] {
					return -1
				}
				return 1
			}
			return strings.Compare(x, y)
		})
		return t, candidates[0], nil
	}
	mode := "on-demand"
	if spec.Spot {
		mode = "spot"
	}
	return "", "", fmt.Errorf("none of %s available %s%s right now (rig types --available to see what is)",
		strings.Join(spec.Types, ", "), mode, pinSuffix(spec.Location))
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

// Up runs the whole launch flow and returns the running, initialized instance.
func (a *App) Up(ctx context.Context, spec UpSpec) (*provider.Instance, error) {
	hostname := HostPrefix + spec.Name
	existing, err := a.Managed(ctx)
	if err != nil {
		return nil, err
	}
	for _, in := range existing {
		if in.Name == hostname && in.Status != "discontinued" {
			return nil, fmt.Errorf("instance %s already exists (%s); pick another --name", hostname, in.Status)
		}
	}

	typ, loc, err := a.Pick(ctx, spec)
	if err != nil {
		return nil, err
	}
	a.logf("selected %s in %s (spot=%v)", typ, loc, spec.Spot)

	image := spec.Image
	if image == "" {
		imgs, err := a.Prov.Images(ctx, typ)
		if err != nil {
			return nil, fmt.Errorf("listing images for %s: %w", typ, err)
		}
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
			return nil, fmt.Errorf("no image available for %s; set image in config", typ)
		}
	}
	a.logf("image: %s", image)

	keyPath, pubkey, err := a.Cfg.PublicKey()
	if err != nil {
		return nil, err
	}
	user := os.Getenv("USER")
	keyID, err := a.Prov.EnsureSSHKey(ctx, fmt.Sprintf("rig-%s-%s", user, filepath.Base(keyPath)), pubkey)
	if err != nil {
		return nil, err
	}

	var volumes []string
	if spec.Volume != nil {
		volID, err := a.EnsureVolume(ctx, spec.Volume, loc)
		if err != nil {
			return nil, err
		}
		volumes = append(volumes, volID)
	}

	a.logf("creating instance %s ...", hostname)
	in, err := a.Prov.CreateInstance(ctx, provider.CreateInstanceRequest{
		Type:            typ,
		Image:           image,
		Name:            hostname,
		Description:     "managed by rig",
		Location:        loc,
		Spot:            spec.Spot,
		SSHKeyIDs:       []string{keyID},
		ExistingVolumes: volumes,
		OSVolumeGB:      spec.OSGB,
	})
	if err != nil {
		return nil, fmt.Errorf("create failed: %w", err)
	}

	in, err = a.waitRunning(ctx, in.ID, 20*time.Minute)
	if err != nil {
		return nil, err
	}
	a.logf("instance running at %s, waiting for ssh ...", in.IP)

	t := a.Target(in)
	if err := t.WaitForSSH(10 * time.Minute); err != nil {
		return in, err
	}

	a.logf("installing TTL watchdog (%s)", spec.TTL)
	if err := remote.Init(t, spec.TTL); err != nil {
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
	return in, nil
}

func (a *App) waitRunning(ctx context.Context, id string, timeout time.Duration) (*provider.Instance, error) {
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		in, err := a.Prov.GetInstance(ctx, id)
		if err == nil {
			if in.Status != last {
				a.logf("status: %s", in.Status)
				last = in.Status
			}
			switch in.Status {
			case "running":
				if in.IP != "" {
					return in, nil
				}
			case "error", "discontinued", "no_capacity":
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
func (a *App) Down(ctx context.Context, in *provider.Instance, deleteVolumes bool) (ledger.Event, error) {
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
	if err := a.Prov.DeleteInstance(ctx, in.ID, volIDs); err != nil {
		return ledger.Event{}, err
	}
	ev := ledger.DownEvent(in, time.Now())
	if err := ledger.Append(ledger.Path(), ev); err != nil {
		a.logf("warning: spend ledger: %v", err)
	}
	return ev, nil
}
