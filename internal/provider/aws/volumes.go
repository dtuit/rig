package aws

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/dtuit/rig/internal/provider"
)

// ListVolumes is every EBS data volume tagged rig:managed, each carrying
// the mkdir and mount commands core runs after attaching it. Boot volumes
// carry the same tag (RunInstances tags what it creates) and are marked
// rig:os=true; they are not data volumes, so they are skipped.
func (d *Driver) ListVolumes(ctx context.Context) ([]provider.Volume, error) {
	if err := d.clients(ctx); err != nil {
		return nil, err
	}
	pag := ec2.NewDescribeVolumesPaginator(d.ec2, &ec2.DescribeVolumesInput{
		Filters: []types.Filter{filter("tag:"+managedTag, "true")},
	})
	var out []provider.Volume
	for pag.HasMorePages() {
		page, err := pag.NextPage(ctx)
		if err != nil {
			return nil, d.fail("listing volumes", err)
		}
		for _, v := range page.Volumes {
			if tagValue(v.Tags, osVolumeTag) == "true" {
				continue
			}
			out = append(out, fromEC2Volume(v))
		}
	}
	return out, nil
}

func (d *Driver) GetVolume(ctx context.Context, id string) (*provider.Volume, error) {
	if err := d.clients(ctx); err != nil {
		return nil, err
	}
	out, err := d.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{id}})
	if err != nil {
		if errCode(err) == "InvalidVolume.NotFound" {
			return nil, fmt.Errorf("volume %s not found", id)
		}
		return nil, d.fail("describing volume "+id, err)
	}
	if len(out.Volumes) == 0 {
		return nil, fmt.Errorf("volume %s not found", id)
	}
	v := fromEC2Volume(out.Volumes[0])
	return &v, nil
}

// CreateVolume makes an EBS volume of the requested type (config
// volume_type, default gp3) in the zone, tagged Name=<name> and
// rig:managed=true, and returns its id.
func (d *Driver) CreateVolume(ctx context.Context, req provider.CreateVolumeRequest) (string, error) {
	if err := d.clients(ctx); err != nil {
		return "", err
	}
	typ := req.Type
	if typ == "" {
		typ = d.cfg.VolumeType
	}
	out, err := d.ec2.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone:  sdk.String(req.Location),
		Size:              sdk.Int32(int32(req.SizeGB)),
		VolumeType:        types.VolumeType(typ),
		TagSpecifications: []types.TagSpecification{{ResourceType: types.ResourceTypeVolume, Tags: managedTags(req.Name)}},
	})
	if err != nil {
		return "", d.fail(fmt.Sprintf("creating volume %s in %s", req.Name, req.Location), err)
	}
	return deref(out.VolumeId), nil
}

// AttachVolume attaches at the instance's next free /dev/sd[f-p] slot.
func (d *Driver) AttachVolume(ctx context.Context, volumeID, instanceID string) error {
	raw, err := d.describe(ctx, instanceID)
	if err != nil {
		return err
	}
	devs := nextDevices(raw.BlockDeviceMappings, 1)
	if len(devs) == 0 {
		return fmt.Errorf("aws: instance %s has no free device slot for %s", instanceID, volumeID)
	}
	_, err = d.ec2.AttachVolume(ctx, &ec2.AttachVolumeInput{
		Device:     sdk.String(devs[0]),
		InstanceId: sdk.String(instanceID),
		VolumeId:   sdk.String(volumeID),
	})
	if err != nil {
		return d.fail(fmt.Sprintf("attaching volume %s to %s", volumeID, instanceID), err)
	}
	return nil
}

func (d *Driver) DetachVolume(ctx context.Context, volumeID, instanceID string) error {
	if err := d.clients(ctx); err != nil {
		return err
	}
	_, err := d.ec2.DetachVolume(ctx, &ec2.DetachVolumeInput{
		VolumeId:   sdk.String(volumeID),
		InstanceId: sdk.String(instanceID),
	})
	if err != nil {
		return d.fail(fmt.Sprintf("detaching volume %s from %s", volumeID, instanceID), err)
	}
	return nil
}

func (d *Driver) DeleteVolume(ctx context.Context, volumeID string) error {
	if err := d.clients(ctx); err != nil {
		return err
	}
	if _, err := d.ec2.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: sdk.String(volumeID)}); err != nil {
		return d.fail("deleting volume "+volumeID, err)
	}
	return nil
}

// errVolumeGone reports a volume EC2 no longer knows, which for a delete
// is success.
var errVolumeGone = errors.New("volume gone")

// waitVolumeAvailable polls until the volume is detached (available).
func (d *Driver) waitVolumeAvailable(ctx context.Context, id string, timeout time.Duration) error {
	deadline := d.now().Add(timeout)
	for {
		out, err := d.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{id}})
		if err != nil {
			if errCode(err) == "InvalidVolume.NotFound" {
				return errVolumeGone
			}
			return d.fail("describing volume "+id, err)
		}
		if len(out.Volumes) == 0 {
			return errVolumeGone
		}
		state := out.Volumes[0].State
		if state == types.VolumeStateAvailable {
			return nil
		}
		if !d.now().Before(deadline) {
			return fmt.Errorf("aws: volume %s still %s after %s; detach it and rerun rig down", id, state, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.poll):
		}
	}
}

// fromEC2Volume maps an EBS volume; the Name tag is the rig volume name
// (the id when untagged) and the mount point is /mnt/<name>.
func fromEC2Volume(v types.Volume) provider.Volume {
	id := deref(v.VolumeId)
	name := tagValue(v.Tags, "Name")
	if name == "" {
		name = id
	}
	var instances []string
	for _, a := range v.Attachments {
		if a.State != types.VolumeAttachmentStateDetached && deref(a.InstanceId) != "" {
			instances = append(instances, deref(a.InstanceId))
		}
	}
	return provider.Volume{
		ID:           id,
		Name:         name,
		SizeGB:       int(sdk.ToInt32(v.Size)),
		Type:         string(v.VolumeType),
		Status:       string(v.State),
		Location:     deref(v.AvailabilityZone),
		InstanceIDs:  instances,
		MkdirCommand: mkdirCommand(name),
		MountCommand: mountCommand(id, name),
	}
}

func mountPoint(name string) string { return "/mnt/" + name }

func mkdirCommand(name string) string { return "sudo mkdir -p " + mountPoint(name) }

// mountCommand is one root shell line: find the EBS device by its NVMe
// serial — the volume id without its dash, as Nitro instances expose it —
// format it only when blkid finds no filesystem, and mount it at
// /mnt/<name> unless something is already mounted there. The awk program
// is double-quoted with escaped dollars so it survives the outer single
// quotes.
func mountCommand(volumeID, name string) string {
	serial := strings.ReplaceAll(volumeID, "-", "")
	mnt := mountPoint(name)
	script := fmt.Sprintf(`dev=$(lsblk -dno PATH,SERIAL | awk -v s=%s "\$2==s{print \$1}"); `+
		`[ -n "$dev" ] || { echo "no block device with serial %s (volume %s) on this instance" >&2; exit 1; }; `+
		`blkid "$dev" >/dev/null 2>&1 || mkfs.ext4 -q "$dev"; `+
		`mountpoint -q %s || mount "$dev" %s`, serial, serial, volumeID, mnt, mnt)
	return "sudo bash -c '" + script + "'"
}

// nextDevices are the first n free data-volume device names of an
// instance, /dev/sdf through /dev/sdp, skipping slots its block device
// mappings already use under either the sd or xvd spelling.
func nextDevices(mappings []types.InstanceBlockDeviceMapping, n int) []string {
	used := map[string]bool{}
	for _, m := range mappings {
		used[strings.TrimPrefix(deref(m.DeviceName), "/dev/")] = true
	}
	var out []string
	for c := 'f'; c <= 'p' && len(out) < n; c++ {
		if used["sd"+string(c)] || used["xvd"+string(c)] {
			continue
		}
		out = append(out, "/dev/sd"+string(c))
	}
	return out
}
