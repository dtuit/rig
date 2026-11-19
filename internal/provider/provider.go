// Package provider defines the cloud-agnostic interface rig drives.
// Each cloud (Verda today, others later) implements Provider.
package provider

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Status is an instance lifecycle state normalised across providers. Drivers
// map their own vocabulary onto it; core, tui and cli compare only these
// constants. The values are the lowercase words `--json` emits (ADR-0002).
type Status string

const (
	Provisioning Status = "provisioning" // ordered or booting, no IP yet
	Running      Status = "running"
	Stopped      Status = "stopped"     // shut down, still billed for storage
	Error        Status = "error"       // the provider gave up on it
	Gone         Status = "gone"        // deleted or being deleted
	NoCapacity   Status = "no_capacity" // ordered but the provider found no hardware
)

// ErrNoCapacity is wrapped by CreateInstance when the provider has no
// hardware for the requested type in the requested location right now; core
// moves on to the next candidate. Every other create error aborts the launch.
var ErrNoCapacity = errors.New("no capacity")

// ErrUnsupported is wrapped by drivers for a feature the cloud lacks (spot,
// volumes, balance). Core refuses such requests up front via Capabilities and
// callers hide what they cannot show.
var ErrUnsupported = errors.New("unsupported by provider")

// Capabilities reports which optional features a driver implements, so core
// can refuse a request it knows the cloud cannot honour before spending a
// call on it, and how the footprint stops an instance of this cloud.
type Capabilities struct {
	Spot    bool // spot-priced instances
	Volumes bool // persistent data volumes
	Balance bool // account balance query
	// DockerImages means an image is any Docker reference the cloud can
	// pull, so Images lists only the default and configured ones and
	// `rig images` says so rather than pretending the list is exhaustive.
	DockerImages bool
	// SelfStop is the shell line the on-instance TTL watchdog runs at the
	// deadline to stop the instance. Empty means the VM default,
	// `shutdown -h now`; a container driver supplies the call that stops
	// its own container using only the instance-scoped token the provider
	// injects (ADR-0003). Trusted driver text, not user input.
	SelfStop string
	// Static means the instances are machines rig is lent, not rents: listed
	// in config, never created, deleted or self-stopped. Core refuses up,
	// down and extend for them, installs no TTL watchdog, and lets the
	// host's configured login user win over config ssh.user.
	Static bool
}

// Endpoint is where an instance's sshd answers. VMs report their IP on port
// 22; a container reports its host-mapped port or a proxy host, and a
// provider whose images log in as a non-root user says so in User. Config
// ssh.user, when set, overrides User.
type Endpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
}

// String renders the endpoint for a human: the host alone on the default ssh
// port (0 or 22, as every VM is), host:port for a container's mapped sshd.
// Every display of an endpoint — `rig status`, `rig tunnel status`, the
// dashboard — goes through this, so the three read the same.
func (e Endpoint) String() string {
	if e.Port == 0 || e.Port == 22 {
		return e.Host
	}
	return fmt.Sprintf("%s:%d", e.Host, e.Port)
}

// Instance is a provider VM as rig sees it. Status is one of the Status
// constants (an unknown provider state is passed through verbatim);
// RawStatus keeps the provider's own status text for display and debugging.
// IP is for display; SSH is what commands connect to. Provider is the name
// of the cloud the instance lives on, stamped by core (not by drivers) so
// local state — the instance cache, the ledger — can tell two clouds' rig-
// instances apart; empty in a record written before the field existed means
// verda.
type Instance struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"` // hostname
	Provider     string    `json:"provider,omitempty"`
	Type         string    `json:"type"`
	Status       Status    `json:"status"`
	RawStatus    string    `json:"raw_status,omitempty"`
	IP           string    `json:"ip"`
	SSH          Endpoint  `json:"ssh"`
	Location     string    `json:"location"`
	Spot         bool      `json:"spot"`
	PricePerHour float64   `json:"price_per_hour"`
	CreatedAt    time.Time `json:"created_at"`
	VolumeIDs    []string  `json:"volume_ids"`
	OSVolumeID   string    `json:"os_volume_id"`
	GPU          string    `json:"gpu"`
	Image        string    `json:"image"`
}

// Endpoint is where rig dials this instance: the ssh endpoint the driver
// reported, with the display IP standing in as the host for a driver that
// fills none.
func (in Instance) Endpoint() Endpoint {
	e := in.SSH
	if e.Host == "" {
		e.Host = in.IP
	}
	return e
}

type InstanceType struct {
	Type         string  `json:"type"`
	GPU          string  `json:"gpu"`
	GPUCount     int     `json:"gpu_count"`
	GPUMemoryGB  int     `json:"gpu_memory_gb"`
	MemoryGB     int     `json:"memory_gb"`
	CPUCount     int     `json:"cpu_count"`
	PricePerHour float64 `json:"price_per_hour"`
	SpotPrice    float64 `json:"spot_price"`
	Description  string  `json:"description"`
}

// MemoryText renders system RAM for a human column: a catalogue that does
// not quote it (RunPod's) reports 0, which reads as a dash, not 0GB.
func (t InstanceType) MemoryText() string {
	if t.MemoryGB == 0 {
		return "-"
	}
	return fmt.Sprintf("%dGB", t.MemoryGB)
}

// PriceText renders an hourly price for a human column. A price of zero is
// a price the cloud did not quote, not a free instance: AWS lists
// p5e.48xlarge with spot history but no on-demand price at all, because it
// sells that capacity only through Capacity Blocks, and "0.00" in the $/H
// column reads as free. Rendering it as a dash beside a real SPOT$/H says
// what is true — this shape is not for rent by the hour on demand.
func PriceText(usdPerHour float64) string {
	if usdPerHour == 0 {
		return "-"
	}
	return fmt.Sprintf("%.2f", usdPerHour)
}

// PriceText is the type's on-demand price as a human column. See PriceText.
func (t InstanceType) PriceText() string { return PriceText(t.PricePerHour) }

type Volume struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	SizeGB       int      `json:"size_gb"`
	Type         string   `json:"type"`
	Status       string   `json:"status"`
	Location     string   `json:"location"`
	InstanceIDs  []string `json:"instance_ids"`
	MountCommand string   `json:"mount_command"`
	MkdirCommand string   `json:"mkdir_command"`
}

func (v Volume) IsAttached() bool { return len(v.InstanceIDs) > 0 }

type Image struct {
	ID      string
	Name    string // value accepted by CreateInstance
	Display string
	Default bool
}

type Location struct {
	Code string
	Name string
}

type CreateInstanceRequest struct {
	Type            string
	Image           string
	Name            string // hostname
	Description     string
	Location        string
	Spot            bool
	SSHKeyIDs       []string
	PublicKey       string // rig's ssh public key text; container providers inject it at create (EnsureSSHKey returns "")
	ExistingVolumes []string
	OSVolumeGB      int // 0 = provider default
}

type CreateVolumeRequest struct {
	Name     string
	SizeGB   int
	Type     string
	Location string
}

type Provider interface {
	Name() string
	Capabilities() Capabilities

	Locations(ctx context.Context) ([]Location, error)
	InstanceTypes(ctx context.Context) ([]InstanceType, error)
	// Availability returns location code -> instance types deployable there now.
	Availability(ctx context.Context, spot bool) (map[string][]string, error)
	Images(ctx context.Context, instanceType string) ([]Image, error)

	ListInstances(ctx context.Context) ([]Instance, error)
	GetInstance(ctx context.Context, id string) (*Instance, error)
	CreateInstance(ctx context.Context, req CreateInstanceRequest) (*Instance, error)
	// DeleteInstance removes the instance; volumes listed in deleteVolumeIDs
	// are destroyed with it, any others are detached and kept.
	DeleteInstance(ctx context.Context, id string, deleteVolumeIDs []string) error
	ShutdownInstance(ctx context.Context, id string) error
	StartInstance(ctx context.Context, id string) error

	ListVolumes(ctx context.Context) ([]Volume, error)
	GetVolume(ctx context.Context, id string) (*Volume, error)
	CreateVolume(ctx context.Context, req CreateVolumeRequest) (string, error)
	AttachVolume(ctx context.Context, volumeID, instanceID string) error
	DetachVolume(ctx context.Context, volumeID, instanceID string) error
	DeleteVolume(ctx context.Context, volumeID string) error

	// EnsureSSHKey registers pubkey under name if not already present and
	// returns the provider-side key ID.
	EnsureSSHKey(ctx context.Context, name, pubkey string) (string, error)

	Balance(ctx context.Context) (float64, string, error)
}
