// Package aws implements provider.Provider against Amazon EC2 in one region
// through aws-sdk-go-v2. Instance types are EC2's native names (g6e.xlarge),
// locations are the region's availability zones, data volumes are EBS
// volumes that the driver's synthesised mount command finds by serial, and
// the default image is the Deep Learning Base GPU AMI resolved through SSM.
// Credentials come from the SDK default chain (environment, shared config,
// SSO) plus an optional profile. AWS has no balance API, so Balance is
// unsupported; capacity is only known at RunInstances time, so
// CreateInstance wraps ErrNoCapacity for core's candidate walk.
package aws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/provider"
)

// Config is the providers.aws block of the config file.
type Config struct {
	// Profile is the shared-config profile; empty uses the SDK default
	// (AWS_PROFILE is honoured).
	Profile string `yaml:"profile"`
	// Region is the one region this provider block drives. Required unless
	// the SDK chain supplies one (AWS_REGION, the profile's region).
	Region string `yaml:"region"`
	// SecurityGroup is the name of the group opened for ssh, created in the
	// default VPC on first use. Default "rig".
	SecurityGroup string `yaml:"security_group"`
	// DefaultImage is an SSM parameter path (starts with /) or an AMI id
	// (ami-…). Empty picks the Deep Learning Base GPU AMI for GPU types and
	// plain Ubuntu 24.04 otherwise.
	DefaultImage string `yaml:"default_image"`
	// VolumeType is the EBS type of data volumes. Default gp3.
	VolumeType string `yaml:"volume_type"`
	// Families are the instance families InstanceTypes lists. Default: the
	// NVIDIA GPU families.
	Families []string `yaml:"families"`
}

const (
	defaultSecurityGroup = "rig"
	defaultVolumeType    = "gp3"
	// managedTag marks every instance, volume, key and group rig creates;
	// ListInstances and ListVolumes filter on it.
	managedTag = "rig:managed"
	// sshUser is the login user of both default AMIs.
	sshUser = "ubuntu"

	// dlamiParameter is the public SSM parameter of the Deep Learning Base
	// OSS Nvidia Driver GPU AMI (Ubuntu 24.04); ubuntuParameter the
	// canonical plain Ubuntu 24.04 server AMI. Both resolve to the current
	// AMI id in the client's region.
	dlamiParameter  = "/aws/service/deeplearning/ami/x86_64/base-oss-nvidia-driver-gpu-ubuntu-24.04/latest/ami-id"
	ubuntuParameter = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"
)

var defaultFamilies = []string{"g4dn", "g5", "g6", "g6e", "gr6", "p3", "p4d", "p5", "p5e", "p5en"}

func init() {
	provider.Register("aws", func(cfg config.ProviderConfig) (provider.Provider, error) {
		var own Config
		if err := cfg.Decode(&own); err != nil {
			return nil, err
		}
		return New(own), nil
	})
}

// ec2API is the slice of the EC2 client the driver uses; tests inject a
// fake. The method shapes match the SDK's so its paginators accept it.
type ec2API interface {
	DescribeAvailabilityZones(context.Context, *ec2.DescribeAvailabilityZonesInput, ...func(*ec2.Options)) (*ec2.DescribeAvailabilityZonesOutput, error)
	DescribeInstanceTypes(context.Context, *ec2.DescribeInstanceTypesInput, ...func(*ec2.Options)) (*ec2.DescribeInstanceTypesOutput, error)
	DescribeInstanceTypeOfferings(context.Context, *ec2.DescribeInstanceTypeOfferingsInput, ...func(*ec2.Options)) (*ec2.DescribeInstanceTypeOfferingsOutput, error)
	DescribeSpotPriceHistory(context.Context, *ec2.DescribeSpotPriceHistoryInput, ...func(*ec2.Options)) (*ec2.DescribeSpotPriceHistoryOutput, error)
	DescribeKeyPairs(context.Context, *ec2.DescribeKeyPairsInput, ...func(*ec2.Options)) (*ec2.DescribeKeyPairsOutput, error)
	ImportKeyPair(context.Context, *ec2.ImportKeyPairInput, ...func(*ec2.Options)) (*ec2.ImportKeyPairOutput, error)
	DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	CreateSecurityGroup(context.Context, *ec2.CreateSecurityGroupInput, ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error)
	AuthorizeSecurityGroupIngress(context.Context, *ec2.AuthorizeSecurityGroupIngressInput, ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupIngressOutput, error)
	RunInstances(context.Context, *ec2.RunInstancesInput, ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	TerminateInstances(context.Context, *ec2.TerminateInstancesInput, ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
	StopInstances(context.Context, *ec2.StopInstancesInput, ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error)
	StartInstances(context.Context, *ec2.StartInstancesInput, ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error)
	CreateVolume(context.Context, *ec2.CreateVolumeInput, ...func(*ec2.Options)) (*ec2.CreateVolumeOutput, error)
	DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
	AttachVolume(context.Context, *ec2.AttachVolumeInput, ...func(*ec2.Options)) (*ec2.AttachVolumeOutput, error)
	DetachVolume(context.Context, *ec2.DetachVolumeInput, ...func(*ec2.Options)) (*ec2.DetachVolumeOutput, error)
	DeleteVolume(context.Context, *ec2.DeleteVolumeInput, ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error)
}

// ssmAPI resolves public AMI parameters.
type ssmAPI interface {
	GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// pricingAPI is the Price List query behind the on-demand price cache.
type pricingAPI interface {
	GetProducts(context.Context, *pricing.GetProductsInput, ...func(*pricing.Options)) (*pricing.GetProductsOutput, error)
}

// Driver is the AWS provider for one region. Clients are built from the
// SDK chain on the first call so that constructing the driver never touches
// credentials.
type Driver struct {
	cfg Config

	loadMu  sync.Mutex
	ec2     ec2API
	ssm     ssmAPI
	pricing pricingAPI
	region  string // resolved region, cfg.Region until the SDK chain fills it

	cacheDir string        // pricing cache directory; empty = do not persist
	poll     time.Duration // between polls while waiting on EC2 state
	now      func() time.Time

	mu          sync.Mutex
	gpuByType   map[string]string  // filled by InstanceTypes, read by listings
	network     map[string]network // zone -> subnet and group, per process
	prices      *priceCache
	pricingDown bool // the Price List API failed once; stop asking
	spot        map[string]spotEntry
}

// New returns a driver for cfg with defaults applied. Nothing is contacted.
func New(cfg Config) *Driver {
	if cfg.SecurityGroup == "" {
		cfg.SecurityGroup = defaultSecurityGroup
	}
	if cfg.VolumeType == "" {
		cfg.VolumeType = defaultVolumeType
	}
	if len(cfg.Families) == 0 {
		cfg.Families = defaultFamilies
	}
	dir := ""
	if base, err := os.UserCacheDir(); err == nil {
		dir = filepath.Join(base, "rig")
	}
	return &Driver{
		cfg:       cfg,
		region:    cfg.Region,
		cacheDir:  dir,
		poll:      10 * time.Second,
		now:       time.Now,
		gpuByType: map[string]string{},
		network:   map[string]network{},
		spot:      map[string]spotEntry{},
	}
}

func (d *Driver) Name() string { return "aws" }

func (d *Driver) Capabilities() provider.Capabilities {
	return provider.Capabilities{Spot: true, Volumes: true, Balance: false}
}

// Balance is unsupported: AWS exposes no account balance, only billing
// reports after the fact.
func (d *Driver) Balance(context.Context) (float64, string, error) {
	return 0, "", fmt.Errorf("aws has no balance API: %w", provider.ErrUnsupported)
}

// clients builds the SDK clients on first use. The Price List client is
// pinned to us-east-1, one of the few regions that host that API; prices
// are still queried for the configured region.
func (d *Driver) clients(ctx context.Context) error {
	d.loadMu.Lock()
	defer d.loadMu.Unlock()
	if d.ec2 != nil && d.ssm != nil && d.pricing != nil {
		return nil
	}
	var opts []func(*awsconfig.LoadOptions) error
	if d.cfg.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(d.cfg.Profile))
	}
	if d.cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(d.cfg.Region))
	}
	ac, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return d.fail("loading AWS configuration", err)
	}
	if ac.Region == "" {
		return errors.New("aws: no region configured; set providers.aws.region (or AWS_REGION)")
	}
	d.region = ac.Region
	if d.ec2 == nil {
		d.ec2 = ec2.NewFromConfig(ac)
	}
	if d.ssm == nil {
		d.ssm = ssm.NewFromConfig(ac)
	}
	if d.pricing == nil {
		d.pricing = pricing.NewFromConfig(ac, func(o *pricing.Options) { o.Region = pricingRegion })
	}
	return nil
}

// fail wraps an SDK error for the CLI user. An expired SSO session, which
// the SDK reports at the first API call rather than when the configuration
// loads, is replaced by the one line that fixes it.
func (d *Driver) fail(what string, err error) error {
	if isSSOExpired(err) {
		return fmt.Errorf("aws: credentials expired; run: aws sso login --profile %s", d.profile())
	}
	return fmt.Errorf("aws: %s: %w", what, err)
}

func isSSOExpired(err error) bool {
	s := strings.ToLower(err.Error())
	for _, needle := range []string{"sso session has expired", "ssoproviderinvalidtoken", "token has expired", "invalidgrantexception"} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// profile is the name the login hint should carry.
func (d *Driver) profile() string {
	if d.cfg.Profile != "" {
		return d.cfg.Profile
	}
	if p := os.Getenv("AWS_PROFILE"); p != "" {
		return p
	}
	return "default"
}

// errCode is the EC2 error code (InsufficientInstanceCapacity, …) or "".
func errCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

// errMessage is the service's own message, or the whole error.
func errMessage(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorMessage()
	}
	return err.Error()
}

// family is the part of an instance type before the dot: g5 of g5.xlarge.
func family(instanceType string) string {
	fam, _, _ := strings.Cut(instanceType, ".")
	return fam
}

func (d *Driver) isFamily(instanceType string) bool {
	fam := family(instanceType)
	for _, f := range d.cfg.Families {
		if f == fam {
			return true
		}
	}
	return false
}

// isGPUType decides which default image a type gets: what
// DescribeInstanceTypes said when InstanceTypes ran, else a configured
// family, else EC2's naming — accelerated families start with g or p.
func (d *Driver) isGPUType(instanceType string) bool {
	d.mu.Lock()
	gpu, known := d.gpuByType[instanceType]
	d.mu.Unlock()
	if known {
		return gpu != ""
	}
	if d.isFamily(instanceType) {
		return true
	}
	fam := family(instanceType)
	return strings.HasPrefix(fam, "g") || strings.HasPrefix(fam, "p")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func filter(name string, values ...string) types.Filter {
	return types.Filter{Name: &name, Values: values}
}

func tag(key, value string) types.Tag {
	return types.Tag{Key: &key, Value: &value}
}

func tagValue(tags []types.Tag, key string) string {
	for _, t := range tags {
		if deref(t.Key) == key {
			return deref(t.Value)
		}
	}
	return ""
}

// managedTags are the tags every rig-created resource carries.
func managedTags(name string) []types.Tag {
	return []types.Tag{tag("Name", name), tag(managedTag, "true")}
}
