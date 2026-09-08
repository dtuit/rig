package vast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dtuit/rig/internal/provider"
)

// labelPrefix marks the instances rig manages: core names every instance
// `rig-<name>` (core.HostPrefix) and the driver stores that as Vast's
// label, so ListInstances can tell them from the account's other rentals.
const labelPrefix = "rig-"

// instance is a rental as `GET /instances/` returns it; only what the driver
// reads is decoded.
type instance struct {
	ID             int64           `json:"id"`
	Label          string          `json:"label"`
	ActualStatus   *string         `json:"actual_status"`
	IntendedStatus string          `json:"intended_status"`
	StatusMsg      string          `json:"status_msg"`
	ImageUUID      string          `json:"image_uuid"`
	GPUName        string          `json:"gpu_name"`
	NumGPUs        int             `json:"num_gpus"`
	DPHTotal       float64         `json:"dph_total"`
	IsBid          bool            `json:"is_bid"`
	Geolocation    string          `json:"geolocation"`
	PublicIP       string          `json:"public_ipaddr"`
	SSHHost        string          `json:"ssh_host"`
	SSHPort        jsonID          `json:"ssh_port"`
	Ports          json.RawMessage `json:"ports"`
	StartDate      float64         `json:"start_date"`
}

// jsonID is a number or a string in the API's JSON — Vast is inconsistent —
// read either way.
type jsonID string

func (j *jsonID) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*j = ""
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*j = jsonID(v)
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*j = jsonID(strconv.FormatInt(int64(f), 10))
	return nil
}

func (j jsonID) String() string { return string(j) }

func (j jsonID) Int() int {
	n, _ := strconv.Atoi(string(j))
	return n
}

// directPort is the host port mapped onto the container's sshd, or 0 when
// the host exposes none (proxy only, or not running yet: `ports` is only
// present on running instances).
func (in *instance) directPort() int {
	if len(in.Ports) == 0 {
		return 0
	}
	var ports map[string][]struct {
		HostPort jsonID `json:"HostPort"`
	}
	if err := json.Unmarshal(in.Ports, &ports); err != nil {
		return 0
	}
	if b := ports["22/tcp"]; len(b) > 0 {
		return b[0].HostPort.Int()
	}
	return 0
}

// fromAPI maps a Vast rental onto provider.Instance.
func fromAPI(in *instance) provider.Instance {
	raw := ""
	if in.ActualStatus != nil {
		raw = *in.ActualStatus
	}
	ip := in.PublicIP
	if ip == "" {
		ip = in.SSHHost
	}
	ep := provider.Endpoint{Host: in.SSHHost, Port: in.SSHPort.Int(), User: "root"}
	if p := in.directPort(); p > 0 && in.PublicIP != "" {
		ep = provider.Endpoint{Host: in.PublicIP, Port: p, User: "root"}
	}
	var created time.Time
	if in.StartDate > 0 {
		sec, frac := math.Modf(in.StartDate)
		created = time.Unix(int64(sec), int64(frac*1e9)).UTC()
	}
	return provider.Instance{
		ID:           strconv.FormatInt(in.ID, 10),
		Name:         in.Label,
		Type:         token(in.NumGPUs, in.GPUName),
		Status:       fromAPIStatus(raw, in.IntendedStatus),
		RawStatus:    raw,
		IP:           ip,
		SSH:          ep,
		Location:     in.Geolocation,
		Spot:         in.IsBid,
		PricePerHour: in.DPHTotal,
		CreatedAt:    created,
		GPU:          in.GPUName,
		Image:        in.ImageUUID,
	}
}

// fromAPIStatus maps Vast's actual_status (with intended_status as the
// tie-breaker) onto provider.Status. Core, the cache and the CLI compare
// Status against the constants, so a word this driver does not know becomes
// Error — the safe, visible answer — rather than a value nothing matches;
// the word itself is kept in Instance.RawStatus for display and debugging.
func fromAPIStatus(actual, intended string) provider.Status {
	switch actual {
	case "running":
		if intended == "stopped" {
			return provider.Stopped
		}
		return provider.Running
	case "loading", "created", "":
		return provider.Provisioning
	case "exited":
		return provider.Stopped
	case "offline", "unknown":
		return provider.Error
	}
	// A word this driver has never seen: Error, so the instance is still
	// visible to every core comparison.
	return provider.Error
}

var owner = url.Values{"owner": {"me"}}

func (d *Driver) ListInstances(ctx context.Context) ([]provider.Instance, error) {
	var res struct {
		Instances []instance `json:"instances"`
	}
	if err := d.c.do(ctx, "GET", "/instances/", owner, nil, &res); err != nil {
		return nil, err
	}
	out := make([]provider.Instance, 0, len(res.Instances))
	for i := range res.Instances {
		if !strings.HasPrefix(res.Instances[i].Label, labelPrefix) {
			continue
		}
		out = append(out, fromAPI(&res.Instances[i]))
	}
	return out, nil
}

// GetInstance reports a rental Vast no longer knows (`{"instances": null}`
// or 404) as Gone rather than an error, so a launch that lost its instance
// stops cleanly.
func (d *Driver) GetInstance(ctx context.Context, id string) (*provider.Instance, error) {
	var res struct {
		Instances json.RawMessage `json:"instances"`
	}
	err := d.c.do(ctx, "GET", "/instances/"+id+"/", owner, nil, &res)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == 404 {
		return gone(id), nil
	}
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(string(res.Instances))
	switch {
	case body == "" || body == "null":
		return gone(id), nil
	case strings.HasPrefix(body, "["): // some endpoints answer with a one-element list
		var list []instance
		if err := json.Unmarshal(res.Instances, &list); err != nil {
			return nil, fmt.Errorf("vast: GET /instances/%s/: decoding response: %w", id, err)
		}
		if len(list) == 0 {
			return gone(id), nil
		}
		v := fromAPI(&list[0])
		return &v, nil
	}
	var in instance
	if err := json.Unmarshal(res.Instances, &in); err != nil {
		return nil, fmt.Errorf("vast: GET /instances/%s/: decoding response: %w", id, err)
	}
	v := fromAPI(&in)
	return &v, nil
}

func gone(id string) *provider.Instance {
	return &provider.Instance{ID: id, Status: provider.Gone, RawStatus: "absent"}
}

// offerGone matches Vast's ways of saying an offer was rented or withdrawn
// between the search and the rental attempt.
var offerGone = regexp.MustCompile(`(?i)no_such_ask|not found|no longer|unavailable|not available|already rented|been rented|not rentable|taken`)

// billingProblem matches the answers no other offer will fix.
var billingProblem = regexp.MustCompile(`(?i)credit|balance|payment|billing|fund`)

// CreateInstance rents the cheapest offer matching the token in the
// requested geolocation, moving to the next when one turns out to be
// taken; when every offer is gone it reports ErrNoCapacity so core tries
// the next candidate. The rental is labelled with the hostname, runs sshd
// on a direct port when the host has one and through the proxy otherwise,
// and carries rig's onstart hook. A bid is min_bid × bid_markup.
func (d *Driver) CreateInstance(ctx context.Context, req provider.CreateInstanceRequest) (*provider.Instance, error) {
	n, gpu, err := d.parseToken(req.Type)
	if err != nil {
		return nil, err
	}
	typ := onDemand
	if req.Spot {
		typ = bid
	}
	clauses := map[string]any{
		"gpu_name": map[string]any{"eq": gpu},
		"num_gpus": map[string]any{"eq": n},
	}
	if req.Location != "" {
		clauses["geolocation"] = map[string]any{"eq": req.Location}
	}
	offers, err := d.search(ctx, d.query(typ, clauses))
	if err != nil {
		return nil, err
	}
	where := req.Location
	if where == "" {
		where = "any location"
	}
	if len(offers) == 0 {
		return nil, fmt.Errorf("no %s offers for %s in %s: %w", typ, req.Type, where, provider.ErrNoCapacity)
	}
	image := req.Image
	if image == "" {
		image = d.cfg.Image
	}
	disk := req.OSVolumeGB
	if disk == 0 {
		disk = d.cfg.DiskGB
	}
	var last error
	for _, o := range offers {
		body := map[string]any{
			"client_id": "me",
			"image":     image,
			"disk":      disk,
			"label":     req.Name,
			// "ssh_direc" is how Vast's own CLI spells it (abbreviated
			// upstream, not a typo here): direct ssh with the proxy as the
			// fallback. Do not "correct" it.
			"runtype":        "ssh_direc ssh_proxy",
			"env":            map[string]string{"-p 22:22": "1"},
			"onstart":        onstart,
			"cancel_unavail": true,
		}
		price := o.DPHTotal
		if req.Spot {
			price = o.MinBid * d.cfg.BidMarkup
			body["price"] = price
		}
		var res struct {
			Success     bool   `json:"success"`
			NewContract int64  `json:"new_contract"`
			Error       string `json:"error"`
			Msg         string `json:"msg"`
		}
		err := d.c.do(ctx, "PUT", fmt.Sprintf("/asks/%d/", o.ID), nil, body, &res)
		if err != nil {
			var ae *apiError
			if errors.As(err, &ae) && ae.Status != 401 && ae.Status != 402 && ae.Status != 403 &&
				(ae.Status == 404 || offerGone.MatchString(ae.text())) {
				last = err
				continue
			}
			return nil, fmt.Errorf("renting offer %d (%s in %s): %w", o.ID, req.Type, o.Geolocation, err)
		}
		if !res.Success {
			text := strings.TrimSpace(res.Error + " " + res.Msg)
			if text == "" {
				text = "no reason given"
			}
			last = fmt.Errorf("offer %d: %s", o.ID, text)
			if billingProblem.MatchString(text) {
				return nil, fmt.Errorf("renting offer %d (%s in %s): %s", o.ID, req.Type, o.Geolocation, text)
			}
			continue
		}
		return &provider.Instance{
			ID:           strconv.FormatInt(res.NewContract, 10),
			Name:         req.Name,
			Type:         token(n, gpu),
			Status:       provider.Provisioning,
			RawStatus:    "created",
			SSH:          provider.Endpoint{User: "root"},
			Location:     o.Geolocation,
			Spot:         req.Spot,
			PricePerHour: price,
			GPU:          gpu,
			Image:        image,
		}, nil
	}
	return nil, fmt.Errorf("all %d %s offers for %s in %s were taken (last: %v): %w",
		len(offers), typ, req.Type, where, last, provider.ErrNoCapacity)
}

// setState is `PUT /instances/{id}/ {"state": ...}` — Vast's stop and start.
func (d *Driver) setState(ctx context.Context, id, state string) error {
	var res struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Msg     string `json:"msg"`
	}
	body := map[string]any{"client_id": "me", "state": state}
	if err := d.c.do(ctx, "PUT", "/instances/"+id+"/", nil, body, &res); err != nil {
		return err
	}
	if !res.Success && (res.Error != "" || res.Msg != "") {
		return fmt.Errorf("vast: setting instance %s to %s: %s", id, state, strings.TrimSpace(res.Error+" "+res.Msg))
	}
	return nil
}

// ShutdownInstance stops the container; Vast keeps billing its storage and
// does not reserve the GPU.
func (d *Driver) ShutdownInstance(ctx context.Context, id string) error {
	return d.setState(ctx, id, "stopped")
}

func (d *Driver) StartInstance(ctx context.Context, id string) error {
	return d.setState(ctx, id, "running")
}

// DeleteInstance destroys the rental. Vast has no volumes for rig to
// manage, so deleteVolumeIDs is ignored.
func (d *Driver) DeleteInstance(ctx context.Context, id string, _ []string) error {
	return d.c.do(ctx, "DELETE", "/instances/"+id+"/", nil, nil, nil)
}

// sshKey is one account-level key from `GET /ssh/`.
type sshKey struct {
	ID        jsonID `json:"id"`
	PublicKey string `json:"public_key"`
	SSHKey    string `json:"ssh_key"` // older spelling of the same field
}

// sshKeys lists the account's keys, whether the API wraps them in
// {"ssh_keys": [...]} or answers with the bare list.
func (d *Driver) sshKeys(ctx context.Context) ([]sshKey, error) {
	var raw json.RawMessage
	if err := d.c.do(ctx, "GET", "/ssh/", nil, nil, &raw); err != nil {
		return nil, fmt.Errorf("listing ssh keys: %w", err)
	}
	body := strings.TrimSpace(string(raw))
	var keys []sshKey
	if strings.HasPrefix(body, "[") {
		if err := json.Unmarshal(raw, &keys); err != nil {
			return nil, fmt.Errorf("vast: GET /ssh/: decoding response: %w", err)
		}
	} else if body != "" && body != "null" {
		var wrapped struct {
			Keys []sshKey `json:"ssh_keys"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, fmt.Errorf("vast: GET /ssh/: decoding response: %w", err)
		}
		keys = wrapped.Keys
	}
	for i := range keys {
		if keys[i].PublicKey == "" {
			keys[i].PublicKey = keys[i].SSHKey
		}
	}
	return keys, nil
}

// trimKey normalises a public key for comparison: surrounding space gone,
// the comment kept (two keys differing only in comment are different keys
// to Vast too).
func trimKey(k string) string { return strings.TrimSpace(k) }
