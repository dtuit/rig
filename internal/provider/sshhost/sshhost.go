// Package sshhost implements provider.Provider over machines rig is lent
// rather than rents: hosts listed by hand under providers.ssh.hosts, reached
// with plain ssh and a key. rig never creates, stops or deletes one, and
// never arms the TTL watchdog on it — the machine is shared and not rig's
// to shut down. Everything that only needs ssh (ssh, run, logs, ps, wait,
// sync, fetch, tunnel, adopt) works as on a cloud instance.
package sshhost

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/provider"
)

// Name is the provider name the driver registers under.
const Name = "ssh"

// Host is one entry of providers.ssh.hosts.
type Host struct {
	Host string `yaml:"host"` // DNS name or IP
	Port int    `yaml:"port"` // 0 = 22
	User string `yaml:"user"` // empty = providers.ssh.user
	GPU  string `yaml:"gpu"`  // display only, e.g. "1x RTX 4090"
}

// Config is the providers.ssh block.
type Config struct {
	User  string          `yaml:"user"` // login user for hosts that name none
	Hosts map[string]Host `yaml:"hosts"`
}

var keyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func init() {
	provider.Register(Name, func(cfg config.ProviderConfig) (provider.Provider, error) {
		var c Config
		if err := cfg.Decode(&c); err != nil {
			return nil, err
		}
		return New(c)
	})
}

// Driver serves the configured hosts. Dial is the reachability probe,
// replaceable in tests.
type Driver struct {
	cfg  Config
	Dial func(ctx context.Context, addr string) error
}

// New validates c: every host needs an address and a user, and a key that
// makes a usable instance name.
func New(c Config) (*Driver, error) {
	for key, h := range c.Hosts {
		if !keyRe.MatchString(key) {
			return nil, fmt.Errorf("providers.ssh.hosts: %q is not a usable name (lowercase letters, digits, dashes)", key)
		}
		if h.Host == "" {
			return nil, fmt.Errorf("providers.ssh.hosts.%s: host is required", key)
		}
		if h.User == "" && c.User == "" {
			return nil, fmt.Errorf("providers.ssh.hosts.%s: no user (set user on the host or providers.ssh.user)", key)
		}
	}
	return &Driver{cfg: c, Dial: tcpDial}, nil
}

func tcpDial(ctx context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

func (d *Driver) Name() string { return Name }

func (d *Driver) Capabilities() provider.Capabilities {
	return provider.Capabilities{Static: true}
}

// errStatic is the answer to every lifecycle and inventory call: these
// hosts are configured, not created.
func errStatic(what string) error {
	return fmt.Errorf("ssh hosts are listed under providers.ssh.hosts, not %s by rig: %w", what, provider.ErrUnsupported)
}

func (d *Driver) Locations(context.Context) ([]provider.Location, error) {
	return []provider.Location{{Code: "local", Name: "configured ssh hosts"}}, nil
}

func (d *Driver) InstanceTypes(context.Context) ([]provider.InstanceType, error) { return nil, nil }

func (d *Driver) Availability(context.Context, bool) (map[string][]string, error) {
	return nil, errStatic("created")
}

func (d *Driver) Images(context.Context, string) ([]provider.Image, error) { return nil, nil }

// ListInstances answers every configured host, probed in parallel: a host
// whose sshd port answers is Running, any other is Error with RawStatus
// "unreachable", so the dashboard does not wait out an ssh timeout on it.
func (d *Driver) ListInstances(ctx context.Context) ([]provider.Instance, error) {
	keys := make([]string, 0, len(d.cfg.Hosts))
	for k := range d.cfg.Hosts {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]provider.Instance, len(keys))
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = d.instance(ctx, k)
		}()
	}
	wg.Wait()
	return out, nil
}

func (d *Driver) instance(ctx context.Context, key string) provider.Instance {
	h := d.cfg.Hosts[key]
	port := h.Port
	if port == 0 {
		port = 22
	}
	user := h.User
	if user == "" {
		user = d.cfg.User
	}
	in := provider.Instance{
		ID:        key,
		Name:      "rig-" + key,
		Type:      "ssh",
		Status:    provider.Running,
		RawStatus: "reachable",
		IP:        h.Host,
		SSH:       provider.Endpoint{Host: h.Host, Port: port, User: user},
		Location:  "local",
		GPU:       h.GPU,
	}
	if err := d.Dial(ctx, net.JoinHostPort(h.Host, strconv.Itoa(port))); err != nil {
		in.Status, in.RawStatus = provider.Error, "unreachable"
	}
	return in
}

func (d *Driver) GetInstance(ctx context.Context, id string) (*provider.Instance, error) {
	if _, ok := d.cfg.Hosts[id]; !ok {
		return nil, fmt.Errorf("ssh host %q not found in providers.ssh.hosts", id)
	}
	in := d.instance(ctx, id)
	return &in, nil
}

func (d *Driver) CreateInstance(context.Context, provider.CreateInstanceRequest) (*provider.Instance, error) {
	return nil, errStatic("created")
}

func (d *Driver) DeleteInstance(context.Context, string, []string) error {
	return errStatic("deleted")
}

func (d *Driver) ShutdownInstance(context.Context, string) error { return errStatic("stopped") }
func (d *Driver) StartInstance(context.Context, string) error    { return errStatic("started") }

func (d *Driver) ListVolumes(context.Context) ([]provider.Volume, error) { return nil, nil }

func (d *Driver) GetVolume(context.Context, string) (*provider.Volume, error) {
	return nil, errStatic("given volumes")
}

func (d *Driver) CreateVolume(context.Context, provider.CreateVolumeRequest) (string, error) {
	return "", errStatic("given volumes")
}

func (d *Driver) AttachVolume(context.Context, string, string) error {
	return errStatic("given volumes")
}
func (d *Driver) DetachVolume(context.Context, string, string) error {
	return errStatic("given volumes")
}
func (d *Driver) DeleteVolume(context.Context, string) error { return errStatic("given volumes") }

func (d *Driver) EnsureSSHKey(context.Context, string, string) (string, error) {
	return "", errStatic("keyed")
}

func (d *Driver) Balance(context.Context) (float64, string, error) {
	return 0, "", errStatic("billed")
}
