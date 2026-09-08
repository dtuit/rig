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

// rootDevice is the root volume's device name on the Ubuntu-based AMIs the
// driver defaults to; the size override in CreateInstance maps it.
const rootDevice = "/dev/sda1"

// ListInstances is every instance tagged rig:managed, in any state —
// terminated ones show as Gone until EC2 forgets them.
func (d *Driver) ListInstances(ctx context.Context) ([]provider.Instance, error) {
	if err := d.clients(ctx); err != nil {
		return nil, err
	}
	pag := ec2.NewDescribeInstancesPaginator(d.ec2, &ec2.DescribeInstancesInput{
		Filters: []types.Filter{filter("tag:"+managedTag, "true")},
	})
	var raw []types.Instance
	for pag.HasMorePages() {
		page, err := pag.NextPage(ctx)
		if err != nil {
			return nil, d.fail("listing instances", err)
		}
		for _, r := range page.Reservations {
			raw = append(raw, r.Instances...)
		}
	}
	return d.fromEC2Instances(ctx, raw), nil
}

func (d *Driver) GetInstance(ctx context.Context, id string) (*provider.Instance, error) {
	raw, err := d.describe(ctx, id)
	if err != nil {
		return nil, err
	}
	out := d.fromEC2Instances(ctx, []types.Instance{*raw})
	return &out[0], nil
}

// describe is DescribeInstances for one id. A missing instance is a "not
// found" error, worded so core's heuristic recognises it.
func (d *Driver) describe(ctx context.Context, id string) (*types.Instance, error) {
	if err := d.clients(ctx); err != nil {
		return nil, err
	}
	out, err := d.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		if code := errCode(err); code == "InvalidInstanceID.NotFound" || code == "InvalidInstanceID.Malformed" {
			return nil, fmt.Errorf("instance %s not found", id)
		}
		return nil, d.fail("describing instance "+id, err)
	}
	for _, r := range out.Reservations {
		for i := range r.Instances {
			return &r.Instances[i], nil
		}
	}
	return nil, fmt.Errorf("instance %s not found", id)
}

// fromEC2Instances maps a batch and prices it: on-demand from the pricing
// cache, spot from the current spot price in the instance's own zone
// (lowest zone when its own is unknown), GPU from the type catalogue when
// InstanceTypes has run in this process.
func (d *Driver) fromEC2Instances(ctx context.Context, raw []types.Instance) []provider.Instance {
	out := make([]provider.Instance, 0, len(raw))
	seen, seenSpot := map[string]bool{}, map[string]bool{}
	var names, spotNames []string
	for _, in := range raw {
		v := fromEC2Instance(in)
		out = append(out, v)
		if v.Type == "" {
			continue
		}
		if !seen[v.Type] {
			seen[v.Type] = true
			names = append(names, v.Type)
		}
		if v.Spot && !seenSpot[v.Type] {
			seenSpot[v.Type] = true
			spotNames = append(spotNames, v.Type)
		}
	}
	prices := d.onDemandPrices(ctx, names)
	var spot map[string]map[string]float64
	if len(spotNames) > 0 {
		spot = d.spotPrices(ctx, spotNames)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range out {
		v := &out[i]
		v.GPU = d.gpuByType[v.Type]
		if !v.Spot {
			v.PricePerHour = prices[v.Type]
			continue
		}
		if p := spot[v.Type][v.Location]; p > 0 {
			v.PricePerHour = p
		} else {
			v.PricePerHour = minSpot(spot[v.Type])
		}
	}
	return out
}

// fromEC2Instance is the pure mapping; prices and GPU are filled by the
// caller. The endpoint is the public IP on 22 as ubuntu, empty-hosted while
// pending.
func fromEC2Instance(in types.Instance) provider.Instance {
	state := ""
	if in.State != nil {
		state = string(in.State.Name)
	}
	ip := deref(in.PublicIpAddress)
	v := provider.Instance{
		ID:        deref(in.InstanceId),
		Name:      tagValue(in.Tags, "Name"),
		Type:      string(in.InstanceType),
		Status:    fromState(state, in.StateReason),
		RawStatus: state,
		IP:        ip,
		SSH:       provider.Endpoint{Host: ip, Port: 22, User: sshUser},
		Spot:      in.InstanceLifecycle == types.InstanceLifecycleTypeSpot,
		Image:     deref(in.ImageId),
	}
	if in.LaunchTime != nil {
		v.CreatedAt = *in.LaunchTime
	}
	if in.Placement != nil {
		v.Location = deref(in.Placement.AvailabilityZone)
	}
	v.OSVolumeID, v.VolumeIDs = splitVolumes(in)
	return v
}

// splitVolumes is the root volume id and the data volume ids of an instance.
func splitVolumes(in types.Instance) (root string, data []string) {
	rootDev := deref(in.RootDeviceName)
	if rootDev == "" {
		rootDev = rootDevice
	}
	for _, m := range in.BlockDeviceMappings {
		if m.Ebs == nil {
			continue
		}
		id := deref(m.Ebs.VolumeId)
		if deref(m.DeviceName) == rootDev {
			root = id
		} else {
			data = append(data, id)
		}
	}
	return root, data
}

// fromState maps EC2 instance states onto provider.Status. A spot instance
// EC2 terminated for want of capacity before it ever ran reports
// NoCapacity so core moves to the next candidate instead of aborting.
func fromState(state string, reason *types.StateReason) provider.Status {
	switch state {
	case "pending":
		return provider.Provisioning
	case "running":
		return provider.Running
	case "stopping", "stopped":
		return provider.Stopped
	case "shutting-down", "terminated":
		if reason != nil && deref(reason.Code) == "Server.InsufficientInstanceCapacity" {
			return provider.NoCapacity
		}
		return provider.Gone
	}
	return provider.Status(state)
}

// network is what a launch in one zone needs: the default VPC's subnet
// there and the rig security group's id.
type network struct {
	subnet, group string
}

// ensureNetwork finds the default VPC, its subnet in zone and the security
// group named in config, creating the group (22/tcp from anywhere, tagged
// rig:managed) when it does not exist. Answers are kept for the process.
func (d *Driver) ensureNetwork(ctx context.Context, zone string) (network, error) {
	d.mu.Lock()
	n, ok := d.network[zone]
	d.mu.Unlock()
	if ok {
		return n, nil
	}

	vpcs, err := d.ec2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{Filters: []types.Filter{filter("is-default", "true")}})
	if err != nil {
		return network{}, d.fail("finding the default VPC", err)
	}
	vpc := ""
	for _, v := range vpcs.Vpcs {
		if sdk.ToBool(v.IsDefault) {
			vpc = deref(v.VpcId)
			break
		}
	}
	if vpc == "" {
		return network{}, fmt.Errorf("aws: no default VPC in %s; create one with: aws ec2 create-default-vpc --region %s", d.region, d.region)
	}

	subs, err := d.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []types.Filter{filter("vpc-id", vpc), filter("availability-zone", zone)},
	})
	if err != nil {
		return network{}, d.fail("finding a subnet in "+zone, err)
	}
	for _, s := range subs.Subnets {
		if deref(s.AvailabilityZone) != zone {
			continue
		}
		if n.subnet == "" || sdk.ToBool(s.DefaultForAz) {
			n.subnet = deref(s.SubnetId)
		}
	}
	if n.subnet == "" {
		return network{}, fmt.Errorf("aws: no subnet in %s in the default VPC %s", zone, vpc)
	}

	groups, err := d.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []types.Filter{filter("vpc-id", vpc), filter("group-name", d.cfg.SecurityGroup)},
	})
	if err != nil {
		return network{}, d.fail("finding security group "+d.cfg.SecurityGroup, err)
	}
	for _, g := range groups.SecurityGroups {
		if deref(g.GroupName) == d.cfg.SecurityGroup {
			n.group = deref(g.GroupId)
		}
	}
	if n.group == "" {
		created, err := d.ec2.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
			GroupName:   sdk.String(d.cfg.SecurityGroup),
			Description: sdk.String("rig: ssh access to rig-managed instances"),
			VpcId:       sdk.String(vpc),
			TagSpecifications: []types.TagSpecification{{
				ResourceType: types.ResourceTypeSecurityGroup,
				Tags:         managedTags(d.cfg.SecurityGroup),
			}},
		})
		if err != nil {
			return network{}, d.fail("creating security group "+d.cfg.SecurityGroup, err)
		}
		n.group = deref(created.GroupId)
		_, err = d.ec2.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: sdk.String(n.group),
			IpPermissions: []types.IpPermission{{
				IpProtocol: sdk.String("tcp"),
				FromPort:   sdk.Int32(22),
				ToPort:     sdk.Int32(22),
				IpRanges:   []types.IpRange{{CidrIp: sdk.String("0.0.0.0/0"), Description: sdk.String("rig ssh")}},
			}},
		})
		if err != nil {
			return network{}, d.fail("opening ssh on security group "+d.cfg.SecurityGroup, err)
		}
	}

	d.mu.Lock()
	d.network[zone] = n
	d.mu.Unlock()
	return n, nil
}

// EnsureSSHKey imports pubkey as an EC2 key pair named name unless one of
// that name exists, and returns the name — RunInstances addresses key pairs
// by name, so that is the id core hands back in SSHKeyIDs. The fingerprint
// of an existing pair is not compared: a rotated local key under the same
// name must be removed with `aws ec2 delete-key-pair` first.
func (d *Driver) EnsureSSHKey(ctx context.Context, name, pubkey string) (string, error) {
	if err := d.clients(ctx); err != nil {
		return "", err
	}
	out, err := d.ec2.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{Filters: []types.Filter{filter("key-name", name)}})
	if err != nil {
		return "", d.fail("listing key pairs", err)
	}
	for _, k := range out.KeyPairs {
		if deref(k.KeyName) == name {
			return name, nil
		}
	}
	_, err = d.ec2.ImportKeyPair(ctx, &ec2.ImportKeyPairInput{
		KeyName:           sdk.String(name),
		PublicKeyMaterial: []byte(strings.TrimSpace(pubkey)),
		TagSpecifications: []types.TagSpecification{{ResourceType: types.ResourceTypeKeyPair, Tags: managedTags(name)}},
	})
	if err != nil {
		return "", d.fail(fmt.Sprintf("registering ssh key %q", name), err)
	}
	return name, nil
}

// CreateInstance launches one instance in the requested zone: the default
// VPC's subnet there, the rig security group, a public IP, the key pair
// named in SSHKeyIDs[0], a gp3 root volume of OSVolumeGB when set, and a
// one-time spot request when Spot (interruption terminates the instance;
// EC2 allows stop only on persistent requests, which would relaunch behind
// rig's back). Instance and volumes are tagged Name=<hostname>,
// rig:managed=true. Existing data volumes are attached once the instance is
// running. No capacity in the zone wraps ErrNoCapacity; a vCPU quota hit
// names the quota to raise.
func (d *Driver) CreateInstance(ctx context.Context, req provider.CreateInstanceRequest) (*provider.Instance, error) {
	if err := d.clients(ctx); err != nil {
		return nil, err
	}
	if len(req.SSHKeyIDs) == 0 {
		return nil, errors.New("aws: create needs the key pair name from EnsureSSHKey")
	}
	net, err := d.ensureNetwork(ctx, req.Location)
	if err != nil {
		return nil, err
	}

	in := &ec2.RunInstancesInput{
		MinCount:     sdk.Int32(1),
		MaxCount:     sdk.Int32(1),
		ImageId:      sdk.String(req.Image),
		InstanceType: types.InstanceType(req.Type),
		KeyName:      sdk.String(req.SSHKeyIDs[0]),
		NetworkInterfaces: []types.InstanceNetworkInterfaceSpecification{{
			DeviceIndex:              sdk.Int32(0),
			SubnetId:                 sdk.String(net.subnet),
			Groups:                   []string{net.group},
			AssociatePublicIpAddress: sdk.Bool(true),
		}},
		TagSpecifications: []types.TagSpecification{
			{ResourceType: types.ResourceTypeInstance, Tags: append(managedTags(req.Name), tag("rig:image", req.Image))},
			{ResourceType: types.ResourceTypeVolume, Tags: managedTags(req.Name)},
		},
	}
	if req.OSVolumeGB > 0 {
		in.BlockDeviceMappings = []types.BlockDeviceMapping{{
			DeviceName: sdk.String(rootDevice),
			Ebs: &types.EbsBlockDevice{
				VolumeSize:          sdk.Int32(int32(req.OSVolumeGB)),
				VolumeType:          types.VolumeTypeGp3,
				DeleteOnTermination: sdk.Bool(true),
			},
		}}
	}
	if req.Spot {
		in.InstanceMarketOptions = &types.InstanceMarketOptionsRequest{
			MarketType: types.MarketTypeSpot,
			SpotOptions: &types.SpotMarketOptions{
				SpotInstanceType:             types.SpotInstanceTypeOneTime,
				InstanceInterruptionBehavior: types.InstanceInterruptionBehaviorTerminate,
			},
		}
	}

	out, err := d.ec2.RunInstances(ctx, in)
	if err != nil {
		return nil, d.createError(req, err)
	}
	if len(out.Instances) == 0 {
		return nil, errors.New("aws: RunInstances returned no instance")
	}
	created := fromEC2Instance(out.Instances[0])
	if created.Name == "" {
		created.Name = req.Name // tags can lag the launch response
	}
	if created.Location == "" {
		created.Location = req.Location
	}
	if len(req.ExistingVolumes) == 0 {
		return &created, nil
	}

	raw, err := d.waitRunning(ctx, created.ID, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	for i, dev := range nextDevices(raw.BlockDeviceMappings, len(req.ExistingVolumes)) {
		vol := req.ExistingVolumes[i]
		_, err := d.ec2.AttachVolume(ctx, &ec2.AttachVolumeInput{
			Device:     sdk.String(dev),
			InstanceId: sdk.String(created.ID),
			VolumeId:   sdk.String(vol),
		})
		if err != nil {
			return nil, d.fail(fmt.Sprintf("attaching volume %s to %s", vol, created.ID), err)
		}
	}
	created = fromEC2Instance(*raw)
	return &created, nil
}

// quota is an EC2 service quota a launch can hit.
type quota struct{ code, name string }

// quotaFor is the vCPU quota that governs a type: G/VT or P, on-demand or
// spot. Defaults from the console: new accounts often have 0.
func quotaFor(instanceType string, spot bool) quota {
	p := strings.HasPrefix(family(instanceType), "p")
	switch {
	case spot && p:
		return quota{"L-7212CCBC", "All P Spot Instance Requests"}
	case spot:
		return quota{"L-3819A6DF", "All G and VT Spot Instance Requests"}
	case p:
		return quota{"L-417A185B", "Running On-Demand P instances"}
	}
	return quota{"L-DB2E81BA", "Running On-Demand G and VT instances"}
}

// createError turns a RunInstances failure into what core needs: the
// capacity sentinel for "none right now in this zone", a quota hint for a
// vCPU limit, and the SSO rewrite for everything else.
func (d *Driver) createError(req provider.CreateInstanceRequest, err error) error {
	code := errCode(err)
	switch code {
	case "InsufficientInstanceCapacity", "Unsupported":
		return fmt.Errorf("aws: %s in %s: %s: %w", req.Type, req.Location, errMessage(err), provider.ErrNoCapacity)
	case "VcpuLimitExceeded", "MaxSpotInstanceCountExceeded":
		q := quotaFor(req.Type, req.Spot || code == "MaxSpotInstanceCountExceeded")
		return fmt.Errorf("aws: vCPU quota exceeded for %s in %s (%s); request an increase of quota %s (%s): aws service-quotas request-service-quota-increase --service-code ec2 --quota-code %s --desired-value <vcpus>",
			req.Type, d.region, errMessage(err), q.code, q.name, q.code)
	}
	return d.fail("creating instance "+req.Name, err)
}

// waitRunning polls until the instance is running, or fails when it is
// torn down first or timeout passes.
func (d *Driver) waitRunning(ctx context.Context, id string, timeout time.Duration) (*types.Instance, error) {
	deadline := d.now().Add(timeout)
	for {
		raw, err := d.describe(ctx, id)
		if err != nil {
			return nil, err
		}
		if raw.State != nil {
			switch raw.State.Name {
			case types.InstanceStateNameRunning:
				return raw, nil
			case types.InstanceStateNameShuttingDown, types.InstanceStateNameTerminated:
				msg := ""
				if raw.StateReason != nil {
					msg = ": " + deref(raw.StateReason.Message)
				}
				return nil, fmt.Errorf("aws: instance %s is %s before it ran%s", id, raw.State.Name, msg)
			}
		}
		if !d.now().Before(deadline) {
			return nil, fmt.Errorf("aws: instance %s not running after %s", id, timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d.poll):
		}
	}
}

// DeleteInstance terminates the instance, which takes its root volume
// with it, then deletes each other requested volume once EC2 reports it
// detached (available).
func (d *Driver) DeleteInstance(ctx context.Context, id string, deleteVolumeIDs []string) error {
	raw, err := d.describe(ctx, id)
	if err != nil {
		return err
	}
	root, _ := splitVolumes(*raw)
	if _, err := d.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{id}}); err != nil {
		return d.fail("terminating instance "+id, err)
	}
	for _, vol := range deleteVolumeIDs {
		if vol == "" || vol == root {
			continue
		}
		err := d.waitVolumeAvailable(ctx, vol, 5*time.Minute)
		if errors.Is(err, errVolumeGone) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := d.ec2.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: sdk.String(vol)}); err != nil {
			return d.fail("deleting volume "+vol, err)
		}
	}
	return nil
}

func (d *Driver) ShutdownInstance(ctx context.Context, id string) error {
	if err := d.clients(ctx); err != nil {
		return err
	}
	if _, err := d.ec2.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{id}}); err != nil {
		return d.fail("stopping instance "+id, err)
	}
	return nil
}

func (d *Driver) StartInstance(ctx context.Context, id string) error {
	if err := d.clients(ctx); err != nil {
		return err
	}
	if _, err := d.ec2.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{id}}); err != nil {
		return d.fail("starting instance "+id, err)
	}
	return nil
}
