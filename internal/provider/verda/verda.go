// Package verda implements provider.Provider against the Verda
// (formerly DataCrunch) public API via the official Go SDK.
package verda

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/verda-cloud/verdacloud-sdk-go/pkg/verda"

	"github.com/dtuit/rig/internal/provider"
)

type Driver struct {
	client *sdk.Client
}

func New(clientID, clientSecret string) (*Driver, error) {
	c, err := sdk.NewClient(
		sdk.WithClientID(clientID),
		sdk.WithClientSecret(clientSecret),
	)
	if err != nil {
		return nil, err
	}
	return &Driver{client: c}, nil
}

func (d *Driver) Name() string { return "verda" }

func (d *Driver) Locations(ctx context.Context) ([]provider.Location, error) {
	locs, err := d.client.Locations.Get(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]provider.Location, 0, len(locs))
	for _, l := range locs {
		out = append(out, provider.Location{Code: l.Code, Name: l.Name})
	}
	return out, nil
}

func (d *Driver) InstanceTypes(ctx context.Context) ([]provider.InstanceType, error) {
	types, err := d.client.InstanceTypes.Get(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]provider.InstanceType, 0, len(types))
	for _, t := range types {
		out = append(out, provider.InstanceType{
			Type:         t.InstanceType,
			GPU:          t.GPU.Description,
			GPUCount:     t.GPU.NumberOfGPUs,
			GPUMemoryGB:  t.GPUMemory.SizeInGigabytes,
			MemoryGB:     t.Memory.SizeInGigabytes,
			CPUCount:     t.CPU.NumberOfCores,
			PricePerHour: float64(t.PricePerHour),
			SpotPrice:    float64(t.SpotPrice),
			Description:  t.Description,
		})
	}
	return out, nil
}

func (d *Driver) Availability(ctx context.Context, spot bool) (map[string][]string, error) {
	avail, err := d.client.InstanceAvailability.GetAllAvailabilities(ctx, spot, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(avail))
	for _, a := range avail {
		out[a.LocationCode] = a.Availabilities
	}
	return out, nil
}

func (d *Driver) Images(ctx context.Context, instanceType string) ([]provider.Image, error) {
	var (
		imgs []sdk.Image
		err  error
	)
	if instanceType != "" {
		imgs, err = d.client.Images.GetImagesByInstanceType(ctx, instanceType)
	} else {
		imgs, err = d.client.Images.Get(ctx)
	}
	if err != nil {
		return nil, err
	}
	out := make([]provider.Image, 0, len(imgs))
	for _, im := range imgs {
		out = append(out, provider.Image{
			ID:      im.ID,
			Name:    im.ImageType,
			Display: im.Name,
			Default: im.IsDefault,
		})
	}
	return out, nil
}

func (d *Driver) ListInstances(ctx context.Context) ([]provider.Instance, error) {
	instances, err := d.client.Instances.Get(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]provider.Instance, 0, len(instances))
	for i := range instances {
		out = append(out, fromSDKInstance(&instances[i]))
	}
	return out, nil
}

func (d *Driver) GetInstance(ctx context.Context, id string) (*provider.Instance, error) {
	in, err := d.client.Instances.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	v := fromSDKInstance(in)
	return &v, nil
}

func (d *Driver) CreateInstance(ctx context.Context, req provider.CreateInstanceRequest) (*provider.Instance, error) {
	r := sdk.CreateInstanceRequest{
		InstanceType:    req.Type,
		Image:           req.Image,
		Hostname:        req.Name,
		Description:     req.Description,
		SSHKeyIDs:       req.SSHKeyIDs,
		LocationCode:    req.Location,
		IsSpot:          req.Spot,
		ExistingVolumes: req.ExistingVolumes,
	}
	if req.OSVolumeGB > 0 {
		r.OSVolume = &sdk.OSVolumeCreateRequest{
			Name: req.Name + "-os",
			Size: req.OSVolumeGB,
		}
	}
	in, err := d.client.Instances.Create(ctx, r)
	if err != nil {
		return nil, err
	}
	v := fromSDKInstance(in)
	return &v, nil
}

func (d *Driver) DeleteInstance(ctx context.Context, id string, deleteVolumeIDs []string) error {
	return d.client.Instances.Delete(ctx, []string{id}, deleteVolumeIDs, false)
}

func (d *Driver) ShutdownInstance(ctx context.Context, id string) error {
	return d.client.Instances.Shutdown(ctx, id)
}

func (d *Driver) StartInstance(ctx context.Context, id string) error {
	return d.client.Instances.Start(ctx, id)
}

func (d *Driver) ListVolumes(ctx context.Context) ([]provider.Volume, error) {
	vols, err := d.client.Volumes.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]provider.Volume, 0, len(vols))
	for i := range vols {
		out = append(out, fromSDKVolume(&vols[i]))
	}
	return out, nil
}

func (d *Driver) GetVolume(ctx context.Context, id string) (*provider.Volume, error) {
	v, err := d.client.Volumes.GetVolume(ctx, id)
	if err != nil {
		return nil, err
	}
	pv := fromSDKVolume(v)
	return &pv, nil
}

func (d *Driver) CreateVolume(ctx context.Context, req provider.CreateVolumeRequest) (string, error) {
	typ := req.Type
	if typ == "" {
		typ = sdk.VolumeTypeNVMeShared
	}
	return d.client.Volumes.CreateVolume(ctx, sdk.VolumeCreateRequest{
		Name:         req.Name,
		Size:         req.SizeGB,
		Type:         typ,
		LocationCode: req.Location,
	})
}

func (d *Driver) AttachVolume(ctx context.Context, volumeID, instanceID string) error {
	return d.client.Volumes.AttachVolume(ctx, volumeID, sdk.VolumeAttachRequest{InstanceID: instanceID})
}

func (d *Driver) DetachVolume(ctx context.Context, volumeID, instanceID string) error {
	return d.client.Volumes.DetachVolume(ctx, volumeID, sdk.VolumeDetachRequest{InstanceID: instanceID})
}

func (d *Driver) DeleteVolume(ctx context.Context, volumeID string) error {
	return d.client.Volumes.DeleteVolume(ctx, volumeID, false)
}

func (d *Driver) EnsureSSHKey(ctx context.Context, name, pubkey string) (string, error) {
	pubkey = strings.TrimSpace(pubkey)
	keys, err := d.client.SSHKeys.GetAllSSHKeys(ctx)
	if err != nil {
		return "", err
	}
	for _, k := range keys {
		if strings.TrimSpace(k.PublicKey) == pubkey {
			return k.ID, nil
		}
	}
	k, err := d.client.SSHKeys.AddSSHKey(ctx, &sdk.CreateSSHKeyRequest{Name: name, PublicKey: pubkey})
	if err != nil {
		return "", fmt.Errorf("registering ssh key %q: %w", name, err)
	}
	return k.ID, nil
}

func (d *Driver) Balance(ctx context.Context) (float64, string, error) {
	b, err := d.client.Balance.Get(ctx)
	if err != nil {
		return 0, "", err
	}
	return b.Amount, b.Currency, nil
}

func fromSDKInstance(in *sdk.Instance) provider.Instance {
	ip := ""
	if in.IP != nil {
		ip = *in.IP
	}
	osVol := ""
	if in.OSVolumeID != nil {
		osVol = *in.OSVolumeID
	}
	return provider.Instance{
		ID:           in.ID,
		Name:         in.Hostname,
		Type:         in.InstanceType,
		Status:       in.Status,
		IP:           ip,
		Location:     in.Location,
		Spot:         in.IsSpot,
		PricePerHour: float64(in.PricePerHour),
		CreatedAt:    in.CreatedAt,
		VolumeIDs:    in.VolumeIDs,
		OSVolumeID:   osVol,
		GPU:          in.GPU.Description,
		Image:        in.Image,
	}
}

func fromSDKVolume(v *sdk.Volume) provider.Volume {
	var instIDs []string
	for _, ai := range v.Instances {
		instIDs = append(instIDs, ai.ID)
	}
	if len(instIDs) == 0 && v.InstanceID != nil && *v.InstanceID != "" {
		instIDs = []string{*v.InstanceID}
	}
	mount := ""
	if v.MountCommand != nil {
		mount = *v.MountCommand
	}
	mkdir := ""
	if v.CreateDirectoryCommand != nil {
		mkdir = *v.CreateDirectoryCommand
	}
	return provider.Volume{
		ID:           v.ID,
		Name:         v.Name,
		SizeGB:       v.Size,
		Type:         v.Type,
		Status:       v.Status,
		Location:     v.Location,
		InstanceIDs:  instIDs,
		MountCommand: mount,
		MkdirCommand: mkdir,
	}
}
