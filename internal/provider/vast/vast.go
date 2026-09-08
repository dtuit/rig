// Package vast implements provider.Provider against the Vast.ai marketplace
// (REST v0). Vast has no fixed instance types: hosts publish offers, and the
// driver reads them as a catalogue — one `<n>x<GPU>` token per GPU shape
// (`1xRTX-4090`, `1xH100-SXM`), cheapest price first, grouped by the host's
// geolocation — and at up time rents the cheapest matching offer, moving to
// the next when one is taken. Instances are Docker containers reached over
// direct ssh when the host has open ports and over Vast's ssh proxy
// otherwise; the footprint stops them with the instance-scoped
// CONTAINER_API_KEY the platform injects (ADR-0003).
package vast

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/provider"
)

// envNames are the credential variables Vast needs.
var envNames = []string{"VAST_API_KEY"}

// DefaultImage is the Docker image rented when neither the profile nor the
// provider block names one. The `-auto` tags of vastai/pytorch pick the
// torch build that matches the host's driver (tag list checked 2026-09-08).
const DefaultImage = "vastai/pytorch:cuda-12.8.1-auto"

// Where a Vast container keeps the credentials that stop it. Vast injects
// CONTAINER_ID and the instance-scoped CONTAINER_API_KEY into the
// container's environment, so PID 1 — the platform's own launcher — always
// has the pair; /root/.vast_api_key and /root/.vast_containerlabel
// ("C.<id>") are Vast's own copies, verified byte-identical to the injected
// key on instance 50300289 (live 2026-09-08).
const (
	procEnviron        = "/proc/1/environ"
	containerKeyFile   = "/root/.vast_api_key"
	containerLabelFile = "/root/.vast_containerlabel"
)

// selfStop is the watchdog's stop line (ADR-0003: the instance-scoped key,
// never the account's). An ssh session inherits neither variable — and
// /etc/environment, which the docs point at, holds only the image's
// build-time env on a Vast container and cannot even be sourced safely
// (`NVIDIA_REQUIRE_CUDA=cuda>=12.8 brand=unknown` redirects) — so the pair
// is read out of PID 1's environment instead.
var selfStop = renderSelfStop(procEnviron, containerKeyFile, containerLabelFile)

// renderSelfStop builds the stop line reading the container's credentials
// from environ, falling back to Vast's own keyFile and labelFile when PID 1
// is not readable. The paths are arguments so the test can run the real
// line against fixtures.
func renderSelfStop(environ, keyFile, labelFile string) string {
	return `e=$(cat ` + environ + ` 2>/dev/null | tr '\0' '\n'); ` +
		`cid=$(printf '%s\n' "$e" | sed -n 's/^CONTAINER_ID=//p' | head -1); ` +
		`key=$(printf '%s\n' "$e" | sed -n 's/^CONTAINER_API_KEY=//p' | head -1); ` +
		`[ -n "$cid" ] || cid=$(sed -n 's/^C\.//p' ` + labelFile + ` 2>/dev/null); ` +
		`[ -n "$key" ] || key=$(cat ` + keyFile + ` 2>/dev/null); ` +
		`curl -fsS -X PUT "https://console.vast.ai/api/v0/instances/$cid/" ` +
		`-H "Authorization: Bearer $key" -H 'Content-Type: application/json' -d '{"state":"stopped"}'`
}

// onstart is the container start hook rig hands every instance: Vast's ssh
// mode drops logins into tmux by default, which would wrap `rig ssh -- cmd`,
// so it is switched off, and the footprint's re-arm hook runs when present
// (see remote.RenderOnstart for the contract).
const onstart = "touch ~/.no_auto_tmux; [ -f ~/.rig/onstart.sh ] && bash ~/.rig/onstart.sh"

// catalogueTTL is how long one process reuses a marketplace crawl: long
// enough for `rig types` (InstanceTypes then Availability) to search once,
// short enough that offers are never trusted across commands.
const catalogueTTL = 30 * time.Second

// pageSize is the most offers one search returns whatever limit is asked
// for (measured 2026-09-08: 512), so the catalogue crawl pages by price.
const pageSize = 512

func init() {
	provider.Register("vast", func(cfg config.ProviderConfig) (provider.Provider, error) {
		creds, err := cfg.Creds(envNames)
		if err != nil {
			return nil, err
		}
		var own Config
		if err := cfg.Decode(&own); err != nil {
			return nil, err
		}
		return New(creds["VAST_API_KEY"], own), nil
	})
}

// Config is the driver's own block under providers.vast. Zero values take
// the defaults noted per field.
type Config struct {
	MinReliability float64        `yaml:"min_reliability"` // host reliability floor, 0..1 (0.95)
	BidMarkup      float64        `yaml:"bid_markup"`      // spot bid = min_bid × markup (1.2)
	DirectOnly     bool           `yaml:"direct_only"`     // only hosts with open ports, so ssh is direct (false)
	Image          string         `yaml:"image"`           // Docker image to rent (DefaultImage)
	DiskGB         int            `yaml:"disk_gb"`         // container disk in GB (20)
	Filters        map[string]any `yaml:"filters"`         // extra offer-query clauses, merged verbatim (cuda_max_good, inet_down, …)
}

func (c Config) withDefaults() Config {
	if c.MinReliability == 0 {
		c.MinReliability = 0.95
	}
	if c.BidMarkup == 0 {
		c.BidMarkup = 1.2
	}
	if c.Image == "" {
		c.Image = DefaultImage
	}
	if c.DiskGB == 0 {
		c.DiskGB = 20
	}
	return c
}

// Driver is the Vast provider. Build one with New.
type Driver struct {
	c        *client
	cfg      Config
	pageSize int

	mu    sync.Mutex
	names map[string]string // lower-cased dashed GPU name → gpu_name as Vast spells it
	cat   *catalogue
}

// New returns a driver for apiKey talking to Vast's public API.
func New(apiKey string, cfg Config) *Driver {
	return &Driver{
		c: &client{
			base:      DefaultBaseURL,
			key:       apiKey,
			http:      &http.Client{Timeout: 60 * time.Second},
			retryWait: 500 * time.Millisecond,
		},
		cfg:      cfg.withDefaults(),
		pageSize: pageSize,
		names:    map[string]string{},
	}
}

func (d *Driver) Name() string { return "vast" }

func (d *Driver) Capabilities() provider.Capabilities {
	return provider.Capabilities{Spot: true, Volumes: false, Balance: true, DockerImages: true, SelfStop: selfStop}
}

// Locations are the distinct geolocation strings hosts currently advertise,
// as Vast spells them ("Sweden, SE").
func (d *Driver) Locations(ctx context.Context) ([]provider.Location, error) {
	cat, err := d.catalogue(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, o := range append(append([]offer(nil), cat.onDemand...), cat.bid...) {
		seen[o.Geolocation] = true
	}
	codes := make([]string, 0, len(seen))
	for g := range seen {
		codes = append(codes, g)
	}
	sort.Strings(codes)
	out := make([]provider.Location, 0, len(codes))
	for _, g := range codes {
		out = append(out, provider.Location{Code: g, Name: g})
	}
	return out, nil
}

// InstanceTypes reads the marketplace as a catalogue: one row per GPU shape
// with the cheapest on-demand price, the cheapest bid as SpotPrice, and the
// offer count in Description, cheapest first. A shape only the bid market
// carries is priced by its cheapest bid and says "(bid only)".
func (d *Driver) InstanceTypes(ctx context.Context) ([]provider.InstanceType, error) {
	cat, err := d.catalogue(ctx)
	if err != nil {
		return nil, err
	}
	return cat.types(), nil
}

// Availability groups the offers by geolocation: the tokens rentable there
// now, on-demand or by bid.
func (d *Driver) Availability(ctx context.Context, spot bool) (map[string][]string, error) {
	cat, err := d.catalogue(ctx)
	if err != nil {
		return nil, err
	}
	offers := cat.onDemand
	if spot {
		offers = cat.bid
	}
	out := map[string][]string{}
	for _, o := range offers {
		tok := token(o.NumGPUs, o.GPUName)
		if !contains(out[o.Geolocation], tok) {
			out[o.Geolocation] = append(out[o.Geolocation], tok)
		}
	}
	for g := range out {
		sort.Strings(out[g])
	}
	return out, nil
}

// Images lists the configured image (the default) and the driver's built-in
// one when the config overrides it; CreateInstance accepts any Docker image
// reference.
func (d *Driver) Images(context.Context, string) ([]provider.Image, error) {
	out := []provider.Image{{ID: d.cfg.Image, Name: d.cfg.Image, Display: d.cfg.Image, Default: true}}
	if d.cfg.Image != DefaultImage {
		out = append(out, provider.Image{ID: DefaultImage, Name: DefaultImage, Display: DefaultImage})
	}
	return out, nil
}

// unsupportedVolumes is every volume method's answer.
func unsupportedVolumes() error {
	return fmt.Errorf("Vast volumes are machine-bound, so rig cannot attach one: copy data with rig sync/rig fetch, or use Vast's cloud sync: %w", provider.ErrUnsupported)
}

func (d *Driver) ListVolumes(context.Context) ([]provider.Volume, error) {
	return nil, unsupportedVolumes()
}

func (d *Driver) GetVolume(context.Context, string) (*provider.Volume, error) {
	return nil, unsupportedVolumes()
}

func (d *Driver) CreateVolume(context.Context, provider.CreateVolumeRequest) (string, error) {
	return "", unsupportedVolumes()
}

func (d *Driver) AttachVolume(context.Context, string, string) error { return unsupportedVolumes() }
func (d *Driver) DetachVolume(context.Context, string, string) error { return unsupportedVolumes() }
func (d *Driver) DeleteVolume(context.Context, string) error         { return unsupportedVolumes() }

// EnsureSSHKey registers pubkey at account level when it is not there yet;
// Vast copies account keys into every new instance.
func (d *Driver) EnsureSSHKey(ctx context.Context, _ string, pubkey string) (string, error) {
	pubkey = trimKey(pubkey)
	keys, err := d.sshKeys(ctx)
	if err != nil {
		return "", err
	}
	for _, k := range keys {
		if trimKey(k.PublicKey) == pubkey {
			return k.ID.String(), nil
		}
	}
	var res struct {
		ID     jsonID `json:"id"`
		SSHKey struct {
			ID jsonID `json:"id"`
		} `json:"ssh_key"`
	}
	if err := d.c.do(ctx, "POST", "/ssh/", nil, map[string]any{"ssh_key": pubkey}, &res); err != nil {
		return "", fmt.Errorf("registering ssh key: %w", err)
	}
	if id := res.ID.String(); id != "" {
		return id, nil
	}
	if id := res.SSHKey.ID.String(); id != "" {
		return id, nil
	}
	// The create answer did not carry the id; the listing does.
	if keys, err = d.sshKeys(ctx); err == nil {
		for _, k := range keys {
			if trimKey(k.PublicKey) == pubkey {
				return k.ID.String(), nil
			}
		}
	}
	return "", nil
}

// Balance is the account's prepaid credit in USD — `credit`, not
// `balance`: on a funded renter account the two are $10.00 and 0 (live
// 2026-09-08), because `balance` is a host's unpaid earnings.
func (d *Driver) Balance(ctx context.Context) (float64, string, error) {
	var res struct {
		Credit float64 `json:"credit"`
	}
	if err := d.c.do(ctx, "GET", "/users/current/", nil, nil, &res); err != nil {
		return 0, "", err
	}
	return res.Credit, "USD", nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
