// Package provider defines the cloud-agnostic interface rig drives.
// Each cloud (Verda today, others later) implements Provider.
package provider

import (
	"context"
	"time"
)

type Instance struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"` // hostname
	Type         string    `json:"type"`
	Status       string    `json:"status"`
	IP           string    `json:"ip"`
	Location     string    `json:"location"`
	Spot         bool      `json:"spot"`
	PricePerHour float64   `json:"price_per_hour"`
	CreatedAt    time.Time `json:"created_at"`
	VolumeIDs    []string  `json:"volume_ids"`
	OSVolumeID   string    `json:"os_volume_id"`
	GPU          string    `json:"gpu"`
	Image        string    `json:"image"`
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
