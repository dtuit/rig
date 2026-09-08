// Package runpod implements provider.Provider against the RunPod REST API v2
// (https://api.runpod.io/v2) with a hand-written net/http client. Pods are
// containers: sshd answers on a host-mapped port that changes on every
// restart, there is no systemd, and the watchdog stops the pod through the
// pod-scoped key RunPod injects (issue 07). GraphQL is used for exactly one
// query, the account balance, which v2 does not expose.
package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/provider"
)

const (
	// DefaultBaseURL is the REST v2 root.
	DefaultBaseURL = "https://api.runpod.io/v2"
	// DefaultGraphQLURL serves the one GraphQL query the driver makes.
	DefaultGraphQLURL = "https://api.runpod.io/graphql"
	// DefaultImage is the official PyTorch image rig launches when config
	// names none; tag verified live on 2026-09-08.
	DefaultImage = "runpod/pytorch:1.2.0-rc.162-cu1290-torch291-ubuntu2404"
	// DefaultDiskGB is the container disk when neither the profile's
	// os_volume_gb nor the block's disk_gb says otherwise.
	DefaultDiskGB = 20
	// MinVolumeGB is RunPod's floor for a network volume.
	MinVolumeGB = 10
	// MountPath is where a data volume appears inside the pod.
	MountPath = "/workspace"
	// AnyLocation is the location code for "let the scheduler choose".
	// Community-cloud stock is not tied to a datacenter, so its availability
	// is reported under this code and a create in it omits dataCenterIds.
	AnyLocation = "any"

	// SelfStop is the watchdog's deadline line, verified on a real pod
	// (issue 07): the pod-scoped key lives in /etc/rp_environment, not in an
	// ssh session's environment.
	SelfStop = `. /etc/rp_environment && curl -fsS -X POST "https://api.runpod.io/v2/pods/$RUNPOD_POD_ID/action" -H "Authorization: Bearer $RUNPOD_API_KEY" -H 'Content-Type: application/json' -d '{"action":"stop"}'`

	// onstartArgs replaces the official image's CMD (/start.sh) so the
	// footprint's onstart hook re-arms the watchdog after a restart. `args`
	// is "arguments passed to the container entrypoint" (v2 OpenAPI), and
	// the official entrypoint execs its arguments, so this is the documented
	// RunPod template pattern. Applied only to runpod/* images, whose
	// start script is known.
	onstartArgs = `bash -c '[ -f ~/.rig/onstart.sh ] && bash ~/.rig/onstart.sh; exec /start.sh'`

	hostPrefix = "rig-"
)

// envNames are the accepted spellings of the API key, in preference order:
// the documented name, then the one the team's shared secrets file uses.
var envNames = []string{"RUNPOD_API_KEY", "RUNPOD_KEY"}

func init() {
	provider.Register("runpod", func(cfg config.ProviderConfig) (provider.Provider, error) {
		var opts Options
		if err := cfg.Decode(&opts); err != nil {
			return nil, err
		}
		key, err := credential(cfg)
		if err != nil {
			return nil, err
		}
		return New(key, opts)
	})
}

// credential resolves the API key under either accepted name; the error is
// the first name's, so the user sees the documented spelling.
func credential(cfg config.ProviderConfig) (string, error) {
	var first error
	for _, n := range envNames {
		creds, err := cfg.Creds([]string{n})
		if err == nil {
			return creds[n], nil
		}
		if first == nil {
			first = err
		}
	}
	return "", fmt.Errorf("%w (RUNPOD_KEY is accepted too)", first)
}

// Options are the providers.runpod block's driver keys.
type Options struct {
	Cloud  string `yaml:"cloud"`   // secure (default) or community
	Image  string `yaml:"image"`   // default Docker image; DefaultImage when empty
	DiskGB int    `yaml:"disk_gb"` // container disk; DefaultDiskGB when 0
}

// Driver is the RunPod provider. Its URL fields are exported so tests point
// it at an httptest server.
type Driver struct {
	BaseURL    string
	GraphQLURL string
	// Log receives progress and warning lines; nil writes them rig-prefixed
	// to stderr.
	Log func(format string, a ...any)

	key    string
	cloud  string // SECURE or COMMUNITY, as the API spells it
	image  string
	diskGB int
	client *http.Client

	mu    sync.Mutex
	names map[string]string // gpu id -> catalog name, for rebuilding type tokens
}

// New builds a driver for the API key with the block's options applied.
func New(key string, opts Options) (*Driver, error) {
	cloud := strings.ToUpper(strings.TrimSpace(opts.Cloud))
	switch cloud {
	case "":
		cloud = "SECURE"
	case "SECURE", "COMMUNITY":
	default:
		return nil, fmt.Errorf("runpod: cloud must be secure or community, not %q", opts.Cloud)
	}
	image := opts.Image
	if image == "" {
		image = DefaultImage
	}
	disk := opts.DiskGB
	if disk <= 0 {
		disk = DefaultDiskGB
	}
	return &Driver{
		BaseURL:    DefaultBaseURL,
		GraphQLURL: DefaultGraphQLURL,
		key:        key,
		cloud:      cloud,
		image:      image,
		diskGB:     disk,
		client:     &http.Client{Timeout: 60 * time.Second},
		names:      map[string]string{},
	}, nil
}

func (d *Driver) Name() string { return "runpod" }

func (d *Driver) Capabilities() provider.Capabilities {
	return provider.Capabilities{Spot: false, Volumes: true, Balance: true, SelfStop: SelfStop}
}

func (d *Driver) logf(format string, a ...any) {
	if d.Log != nil {
		d.Log(format, a...)
		return
	}
	fmt.Fprintf(os.Stderr, "rig: "+format+"\n", a...)
}

// --- catalog ---------------------------------------------------------------

type gpuType struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Memory       int    `json:"memory"`
	Secure       bool   `json:"secure"`
	Community    bool   `json:"community"`
	Availability string `json:"availability"`
	Price        struct {
		Secure    float64 `json:"secure"`
		Community float64 `json:"community"`
	} `json:"price"`
	MaxCount struct {
		Secure    int `json:"secure"`
		Community int `json:"community"`
	} `json:"maxCount"`
	DataCenters []struct {
		ID           string `json:"id"`
		Availability string `json:"availability"`
	} `json:"dataCenters"`
}

// offered says whether this GPU is sold at all on the configured cloud.
func (d *Driver) offered(g gpuType) bool {
	return d.maxCount(g) > 0 && d.onCloud(g)
}

func (d *Driver) onCloud(g gpuType) bool {
	if d.cloud == "COMMUNITY" {
		return g.Community
	}
	return g.Secure
}

func (d *Driver) maxCount(g gpuType) int {
	if d.cloud == "COMMUNITY" {
		return g.MaxCount.Community
	}
	return g.MaxCount.Secure
}

func (d *Driver) price(g gpuType) float64 {
	if d.cloud == "COMMUNITY" {
		return g.Price.Community
	}
	return g.Price.Secure
}

// counts are the GPU counts rig offers as types, capped by the pod maximum.
func counts(max int) []int {
	var out []int
	for _, n := range []int{1, 2, 4, 8} {
		if n <= max {
			out = append(out, n)
		}
	}
	return out
}

// catalog fetches the GPU catalogue with live availability for the
// configured cloud and refreshes the id -> name map pods are rendered with.
func (d *Driver) catalog(ctx context.Context) ([]gpuType, error) {
	var resp struct {
		GPUs []gpuType `json:"gpus"`
	}
	path := "/catalog/gpus?include=AVAILABILITY&product=POD&count=1&cloud=" + d.cloud
	if _, err := d.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	d.mu.Lock()
	for _, g := range resp.GPUs {
		d.names[g.ID] = g.Name
	}
	d.mu.Unlock()
	return resp.GPUs, nil
}

// tokenName is a catalogue name as it appears in a type token: spaces
// become dashes so the token is one shell word.
func tokenName(name string) string { return strings.ReplaceAll(name, " ", "-") }

// token spells an instance type: <count>x<name>, e.g. 1xRTX-4090.
func token(count int, name string) string {
	return strconv.Itoa(count) + "x" + tokenName(name)
}

// parseToken splits 2xA100-SXM into (2, "A100-SXM").
func parseToken(tok string) (int, string, error) {
	i := strings.IndexByte(tok, 'x')
	if i <= 0 || i == len(tok)-1 {
		return 0, "", fmt.Errorf("runpod: instance type %q is not <count>x<gpu>, e.g. 1xRTX-4090", tok)
	}
	n, err := strconv.Atoi(tok[:i])
	if err != nil || n <= 0 {
		return 0, "", fmt.Errorf("runpod: instance type %q is not <count>x<gpu>, e.g. 1xRTX-4090", tok)
	}
	return n, tok[i+1:], nil
}

func (d *Driver) Locations(ctx context.Context) ([]provider.Location, error) {
	var resp struct {
		DataCenters []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"dataCenters"`
	}
	if _, err := d.do(ctx, http.MethodGet, "/catalog/datacenters", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]provider.Location, 0, len(resp.DataCenters)+1)
	for _, dc := range resp.DataCenters {
		out = append(out, provider.Location{Code: dc.ID, Name: dc.Name})
	}
	out = append(out, provider.Location{Code: AnyLocation, Name: "any datacenter (scheduler's choice)"})
	return out, nil
}

func (d *Driver) InstanceTypes(ctx context.Context) ([]provider.InstanceType, error) {
	gpus, err := d.catalog(ctx)
	if err != nil {
		return nil, err
	}
	var out []provider.InstanceType
	for _, g := range gpus {
		if !d.offered(g) {
			continue
		}
		for _, n := range counts(d.maxCount(g)) {
			out = append(out, provider.InstanceType{
				Type:         token(n, g.Name),
				GPU:          g.Name,
				GPUCount:     n,
				GPUMemoryGB:  g.Memory * n,
				PricePerHour: d.price(g) * float64(n),
				Description:  g.ID,
			})
		}
	}
	return out, nil
}

// Availability maps datacenter id -> type tokens with stock there now. Secure
// stock is per datacenter; community stock has no datacenter and is
// reported under AnyLocation. spot is ignored: RunPod v2 has no spot, and
// Capabilities refuses it before this is asked.
func (d *Driver) Availability(ctx context.Context, _ bool) (map[string][]string, error) {
	gpus, err := d.catalog(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, g := range gpus {
		if !d.offered(g) {
			continue
		}
		var locs []string
		for _, dc := range g.DataCenters {
			if dc.Availability != "NONE" {
				locs = append(locs, dc.ID)
			}
		}
		if len(g.DataCenters) == 0 && g.Availability != "NONE" {
			locs = []string{AnyLocation}
		}
		for _, loc := range locs {
			for _, n := range counts(d.maxCount(g)) {
				out[loc] = append(out[loc], token(n, g.Name))
			}
		}
	}
	for _, types := range out {
		sort.Strings(types)
	}
	return out, nil
}

// Images lists the driver default and, when it differs, the configured
// image as the default; any Docker image reference is accepted at create.
func (d *Driver) Images(context.Context, string) ([]provider.Image, error) {
	out := []provider.Image{{ID: d.image, Name: d.image, Display: d.image, Default: true}}
	if d.image != DefaultImage {
		out = append(out, provider.Image{ID: DefaultImage, Name: DefaultImage, Display: DefaultImage + " (rig default)"})
	}
	return out, nil
}

// --- pods ------------------------------------------------------------------

type sshEndpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type pod struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	Image        string  `json:"image"`
	Cost         float64 `json:"cost"`
	CreatedAt    string  `json:"createdAt"`
	DataCenterID string  `json:"dataCenterId"`
	GPU          *struct {
		ID    string `json:"id"`
		Count int    `json:"count"`
	} `json:"gpu"`
	Mounts *struct {
		Network []struct {
			VolumeID string `json:"volumeId"`
		} `json:"network"`
	} `json:"mounts"`
	Runtime *struct {
		Ports []struct {
			IP      *string `json:"ip"`
			Private int     `json:"private"`
			Public  *int    `json:"public"`
			Type    string  `json:"type"`
		} `json:"ports"`
	} `json:"runtime"`
	SSH *struct {
		Direct *sshEndpoint `json:"direct"`
	} `json:"ssh"`
}

// endpoint is where the pod's sshd is reachable: the published direct
// mapping when RunPod has resolved it, else the raw 22/tcp runtime port.
func (p *pod) endpoint() provider.Endpoint {
	ep := provider.Endpoint{User: "root"}
	if p.SSH != nil && p.SSH.Direct != nil && p.SSH.Direct.Host != "" && p.SSH.Direct.Port > 0 {
		ep.Host, ep.Port = p.SSH.Direct.Host, p.SSH.Direct.Port
		return ep
	}
	if p.Runtime != nil {
		for _, port := range p.Runtime.Ports {
			if port.Private == 22 && port.Type == "tcp" && port.IP != nil && port.Public != nil && *port.Public > 0 {
				ep.Host, ep.Port = *port.IP, *port.Public
				return ep
			}
		}
	}
	return ep
}

// status maps the v2 lifecycle words onto provider.Status. A RUNNING pod
// whose ssh port is not yet published is still provisioning as far as rig
// is concerned: nothing can connect to it.
func status(raw string, ep provider.Endpoint) provider.Status {
	switch raw {
	case "RUNNING":
		if ep.Host == "" {
			return provider.Provisioning
		}
		return provider.Running
	case "PROVISIONING", "STARTING":
		return provider.Provisioning
	case "EXITED":
		return provider.Stopped
	case "ERROR":
		return provider.Error
	case "TERMINATED":
		return provider.Gone
	}
	return provider.Status(strings.ToLower(raw))
}

// gpuName is the catalogue name for a gpu id, fetching the catalogue once
// when the driver has not seen it yet; the id itself when unknown.
func (d *Driver) gpuName(ctx context.Context, id string) string {
	d.mu.Lock()
	name, ok := d.names[id]
	empty := len(d.names) == 0
	d.mu.Unlock()
	if ok {
		return name
	}
	if empty {
		if _, err := d.catalog(ctx); err == nil {
			d.mu.Lock()
			name, ok = d.names[id]
			d.mu.Unlock()
			if ok {
				return name
			}
		}
	}
	return id
}

func (d *Driver) toInstance(ctx context.Context, p *pod) provider.Instance {
	ep := p.endpoint()
	in := provider.Instance{
		ID:           p.ID,
		Name:         p.Name,
		Status:       status(p.Status, ep),
		RawStatus:    p.Status,
		IP:           ep.Host,
		SSH:          ep,
		Location:     p.DataCenterID,
		PricePerHour: p.Cost,
		Image:        p.Image,
	}
	if t, err := time.Parse(time.RFC3339Nano, p.CreatedAt); err == nil {
		in.CreatedAt = t
	}
	if p.GPU != nil {
		in.GPU = p.GPU.ID
		in.Type = token(p.GPU.Count, d.gpuName(ctx, p.GPU.ID))
	}
	if p.Mounts != nil {
		for _, m := range p.Mounts.Network {
			in.VolumeIDs = append(in.VolumeIDs, m.VolumeID)
		}
	}
	return in
}

func (d *Driver) listPods(ctx context.Context) ([]pod, error) {
	var resp struct {
		Pods []pod `json:"pods"`
	}
	if _, err := d.do(ctx, http.MethodGet, "/pods", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Pods, nil
}

// ListInstances returns the account's rig- pods; other pods are not rig's.
func (d *Driver) ListInstances(ctx context.Context) ([]provider.Instance, error) {
	pods, err := d.listPods(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]provider.Instance, 0, len(pods))
	for i := range pods {
		if !strings.HasPrefix(pods[i].Name, hostPrefix) {
			continue
		}
		out = append(out, d.toInstance(ctx, &pods[i]))
	}
	return out, nil
}

// GetInstance reports a pod RunPod no longer knows as Gone rather than
// erroring: a terminated pod disappears from the API.
func (d *Driver) GetInstance(ctx context.Context, id string) (*provider.Instance, error) {
	var p pod
	if _, err := d.do(ctx, http.MethodGet, "/pods/"+id, nil, &p); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			return &provider.Instance{ID: id, Status: provider.Gone, RawStatus: "not found"}, nil
		}
		return nil, err
	}
	in := d.toInstance(ctx, &p)
	return &in, nil
}

func (d *Driver) CreateInstance(ctx context.Context, req provider.CreateInstanceRequest) (*provider.Instance, error) {
	if req.Spot {
		return nil, fmt.Errorf("runpod v2 has no spot pods: %w", provider.ErrUnsupported)
	}
	n, name, err := parseToken(req.Type)
	if err != nil {
		return nil, err
	}
	gpus, err := d.catalog(ctx)
	if err != nil {
		return nil, err
	}
	gpuID := ""
	for _, g := range gpus {
		if strings.EqualFold(tokenName(g.Name), name) {
			gpuID = g.ID
			break
		}
	}
	if gpuID == "" {
		return nil, fmt.Errorf("runpod: unknown GPU %q in instance type %s; see rig types", name, req.Type)
	}
	image := req.Image
	if image == "" {
		image = d.image
	}
	disk := req.OSVolumeGB
	if disk <= 0 {
		disk = d.diskGB
	}
	body := map[string]any{
		"name":     req.Name,
		"image":    image,
		"gpu":      map[string]any{"id": gpuID, "count": n},
		"cloud":    d.cloud,
		"disk":     disk,
		"ports":    []string{"22/tcp"},
		"startSsh": true,
		"env":      map[string]string{"PUBLIC_KEY": strings.TrimSpace(req.PublicKey)},
	}
	if req.Location != "" && !strings.EqualFold(req.Location, AnyLocation) {
		body["dataCenterIds"] = []string{req.Location}
	}
	if len(req.ExistingVolumes) > 0 {
		var mounts []map[string]string
		for _, id := range req.ExistingVolumes {
			mounts = append(mounts, map[string]string{"volumeId": id, "path": MountPath})
		}
		body["mounts"] = map[string]any{"network": mounts}
	}
	if strings.HasPrefix(image, "runpod/") {
		body["args"] = onstartArgs
	}
	var p pod
	if _, err := d.do(ctx, http.MethodPost, "/pods", body, &p); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.noCapacity() {
			return nil, fmt.Errorf("%s: %w", ae.Detail, provider.ErrNoCapacity)
		}
		return nil, err
	}
	in := d.toInstance(ctx, &p)
	return &in, nil
}

func (d *Driver) action(ctx context.Context, id, action string) error {
	_, err := d.do(ctx, http.MethodPost, "/pods/"+id+"/action", map[string]string{"action": action}, nil)
	return err
}

// DeleteInstance terminates the pod (a pod already gone counts as done) and
// then deletes the requested network volumes, retrying briefly while RunPod
// still sees them mounted on the terminating pod.
func (d *Driver) DeleteInstance(ctx context.Context, id string, deleteVolumeIDs []string) error {
	if err := d.action(ctx, id, "terminate"); err != nil {
		var ae *apiError
		if !errors.As(err, &ae) || ae.Status != http.StatusNotFound {
			return err
		}
	}
	for _, vol := range deleteVolumeIDs {
		var err error
		for attempt := 0; attempt < 5; attempt++ {
			if err = d.DeleteVolume(ctx, vol); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		if err != nil {
			return fmt.Errorf("deleting volume %s: %w", vol, err)
		}
	}
	return nil
}

func (d *Driver) ShutdownInstance(ctx context.Context, id string) error {
	return d.action(ctx, id, "stop")
}

func (d *Driver) StartInstance(ctx context.Context, id string) error {
	return d.action(ctx, id, "start")
}

// --- network volumes -------------------------------------------------------

type networkVolume struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Size       int    `json:"size"`
	DataCenter string `json:"dataCenter"`
	Type       string `json:"type"`
}

// mountedOn maps volume id -> ids of pods (not terminated) that mount it;
// the volume API itself does not say.
func (d *Driver) mountedOn(ctx context.Context) (map[string][]string, error) {
	pods, err := d.listPods(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, p := range pods {
		if p.Status == "TERMINATED" || p.Mounts == nil {
			continue
		}
		for _, m := range p.Mounts.Network {
			out[m.VolumeID] = append(out[m.VolumeID], p.ID)
		}
	}
	return out, nil
}

func toVolume(v networkVolume, pods []string) provider.Volume {
	st := "available"
	if len(pods) > 0 {
		st = "attached"
	}
	return provider.Volume{
		ID:          v.ID,
		Name:        v.Name,
		SizeGB:      v.Size,
		Type:        v.Type,
		Status:      st,
		Location:    v.DataCenter,
		InstanceIDs: pods,
	}
}

func (d *Driver) ListVolumes(ctx context.Context) ([]provider.Volume, error) {
	var resp struct {
		NetworkVolumes []networkVolume `json:"networkVolumes"`
	}
	if _, err := d.do(ctx, http.MethodGet, "/network-volumes", nil, &resp); err != nil {
		return nil, err
	}
	mounted, err := d.mountedOn(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]provider.Volume, 0, len(resp.NetworkVolumes))
	for _, v := range resp.NetworkVolumes {
		out = append(out, toVolume(v, mounted[v.ID]))
	}
	return out, nil
}

func (d *Driver) GetVolume(ctx context.Context, id string) (*provider.Volume, error) {
	var v networkVolume
	if _, err := d.do(ctx, http.MethodGet, "/network-volumes/"+id, nil, &v); err != nil {
		return nil, err
	}
	mounted, err := d.mountedOn(ctx)
	if err != nil {
		return nil, err
	}
	out := toVolume(v, mounted[v.ID])
	return &out, nil
}

// CreateVolume makes a network volume in the datacenter; sizes under
// RunPod's 10 GB floor are raised to it.
func (d *Driver) CreateVolume(ctx context.Context, req provider.CreateVolumeRequest) (string, error) {
	size := req.SizeGB
	if size < MinVolumeGB {
		d.logf("runpod network volumes are at least %dGB; creating %s at %dGB instead of %dGB", MinVolumeGB, req.Name, MinVolumeGB, size)
		size = MinVolumeGB
	}
	body := map[string]any{"name": req.Name, "size": size, "dataCenter": req.Location}
	if req.Type != "" {
		body["type"] = strings.ToUpper(req.Type)
	}
	var v networkVolume
	if _, err := d.do(ctx, http.MethodPost, "/network-volumes", body, &v); err != nil {
		return "", err
	}
	return v.ID, nil
}

func (d *Driver) AttachVolume(context.Context, string, string) error {
	return fmt.Errorf("runpod mounts network volumes only when a pod is created: %w", provider.ErrUnsupported)
}

func (d *Driver) DetachVolume(context.Context, string, string) error {
	return fmt.Errorf("runpod mounts network volumes only when a pod is created: %w", provider.ErrUnsupported)
}

// DeleteVolume removes the network volume; one already gone counts as done.
func (d *Driver) DeleteVolume(ctx context.Context, volumeID string) error {
	_, err := d.do(ctx, http.MethodDelete, "/network-volumes/"+volumeID, nil, nil)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// EnsureSSHKey registers nothing: the key rides in the pod's PUBLIC_KEY
// environment variable at create, so no account-side key is needed.
func (d *Driver) EnsureSSHKey(context.Context, string, string) (string, error) {
	return "", nil
}

// Balance asks GraphQL for myself.clientBalance, the one query v2 lacks;
// any failure is reported as unsupported so the dashboard hides the tile.
func (d *Driver) Balance(ctx context.Context) (float64, string, error) {
	var resp struct {
		Data struct {
			Myself struct {
				ClientBalance *float64 `json:"clientBalance"`
			} `json:"myself"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	err := d.post(ctx, d.GraphQLURL, "POST graphql", map[string]string{"query": "{ myself { clientBalance } }"}, &resp)
	switch {
	case err != nil:
		return 0, "", fmt.Errorf("runpod balance: %v: %w", err, provider.ErrUnsupported)
	case len(resp.Errors) > 0:
		return 0, "", fmt.Errorf("runpod balance: %s: %w", resp.Errors[0].Message, provider.ErrUnsupported)
	case resp.Data.Myself.ClientBalance == nil:
		return 0, "", fmt.Errorf("runpod balance: empty reply: %w", provider.ErrUnsupported)
	}
	return *resp.Data.Myself.ClientBalance, "USD", nil
}

// --- http ------------------------------------------------------------------

// apiError is a non-2xx reply, in RunPod's problem+json vocabulary.
type apiError struct {
	Method string
	Path   string
	Status int
	Title  string
	Detail string
}

func (e *apiError) Error() string {
	msg := e.Detail
	if msg == "" {
		msg = e.Title
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("runpod: %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, msg)
}

// noCapacity recognises the create-time "nothing matches right now" reply,
// verified 2026-09-08 as HTTP 400 with this detail.
func (e *apiError) noCapacity() bool {
	if e.Status != http.StatusBadRequest {
		return false
	}
	s := strings.ToLower(e.Detail)
	return strings.Contains(s, "no longer any instances available") || strings.Contains(s, "no available gpu")
}

// do sends a v2 request. body (when non-nil) is JSON-encoded; a 2xx reply
// is decoded into out when out is non-nil and the reply has a body. It
// returns the status code and an *apiError for anything not 2xx.
func (d *Driver) do(ctx context.Context, method, path string, body, out any) (int, error) {
	return d.send(ctx, method, d.BaseURL+path, method+" "+strings.SplitN(path, "?", 2)[0], body, out)
}

func (d *Driver) post(ctx context.Context, url, label string, body, out any) error {
	_, err := d.send(ctx, http.MethodPost, url, label, body, out)
	return err
}

func (d *Driver) send(ctx context.Context, method, url, label string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+d.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("runpod: %s: %w", label, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("runpod: %s: reading reply: %w", label, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		ae := &apiError{Status: resp.StatusCode}
		ae.Method, ae.Path, _ = strings.Cut(label, " ")
		var prob struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(data, &prob) == nil {
			ae.Title, ae.Detail = prob.Title, prob.Detail
		}
		if ae.Title == "" && ae.Detail == "" && len(data) > 0 && len(data) < 300 {
			ae.Detail = strings.TrimSpace(string(data))
		}
		return resp.StatusCode, ae
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("runpod: %s: decoding reply: %w", label, err)
		}
	}
	return resp.StatusCode, nil
}
