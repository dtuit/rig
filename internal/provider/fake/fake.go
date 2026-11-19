// Package fake is an in-memory provider.Provider that tests script: what is
// available where, how each CreateInstance call turns out, and which
// statuses an instance reports over successive GetInstance calls. It also
// records what core asked of it. Nothing here talks to a cloud.
package fake

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/dtuit/rig/internal/provider"
)

// Provider is scripted by setting its exported fields before use. Zero
// values mean "everything works": every create succeeds, every instance is
// running with an IP on its first GetInstance.
type Provider struct {
	// Caps is what Capabilities reports; New sets every feature on.
	Caps provider.Capabilities
	// Avail is location -> instance types, returned by Availability for
	// either spot mode.
	Avail map[string][]string
	// Volumes is what ListVolumes returns; CreateVolume appends to it.
	Volumes []provider.Volume
	// Creates is consumed one per CreateInstance call, in order: a nil entry
	// succeeds, any other is returned as the call's error. Past the end of
	// the list every create succeeds.
	Creates []error
	// Statuses scripts GetInstance per instance ID ("fake-1", "fake-2", ...
	// in create order): each call pops the next status, the last one
	// sticks. An unscripted instance reports Running.
	Statuses map[string][]provider.Status
	// SSH overrides the endpoint an instance reports, by ID — a container
	// whose host-mapped port moved on restart. Unscripted instances report
	// their IP on port 22 as root, like a VM.
	SSH map[string]provider.Endpoint
	// ListErr is returned by ListInstances instead of a listing: the cloud
	// that cannot be asked at all (bad credentials, an outage), which a
	// caller surveying several has to survive.
	ListErr error
	// Bal is what Balance reports, in Cur; the zero value is 42 USD.
	Bal float64
	Cur string

	// Requests records every CreateInstanceRequest received, in order.
	Requests []provider.CreateInstanceRequest
	// Deleted records every DeleteInstance call, in order.
	Deleted []Deletion

	name      string
	instances map[string]*provider.Instance
	created   int
	volumes   int
}

// Deletion is one DeleteInstance call as the fake saw it.
type Deletion struct {
	ID        string
	VolumeIDs []string
}

// New returns a fake named "fake" with every capability on and nothing
// scripted.
func New() *Provider { return Named("fake") }

// Named is New with the provider name Name reports — two fakes with
// different names stand in for two clouds sharing one workstation's state.
func Named(name string) *Provider {
	return &Provider{
		Caps:      provider.Capabilities{Spot: true, Volumes: true, Balance: true},
		Statuses:  map[string][]provider.Status{},
		SSH:       map[string]provider.Endpoint{},
		name:      name,
		instances: map[string]*provider.Instance{},
	}
}

func (p *Provider) Name() string                        { return p.name }
func (p *Provider) Capabilities() provider.Capabilities { return p.Caps }

func (p *Provider) Locations(context.Context) ([]provider.Location, error) {
	codes := make([]string, 0, len(p.Avail))
	for code := range p.Avail {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	out := make([]provider.Location, 0, len(codes))
	for _, code := range codes {
		out = append(out, provider.Location{Code: code, Name: code})
	}
	return out, nil
}

func (p *Provider) InstanceTypes(context.Context) ([]provider.InstanceType, error) {
	var names []string
	for _, types := range p.Avail {
		for _, t := range types {
			if !slices.Contains(names, t) {
				names = append(names, t)
			}
		}
	}
	sort.Strings(names)
	out := make([]provider.InstanceType, 0, len(names))
	for _, n := range names {
		out = append(out, provider.InstanceType{Type: n, PricePerHour: 1})
	}
	return out, nil
}

func (p *Provider) Availability(context.Context, bool) (map[string][]string, error) {
	return p.Avail, nil
}

func (p *Provider) Images(_ context.Context, instanceType string) ([]provider.Image, error) {
	return []provider.Image{{ID: "img-1", Name: "fake-image", Display: "Fake image for " + instanceType, Default: true}}, nil
}

func (p *Provider) ListInstances(context.Context) ([]provider.Instance, error) {
	if p.ListErr != nil {
		return nil, p.ListErr
	}
	ids := make([]string, 0, len(p.instances))
	for id := range p.instances {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]provider.Instance, 0, len(ids))
	for _, id := range ids {
		out = append(out, p.view(p.instances[id]))
	}
	return out, nil
}

// view is the instance as the cloud would report it now: its scripted
// endpoint override applied.
func (p *Provider) view(in *provider.Instance) provider.Instance {
	copy := *in
	if ep, ok := p.SSH[in.ID]; ok {
		copy.SSH = ep
	}
	return copy
}

func (p *Provider) GetInstance(_ context.Context, id string) (*provider.Instance, error) {
	in, ok := p.instances[id]
	if !ok {
		return nil, fmt.Errorf("instance %s not found", id)
	}
	if seq := p.Statuses[id]; len(seq) > 0 {
		in.Status = seq[0]
		if len(seq) > 1 {
			p.Statuses[id] = seq[1:]
		}
	} else {
		in.Status = provider.Running
	}
	in.RawStatus = string(in.Status)
	copy := p.view(in)
	return &copy, nil
}

func (p *Provider) CreateInstance(_ context.Context, req provider.CreateInstanceRequest) (*provider.Instance, error) {
	p.Requests = append(p.Requests, req)
	call := len(p.Requests) - 1
	if call < len(p.Creates) && p.Creates[call] != nil {
		return nil, p.Creates[call]
	}
	p.created++
	in := &provider.Instance{
		ID:           fmt.Sprintf("fake-%d", p.created),
		Name:         req.Name,
		Type:         req.Type,
		Status:       provider.Provisioning,
		RawStatus:    string(provider.Provisioning),
		IP:           fmt.Sprintf("10.0.0.%d", p.created),
		SSH:          provider.Endpoint{Host: fmt.Sprintf("10.0.0.%d", p.created), Port: 22, User: "root"},
		Location:     req.Location,
		Spot:         req.Spot,
		PricePerHour: 1,
		VolumeIDs:    append([]string(nil), req.ExistingVolumes...),
		OSVolumeID:   fmt.Sprintf("os-%d", p.created),
		Image:        req.Image,
	}
	if p.instances == nil {
		p.instances = map[string]*provider.Instance{}
	}
	p.instances[in.ID] = in
	copy := *in
	return &copy, nil
}

// Put places in into the fake as an instance the cloud already holds —
// the one a listing finds that the test did not create through
// CreateInstance, with the age, price and location the test needs it to
// have.
func (p *Provider) Put(in provider.Instance) {
	if p.instances == nil {
		p.instances = map[string]*provider.Instance{}
	}
	p.instances[in.ID] = &in
	p.Statuses[in.ID] = []provider.Status{in.Status}
}

func (p *Provider) DeleteInstance(_ context.Context, id string, deleteVolumeIDs []string) error {
	p.Deleted = append(p.Deleted, Deletion{ID: id, VolumeIDs: deleteVolumeIDs})
	if _, ok := p.instances[id]; !ok {
		return fmt.Errorf("instance %s not found", id)
	}
	delete(p.instances, id)
	return nil
}

func (p *Provider) ShutdownInstance(_ context.Context, id string) error {
	return p.setStatus(id, provider.Stopped)
}

func (p *Provider) StartInstance(_ context.Context, id string) error {
	return p.setStatus(id, provider.Running)
}

// SetStatus makes id report s from now on, in listings as well as gets —
// for a status no rig command produces, such as an instance the cloud has
// already discarded (provider.Gone).
func (p *Provider) SetStatus(id string, s provider.Status) error { return p.setStatus(id, s) }

func (p *Provider) setStatus(id string, s provider.Status) error {
	in, ok := p.instances[id]
	if !ok {
		return fmt.Errorf("instance %s not found", id)
	}
	in.Status, in.RawStatus = s, string(s)
	p.Statuses[id] = []provider.Status{s}
	return nil
}

func (p *Provider) ListVolumes(context.Context) ([]provider.Volume, error) {
	return p.Volumes, nil
}

func (p *Provider) GetVolume(_ context.Context, id string) (*provider.Volume, error) {
	for i := range p.Volumes {
		if p.Volumes[i].ID == id {
			v := p.Volumes[i]
			return &v, nil
		}
	}
	return nil, fmt.Errorf("volume %s not found", id)
}

func (p *Provider) CreateVolume(_ context.Context, req provider.CreateVolumeRequest) (string, error) {
	p.volumes++
	id := fmt.Sprintf("vol-%d", p.volumes)
	p.Volumes = append(p.Volumes, provider.Volume{
		ID: id, Name: req.Name, SizeGB: req.SizeGB, Type: req.Type, Location: req.Location, Status: "attached",
	})
	return id, nil
}

func (p *Provider) AttachVolume(_ context.Context, volumeID, instanceID string) error {
	for i := range p.Volumes {
		if p.Volumes[i].ID == volumeID {
			p.Volumes[i].InstanceIDs = append(p.Volumes[i].InstanceIDs, instanceID)
			return nil
		}
	}
	return fmt.Errorf("volume %s not found", volumeID)
}

func (p *Provider) DetachVolume(_ context.Context, volumeID, instanceID string) error {
	for i := range p.Volumes {
		if p.Volumes[i].ID == volumeID {
			p.Volumes[i].InstanceIDs = slices.DeleteFunc(p.Volumes[i].InstanceIDs, func(id string) bool { return id == instanceID })
			return nil
		}
	}
	return fmt.Errorf("volume %s not found", volumeID)
}

func (p *Provider) DeleteVolume(_ context.Context, volumeID string) error {
	n := len(p.Volumes)
	p.Volumes = slices.DeleteFunc(p.Volumes, func(v provider.Volume) bool { return v.ID == volumeID })
	if len(p.Volumes) == n {
		return fmt.Errorf("volume %s not found", volumeID)
	}
	return nil
}

func (p *Provider) EnsureSSHKey(context.Context, string, string) (string, error) {
	return "key-1", nil
}

func (p *Provider) Balance(context.Context) (float64, string, error) {
	if !p.Caps.Balance {
		return 0, "", fmt.Errorf("fake has no balance: %w", provider.ErrUnsupported)
	}
	if p.Bal != 0 || p.Cur != "" {
		return p.Bal, p.Cur, nil
	}
	return 42, "USD", nil
}
