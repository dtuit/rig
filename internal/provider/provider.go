// Package provider defines the cloud-agnostic interface rig drives.
// Each cloud (Verda today, others later) implements Provider.
package provider

import (
	"context"
	"time"
)

type Instance struct {
	ID           string
	Name         string // hostname
	Type         string
	Status       string
	IP           string
	Location     string
	Spot         bool
	PricePerHour float64
	CreatedAt    time.Time
	VolumeIDs    []string
	OSVolumeID   string
	GPU          string
	Image        string
}

type InstanceType struct {
	Type         string
	GPU          string
	GPUCount     int
	GPUMemoryGB  int
	MemoryGB     int
	CPUCount     int
	PricePerHour float64
	SpotPrice    float64
	Description  string
}

type Volume struct {
	ID           string
	Name         string
	SizeGB       int
	Type         string
	Status       string
	Location     string
	InstanceIDs  []string
	MountCommand string
	MkdirCommand string
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
