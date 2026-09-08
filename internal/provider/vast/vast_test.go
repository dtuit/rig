package vast

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/provider"
)

const testKey = "test-key"

// fakeVast serves the recorded fixtures the way console.vast.ai does: offer
// searches honour the filter DSL (eq/gt/gte/lt/lte per field, price order,
// limit) so paging and token clauses behave; rentals of taken offers fail
// the way Vast's do; state changes and deletions are recorded.
type fakeVast struct {
	t      *testing.T
	mu     sync.Mutex
	offers map[string][]map[string]any // market type → offers
	taken  map[int64]bool              // asks that answer "gone"

	rateLimit int // 429s to serve before answering
	bareKeys  bool
	keys      []map[string]any

	// recorded
	asks     []map[string]any // PUT /asks bodies in order, with "_id"
	states   []string         // "<id>:<state>"
	deleted  []string
	posted   []map[string]any
	searches int
}

func loadJSON(t *testing.T, name string, out any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func newFake(t *testing.T) *fakeVast {
	f := &fakeVast{t: t, offers: map[string][]map[string]any{}, taken: map[int64]bool{}}
	for typ, file := range map[string]string{"on-demand": "offers_ondemand.json", "bid": "offers_bid.json"} {
		var res struct {
			Offers []map[string]any `json:"offers"`
		}
		loadJSON(t, file, &res)
		f.offers[typ] = res.Offers
	}
	var keys struct {
		Keys []map[string]any `json:"ssh_keys"`
	}
	loadJSON(t, "ssh_keys.json", &keys)
	f.keys = keys.Keys
	return f
}

// addOffers appends a fixture's offers to one market, so a test can model
// a GPU shape the crawl only reaches there.
func (f *fakeVast) addOffers(typ, file string) {
	f.t.Helper()
	var res struct {
		Offers []map[string]any `json:"offers"`
	}
	loadJSON(f.t, file, &res)
	f.offers[typ] = append(f.offers[typ], res.Offers...)
}

func (f *fakeVast) write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if s, ok := v.(string); ok {
		fmt.Fprint(w, s)
		return
	}
	json.NewEncoder(w).Encode(v)
}

func (f *fakeVast) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+testKey {
		f.write(w, 401, map[string]any{"error": "invalid_api_key", "msg": "Invalid API key"})
		return
	}
	if f.rateLimit > 0 {
		f.rateLimit--
		f.write(w, 429, map[string]any{"error": "rate_limit", "msg": "Too many requests"})
		return
	}
	var body map[string]any
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&body)
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	switch {
	case r.Method == "POST" && path == "/bundles":
		f.searches++
		f.write(w, 200, map[string]any{"offers": f.search(body)})
	case r.Method == "PUT" && len(parts) == 2 && parts[0] == "asks":
		var id int64
		fmt.Sscanf(parts[1], "%d", &id)
		body["_id"] = id
		f.asks = append(f.asks, body)
		if f.taken[id] {
			f.write(w, 400, map[string]any{"success": false, "error": "no_such_ask", "msg": "This offer is no longer available"})
			return
		}
		f.write(w, 200, map[string]any{"success": true, "new_contract": 30000000 + id%1000})
	case r.Method == "GET" && path == "/instances":
		b, _ := os.ReadFile(filepath.Join("testdata", "instances.json"))
		f.write(w, 200, string(b))
	case r.Method == "GET" && len(parts) == 2 && parts[0] == "instances":
		var res struct {
			Instances []map[string]any `json:"instances"`
		}
		loadJSON(f.t, "instances.json", &res)
		for _, in := range res.Instances {
			if fmt.Sprint(in["id"]) == parts[1] {
				f.write(w, 200, map[string]any{"instances": in})
				return
			}
		}
		f.write(w, 200, map[string]any{"instances": nil})
	case r.Method == "PUT" && len(parts) == 2 && parts[0] == "instances":
		f.states = append(f.states, parts[1]+":"+fmt.Sprint(body["state"]))
		f.write(w, 200, map[string]any{"success": true})
	case r.Method == "DELETE" && len(parts) == 2 && parts[0] == "instances":
		if parts[1] == "99" {
			f.write(w, 404, map[string]any{"error": "no_such_instance", "msg": "Instance not found"})
			return
		}
		f.deleted = append(f.deleted, parts[1])
		f.write(w, 200, map[string]any{"success": true})
	case r.Method == "GET" && path == "/users/current":
		b, _ := os.ReadFile(filepath.Join("testdata", "user.json"))
		f.write(w, 200, string(b))
	case r.Method == "GET" && path == "/ssh":
		if f.bareKeys {
			f.write(w, 200, f.keys)
			return
		}
		f.write(w, 200, map[string]any{"ssh_keys": f.keys})
	case r.Method == "POST" && path == "/ssh":
		f.posted = append(f.posted, body)
		k := map[string]any{"id": 555, "public_key": body["ssh_key"]}
		f.keys = append(f.keys, k)
		f.write(w, 200, map[string]any{"success": true, "ssh_key": k})
	default:
		f.write(w, 404, "<html><body>404 Not Found</body></html>")
	}
}

// search applies the query's filter clauses to the market's offers.
func (f *fakeVast) search(q map[string]any) []map[string]any {
	typ, _ := q["type"].(string)
	var out []map[string]any
	for _, o := range f.offers[typ] {
		if matches(o, q) {
			out = append(out, o)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["dph_total"].(float64) < out[j]["dph_total"].(float64) })
	if lim, ok := q["limit"].(float64); ok && int(lim) < len(out) {
		out = out[:int(lim)]
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out
}

func matches(o, q map[string]any) bool {
	for field, clause := range q {
		ops, ok := clause.(map[string]any)
		if !ok {
			continue // type, order, limit
		}
		have, present := o[field]
		if !present || have == nil {
			continue // the live API answers verified:null for verified:{eq:true}
		}
		for op, want := range ops {
			hn, hok := have.(float64)
			wn, wok := want.(float64)
			// Measured live 2026-09-08: a geolocation clause matches the
			// host's upper-case country code ("KR"), never the spelled-out
			// string the response carries ("South Korea, KR") and never a
			// lower-case code.
			if field == "geolocation" {
				code := ""
				if s := fmt.Sprint(have); strings.Contains(s, ",") {
					code = strings.TrimSpace(s[strings.LastIndex(s, ",")+1:])
				}
				if op == "eq" && fmt.Sprint(want) != code {
					return false
				}
				continue
			}
			switch op {
			case "eq":
				if fmt.Sprint(have) != fmt.Sprint(want) {
					return false
				}
			case "gt", "gte", "lt", "lte":
				if !hok || !wok {
					return false
				}
				if (op == "gt" && !(hn > wn)) || (op == "gte" && !(hn >= wn)) || (op == "lt" && !(hn < wn)) || (op == "lte" && !(hn <= wn)) {
					return false
				}
			}
		}
	}
	return true
}

func newTestDriver(t *testing.T, f *fakeVast, cfg Config) provider.Provider {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	d := New(testKey, cfg)
	d.c.base = srv.URL
	d.c.retryWait = time.Millisecond
	return d
}

func typeByToken(t *testing.T, types []provider.InstanceType, tok string) provider.InstanceType {
	t.Helper()
	for _, it := range types {
		if it.Type == tok {
			return it
		}
	}
	t.Fatalf("no type %q in %v", tok, tokens(types))
	return provider.InstanceType{}
}

func tokens(types []provider.InstanceType) []string {
	var out []string
	for _, it := range types {
		out = append(out, it.Type)
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCapabilities(t *testing.T) {
	d := New(testKey, Config{})
	caps := d.Capabilities()
	if !caps.Spot || caps.Volumes || !caps.Balance || !caps.DockerImages {
		t.Errorf("Capabilities = %+v, want spot, balance and docker images, no volumes", caps)
	}
	for _, want := range []string{procEnviron, "CONTAINER_ID=", "CONTAINER_API_KEY=", `{"state":"stopped"}`} {
		if !strings.Contains(caps.SelfStop, want) {
			t.Errorf("SelfStop lacks %q: %s", want, caps.SelfStop)
		}
	}
	// Live 2026-09-08: /etc/environment on a Vast container holds only the
	// image's build-time env — neither variable is in it — and its unquoted
	// values (`NVIDIA_REQUIRE_CUDA=cuda>=12.8 brand=unknown`) make sourcing
	// it fail outright. Reading it must not come back.
	if strings.Contains(caps.SelfStop, "/etc/environment") {
		t.Errorf("SelfStop reads /etc/environment, which carries neither variable: %s", caps.SelfStop)
	}
	if d.Name() != "vast" {
		t.Errorf("Name = %q", d.Name())
	}
}

func TestFactoryDecodesBlock(t *testing.T) {
	t.Setenv("VAST_API_KEY", testKey)
	var pc config.ProviderConfig
	if err := yaml.Unmarshal([]byte("min_reliability: 0.9\nimage: myorg/img:1\ndisk_gb: 40\nfilters:\n  cuda_max_good: {gte: 12.8}\n"), &pc); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Open("vast", pc)
	if err != nil {
		t.Fatal(err)
	}
	imgs, _ := p.Images(t.Context(), "1xRTX-4090")
	if len(imgs) != 2 || imgs[0].Name != "myorg/img:1" || !imgs[0].Default || imgs[1].Name != DefaultImage {
		t.Errorf("Images = %+v, want configured image as default then %s", imgs, DefaultImage)
	}
	d := p.(*Driver)
	if d.cfg.MinReliability != 0.9 || d.cfg.DiskGB != 40 || d.cfg.BidMarkup != 1.2 {
		t.Errorf("config = %+v, want min_reliability 0.9, disk 40, markup default 1.2", d.cfg)
	}
}

func TestFactoryNeedsKey(t *testing.T) {
	t.Setenv("VAST_API_KEY", "")
	_, err := provider.Open("vast", config.ProviderConfig{})
	if err == nil || !strings.Contains(err.Error(), "VAST_API_KEY") {
		t.Errorf("err = %v, want it to name VAST_API_KEY", err)
	}
}

// The catalogue: one row per (num_gpus, gpu_name), cheapest on-demand price,
// cheapest min_bid as the spot price, hardware from the offer, offer count.
func TestInstanceTypesGroupOffers(t *testing.T) {
	p := newTestDriver(t, newFake(t), Config{})
	types, err := p.InstanceTypes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := tokens(types); !reflect.DeepEqual(got, []string{"1xRTX-3060", "1xRTX-4090", "2xRTX-4090", "1xH100-SXM"}) {
		t.Errorf("tokens = %v, want cheapest first", got)
	}
	r4090 := typeByToken(t, types, "1xRTX-4090")
	if !near(r4090.PricePerHour, 0.2962962962962963) || !near(r4090.SpotPrice, 0.06666666666666667) {
		t.Errorf("1xRTX-4090 price %v spot %v, want 0.2962962962962963 / 0.06666666666666667", r4090.PricePerHour, r4090.SpotPrice)
	}
	if r4090.GPU != "RTX 4090" || r4090.GPUCount != 1 || r4090.GPUMemoryGB != 24 || r4090.MemoryGB != 47 || r4090.CPUCount != 24 {
		t.Errorf("1xRTX-4090 hardware = %+v, want RTX 4090 x1, 24GB, 47GB RAM, 24 cores", r4090)
	}
	if r4090.Description != "3 offers" {
		t.Errorf("Description = %q, want 3 offers", r4090.Description)
	}
	h100 := typeByToken(t, types, "1xH100-SXM")
	if !near(h100.PricePerHour, 1.788888888888889) || !near(h100.SpotPrice, 0.6533333333333333) || h100.GPUMemoryGB != 80 {
		t.Errorf("1xH100-SXM = %+v, want 1.788888888888889 / 0.6533333333333333 / 80GB", h100)
	}
	two := typeByToken(t, types, "2xRTX-4090")
	if !near(two.PricePerHour, 0.5355555555555556) || !near(two.SpotPrice, 0.25) || two.GPUCount != 2 {
		t.Errorf("2xRTX-4090 = %+v, want 0.5355555555555556 / 0.25 / 2 GPUs", two)
	}
}

// A GPU shape the crawl only reaches in the bid market still gets a real
// price — its cheapest bid-market dph_total, not $0.00 — an honest count of
// the offers behind that price, and "(bid only)" so the row is not read as
// on-demand capacity.
func TestInstanceTypesBidOnlyShape(t *testing.T) {
	f := newFake(t)
	f.addOffers("bid", "offers_bid_only.json")
	p := newTestDriver(t, f, Config{})
	types, err := p.InstanceTypes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := tokens(types); !reflect.DeepEqual(got, []string{"1xRTX-3060", "2xRTX-5080", "1xRTX-4090", "2xRTX-4090", "1xH100-SXM"}) {
		t.Errorf("tokens = %v, want the bid-only shape sorted by its real price", got)
	}
	bidOnly := typeByToken(t, types, "2xRTX-5080")
	if !near(bidOnly.PricePerHour, 0.2817777777777778) || !near(bidOnly.SpotPrice, 0.28) {
		t.Errorf("2xRTX-5080 price %v spot %v, want 0.2817777777777778 / 0.28", bidOnly.PricePerHour, bidOnly.SpotPrice)
	}
	if bidOnly.Description != "2 offers (bid only)" {
		t.Errorf("Description = %q, want \"2 offers (bid only)\"", bidOnly.Description)
	}
	if bidOnly.GPU != "RTX 5080" || bidOnly.GPUCount != 2 || bidOnly.GPUMemoryGB != 16 || bidOnly.MemoryGB != 126 || bidOnly.CPUCount != 20 {
		t.Errorf("2xRTX-5080 hardware = %+v, want RTX 5080 x2, 16GB, 126GB RAM, 20 cores", bidOnly)
	}
	// a shape both markets carry is unchanged: on-demand price and count
	r4090 := typeByToken(t, types, "1xRTX-4090")
	if !near(r4090.PricePerHour, 0.2962962962962963) || r4090.Description != "3 offers" {
		t.Errorf("1xRTX-4090 = %+v, want 0.2962962962962963 and 3 offers", r4090)
	}
}

// A search page is capped at 512 offers whatever limit is asked for, so the
// catalogue pages by price; with a page of 3 the expensive end must still
// be there and prices must still be the cheapest.
func TestInstanceTypesPageByPrice(t *testing.T) {
	f := newFake(t)
	p := newTestDriver(t, f, Config{})
	p.(*Driver).pageSize = 3
	types, err := p.InstanceTypes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 4 {
		t.Fatalf("types = %v, want all 4 shapes across pages", tokens(types))
	}
	h100 := typeByToken(t, types, "1xH100-SXM")
	if !near(h100.PricePerHour, 1.788888888888889) || h100.Description != "3 offers" {
		t.Errorf("1xH100-SXM = %+v, want 1.788888888888889 and 3 offers", h100)
	}
	if !near(typeByToken(t, types, "1xRTX-3060").PricePerHour, 0.049629629629629635) {
		t.Errorf("1xRTX-3060 price = %v", typeByToken(t, types, "1xRTX-3060").PricePerHour)
	}
	if f.searches < 6 {
		t.Errorf("searches = %d, want at least 3 on-demand + 3 bid pages", f.searches)
	}
}

// rig types calls InstanceTypes then Availability; one crawl serves both.
func TestCatalogueSharedWithinCommand(t *testing.T) {
	f := newFake(t)
	p := newTestDriver(t, f, Config{})
	if _, err := p.InstanceTypes(t.Context()); err != nil {
		t.Fatal(err)
	}
	n := f.searches
	if _, err := p.Availability(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Locations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.searches != n {
		t.Errorf("Availability/Locations searched again (%d → %d)", n, f.searches)
	}
}

func TestAvailabilityGroupsByGeolocation(t *testing.T) {
	p := newTestDriver(t, newFake(t), Config{})
	avail, err := p.Availability(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"United States, US": {"1xRTX-3060", "1xRTX-4090", "2xRTX-4090"},
		"Japan, JP":         {"1xH100-SXM", "2xRTX-4090"},
		", US":              {"1xRTX-4090"},
		"Idaho, US":         {"1xH100-SXM"},
		"Montana, US":       {"1xH100-SXM"},
		"South Korea, KR":   {"1xRTX-3060"},
		"Delaware, US":      {"1xRTX-4090"},
		"Türkiye, TR":       {"2xRTX-4090"},
	}
	if !reflect.DeepEqual(avail, want) {
		t.Errorf("Availability(on-demand) = %v\nwant %v", avail, want)
	}
	spot, err := p.Availability(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := spot["Türkiye, TR"]; !reflect.DeepEqual(got, []string{"1xRTX-4090", "2xRTX-4090"}) {
		t.Errorf("spot Türkiye = %v, want [1xRTX-4090 2xRTX-4090]", got)
	}
	if got := spot["Ohio, US"]; !reflect.DeepEqual(got, []string{"1xRTX-3060"}) {
		t.Errorf("spot Ohio = %v, want [1xRTX-3060]", got)
	}
	if _, ok := spot["Montana, US"]; ok {
		t.Errorf("Montana has no bid offers but appears in spot availability")
	}
}

func TestLocationsAreDistinctGeolocations(t *testing.T) {
	p := newTestDriver(t, newFake(t), Config{})
	locs, err := p.Locations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, l := range locs {
		if l.Name != l.Code {
			t.Errorf("location %q has name %q", l.Code, l.Name)
		}
		codes = append(codes, l.Code)
	}
	want := []string{", US", "Czechia, CZ", "Delaware, US", "Hong Kong, HK", "Idaho, US", "Japan, JP", "Montana, US", "Ohio, US", "South Korea, KR", "Texas, US", "Thailand, TH", "Türkiye, TR", "United States, US"}
	if !reflect.DeepEqual(codes, want) {
		t.Errorf("Locations = %v\nwant %v", codes, want)
	}
}

// Instances: label filter, status normalisation and the two ssh routes.
func TestListInstancesMapsStatusAndEndpoint(t *testing.T) {
	p := newTestDriver(t, newFake(t), Config{})
	ins, err := p.ListInstances(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]provider.Instance{}
	for _, in := range ins {
		byName[in.Name] = in
	}
	if _, ok := byName["jupyter-scratch"]; ok || len(ins) != 7 {
		t.Errorf("ListInstances kept a non-rig label: %v", byName)
	}
	direct := byName["rig-train"]
	if direct.Status != provider.Running || direct.RawStatus != "running" {
		t.Errorf("rig-train status = %s/%s, want running", direct.Status, direct.RawStatus)
	}
	if direct.SSH != (provider.Endpoint{Host: "185.62.108.2", Port: 41022, User: "root"}) {
		t.Errorf("direct endpoint = %+v, want 185.62.108.2:41022 root", direct.SSH)
	}
	if direct.ID != "20001" || direct.IP != "185.62.108.2" || direct.Type != "1xRTX-4090" || direct.GPU != "RTX 4090" ||
		direct.Location != "United States, US" || !near(direct.PricePerHour, 0.2962962962962963) || direct.Spot ||
		direct.Image != "vastai/pytorch:cuda-12.8.1-auto" {
		t.Errorf("rig-train = %+v", direct)
	}
	if direct.CreatedAt.Unix() != 1757300000 || direct.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt = %v, want start_date 1757300000 as UTC", direct.CreatedAt)
	}
	proxy := byName["rig-proxy"]
	if proxy.SSH != (provider.Endpoint{Host: "ssh4.vast.ai", Port: 22334, User: "root"}) {
		t.Errorf("proxy endpoint = %+v, want ssh4.vast.ai:22334 root", proxy.SSH)
	}
	if proxy.IP != "83.11.4.9" || proxy.Type != "1xH100-SXM" || proxy.Location != "Japan, JP" {
		t.Errorf("rig-proxy = %+v", proxy)
	}
	for name, want := range map[string]provider.Status{
		"rig-old":     provider.Stopped,      // exited
		"rig-new":     provider.Provisioning, // actual_status null
		"rig-dead":    provider.Error,        // offline
		"rig-halting": provider.Stopped,      // running but intended stopped
		"rig-odd":     provider.Error,        // a word the driver does not map
	} {
		if got := byName[name].Status; got != want {
			t.Errorf("%s status = %s, want %s", name, got, want)
		}
	}
	if old := byName["rig-old"]; !old.Spot || old.Location != "Hong Kong, HK" {
		t.Errorf("rig-old = %+v, want spot in Hong Kong", old)
	}
	// an unmapped word must not leak into the enum core compares, but the
	// provider's own word is kept for display
	if odd := byName["rig-odd"]; odd.Status != provider.Error || odd.RawStatus != "recreating" {
		t.Errorf("rig-odd = %s/%s, want error/recreating", odd.Status, odd.RawStatus)
	}
	if byName["rig-new"].Type != "2xRTX-4090" {
		t.Errorf("rig-new type = %q", byName["rig-new"].Type)
	}
}

func TestGetInstanceAndGone(t *testing.T) {
	p := newTestDriver(t, newFake(t), Config{})
	in, err := p.GetInstance(t.Context(), "20002")
	if err != nil {
		t.Fatal(err)
	}
	if in.Name != "rig-proxy" || in.Status != provider.Running || in.SSH.Host != "ssh4.vast.ai" {
		t.Errorf("GetInstance(20002) = %+v", in)
	}
	missing, err := p.GetInstance(t.Context(), "99")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Status != provider.Gone || missing.ID != "99" {
		t.Errorf("GetInstance(missing) = %+v, want Gone", missing)
	}
}

func TestStopStartDelete(t *testing.T) {
	f := newFake(t)
	p := newTestDriver(t, f, Config{})
	if err := p.ShutdownInstance(t.Context(), "20001"); err != nil {
		t.Fatal(err)
	}
	if err := p.StartInstance(t.Context(), "20001"); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteInstance(t.Context(), "20001", nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.states, []string{"20001:stopped", "20001:running"}) || !reflect.DeepEqual(f.deleted, []string{"20001"}) {
		t.Errorf("states %v deleted %v", f.states, f.deleted)
	}
	// core's Down forgets a cached instance when the provider says "not found"
	err := p.DeleteInstance(t.Context(), "99", nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "not found") {
		t.Errorf("DeleteInstance(missing) = %v, want a 'not found' error", err)
	}
}

// Create walks the offers cheapest first and rents the first one still
// there; the new contract id is the instance id.
func TestCreateInstanceSkipsTakenOffers(t *testing.T) {
	f := newFake(t)
	f.taken[50243756] = true // United States, US — cheapest
	f.taken[45786457] = true // Delaware, US
	p := newTestDriver(t, f, Config{})
	in, err := p.CreateInstance(t.Context(), provider.CreateInstanceRequest{
		Type: "1xRTX-4090", Name: "rig-train", Image: "myorg/img:1", OSVolumeGB: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if in.ID != "30000235" || in.Name != "rig-train" || in.Status != provider.Provisioning || in.Location != ", US" ||
		in.Type != "1xRTX-4090" || in.GPU != "RTX 4090" || in.Spot || !near(in.PricePerHour, 0.32962962962962966) || in.Image != "myorg/img:1" {
		t.Errorf("CreateInstance = %+v, want the third offer (40912235 in \", US\") as contract 30000235", in)
	}
	var ids []int64
	for _, a := range f.asks {
		ids = append(ids, a["_id"].(int64))
	}
	if !reflect.DeepEqual(ids, []int64{50243756, 45786457, 40912235}) {
		t.Errorf("asks tried = %v, want cheapest first", ids)
	}
	last := f.asks[2]
	if last["label"] != "rig-train" || last["image"] != "myorg/img:1" || last["disk"] != float64(50) ||
		last["runtype"] != "ssh_direc ssh_proxy" || last["cancel_unavail"] != true || last["client_id"] != "me" {
		t.Errorf("rental body = %v", last)
	}
	if env, _ := last["env"].(map[string]any); env["-p 22:22"] != "1" {
		t.Errorf("env = %v, want the ssh port mapping", last["env"])
	}
	if on, _ := last["onstart"].(string); !strings.Contains(on, "no_auto_tmux") || !strings.Contains(on, "~/.rig/onstart.sh") {
		t.Errorf("onstart = %q", on)
	}
	if _, bid := last["price"]; bid {
		t.Errorf("on-demand rental carries a bid price: %v", last["price"])
	}
}

func TestCreateInstanceExhaustedIsNoCapacity(t *testing.T) {
	f := newFake(t)
	for _, id := range []int64{50243756, 45786457, 40912235} {
		f.taken[id] = true
	}
	p := newTestDriver(t, f, Config{})
	_, err := p.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1xRTX-4090", Name: "rig-x"})
	if !errors.Is(err, provider.ErrNoCapacity) {
		t.Fatalf("err = %v, want ErrNoCapacity", err)
	}
	if !strings.Contains(err.Error(), "3 on-demand offers") || !strings.Contains(err.Error(), "no longer available") {
		t.Errorf("err = %v, want the count and Vast's last message", err)
	}
	// a location with no offers at all is no capacity too, without a rental attempt
	n := len(f.asks)
	_, err = p.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1xRTX-4090", Name: "rig-x", Location: "Antarctica, AQ"})
	if !errors.Is(err, provider.ErrNoCapacity) || len(f.asks) != n {
		t.Errorf("err = %v (asks %d → %d), want ErrNoCapacity with no rental", err, n, len(f.asks))
	}
}

func TestCreateInstancePinsLocationAndDefaults(t *testing.T) {
	f := newFake(t)
	p := newTestDriver(t, f, Config{Image: "myorg/img:1", DiskGB: 35})
	in, err := p.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1xRTX-4090", Name: "rig-jp", Location: "Delaware, US"})
	if err != nil {
		t.Fatal(err)
	}
	if in.Location != "Delaware, US" || len(f.asks) != 1 || f.asks[0]["_id"] != int64(45786457) {
		t.Errorf("instance %+v via asks %v, want the Delaware offer only", in, f.asks)
	}
	if f.asks[0]["image"] != "myorg/img:1" || f.asks[0]["disk"] != float64(35) {
		t.Errorf("defaults not applied: image %v disk %v", f.asks[0]["image"], f.asks[0]["disk"])
	}
}

// A location pin is exact within its country. Vast's offer filter only
// understands the country code, so three US offers come back for a US pin
// and the driver must rent the one whose geolocation the user asked for —
// not the cheapest of the country (regression: live smoke 2026-09-08, where
// pinning "South Korea, KR" matched no offer at all and every rig up on
// Vast failed with ErrNoCapacity).
func TestCreateInstanceLocationPinIsExactWithinCountry(t *testing.T) {
	for _, tc := range []struct {
		pin  string
		want int64
	}{
		{"United States, US", 50243756}, // cheapest of the three US offers
		{"Delaware, US", 45786457},      // dearer, but the one pinned
		{", US", 40912235},              // a host with no region: dearest of the three
		{"us", 50243756},                // a bare country code takes the country's cheapest
	} {
		f := newFake(t)
		p := newTestDriver(t, f, Config{})
		in, err := p.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1xRTX-4090", Name: "rig-loc", Location: tc.pin})
		if err != nil {
			t.Fatalf("pin %q: %v", tc.pin, err)
		}
		if len(f.asks) != 1 || f.asks[0]["_id"] != tc.want {
			t.Errorf("pin %q rented %v, want offer %d only", tc.pin, f.asks, tc.want)
		}
		if tc.pin != "us" && in.Location != tc.pin {
			t.Errorf("pin %q → instance location %q", tc.pin, in.Location)
		}
	}
}

// A bid rents from the bid market at min_bid × bid_markup.
func TestCreateInstanceBidPrice(t *testing.T) {
	f := newFake(t)
	p := newTestDriver(t, f, Config{})
	in, err := p.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1xRTX-4090", Name: "rig-bid", Spot: true})
	if err != nil {
		t.Fatal(err)
	}
	if !in.Spot || in.Location != "Hong Kong, HK" || f.asks[0]["_id"] != int64(39184681) {
		t.Errorf("bid rented %+v via %v, want the cheapest bid offer 39184681 in Hong Kong", in, f.asks)
	}
	price, _ := f.asks[0]["price"].(float64)
	if !near(price, 0.06666666666666667*1.2) || !near(in.PricePerHour, 0.08) {
		t.Errorf("bid price = %v (instance %v), want min_bid 0.06666666666666667 × 1.2 = 0.08", price, in.PricePerHour)
	}
	f.asks = nil
	p2 := newTestDriver(t, f, Config{BidMarkup: 1.5})
	if _, err := p2.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1xRTX-4090", Name: "rig-bid", Spot: true}); err != nil {
		t.Fatal(err)
	}
	if price, _ := f.asks[0]["price"].(float64); !near(price, 0.1) {
		t.Errorf("bid_markup 1.5 → price %v, want 0.1", price)
	}
}

// Tokens resolve to Vast's spelling of the GPU name: dashes become spaces
// before any search, and once a search has run (rig up always lists
// availability first) the match is case-insensitive too.
func TestTokenSpelling(t *testing.T) {
	f := newFake(t)
	p := newTestDriver(t, f, Config{})
	if _, err := p.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "2xRTX-4090", Name: "rig-a", Location: "Japan, JP"}); err != nil {
		t.Errorf("token before any search: %v", err)
	}
	if _, err := p.InstanceTypes(t.Context()); err != nil {
		t.Fatal(err)
	}
	in, err := p.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1XH100-sxm", Name: "rig-b", Location: "Idaho, US"})
	if err != nil {
		t.Fatalf("mixed-case token after a search: %v", err)
	}
	if in.Type != "1xH100-SXM" || in.GPU != "H100 SXM" {
		t.Errorf("instance type %q gpu %q, want canonical 1xH100-SXM / H100 SXM", in.Type, in.GPU)
	}
	for _, bad := range []string{"RTX-4090", "x4090", "0xRTX-4090", "1x"} {
		if _, err := p.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: bad, Name: "rig-c"}); err == nil || !strings.Contains(err.Error(), "<count>x<gpu>") {
			t.Errorf("%q: err = %v, want the token format explained", bad, err)
		}
	}
}

// direct_only and raw filters narrow the offers a rental may use.
func TestDirectOnlyAndFilters(t *testing.T) {
	f := newFake(t)
	p := newTestDriver(t, f, Config{DirectOnly: true, Filters: map[string]any{"cpu_ram": map[string]any{"gte": float64(60000)}}})
	in, err := p.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1xRTX-4090", Name: "rig-d"})
	if err != nil {
		t.Fatal(err)
	}
	// every 1xRTX-4090 offer has direct ports; only Delaware has ≥ 60000 MB of RAM
	if in.Location != "Delaware, US" {
		t.Errorf("rented in %q, want Delaware, US (cpu_ram filter)", in.Location)
	}
	p2 := newTestDriver(t, f, Config{Filters: map[string]any{"direct_port_count": map[string]any{"gte": float64(200)}}})
	types, err := p2.InstanceTypes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// only Türkiye's 2xRTX-4090 and the Montana/Japan H100 hosts have ≥ 200 direct ports
	if got := tokens(types); !reflect.DeepEqual(got, []string{"2xRTX-4090", "1xH100-SXM"}) {
		t.Errorf("filtered catalogue = %v, want [2xRTX-4090 1xH100-SXM]", got)
	}
}

func TestVolumesUnsupported(t *testing.T) {
	p := newTestDriver(t, newFake(t), Config{})
	ctx := t.Context()
	_, lerr := p.ListVolumes(ctx)
	_, gerr := p.GetVolume(ctx, "v")
	_, cerr := p.CreateVolume(ctx, provider.CreateVolumeRequest{Name: "data"})
	for name, err := range map[string]error{
		"ListVolumes": lerr, "GetVolume": gerr, "CreateVolume": cerr,
		"AttachVolume": p.AttachVolume(ctx, "v", "i"),
		"DetachVolume": p.DetachVolume(ctx, "v", "i"),
		"DeleteVolume": p.DeleteVolume(ctx, "v"),
	} {
		if !errors.Is(err, provider.ErrUnsupported) || !strings.Contains(err.Error(), "machine-bound") ||
			!strings.Contains(err.Error(), "rig sync") {
			t.Errorf("%s = %v, want ErrUnsupported explaining machine-bound volumes and the workaround", name, err)
		}
		if strings.Contains(err.Error(), "spec") {
			t.Errorf("%s = %v, points the user at internal planning notes", name, err)
		}
	}
}

// A renter's spendable money is `credit`; `balance` is 0 on a funded
// account (live 2026-09-08: $10 of credit read back as balance 0, which the
// dashboard showed as $0.00), so the fixture is that live shape.
func TestBalanceIsTheAccountCredit(t *testing.T) {
	p := newTestDriver(t, newFake(t), Config{})
	amount, cur, err := p.Balance(t.Context())
	if err != nil || amount != 9.99419829187 || cur != "USD" {
		t.Errorf("Balance = %v %s %v, want 9.99419829187 USD (the credit, not the balance)", amount, cur, err)
	}
}

func TestEnsureSSHKey(t *testing.T) {
	existing := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExistingKeyFixture0000000000000000000000 darren@laptop"
	for _, bare := range []bool{false, true} {
		f := newFake(t)
		f.bareKeys = bare
		p := newTestDriver(t, f, Config{})
		id, err := p.EnsureSSHKey(t.Context(), "rig-darren-id_ed25519.pub", existing+"\n")
		if err != nil || id != "4211" || len(f.posted) != 0 {
			t.Errorf("bare=%v existing key: id %q err %v posted %v, want 4211 and no registration", bare, id, err, f.posted)
		}
		fresh := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFreshKey000000000000000000000000000000000 darren@laptop"
		id, err = p.EnsureSSHKey(t.Context(), "rig-darren-id_ed25519.pub", fresh)
		if err != nil || id != "555" || len(f.posted) != 1 || f.posted[0]["ssh_key"] != fresh {
			t.Errorf("bare=%v new key: id %q err %v posted %v, want 555 after one registration", bare, id, err, f.posted)
		}
	}
}

// 429 is retried with backoff; a persistent one surfaces as an error.
func TestRateLimitRetry(t *testing.T) {
	f := newFake(t)
	f.rateLimit = 3
	p := newTestDriver(t, f, Config{})
	if amount, _, err := p.Balance(t.Context()); err != nil || amount != 9.99419829187 {
		t.Errorf("after 3 × 429: %v %v, want the fixture's credit", amount, err)
	}
	f.rateLimit = 4
	_, _, err := p.Balance(t.Context())
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("after 4 × 429: %v, want a rate-limit error", err)
	}
}

func TestBadKeyIsClearError(t *testing.T) {
	f := newFake(t)
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	d := New("wrong", Config{})
	d.c.base = srv.URL
	_, err := d.ListInstances(t.Context())
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("err = %v, want HTTP 401 with Vast's message", err)
	}
}

// The stop line is rig's whole cost-safety story on Vast, so it is proved
// by running it: rendered against a fake PID-1 environment and a fake curl
// on PATH, it must PUT {"state":"stopped"} to this container's own id with
// the instance-scoped key, and it must survive the hostile values a
// container environment carries (an unquoted `cuda>=12.8 brand=unknown`
// would redirect if the environment were sourced instead of parsed).
// Measured live 2026-09-08 on instance 50300289.
func TestSelfStopLineStopsThisContainer(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash on PATH")
	}
	const wantKey = "instance-scoped-key"
	dir := t.TempDir()
	environ := filepath.Join(dir, "environ")
	entries := []string{
		"NVIDIA_REQUIRE_CUDA=cuda>=12.8 brand=unknown",
		"CONTAINER_ID=50300289",
		"CONTAINER_API_KEY=" + wantKey,
		"CONTAINER_LABEL=C.50300289",
	}
	if err := os.WriteFile(environ, []byte(strings.Join(entries, "\x00")+"\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "vast_api_key")
	labelFile := filepath.Join(dir, "vast_containerlabel")
	if err := os.WriteFile(keyFile, []byte("fallback-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(labelFile, []byte("C.50300289\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// a curl that records its arguments instead of calling Vast
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "curl.args")
	script := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > " + log + "\n"
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(name, line string) string {
		t.Helper()
		if err := os.Remove(log); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "-c", "set -u\n"+line)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: running the stop line: %v: %s", name, err, out)
		}
		if len(out) > 0 {
			t.Errorf("%s: the stop line wrote %q; it must be quiet until curl speaks", name, out)
		}
		b, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("%s: the stop line did not call curl: %v", name, err)
		}
		return string(b)
	}

	args := run("pid 1", renderSelfStop(environ, keyFile, labelFile))
	for _, want := range []string{
		"https://console.vast.ai/api/v0/instances/50300289/",
		"Authorization: Bearer " + wantKey,
		`{"state":"stopped"}`,
	} {
		if !strings.Contains(args, want) {
			t.Errorf("curl args lack %q:\n%s", want, args)
		}
	}

	// With PID 1's environment unreadable, Vast's own files answer instead.
	args = run("fallback", renderSelfStop(filepath.Join(dir, "gone"), keyFile, labelFile))
	for _, want := range []string{"instances/50300289/", "Bearer fallback-key"} {
		if !strings.Contains(args, want) {
			t.Errorf("fallback curl args lack %q:\n%s", want, args)
		}
	}
}

// A host with no direct ports serves ssh through Vast's proxy alone, and
// asking such a host for a port mapping makes the rental un-creatable:
// live 2026-09-08, renting offer 50103181 with env {"-p 22:22": "1"} left
// the instance in loading for ever with status_msg `docker: invalid
// hostPort: -1`. The port request must reach only hosts that have ports.
func TestCreateInstanceAsksForAPortOnlyWhereTheHostHasOne(t *testing.T) {
	f := newFake(t)
	f.addOffers("on-demand", "offers_proxy_only.json")
	p := newTestDriver(t, f, Config{})

	if _, err := p.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1xRTX-PRO-4000", Name: "rig-proxy", Location: "Alberta, CA"}); err != nil {
		t.Fatal(err)
	}
	if len(f.asks) != 1 || f.asks[0]["_id"] != int64(50103181) {
		t.Fatalf("asks = %v, want the proxy-only offer", f.asks)
	}
	if env, ok := f.asks[0]["env"]; ok {
		t.Errorf("proxy-only host asked for env %v, want no port request at all", env)
	}
	// Direct ssh must not even be asked for: it is the runtype that makes
	// docker map the port, so "ssh_direc ssh_proxy" fails on this host the
	// same way (measured live 2026-09-08, twice). Proxy ssh alone works.
	if rt, _ := f.asks[0]["runtype"].(string); rt != "ssh_proxy" {
		t.Errorf("runtype = %q, want ssh_proxy alone on a host with no ports", rt)
	}

	f.asks = nil
	if _, err := p.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1xRTX-4090", Name: "rig-direct", Location: "Delaware, US"}); err != nil {
		t.Fatal(err)
	}
	env, _ := f.asks[0]["env"].(map[string]any)
	if env["-p 22:22"] != "1" {
		t.Errorf("host with 12 direct ports asked for env %v, want the sshd port mapped", f.asks[0]["env"])
	}
	if rt, _ := f.asks[0]["runtype"].(string); rt != "ssh_direc ssh_proxy" {
		t.Errorf("runtype = %q, want direct ssh with the proxy as fallback", rt)
	}
}
