package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"

	"github.com/dtuit/rig/internal/provider"
)

var launch = time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)

// fakeEC2 is the hand-rolled ec2API: describe calls answer from its fields,
// mutating calls record what they were asked and answer from scripted
// errors. Anything unscripted answers empty.
type fakeEC2 struct {
	zones     []types.AvailabilityZone
	typeInfos []types.InstanceTypeInfo
	offerings []types.InstanceTypeOffering
	spot      []types.SpotPrice
	keys      []types.KeyPairInfo
	vpcs      []types.Vpc
	subnets   []types.Subnet
	groups    []types.SecurityGroup
	instances []types.Instance
	volumes   []types.Volume

	zonesErr error
	runErr   error
	runOut   *types.Instance // what RunInstances returns on success

	runIn        *ec2.RunInstancesInput
	imported     *ec2.ImportKeyPairInput
	createdGroup *ec2.CreateSecurityGroupInput
	authorized   *ec2.AuthorizeSecurityGroupIngressInput
	terminated   []string
	stopped      []string
	started      []string
	attached     []*ec2.AttachVolumeInput
	detached     []*ec2.DetachVolumeInput
	deletedVols  []string
	createdVol   *ec2.CreateVolumeInput
}

func (f *fakeEC2) DescribeAvailabilityZones(context.Context, *ec2.DescribeAvailabilityZonesInput, ...func(*ec2.Options)) (*ec2.DescribeAvailabilityZonesOutput, error) {
	if f.zonesErr != nil {
		return nil, f.zonesErr
	}
	return &ec2.DescribeAvailabilityZonesOutput{AvailabilityZones: f.zones}, nil
}

func (f *fakeEC2) DescribeInstanceTypes(context.Context, *ec2.DescribeInstanceTypesInput, ...func(*ec2.Options)) (*ec2.DescribeInstanceTypesOutput, error) {
	return &ec2.DescribeInstanceTypesOutput{InstanceTypes: f.typeInfos}, nil
}

func (f *fakeEC2) DescribeInstanceTypeOfferings(context.Context, *ec2.DescribeInstanceTypeOfferingsInput, ...func(*ec2.Options)) (*ec2.DescribeInstanceTypeOfferingsOutput, error) {
	return &ec2.DescribeInstanceTypeOfferingsOutput{InstanceTypeOfferings: f.offerings}, nil
}

func (f *fakeEC2) DescribeSpotPriceHistory(context.Context, *ec2.DescribeSpotPriceHistoryInput, ...func(*ec2.Options)) (*ec2.DescribeSpotPriceHistoryOutput, error) {
	return &ec2.DescribeSpotPriceHistoryOutput{SpotPriceHistory: f.spot}, nil
}

func (f *fakeEC2) DescribeKeyPairs(context.Context, *ec2.DescribeKeyPairsInput, ...func(*ec2.Options)) (*ec2.DescribeKeyPairsOutput, error) {
	return &ec2.DescribeKeyPairsOutput{KeyPairs: f.keys}, nil
}

func (f *fakeEC2) ImportKeyPair(_ context.Context, in *ec2.ImportKeyPairInput, _ ...func(*ec2.Options)) (*ec2.ImportKeyPairOutput, error) {
	f.imported = in
	return &ec2.ImportKeyPairOutput{KeyName: in.KeyName, KeyPairId: sdk.String("key-0new")}, nil
}

func (f *fakeEC2) DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return &ec2.DescribeVpcsOutput{Vpcs: f.vpcs}, nil
}

func (f *fakeEC2) DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	return &ec2.DescribeSubnetsOutput{Subnets: f.subnets}, nil
}

func (f *fakeEC2) DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: f.groups}, nil
}

func (f *fakeEC2) CreateSecurityGroup(_ context.Context, in *ec2.CreateSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error) {
	f.createdGroup = in
	return &ec2.CreateSecurityGroupOutput{GroupId: sdk.String("sg-0new")}, nil
}

func (f *fakeEC2) AuthorizeSecurityGroupIngress(_ context.Context, in *ec2.AuthorizeSecurityGroupIngressInput, _ ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupIngressOutput, error) {
	f.authorized = in
	return &ec2.AuthorizeSecurityGroupIngressOutput{}, nil
}

func (f *fakeEC2) RunInstances(_ context.Context, in *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	f.runIn = in
	if f.runErr != nil {
		return nil, f.runErr
	}
	out := f.runOut
	if out == nil {
		out = &types.Instance{
			InstanceId:   sdk.String("i-0new"),
			InstanceType: types.InstanceType(in.InstanceType),
			State:        &types.InstanceState{Name: types.InstanceStateNamePending},
			LaunchTime:   sdk.Time(launch),
		}
	}
	return &ec2.RunInstancesOutput{Instances: []types.Instance{*out}}, nil
}

func (f *fakeEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	var out []types.Instance
	for _, i := range f.instances {
		if len(in.InstanceIds) == 0 || i.InstanceId != nil && in.InstanceIds[0] == *i.InstanceId {
			out = append(out, i)
		}
	}
	if len(in.InstanceIds) > 0 && len(out) == 0 {
		return nil, &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound", Message: "The instance ID '" + in.InstanceIds[0] + "' does not exist"}
	}
	return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: out}}}, nil
}

func (f *fakeEC2) TerminateInstances(_ context.Context, in *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	f.terminated = append(f.terminated, in.InstanceIds...)
	return &ec2.TerminateInstancesOutput{}, nil
}

func (f *fakeEC2) StopInstances(_ context.Context, in *ec2.StopInstancesInput, _ ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
	f.stopped = append(f.stopped, in.InstanceIds...)
	return &ec2.StopInstancesOutput{}, nil
}

func (f *fakeEC2) StartInstances(_ context.Context, in *ec2.StartInstancesInput, _ ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	f.started = append(f.started, in.InstanceIds...)
	return &ec2.StartInstancesOutput{}, nil
}

func (f *fakeEC2) CreateVolume(_ context.Context, in *ec2.CreateVolumeInput, _ ...func(*ec2.Options)) (*ec2.CreateVolumeOutput, error) {
	f.createdVol = in
	return &ec2.CreateVolumeOutput{VolumeId: sdk.String("vol-0new")}, nil
}

func (f *fakeEC2) DescribeVolumes(_ context.Context, in *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	var out []types.Volume
	for _, v := range f.volumes {
		if len(in.VolumeIds) == 0 || v.VolumeId != nil && in.VolumeIds[0] == *v.VolumeId {
			out = append(out, v)
		}
	}
	return &ec2.DescribeVolumesOutput{Volumes: out}, nil
}

func (f *fakeEC2) AttachVolume(_ context.Context, in *ec2.AttachVolumeInput, _ ...func(*ec2.Options)) (*ec2.AttachVolumeOutput, error) {
	f.attached = append(f.attached, in)
	return &ec2.AttachVolumeOutput{}, nil
}

func (f *fakeEC2) DetachVolume(_ context.Context, in *ec2.DetachVolumeInput, _ ...func(*ec2.Options)) (*ec2.DetachVolumeOutput, error) {
	f.detached = append(f.detached, in)
	return &ec2.DetachVolumeOutput{}, nil
}

func (f *fakeEC2) DeleteVolume(_ context.Context, in *ec2.DeleteVolumeInput, _ ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error) {
	f.deletedVols = append(f.deletedVols, *in.VolumeId)
	return &ec2.DeleteVolumeOutput{}, nil
}

type fakeSSM struct {
	params map[string]string
	asked  []string
}

func (f *fakeSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.asked = append(f.asked, *in.Name)
	v, ok := f.params[*in.Name]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "ParameterNotFound", Message: *in.Name}
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Name: in.Name, Value: sdk.String(v)}}, nil
}

type fakePricing struct {
	calls int
	items []string // price-list JSON documents
	err   error
}

func (f *fakePricing) GetProducts(context.Context, *pricing.GetProductsInput, ...func(*pricing.Options)) (*pricing.GetProductsOutput, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &pricing.GetProductsOutput{PriceList: f.items}, nil
}

// priceDoc is one AWS Price List item in the shape GetProducts returns it,
// trimmed to what the driver reads.
func priceDoc(instanceType, usd string) string {
	return fmt.Sprintf(`{"product":{"productFamily":"Compute Instance","attributes":{"instanceType":%q,"operation":"RunInstances","operatingSystem":"Linux","tenancy":"Shared","preInstalledSw":"NA","capacitystatus":"Used"}},"terms":{"OnDemand":{"SKU.JRTCKXETXF":{"priceDimensions":{"SKU.JRTCKXETXF.6YS6EN2CT7":{"unit":"Hrs","pricePerUnit":{"USD":%q}}}}}}}`, instanceType, usd)
}

// newTestDriver wires the fakes into a driver for ap-southeast-2 with a
// throwaway pricing cache directory and no waiting between polls.
func newTestDriver(t *testing.T, e *fakeEC2, s *fakeSSM, p *fakePricing) *Driver {
	t.Helper()
	d := New(Config{Profile: "work", Region: "ap-southeast-2"})
	d.ec2, d.ssm, d.pricing = e, s, p
	d.cacheDir = t.TempDir()
	d.poll = 0
	return d
}

func seedPriceCache(t *testing.T, d *Driver, prices map[string]float64) {
	t.Helper()
	b, _ := json.Marshal(priceCache{FetchedAt: time.Now(), Prices: prices})
	if err := os.WriteFile(filepath.Join(d.cacheDir, "aws-pricing-ap-southeast-2.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func ec2Instance(id, name string, state types.InstanceStateName, ip string) types.Instance {
	in := types.Instance{
		InstanceId:     sdk.String(id),
		InstanceType:   types.InstanceTypeG5Xlarge,
		ImageId:        sdk.String("ami-0abc"),
		State:          &types.InstanceState{Name: state},
		LaunchTime:     sdk.Time(launch),
		Placement:      &types.Placement{AvailabilityZone: sdk.String("ap-southeast-2a")},
		Tags:           []types.Tag{tag("Name", name), tag("rig:managed", "true")},
		RootDeviceName: sdk.String("/dev/sda1"),
		BlockDeviceMappings: []types.InstanceBlockDeviceMapping{
			{DeviceName: sdk.String("/dev/sda1"), Ebs: &types.EbsInstanceBlockDevice{VolumeId: sdk.String("vol-0root")}},
			{DeviceName: sdk.String("/dev/sdf"), Ebs: &types.EbsInstanceBlockDevice{VolumeId: sdk.String("vol-0data")}},
		},
	}
	if ip != "" {
		in.PublicIpAddress = sdk.String(ip)
	}
	return in
}

func defaultNetwork() (vpcs []types.Vpc, subnets []types.Subnet) {
	return []types.Vpc{{VpcId: sdk.String("vpc-0def"), IsDefault: sdk.Bool(true)}},
		[]types.Subnet{
			{SubnetId: sdk.String("subnet-0b"), AvailabilityZone: sdk.String("ap-southeast-2b"), DefaultForAz: sdk.Bool(true)},
			{SubnetId: sdk.String("subnet-0a"), AvailabilityZone: sdk.String("ap-southeast-2a"), DefaultForAz: sdk.Bool(true)},
		}
}

func TestListInstancesMapsStateAndEndpoint(t *testing.T) {
	spot := ec2Instance("i-0spot", "rig-cheap", types.InstanceStateNameRunning, "3.4.5.6")
	spot.InstanceLifecycle = types.InstanceLifecycleTypeSpot
	e := &fakeEC2{
		instances: []types.Instance{
			ec2Instance("i-0pend", "rig-new", types.InstanceStateNamePending, ""),
			ec2Instance("i-0run", "rig-train", types.InstanceStateNameRunning, "13.210.1.2"),
			spot,
			ec2Instance("i-0stop", "rig-idle", types.InstanceStateNameStopped, ""),
			ec2Instance("i-0shut", "rig-bye", types.InstanceStateNameShuttingDown, ""),
			ec2Instance("i-0term", "rig-gone", types.InstanceStateNameTerminated, ""),
		},
		spot: []types.SpotPrice{
			{InstanceType: types.InstanceTypeG5Xlarge, AvailabilityZone: sdk.String("ap-southeast-2a"), SpotPrice: sdk.String("0.4020"), Timestamp: sdk.Time(launch)},
			{InstanceType: types.InstanceTypeG5Xlarge, AvailabilityZone: sdk.String("ap-southeast-2a"), SpotPrice: sdk.String("0.9000"), Timestamp: sdk.Time(launch.Add(-time.Hour))},
			{InstanceType: types.InstanceTypeG5Xlarge, AvailabilityZone: sdk.String("ap-southeast-2b"), SpotPrice: sdk.String("0.3000"), Timestamp: sdk.Time(launch)},
		},
	}
	p := &fakePricing{}
	d := newTestDriver(t, e, &fakeSSM{}, p)
	seedPriceCache(t, d, map[string]float64{"g5.xlarge": 1.006})

	got, err := d.ListInstances(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d instances, want 6", len(got))
	}
	wantStatus := []provider.Status{provider.Provisioning, provider.Running, provider.Running, provider.Stopped, provider.Gone, provider.Gone}
	wantRaw := []string{"pending", "running", "running", "stopped", "shutting-down", "terminated"}
	for i, in := range got {
		if in.Status != wantStatus[i] || in.RawStatus != wantRaw[i] {
			t.Errorf("[%d] %s status = %s/%s, want %s/%s", i, in.ID, in.Status, in.RawStatus, wantStatus[i], wantRaw[i])
		}
	}

	pend := got[0]
	if pend.IP != "" || pend.SSH.Host != "" {
		t.Errorf("pending instance must have no endpoint yet, got IP %q ssh %+v", pend.IP, pend.SSH)
	}
	run := got[1]
	want := provider.Endpoint{Host: "13.210.1.2", Port: 22, User: "ubuntu"}
	if run.SSH != want || run.IP != "13.210.1.2" {
		t.Errorf("running endpoint = %+v ip %q, want %+v", run.SSH, run.IP, want)
	}
	if run.Name != "rig-train" || run.Type != "g5.xlarge" || run.Location != "ap-southeast-2a" || run.Image != "ami-0abc" || !run.CreatedAt.Equal(launch) {
		t.Errorf("fields lost: %+v", run)
	}
	if run.OSVolumeID != "vol-0root" || len(run.VolumeIDs) != 1 || run.VolumeIDs[0] != "vol-0data" {
		t.Errorf("volumes: os %q data %v, want vol-0root / [vol-0data]", run.OSVolumeID, run.VolumeIDs)
	}
	if run.Spot || run.PricePerHour != 1.006 {
		t.Errorf("on-demand: spot=%v price=%v, want false / 1.006 from the cache", run.Spot, run.PricePerHour)
	}
	if !got[2].Spot || got[2].PricePerHour != 0.402 {
		t.Errorf("spot: spot=%v price=%v, want true / 0.402 (latest in its own zone)", got[2].Spot, got[2].PricePerHour)
	}
	if run.Provider != "" {
		t.Errorf("Provider is stamped by core, driver set %q", run.Provider)
	}
	if p.calls != 0 {
		t.Errorf("pricing API called %d times with a fresh cache, want 0", p.calls)
	}
}

func TestGetInstanceNotFoundSaysSo(t *testing.T) {
	d := newTestDriver(t, &fakeEC2{}, &fakeSSM{}, &fakePricing{})
	_, err := d.GetInstance(t.Context(), "i-0nope")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want one core's isNotFound recognises", err)
	}
}

func TestExpiredSSOTellsUserToLogin(t *testing.T) {
	e := &fakeEC2{zonesErr: errors.New("operation error EC2: DescribeAvailabilityZones, get identity: get credentials: failed to refresh cached credentials, the SSO session has expired or is invalid: open /home/u/.aws/sso/cache/x.json: no such file or directory")}
	d := newTestDriver(t, e, &fakeSSM{}, &fakePricing{})
	_, err := d.Locations(t.Context())
	want := "aws: credentials expired; run: aws sso login --profile work"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}

	e.zonesErr = &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "You are not authorized"}
	_, err = d.Locations(t.Context())
	if err == nil || strings.Contains(err.Error(), "sso login") {
		t.Errorf("a non-SSO error must not be rewritten as expired credentials: %v", err)
	}
}

func TestLocationsAreAvailableZones(t *testing.T) {
	e := &fakeEC2{zones: []types.AvailabilityZone{
		{ZoneName: sdk.String("ap-southeast-2a"), State: types.AvailabilityZoneStateAvailable},
		{ZoneName: sdk.String("ap-southeast-2b"), State: types.AvailabilityZoneStateAvailable},
		{ZoneName: sdk.String("ap-southeast-2c"), State: types.AvailabilityZoneStateUnavailable},
	}}
	d := newTestDriver(t, e, &fakeSSM{}, &fakePricing{})
	locs, err := d.Locations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 2 || locs[0] != (provider.Location{Code: "ap-southeast-2a", Name: "ap-southeast-2a"}) || locs[1].Code != "ap-southeast-2b" {
		t.Errorf("Locations = %+v", locs)
	}
}

func TestAvailabilityGroupsOfferingsByZone(t *testing.T) {
	e := &fakeEC2{offerings: []types.InstanceTypeOffering{
		{InstanceType: types.InstanceTypeG6Xlarge, Location: sdk.String("ap-southeast-2a")},
		{InstanceType: types.InstanceTypeG5Xlarge, Location: sdk.String("ap-southeast-2b")},
		{InstanceType: types.InstanceTypeG5Xlarge, Location: sdk.String("ap-southeast-2a")},
		{InstanceType: types.InstanceTypeT3Medium, Location: sdk.String("ap-southeast-2a")},
	}}
	d := newTestDriver(t, e, &fakeSSM{}, &fakePricing{})
	for _, spot := range []bool{false, true} {
		got, err := d.Availability(t.Context(), spot)
		if err != nil {
			t.Fatal(err)
		}
		a, b := got["ap-southeast-2a"], got["ap-southeast-2b"]
		if len(got) != 2 || strings.Join(a, ",") != "g5.xlarge,g6.xlarge,t3.medium" || strings.Join(b, ",") != "g5.xlarge" {
			t.Errorf("Availability(spot=%v) = %v", spot, got)
		}
	}
}

func TestInstanceTypesFromCacheAndGpuInfo(t *testing.T) {
	e := &fakeEC2{
		typeInfos: []types.InstanceTypeInfo{
			{
				InstanceType: types.InstanceTypeG5Xlarge,
				GpuInfo: &types.GpuInfo{
					Gpus:                []types.GpuDeviceInfo{{Manufacturer: sdk.String("NVIDIA"), Name: sdk.String("A10G"), Count: sdk.Int32(1), MemoryInfo: &types.GpuDeviceMemoryInfo{SizeInMiB: sdk.Int32(24576)}}},
					TotalGpuMemoryInMiB: sdk.Int32(24576),
				},
				MemoryInfo: &types.MemoryInfo{SizeInMiB: sdk.Int64(16384)},
				VCpuInfo:   &types.VCpuInfo{DefaultVCpus: sdk.Int32(4)},
			},
			{
				InstanceType: types.InstanceTypeP4d24xlarge,
				GpuInfo: &types.GpuInfo{
					Gpus:                []types.GpuDeviceInfo{{Manufacturer: sdk.String("NVIDIA"), Name: sdk.String("A100"), Count: sdk.Int32(8), MemoryInfo: &types.GpuDeviceMemoryInfo{SizeInMiB: sdk.Int32(40960)}}},
					TotalGpuMemoryInMiB: sdk.Int32(327680),
				},
				MemoryInfo: &types.MemoryInfo{SizeInMiB: sdk.Int64(1179648)},
				VCpuInfo:   &types.VCpuInfo{DefaultVCpus: sdk.Int32(96)},
			},
			{ // not a configured family: the server-side wildcard filter is trusted, but a stray answer is dropped
				InstanceType: types.InstanceTypeT3Medium,
				MemoryInfo:   &types.MemoryInfo{SizeInMiB: sdk.Int64(4096)},
				VCpuInfo:     &types.VCpuInfo{DefaultVCpus: sdk.Int32(2)},
			},
		},
		spot: []types.SpotPrice{
			{InstanceType: types.InstanceTypeG5Xlarge, AvailabilityZone: sdk.String("ap-southeast-2a"), SpotPrice: sdk.String("0.4020"), Timestamp: sdk.Time(launch)},
			{InstanceType: types.InstanceTypeG5Xlarge, AvailabilityZone: sdk.String("ap-southeast-2b"), SpotPrice: sdk.String("0.3000"), Timestamp: sdk.Time(launch)},
			{InstanceType: types.InstanceTypeG5Xlarge, AvailabilityZone: sdk.String("ap-southeast-2b"), SpotPrice: sdk.String("0.1000"), Timestamp: sdk.Time(launch.Add(-time.Hour))},
		},
	}
	p := &fakePricing{}
	d := newTestDriver(t, e, &fakeSSM{}, p)
	seedPriceCache(t, d, map[string]float64{"g5.xlarge": 1.006, "p4d.24xlarge": 32.7726})

	got, err := d.InstanceTypes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d types, want 2 (t3.medium is not a GPU family): %+v", len(got), got)
	}
	want := provider.InstanceType{Type: "g5.xlarge", GPU: "NVIDIA A10G", GPUCount: 1, GPUMemoryGB: 24, MemoryGB: 16, CPUCount: 4, PricePerHour: 1.006, SpotPrice: 0.3}
	if got[0] != want {
		t.Errorf("g5.xlarge = %+v\nwant %+v", got[0], want)
	}
	if got[1].Type != "p4d.24xlarge" || got[1].GPUCount != 8 || got[1].GPUMemoryGB != 320 || got[1].MemoryGB != 1152 || got[1].PricePerHour != 32.7726 || got[1].SpotPrice != 0 {
		t.Errorf("p4d.24xlarge = %+v", got[1])
	}
	if p.calls != 0 {
		t.Errorf("pricing API called %d times with a fresh cache, want 0", p.calls)
	}
}

func TestInstanceTypesFetchesPricesOnceAndCaches(t *testing.T) {
	e := &fakeEC2{typeInfos: []types.InstanceTypeInfo{{
		InstanceType: types.InstanceTypeG5Xlarge,
		GpuInfo:      &types.GpuInfo{Gpus: []types.GpuDeviceInfo{{Manufacturer: sdk.String("NVIDIA"), Name: sdk.String("A10G"), Count: sdk.Int32(1)}}, TotalGpuMemoryInMiB: sdk.Int32(24576)},
		MemoryInfo:   &types.MemoryInfo{SizeInMiB: sdk.Int64(16384)},
		VCpuInfo:     &types.VCpuInfo{DefaultVCpus: sdk.Int32(4)},
	}}}
	p := &fakePricing{items: []string{priceDoc("g5.xlarge", "1.0060000000"), priceDoc("t3.medium", "0.0528000000")}}
	d := newTestDriver(t, e, &fakeSSM{}, p)

	for i := 0; i < 2; i++ {
		got, err := d.InstanceTypes(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].PricePerHour != 1.006 {
			t.Errorf("call %d: %+v, want g5.xlarge at 1.006", i, got)
		}
	}
	if p.calls != 1 {
		t.Errorf("pricing API called %d times, want 1 (second call served from the cache)", p.calls)
	}
	b, err := os.ReadFile(filepath.Join(d.cacheDir, "aws-pricing-ap-southeast-2.json"))
	if err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
	var pc priceCache
	if err := json.Unmarshal(b, &pc); err != nil || pc.Prices["t3.medium"] != 0.0528 {
		t.Errorf("cache = %s (%v), want every fetched type kept", b, err)
	}

	// a pricing outage leaves prices at 0 rather than failing the listing
	p2 := &fakePricing{err: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no pricing:GetProducts"}}
	d2 := newTestDriver(t, e, &fakeSSM{}, p2)
	got, err := d2.InstanceTypes(t.Context())
	if err != nil || len(got) != 1 || got[0].PricePerHour != 0 {
		t.Errorf("with pricing denied: %+v, %v; want the type at price 0 and no error", got, err)
	}
}

func TestImagesResolveSSMParameterPerTypeClass(t *testing.T) {
	s := &fakeSSM{params: map[string]string{
		dlamiParameter:  "ami-0gpu",
		ubuntuParameter: "ami-0cpu",
	}}
	d := newTestDriver(t, &fakeEC2{}, s, &fakePricing{})

	imgs, err := d.Images(t.Context(), "g5.xlarge")
	if err != nil {
		t.Fatal(err)
	}
	want := provider.Image{ID: "ami-0gpu", Name: "ami-0gpu", Display: dlamiParameter, Default: true}
	if len(imgs) != 1 || imgs[0] != want {
		t.Errorf("Images(g5.xlarge) = %+v, want [%+v]", imgs, want)
	}
	imgs, err = d.Images(t.Context(), "t3.medium")
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || imgs[0].Name != "ami-0cpu" || imgs[0].Display != ubuntuParameter {
		t.Errorf("Images(t3.medium) = %+v, want the plain Ubuntu parameter", imgs)
	}

	// a configured AMI id is used as-is, without asking SSM
	asked := len(s.asked)
	d.cfg.DefaultImage = "ami-0mine"
	imgs, err = d.Images(t.Context(), "g5.xlarge")
	if err != nil || len(imgs) != 1 || imgs[0].Name != "ami-0mine" || !imgs[0].Default || len(s.asked) != asked {
		t.Errorf("Images with default_image ami-0mine = %+v, %v (ssm asked %d more times)", imgs, err, len(s.asked)-asked)
	}

	// a configured parameter path is resolved for every type
	d.cfg.DefaultImage = "/my/org/gpu-ami"
	s.params["/my/org/gpu-ami"] = "ami-0org"
	imgs, err = d.Images(t.Context(), "t3.medium")
	if err != nil || len(imgs) != 1 || imgs[0].Name != "ami-0org" {
		t.Errorf("Images with default_image parameter = %+v, %v", imgs, err)
	}
}

func TestEnsureSSHKeyImportsOnlyWhenMissing(t *testing.T) {
	e := &fakeEC2{}
	d := newTestDriver(t, e, &fakeSSM{}, &fakePricing{})
	id, err := d.EnsureSSHKey(t.Context(), "rig-u-id_ed25519.pub", "ssh-ed25519 AAAA test\n")
	if err != nil || id != "rig-u-id_ed25519.pub" {
		t.Fatalf("EnsureSSHKey = %q, %v; want the key name RunInstances takes", id, err)
	}
	if e.imported == nil || *e.imported.KeyName != "rig-u-id_ed25519.pub" || string(e.imported.PublicKeyMaterial) != "ssh-ed25519 AAAA test" {
		t.Errorf("ImportKeyPair input = %+v", e.imported)
	}

	e2 := &fakeEC2{keys: []types.KeyPairInfo{{KeyName: sdk.String("rig-u-id_ed25519.pub"), KeyPairId: sdk.String("key-0have")}}}
	d2 := newTestDriver(t, e2, &fakeSSM{}, &fakePricing{})
	id, err = d2.EnsureSSHKey(t.Context(), "rig-u-id_ed25519.pub", "ssh-ed25519 AAAA test")
	if err != nil || id != "rig-u-id_ed25519.pub" || e2.imported != nil {
		t.Errorf("existing key: id %q err %v imported %v", id, err, e2.imported != nil)
	}
}

func TestCreateInstanceRequestShapeAndNetworkOnFirstUse(t *testing.T) {
	vpcs, subnets := defaultNetwork()
	e := &fakeEC2{vpcs: vpcs, subnets: subnets}
	d := newTestDriver(t, e, &fakeSSM{}, &fakePricing{})

	in, err := d.CreateInstance(t.Context(), provider.CreateInstanceRequest{
		Type: "g5.xlarge", Image: "ami-0abc", Name: "rig-train", Location: "ap-southeast-2a",
		Spot: true, SSHKeyIDs: []string{"rig-u-id_ed25519.pub"}, OSVolumeGB: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if in.ID != "i-0new" || in.Status != provider.Provisioning || in.Name != "rig-train" || in.Location != "ap-southeast-2a" {
		t.Errorf("created = %+v", in)
	}

	// security group created and opened on first use, in the default VPC
	if e.createdGroup == nil || *e.createdGroup.GroupName != "rig" || *e.createdGroup.VpcId != "vpc-0def" {
		t.Errorf("CreateSecurityGroup = %+v", e.createdGroup)
	}
	if e.authorized == nil || *e.authorized.GroupId != "sg-0new" || len(e.authorized.IpPermissions) != 1 ||
		*e.authorized.IpPermissions[0].FromPort != 22 || *e.authorized.IpPermissions[0].ToPort != 22 ||
		*e.authorized.IpPermissions[0].IpRanges[0].CidrIp != "0.0.0.0/0" {
		t.Errorf("AuthorizeSecurityGroupIngress = %+v", e.authorized)
	}

	r := e.runIn
	if r == nil {
		t.Fatal("RunInstances not called")
	}
	if *r.MinCount != 1 || *r.MaxCount != 1 || *r.ImageId != "ami-0abc" || r.InstanceType != types.InstanceTypeG5Xlarge || *r.KeyName != "rig-u-id_ed25519.pub" {
		t.Errorf("RunInstances basics = image %v type %v key %v", r.ImageId, r.InstanceType, r.KeyName)
	}
	if len(r.NetworkInterfaces) != 1 || *r.NetworkInterfaces[0].SubnetId != "subnet-0a" || r.NetworkInterfaces[0].Groups[0] != "sg-0new" || !*r.NetworkInterfaces[0].AssociatePublicIpAddress {
		t.Errorf("NetworkInterfaces = %+v (want the zone's subnet, the rig group, a public IP)", r.NetworkInterfaces)
	}
	if len(r.BlockDeviceMappings) != 1 || *r.BlockDeviceMappings[0].DeviceName != "/dev/sda1" || *r.BlockDeviceMappings[0].Ebs.VolumeSize != 200 || r.BlockDeviceMappings[0].Ebs.VolumeType != types.VolumeTypeGp3 || !*r.BlockDeviceMappings[0].Ebs.DeleteOnTermination {
		t.Errorf("BlockDeviceMappings = %+v", r.BlockDeviceMappings)
	}
	if r.InstanceMarketOptions == nil || r.InstanceMarketOptions.MarketType != types.MarketTypeSpot || r.InstanceMarketOptions.SpotOptions.SpotInstanceType != types.SpotInstanceTypeOneTime {
		t.Errorf("InstanceMarketOptions = %+v, want a one-time spot request", r.InstanceMarketOptions)
	}
	tags := map[types.ResourceType]map[string]string{}
	for _, ts := range r.TagSpecifications {
		m := map[string]string{}
		for _, tg := range ts.Tags {
			m[*tg.Key] = *tg.Value
		}
		tags[ts.ResourceType] = m
	}
	for _, rt := range []types.ResourceType{types.ResourceTypeInstance, types.ResourceTypeVolume} {
		if tags[rt]["Name"] != "rig-train" || tags[rt]["rig:managed"] != "true" {
			t.Errorf("%s tags = %v", rt, tags[rt])
		}
	}
	if tags[types.ResourceTypeInstance]["rig:image"] != "ami-0abc" {
		t.Errorf("instance tags = %v, want rig:image", tags[types.ResourceTypeInstance])
	}

	// second create in the same zone reuses the group, no new create
	e.createdGroup = nil
	e.groups = []types.SecurityGroup{{GroupId: sdk.String("sg-0new"), GroupName: sdk.String("rig")}}
	if _, err := d.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "g5.xlarge", Image: "ami-0abc", Name: "rig-b", Location: "ap-southeast-2a", SSHKeyIDs: []string{"k"}}); err != nil {
		t.Fatal(err)
	}
	if e.createdGroup != nil {
		t.Error("security group created again although it exists")
	}
	if e.runIn.InstanceMarketOptions != nil || len(e.runIn.BlockDeviceMappings) != 0 {
		t.Errorf("on-demand, default root size: market %+v mappings %+v", e.runIn.InstanceMarketOptions, e.runIn.BlockDeviceMappings)
	}
}

func TestCreateInstanceAttachesExistingVolumesOnceRunning(t *testing.T) {
	vpcs, subnets := defaultNetwork()
	running := ec2Instance("i-0new", "rig-train", types.InstanceStateNameRunning, "13.210.1.2")
	running.BlockDeviceMappings = running.BlockDeviceMappings[:1] // root only
	e := &fakeEC2{vpcs: vpcs, subnets: subnets, instances: []types.Instance{running}}
	d := newTestDriver(t, e, &fakeSSM{}, &fakePricing{})

	in, err := d.CreateInstance(t.Context(), provider.CreateInstanceRequest{
		Type: "g5.xlarge", Image: "ami-0abc", Name: "rig-train", Location: "ap-southeast-2a",
		SSHKeyIDs: []string{"k"}, ExistingVolumes: []string{"vol-0data", "vol-0more"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if in.Status != provider.Running || in.IP != "13.210.1.2" {
		t.Errorf("with volumes to attach the driver waits for running: %+v", in)
	}
	if len(e.attached) != 2 || *e.attached[0].VolumeId != "vol-0data" || *e.attached[0].Device != "/dev/sdf" || *e.attached[1].Device != "/dev/sdg" || *e.attached[1].InstanceId != "i-0new" {
		t.Errorf("AttachVolume calls = %+v", e.attached)
	}
}

func TestCreateInstanceNoCapacityIsTheSentinel(t *testing.T) {
	vpcs, subnets := defaultNetwork()
	for _, code := range []string{"InsufficientInstanceCapacity", "Unsupported"} {
		e := &fakeEC2{vpcs: vpcs, subnets: subnets, runErr: &smithy.GenericAPIError{Code: code, Message: "We currently do not have sufficient g5.xlarge capacity in the Availability Zone you requested (ap-southeast-2a)."}}
		d := newTestDriver(t, e, &fakeSSM{}, &fakePricing{})
		_, err := d.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "g5.xlarge", Image: "ami-0abc", Name: "rig-train", Location: "ap-southeast-2a", SSHKeyIDs: []string{"k"}})
		if !errors.Is(err, provider.ErrNoCapacity) {
			t.Errorf("%s: err = %v, want ErrNoCapacity", code, err)
		}
	}
}

func TestCreateInstanceQuotaErrorNamesTheQuotaCode(t *testing.T) {
	vpcs, subnets := defaultNetwork()
	cases := []struct{ typ, code, want string }{
		{"g5.xlarge", "VcpuLimitExceeded", "L-DB2E81BA"},
		{"g6e.12xlarge", "VcpuLimitExceeded", "L-DB2E81BA"},
		{"p4d.24xlarge", "VcpuLimitExceeded", "L-417A185B"},
		{"g5.xlarge", "MaxSpotInstanceCountExceeded", "L-3819A6DF"},
		{"p5.48xlarge", "MaxSpotInstanceCountExceeded", "L-7212CCBC"},
	}
	for _, c := range cases {
		e := &fakeEC2{vpcs: vpcs, subnets: subnets, runErr: &smithy.GenericAPIError{Code: c.code, Message: "You have requested more vCPU capacity than your current vCPU limit of 0 allows"}}
		d := newTestDriver(t, e, &fakeSSM{}, &fakePricing{})
		_, err := d.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: c.typ, Image: "ami-0abc", Name: "rig-train", Location: "ap-southeast-2a", SSHKeyIDs: []string{"k"}})
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), c.typ) {
			t.Errorf("%s %s: err = %v, want it to name %s and the type", c.typ, c.code, err, c.want)
		}
		if errors.Is(err, provider.ErrNoCapacity) {
			t.Errorf("%s: a quota error must abort the launch, not be retried as no capacity", c.code)
		}
	}
}

func TestCreateInstanceNeedsADefaultVPCAndSubnet(t *testing.T) {
	d := newTestDriver(t, &fakeEC2{}, &fakeSSM{}, &fakePricing{})
	_, err := d.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "g5.xlarge", Image: "ami-0abc", Name: "rig-train", Location: "ap-southeast-2a", SSHKeyIDs: []string{"k"}})
	if err == nil || !strings.Contains(err.Error(), "default VPC") {
		t.Errorf("err = %v, want it to name the missing default VPC", err)
	}
	vpcs, _ := defaultNetwork()
	d = newTestDriver(t, &fakeEC2{vpcs: vpcs}, &fakeSSM{}, &fakePricing{})
	_, err = d.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "g5.xlarge", Image: "ami-0abc", Name: "rig-train", Location: "ap-southeast-2a", SSHKeyIDs: []string{"k"}})
	if err == nil || !strings.Contains(err.Error(), "subnet") || !strings.Contains(err.Error(), "ap-southeast-2a") {
		t.Errorf("err = %v, want it to name the zone without a subnet", err)
	}
}

func TestDeleteInstanceTerminatesAndDeletesOnlyDataVolumes(t *testing.T) {
	e := &fakeEC2{
		instances: []types.Instance{ec2Instance("i-0run", "rig-train", types.InstanceStateNameRunning, "13.210.1.2")},
		volumes: []types.Volume{
			{VolumeId: sdk.String("vol-0data"), State: types.VolumeStateAvailable},
		},
	}
	d := newTestDriver(t, e, &fakeSSM{}, &fakePricing{})
	if err := d.DeleteInstance(t.Context(), "i-0run", []string{"vol-0root", "vol-0data"}); err != nil {
		t.Fatal(err)
	}
	if len(e.terminated) != 1 || e.terminated[0] != "i-0run" {
		t.Errorf("terminated = %v", e.terminated)
	}
	if len(e.deletedVols) != 1 || e.deletedVols[0] != "vol-0data" {
		t.Errorf("deleted volumes = %v, want only the data volume (the root goes with the instance)", e.deletedVols)
	}

	// keep-volumes down: nothing but the root requested, nothing deleted
	e.deletedVols = nil
	if err := d.DeleteInstance(t.Context(), "i-0run", []string{"vol-0root"}); err != nil {
		t.Fatal(err)
	}
	if len(e.deletedVols) != 0 {
		t.Errorf("deleted volumes = %v, want none", e.deletedVols)
	}

	if err := d.DeleteInstance(t.Context(), "i-0nope", nil); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("deleting a missing instance: %v, want a not-found error", err)
	}
}

func TestStopStartMapToEC2(t *testing.T) {
	e := &fakeEC2{}
	d := newTestDriver(t, e, &fakeSSM{}, &fakePricing{})
	if err := d.ShutdownInstance(t.Context(), "i-0run"); err != nil {
		t.Fatal(err)
	}
	if err := d.StartInstance(t.Context(), "i-0run"); err != nil {
		t.Fatal(err)
	}
	if len(e.stopped) != 1 || e.stopped[0] != "i-0run" || len(e.started) != 1 || e.started[0] != "i-0run" {
		t.Errorf("stopped %v started %v", e.stopped, e.started)
	}
	if _, _, err := d.Balance(t.Context()); !errors.Is(err, provider.ErrUnsupported) {
		t.Errorf("Balance err = %v, want ErrUnsupported", err)
	}
	caps := d.Capabilities()
	if !caps.Spot || !caps.Volumes || caps.Balance || caps.SelfStop != "" {
		t.Errorf("Capabilities = %+v", caps)
	}
	if d.Name() != "aws" {
		t.Errorf("Name = %q", d.Name())
	}
}

func TestListVolumesSynthesisesMountCommands(t *testing.T) {
	e := &fakeEC2{volumes: []types.Volume{{
		VolumeId:         sdk.String("vol-0abc"),
		Size:             sdk.Int32(100),
		VolumeType:       types.VolumeTypeGp3,
		State:            types.VolumeStateInUse,
		AvailabilityZone: sdk.String("ap-southeast-2a"),
		Tags:             []types.Tag{tag("Name", "data"), tag("rig:managed", "true")},
		Attachments:      []types.VolumeAttachment{{InstanceId: sdk.String("i-0run"), State: types.VolumeAttachmentStateAttached}},
	}}}
	d := newTestDriver(t, e, &fakeSSM{}, &fakePricing{})
	vols, err := d.ListVolumes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 1 {
		t.Fatalf("got %d volumes", len(vols))
	}
	v := vols[0]
	if v.ID != "vol-0abc" || v.Name != "data" || v.SizeGB != 100 || v.Type != "gp3" || v.Status != "in-use" || v.Location != "ap-southeast-2a" || len(v.InstanceIDs) != 1 || v.InstanceIDs[0] != "i-0run" {
		t.Errorf("volume = %+v", v)
	}
	if v.MkdirCommand != "sudo mkdir -p /mnt/data" {
		t.Errorf("MkdirCommand = %q", v.MkdirCommand)
	}
	for _, want := range []string{"sudo bash -c '", "lsblk -dno PATH,SERIAL", "s=vol0abc", "blkid", "mkfs.ext4", "mountpoint -q /mnt/data", "mount \"$dev\" /mnt/data"} {
		if !strings.Contains(v.MountCommand, want) {
			t.Errorf("MountCommand lacks %q:\n%s", want, v.MountCommand)
		}
	}
	if strings.Contains(v.MountCommand, "s=vol-0abc") {
		t.Errorf("the serial must be the volume id without its dash:\n%s", v.MountCommand)
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		if out, err := exec.Command(bash, "-n", "-c", v.MountCommand).CombinedOutput(); err != nil {
			t.Errorf("MountCommand does not parse: %v\n%s", err, out)
		}
	}

	got, err := d.GetVolume(t.Context(), "vol-0abc")
	if err != nil || got.Name != "data" {
		t.Errorf("GetVolume = %+v, %v", got, err)
	}
}

func TestCreateVolumeIsTaggedGp3InTheZone(t *testing.T) {
	e := &fakeEC2{}
	d := newTestDriver(t, e, &fakeSSM{}, &fakePricing{})
	id, err := d.CreateVolume(t.Context(), provider.CreateVolumeRequest{Name: "data", SizeGB: 100, Location: "ap-southeast-2b"})
	if err != nil || id != "vol-0new" {
		t.Fatalf("CreateVolume = %q, %v", id, err)
	}
	c := e.createdVol
	if *c.AvailabilityZone != "ap-southeast-2b" || *c.Size != 100 || c.VolumeType != types.VolumeTypeGp3 {
		t.Errorf("CreateVolume input = %+v", c)
	}
	got := map[string]string{}
	for _, tg := range c.TagSpecifications[0].Tags {
		got[*tg.Key] = *tg.Value
	}
	if c.TagSpecifications[0].ResourceType != types.ResourceTypeVolume || got["Name"] != "data" || got["rig:managed"] != "true" {
		t.Errorf("volume tags = %v", got)
	}

	if err := d.DetachVolume(t.Context(), "vol-0new", "i-0run"); err != nil {
		t.Fatal(err)
	}
	if len(e.detached) != 1 || *e.detached[0].VolumeId != "vol-0new" || *e.detached[0].InstanceId != "i-0run" {
		t.Errorf("DetachVolume = %+v", e.detached)
	}
	if err := d.DeleteVolume(t.Context(), "vol-0new"); err != nil || len(e.deletedVols) != 1 {
		t.Errorf("DeleteVolume: %v, deleted %v", err, e.deletedVols)
	}
}

func TestAttachVolumePicksTheNextFreeDevice(t *testing.T) {
	e := &fakeEC2{instances: []types.Instance{ec2Instance("i-0run", "rig-train", types.InstanceStateNameRunning, "13.210.1.2")}} // has /dev/sda1 and /dev/sdf
	d := newTestDriver(t, e, &fakeSSM{}, &fakePricing{})
	if err := d.AttachVolume(t.Context(), "vol-0more", "i-0run"); err != nil {
		t.Fatal(err)
	}
	if len(e.attached) != 1 || *e.attached[0].Device != "/dev/sdg" {
		t.Errorf("AttachVolume = %+v, want /dev/sdg (sdf is taken)", e.attached)
	}
}
