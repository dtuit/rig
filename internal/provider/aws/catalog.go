package aws

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/dtuit/rig/internal/provider"
)

// Locations are the region's availability zones that are currently
// available; code and name are both the zone name (ap-southeast-2a).
func (d *Driver) Locations(ctx context.Context) ([]provider.Location, error) {
	if err := d.clients(ctx); err != nil {
		return nil, err
	}
	out, err := d.ec2.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{
		Filters: []types.Filter{filter("state", string(types.AvailabilityZoneStateAvailable))},
	})
	if err != nil {
		return nil, d.fail("listing availability zones", err)
	}
	locs := make([]provider.Location, 0, len(out.AvailabilityZones))
	for _, z := range out.AvailabilityZones {
		if z.State != types.AvailabilityZoneStateAvailable || z.ZoneName == nil {
			continue
		}
		locs = append(locs, provider.Location{Code: *z.ZoneName, Name: *z.ZoneName})
	}
	sort.Slice(locs, func(i, j int) bool { return locs[i].Code < locs[j].Code })
	return locs, nil
}

// InstanceTypes lists the configured families with GPU shape from
// DescribeInstanceTypes, the Linux on-demand price from the pricing cache
// and the current spot price (lowest zone). Prices are decoration: when
// either lookup fails the type is listed at 0.
func (d *Driver) InstanceTypes(ctx context.Context) ([]provider.InstanceType, error) {
	if err := d.clients(ctx); err != nil {
		return nil, err
	}
	patterns := make([]string, 0, len(d.cfg.Families))
	for _, f := range d.cfg.Families {
		patterns = append(patterns, f+".*")
	}
	pag := ec2.NewDescribeInstanceTypesPaginator(d.ec2, &ec2.DescribeInstanceTypesInput{
		Filters: []types.Filter{filter("instance-type", patterns...)},
	})
	var infos []types.InstanceTypeInfo
	for pag.HasMorePages() {
		page, err := pag.NextPage(ctx)
		if err != nil {
			return nil, d.fail("listing instance types", err)
		}
		for _, it := range page.InstanceTypes {
			if d.isFamily(string(it.InstanceType)) {
				infos = append(infos, it)
			}
		}
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].InstanceType < infos[j].InstanceType })

	names := make([]string, 0, len(infos))
	for _, it := range infos {
		names = append(names, string(it.InstanceType))
	}
	prices := d.onDemandPrices(ctx, names)
	spot := d.spotPrices(ctx, names)

	out := make([]provider.InstanceType, 0, len(infos))
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, it := range infos {
		name := string(it.InstanceType)
		t := provider.InstanceType{
			Type:         name,
			PricePerHour: prices[name],
			SpotPrice:    minSpot(spot[name]),
		}
		if g := it.GpuInfo; g != nil {
			if len(g.Gpus) > 0 {
				t.GPU = strings.TrimSpace(deref(g.Gpus[0].Manufacturer) + " " + deref(g.Gpus[0].Name))
			}
			for _, gpu := range g.Gpus {
				t.GPUCount += int(sdk.ToInt32(gpu.Count))
			}
			t.GPUMemoryGB = int(sdk.ToInt32(g.TotalGpuMemoryInMiB) / 1024)
		}
		if it.MemoryInfo != nil {
			t.MemoryGB = int(sdk.ToInt64(it.MemoryInfo.SizeInMiB) / 1024)
		}
		if it.VCpuInfo != nil {
			t.CPUCount = int(sdk.ToInt32(it.VCpuInfo.DefaultVCpus))
		}
		d.gpuByType[name] = t.GPU
		out = append(out, t)
	}
	return out, nil
}

// Availability is zone -> instance types offered there. Offered is not
// guaranteed: EC2 only knows about capacity at RunInstances, which is why
// CreateInstance wraps ErrNoCapacity for core's retry. Spot and on-demand
// offerings are the same list.
func (d *Driver) Availability(ctx context.Context, _ bool) (map[string][]string, error) {
	if err := d.clients(ctx); err != nil {
		return nil, err
	}
	pag := ec2.NewDescribeInstanceTypeOfferingsPaginator(d.ec2, &ec2.DescribeInstanceTypeOfferingsInput{
		LocationType: types.LocationTypeAvailabilityZone,
	})
	out := map[string][]string{}
	for pag.HasMorePages() {
		page, err := pag.NextPage(ctx)
		if err != nil {
			return nil, d.fail("listing instance type offerings", err)
		}
		for _, o := range page.InstanceTypeOfferings {
			zone := deref(o.Location)
			if zone == "" {
				continue
			}
			out[zone] = append(out[zone], string(o.InstanceType))
		}
	}
	for _, ts := range out {
		sort.Strings(ts)
	}
	return out, nil
}

// Images is the one default image for the type: default_image when it is
// an AMI id, else the SSM public parameter — default_image when it is a
// path, the Deep Learning Base GPU AMI for GPU types, plain Ubuntu 24.04
// otherwise — resolved to today's AMI id. Any ami-… given as the image at
// create time is used as-is.
func (d *Driver) Images(ctx context.Context, instanceType string) ([]provider.Image, error) {
	if err := d.clients(ctx); err != nil {
		return nil, err
	}
	src := d.cfg.DefaultImage
	if strings.HasPrefix(src, "ami-") {
		return []provider.Image{{ID: src, Name: src, Display: src, Default: true}}, nil
	}
	if src == "" {
		src = ubuntuParameter
		if d.isGPUType(instanceType) {
			src = dlamiParameter
		}
	}
	out, err := d.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: &src})
	if err != nil {
		return nil, d.fail(fmt.Sprintf("resolving image parameter %s", src), err)
	}
	ami := ""
	if out.Parameter != nil {
		ami = deref(out.Parameter.Value)
	}
	if ami == "" {
		return nil, fmt.Errorf("aws: image parameter %s is empty", src)
	}
	return []provider.Image{{ID: ami, Name: ami, Display: src, Default: true}}, nil
}

// spotEntry is the current spot price of one type per zone, and when it
// was fetched; listings poll every few seconds and need not ask EC2 each
// time.
type spotEntry struct {
	at    time.Time
	zones map[string]float64
}

const spotCacheTTL = 5 * time.Minute

// spotPrices answers type -> zone -> current Linux spot price for names,
// asking EC2 only for types not fetched in the last few minutes. A failed
// query leaves those types absent (price 0); pricing never fails a listing.
func (d *Driver) spotPrices(ctx context.Context, names []string) map[string]map[string]float64 {
	out := map[string]map[string]float64{}
	var ask []types.InstanceType
	d.mu.Lock()
	for _, n := range names {
		if e, ok := d.spot[n]; ok && d.now().Sub(e.at) < spotCacheTTL {
			out[n] = e.zones
		} else {
			ask = append(ask, types.InstanceType(n))
		}
	}
	d.mu.Unlock()
	if len(ask) == 0 {
		return out
	}

	fetched := map[string]map[string]float64{}
	latest := map[string]map[string]time.Time{}
	pag := ec2.NewDescribeSpotPriceHistoryPaginator(d.ec2, &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes:       ask,
		ProductDescriptions: []string{"Linux/UNIX"},
		StartTime:           sdk.Time(d.now()),
	})
	for pag.HasMorePages() {
		page, err := pag.NextPage(ctx)
		if err != nil {
			return out
		}
		for _, sp := range page.SpotPriceHistory {
			typ, zone := string(sp.InstanceType), deref(sp.AvailabilityZone)
			price, err := strconv.ParseFloat(deref(sp.SpotPrice), 64)
			if err != nil || zone == "" || sp.Timestamp == nil {
				continue
			}
			if prev, ok := latest[typ][zone]; ok && !sp.Timestamp.After(prev) {
				continue
			}
			if fetched[typ] == nil {
				fetched[typ] = map[string]float64{}
				latest[typ] = map[string]time.Time{}
			}
			fetched[typ][zone] = price
			latest[typ][zone] = *sp.Timestamp
		}
	}
	d.mu.Lock()
	for _, t := range ask {
		zones := fetched[string(t)]
		if zones == nil {
			zones = map[string]float64{}
		}
		d.spot[string(t)] = spotEntry{at: d.now(), zones: zones}
		out[string(t)] = zones
	}
	d.mu.Unlock()
	return out
}

// minSpot is the lowest zone price, 0 when none is known.
func minSpot(zones map[string]float64) float64 {
	min := 0.0
	for _, p := range zones {
		if min == 0 || p < min {
			min = p
		}
	}
	return min
}
