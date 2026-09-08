package runpod_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/provider"
	"github.com/dtuit/rig/internal/provider/runpod"
)

// fixture is a recorded v2 reply from testdata (2026-09-08 probes).
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// reply is what the fake API answers to one "METHOD /path" (query string
// dropped): a status and a body.
type reply struct {
	status int
	body   []byte
}

// server is a scripted RunPod API: routes map "METHOD /path" to a reply,
// and every request body is recorded under the same key.
type server struct {
	t      *testing.T
	routes map[string]reply
	bodies map[string][]map[string]any
	auth   []string
	*httptest.Server
}

func newServer(t *testing.T, routes map[string]reply) *server {
	s := &server{t: t, routes: routes, bodies: map[string][]map[string]any{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Errorf("%s: body is not a JSON object: %v", key, err)
			}
			s.bodies[key] = append(s.bodies[key], m)
		}
		rp, ok := routes[key]
		if !ok {
			t.Errorf("unexpected request %s", key)
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rp.status)
		w.Write(rp.body)
	}))
	t.Cleanup(s.Close)
	return s
}

// driver builds a Driver pointed at s with the given block options.
func (s *server) driver(t *testing.T, opts runpod.Options) *runpod.Driver {
	t.Helper()
	d, err := runpod.New("test-key", opts)
	if err != nil {
		t.Fatal(err)
	}
	d.BaseURL = s.URL
	d.GraphQLURL = s.URL + "/graphql"
	d.Log = func(string, ...any) {}
	return d
}

func ok(body []byte) reply               { return reply{http.StatusOK, body} }
func code(status int, body []byte) reply { return reply{status, body} }

func typeByName(types []provider.InstanceType, name string) (provider.InstanceType, bool) {
	for _, tt := range types {
		if tt.Type == name {
			return tt, true
		}
	}
	return provider.InstanceType{}, false
}

func TestInstanceTypesAreCountTokensPricedPerGPU(t *testing.T) {
	s := newServer(t, map[string]reply{"GET /catalog/gpus": ok(fixture(t, "gpus-secure.json"))})
	d := s.driver(t, runpod.Options{})
	types, err := d.InstanceTypes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		tok   string
		price float64
		mem   int
		gpu   string
		desc  string
	}{
		{"1xRTX-4090", 0.74, 24, "RTX 4090", "NVIDIA GeForce RTX 4090"},
		{"2xRTX-4090", 1.48, 48, "RTX 4090", "NVIDIA GeForce RTX 4090"},
		{"8xA100-SXM", 12.72, 640, "A100 SXM", "NVIDIA A100-SXM4-80GB"},
		{"1xPRO-6000-MIG-24GB", 0.59, 24, "PRO 6000 MIG 24GB", "NVIDIA RTX PRO 6000 Blackwell Server Edition MIG 1g.24gb"},
		{"1xRTX-A4000", 0.25, 16, "RTX A4000", "NVIDIA RTX A4000"}, // listed even with no stock
	}
	for _, c := range cases {
		got, found := typeByName(types, c.tok)
		if !found {
			t.Errorf("%s missing from %d types", c.tok, len(types))
			continue
		}
		if fmt.Sprintf("%.2f", got.PricePerHour) != fmt.Sprintf("%.2f", c.price) || got.GPUMemoryGB != c.mem || got.GPU != c.gpu || got.Description != c.desc || got.SpotPrice != 0 {
			t.Errorf("%s = %+v, want price %.2f mem %d gpu %s desc %s", c.tok, got, c.price, c.mem, c.gpu, c.desc)
		}
	}
	for _, absent := range []string{"16xPRO-6000-MIG-24GB", "3xRTX-4090", "1xunknown"} {
		if _, found := typeByName(types, absent); found {
			t.Errorf("%s should not be offered", absent)
		}
	}
}

func TestCommunityCloudCapsCountsAndPrices(t *testing.T) {
	s := newServer(t, map[string]reply{"GET /catalog/gpus": ok(fixture(t, "gpus-community.json"))})
	d := s.driver(t, runpod.Options{Cloud: "community"})
	types, err := d.InstanceTypes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, found := typeByName(types, "1xRTX-3090")
	if !found || fmt.Sprintf("%.2f", got.PricePerHour) != "0.22" {
		t.Errorf("1xRTX-3090 = %+v, %v; want community price 0.22", got, found)
	}
	if _, found := typeByName(types, "4xRTX-3090"); !found {
		t.Errorf("4xRTX-3090 missing: maxCount.community is 6")
	}
	if _, found := typeByName(types, "8xRTX-3090"); found {
		t.Errorf("8xRTX-3090 offered beyond maxCount.community 6")
	}
	if _, found := typeByName(types, "1xMI300X"); found {
		t.Errorf("MI300X offered on community cloud where maxCount is 0")
	}
}

func TestAvailabilityIsPerDatacenterOnSecureCloud(t *testing.T) {
	s := newServer(t, map[string]reply{"GET /catalog/gpus": ok(fixture(t, "gpus-secure.json"))})
	d := s.driver(t, runpod.Options{})
	avail, err := d.Availability(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(avail["EU-RO-1"], "1xRTX-4090") || !slices.Contains(avail["EU-RO-1"], "8xRTX-4090") {
		t.Errorf("EU-RO-1 = %v, want RTX 4090 tokens", avail["EU-RO-1"])
	}
	if !slices.Contains(avail["CA-MTL-1"], "1xA40") {
		t.Errorf("CA-MTL-1 = %v, want 1xA40 (datacenter LOW even though the overall level is NONE)", avail["CA-MTL-1"])
	}
	for loc, types := range avail {
		if slices.Contains(types, "1xRTX-A4000") || slices.Contains(types, "1xRTX-3090") {
			t.Errorf("%s lists a GPU with no stock: %v", loc, types)
		}
	}
	if _, has := avail[runpod.AnyLocation]; has {
		t.Errorf("secure stock is datacenter-bound; got %s: %v", runpod.AnyLocation, avail[runpod.AnyLocation])
	}
}

func TestAvailabilityOnCommunityCloudIsAnyLocation(t *testing.T) {
	s := newServer(t, map[string]reply{"GET /catalog/gpus": ok(fixture(t, "gpus-community.json"))})
	d := s.driver(t, runpod.Options{Cloud: "community"})
	avail, err := d.Availability(t.Context(), true) // spot flag is irrelevant
	if err != nil {
		t.Fatal(err)
	}
	if len(avail) != 1 {
		t.Fatalf("avail = %v, want only %q", avail, runpod.AnyLocation)
	}
	got := avail[runpod.AnyLocation]
	if !slices.Contains(got, "1xRTX-3090") || !slices.Contains(got, "2xA100-SXM") {
		t.Errorf("%s = %v, want the community GPUs with stock", runpod.AnyLocation, got)
	}
	if slices.Contains(got, "1xRTX-A4000") {
		t.Errorf("%s lists RTX A4000, which has no community stock", runpod.AnyLocation)
	}
}

func TestLocationsEndWithAny(t *testing.T) {
	s := newServer(t, map[string]reply{"GET /catalog/datacenters": ok(fixture(t, "datacenters.json"))})
	locs, err := s.driver(t, runpod.Options{}).Locations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, l := range locs {
		codes = append(codes, l.Code)
	}
	want := []string{"CA-MTL-1", "EU-CZ-1", "EU-RO-1", "US-TX-3", "any"}
	if !slices.Equal(codes, want) {
		t.Errorf("Locations = %v, want %v", codes, want)
	}
}

func TestImagesDefaultAndConfigured(t *testing.T) {
	s := newServer(t, nil)
	imgs, _ := s.driver(t, runpod.Options{}).Images(t.Context(), "1xRTX-4090")
	if len(imgs) != 1 || imgs[0].Name != runpod.DefaultImage || !imgs[0].Default {
		t.Errorf("default Images = %+v", imgs)
	}
	imgs, _ = s.driver(t, runpod.Options{Image: "me/mine:1"}).Images(t.Context(), "")
	if len(imgs) != 2 || imgs[0].Name != "me/mine:1" || !imgs[0].Default || imgs[1].Name != runpod.DefaultImage || imgs[1].Default {
		t.Errorf("configured Images = %+v", imgs)
	}
}

func TestCreateInstanceBody(t *testing.T) {
	s := newServer(t, map[string]reply{
		"GET /catalog/gpus": ok(fixture(t, "gpus-secure.json")),
		"POST /pods":        code(http.StatusCreated, fixture(t, "pod-starting.json")),
	})
	d := s.driver(t, runpod.Options{})
	in, err := d.CreateInstance(t.Context(), provider.CreateInstanceRequest{
		Type:            "1xrtx-4090", // case-insensitive
		Name:            "rig-smoke",
		Location:        "EU-RO-1",
		PublicKey:       "ssh-ed25519 AAAA rig\n",
		ExistingVolumes: []string{"nv-abc123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if in.ID != "9wxg5cwn7lhd2t" || in.Status != provider.Provisioning || in.Name != "rig-smoke" {
		t.Errorf("created = %+v", in)
	}
	body := s.bodies["POST /pods"][0]
	want := map[string]any{
		"name":          "rig-smoke",
		"image":         runpod.DefaultImage,
		"gpu":           map[string]any{"id": "NVIDIA GeForce RTX 4090", "count": float64(1)},
		"cloud":         "SECURE",
		"dataCenterIds": []any{"EU-RO-1"},
		"disk":          float64(20),
		"ports":         []any{"22/tcp"},
		"startSsh":      true,
		"env":           map[string]any{"PUBLIC_KEY": "ssh-ed25519 AAAA rig"},
		"mounts":        map[string]any{"network": []any{map[string]any{"volumeId": "nv-abc123", "path": "/workspace"}}},
		"args":          `bash -c '[ -f ~/.rig/onstart.sh ] && bash ~/.rig/onstart.sh; exec /start.sh'`,
	}
	for k, v := range want {
		if got := fmt.Sprint(body[k]); got != fmt.Sprint(v) {
			t.Errorf("body[%s] = %v, want %v", k, got, v)
		}
	}
	if len(body) != len(want) {
		t.Errorf("body has keys %v, want exactly %d", body, len(want))
	}
	if s.auth[0] != "Bearer test-key" {
		t.Errorf("Authorization = %q", s.auth[0])
	}
}

func TestCreateInstanceOmitsPlacementAndArgsWhenNotApplicable(t *testing.T) {
	s := newServer(t, map[string]reply{
		"GET /catalog/gpus": ok(fixture(t, "gpus-community.json")),
		"POST /pods":        code(http.StatusCreated, fixture(t, "pod-starting.json")),
	})
	d := s.driver(t, runpod.Options{Cloud: "community", Image: "nvidia/cuda:12.4.0-base", DiskGB: 40})
	_, err := d.CreateInstance(t.Context(), provider.CreateInstanceRequest{
		Type: "2xRTX-3090", Name: "rig-x", Location: runpod.AnyLocation, PublicKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := s.bodies["POST /pods"][0]
	for _, absent := range []string{"dataCenterIds", "mounts", "args"} {
		if _, has := body[absent]; has {
			t.Errorf("body has %s = %v; want it omitted", absent, body[absent])
		}
	}
	if body["cloud"] != "COMMUNITY" || body["disk"] != float64(40) || body["image"] != "nvidia/cuda:12.4.0-base" {
		t.Errorf("body = %v", body)
	}
	if gpu := body["gpu"].(map[string]any); gpu["count"] != float64(2) || gpu["id"] != "NVIDIA GeForce RTX 3090" {
		t.Errorf("gpu = %v", gpu)
	}
}

func TestCreateInstanceProfileDiskOverridesBlock(t *testing.T) {
	s := newServer(t, map[string]reply{
		"GET /catalog/gpus": ok(fixture(t, "gpus-secure.json")),
		"POST /pods":        code(http.StatusCreated, fixture(t, "pod-starting.json")),
	})
	d := s.driver(t, runpod.Options{DiskGB: 40})
	if _, err := d.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1xRTX-4090", Name: "rig-x", OSVolumeGB: 75}); err != nil {
		t.Fatal(err)
	}
	if got := s.bodies["POST /pods"][0]["disk"]; got != float64(75) {
		t.Errorf("disk = %v, want 75 (profile os_volume_gb)", got)
	}
}

func TestCreateInstanceNoStockIsErrNoCapacity(t *testing.T) {
	s := newServer(t, map[string]reply{
		"GET /catalog/gpus": ok(fixture(t, "gpus-secure.json")),
		"POST /pods":        code(http.StatusBadRequest, fixture(t, "create-nocapacity.json")),
	})
	_, err := s.driver(t, runpod.Options{}).CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1xRTX-4090", Name: "rig-x", Location: "EU-CZ-1"})
	if !errors.Is(err, provider.ErrNoCapacity) {
		t.Fatalf("err = %v, want ErrNoCapacity", err)
	}
}

func TestCreateInstanceOtherErrorsNameTheCall(t *testing.T) {
	s := newServer(t, map[string]reply{
		"GET /catalog/gpus": ok(fixture(t, "gpus-secure.json")),
		"POST /pods":        code(http.StatusUnprocessableEntity, []byte(`{"detail":"disk must be at least 5","status":422,"title":"Unprocessable Entity"}`)),
	})
	_, err := s.driver(t, runpod.Options{}).CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1xRTX-4090", Name: "rig-x"})
	want := "runpod: POST /pods: HTTP 422: disk must be at least 5"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
	if errors.Is(err, provider.ErrNoCapacity) {
		t.Errorf("a 422 must not read as no capacity")
	}
}

func TestCreateInstanceRefusesSpotAndUnknownTypes(t *testing.T) {
	s := newServer(t, map[string]reply{"GET /catalog/gpus": ok(fixture(t, "gpus-secure.json"))})
	d := s.driver(t, runpod.Options{})
	_, err := d.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1xRTX-4090", Spot: true})
	if !errors.Is(err, provider.ErrUnsupported) {
		t.Errorf("spot err = %v, want ErrUnsupported", err)
	}
	_, err = d.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1xRTX-9999"})
	if err == nil || !strings.Contains(err.Error(), `unknown GPU "RTX-9999"`) {
		t.Errorf("unknown gpu err = %v", err)
	}
	_, err = d.CreateInstance(t.Context(), provider.CreateInstanceRequest{Type: "1H100.80S.30V"})
	if err == nil || !strings.Contains(err.Error(), "<count>x<gpu>") {
		t.Errorf("verda-style type err = %v", err)
	}
	if len(s.bodies["POST /pods"]) != 0 {
		t.Errorf("refused creates must not reach the API")
	}
}

func TestGetInstanceMapsRunningPod(t *testing.T) {
	s := newServer(t, map[string]reply{
		"GET /catalog/gpus":        ok(fixture(t, "gpus-secure.json")),
		"GET /pods/9wxg5cwn7lhd2t": ok(fixture(t, "pod-running.json")),
	})
	in, err := s.driver(t, runpod.Options{}).GetInstance(t.Context(), "9wxg5cwn7lhd2t")
	if err != nil {
		t.Fatal(err)
	}
	want := provider.Endpoint{Host: "213.192.2.92", Port: 40076, User: "root"}
	if in.Status != provider.Running || in.RawStatus != "RUNNING" || in.SSH != want || in.IP != "213.192.2.92" {
		t.Errorf("running pod = %+v", in)
	}
	if in.Type != "1xRTX-3090" || in.GPU != "NVIDIA GeForce RTX 3090" || in.Location != "EU-CZ-1" || in.PricePerHour != 0.5 {
		t.Errorf("type/gpu/location/price = %s %s %s %v", in.Type, in.GPU, in.Location, in.PricePerHour)
	}
	if in.Image != runpod.DefaultImage || !slices.Equal(in.VolumeIDs, []string{"nv-abc123"}) || in.OSVolumeID != "" || in.Spot {
		t.Errorf("image/volumes = %s %v %q", in.Image, in.VolumeIDs, in.OSVolumeID)
	}
	if in.CreatedAt.Format("2006-01-02T15:04:05Z") != "2026-09-08T09:12:33Z" {
		t.Errorf("CreatedAt = %v", in.CreatedAt)
	}
}

func TestGetInstanceStatusAndEndpointVariants(t *testing.T) {
	cases := []struct {
		fixture string
		status  int
		want    provider.Status
		host    string
		port    int
	}{
		{"pod-running-nodirect.json", 200, provider.Running, "213.192.2.92", 40076}, // runtime.ports fallback
		{"pod-starting.json", 200, provider.Provisioning, "", 0},                    // RUNNING, no port published yet
		{"pod-exited.json", 200, provider.Stopped, "", 0},
		{"pod-notfound.json", 404, provider.Gone, "", 0},
	}
	for _, c := range cases {
		s := newServer(t, map[string]reply{
			"GET /catalog/gpus": ok(fixture(t, "gpus-secure.json")),
			"GET /pods/p":       code(c.status, fixture(t, c.fixture)),
		})
		in, err := s.driver(t, runpod.Options{}).GetInstance(t.Context(), "p")
		if err != nil {
			t.Errorf("%s: %v", c.fixture, err)
			continue
		}
		if in.Status != c.want || in.SSH.Host != c.host || in.SSH.Port != c.port || in.IP != c.host {
			t.Errorf("%s: status %s ssh %+v ip %q; want %s %s:%d", c.fixture, in.Status, in.SSH, in.IP, c.want, c.host, c.port)
		}
	}
}

func TestGetInstanceTerminatedIsGone(t *testing.T) {
	body := strings.Replace(string(fixture(t, "pod-exited.json")), `"status": "EXITED"`, `"status": "TERMINATED"`, 1)
	s := newServer(t, map[string]reply{
		"GET /catalog/gpus": ok(fixture(t, "gpus-secure.json")),
		"GET /pods/p":       ok([]byte(body)),
	})
	in, err := s.driver(t, runpod.Options{}).GetInstance(t.Context(), "p")
	if err != nil || in.Status != provider.Gone || in.RawStatus != "TERMINATED" {
		t.Errorf("terminated = %+v, %v", in, err)
	}
}

func TestListInstancesKeepsOnlyRigPods(t *testing.T) {
	running := string(fixture(t, "pod-running.json"))
	other := strings.Replace(strings.Replace(running, `"rig-smoke"`, `"jupyter-playground"`, 1), `"9wxg5cwn7lhd2t"`, `"other1"`, 1)
	list := `{"pods":[` + running + `,` + other + `]}`
	s := newServer(t, map[string]reply{
		"GET /pods":         ok([]byte(list)),
		"GET /catalog/gpus": ok(fixture(t, "gpus-secure.json")),
	})
	ins, err := s.driver(t, runpod.Options{}).ListInstances(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 1 || ins[0].Name != "rig-smoke" || ins[0].Type != "1xRTX-3090" {
		t.Errorf("ListInstances = %+v, want only rig-smoke", ins)
	}
}

func TestLifecycleActions(t *testing.T) {
	s := newServer(t, map[string]reply{
		"POST /pods/p/action": ok(fixture(t, "pod-exited.json")),
	})
	d := s.driver(t, runpod.Options{})
	if err := d.ShutdownInstance(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	if err := d.StartInstance(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	got := s.bodies["POST /pods/p/action"]
	if len(got) != 2 || got[0]["action"] != "stop" || got[1]["action"] != "start" {
		t.Errorf("actions = %v, want stop then start", got)
	}
}

func TestDeleteInstanceTerminatesThenDeletesVolumes(t *testing.T) {
	s := newServer(t, map[string]reply{
		"POST /pods/p/action":               code(http.StatusNoContent, nil),
		"DELETE /network-volumes/nv-abc123": code(http.StatusNoContent, nil),
	})
	if err := s.driver(t, runpod.Options{}).DeleteInstance(t.Context(), "p", []string{"nv-abc123"}); err != nil {
		t.Fatal(err)
	}
	if got := s.bodies["POST /pods/p/action"]; len(got) != 1 || got[0]["action"] != "terminate" {
		t.Errorf("actions = %v, want terminate", got)
	}
}

func TestDeleteInstanceGoneAlreadyIsSuccess(t *testing.T) {
	s := newServer(t, map[string]reply{
		"POST /pods/p/action": code(http.StatusNotFound, fixture(t, "pod-notfound.json")),
	})
	if err := s.driver(t, runpod.Options{}).DeleteInstance(t.Context(), "p", nil); err != nil {
		t.Errorf("terminate of a missing pod = %v, want nil", err)
	}
}

func TestDeleteInstanceOtherErrorsSurface(t *testing.T) {
	s := newServer(t, map[string]reply{
		"POST /pods/p/action": code(http.StatusConflict, []byte(`{"detail":"pod is locked","status":409,"title":"Conflict"}`)),
	})
	err := s.driver(t, runpod.Options{}).DeleteInstance(t.Context(), "p", nil)
	want := "runpod: POST /pods/p/action: HTTP 409: pod is locked"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
}

func TestVolumesCarryDatacenterAndMountingPods(t *testing.T) {
	s := newServer(t, map[string]reply{
		"GET /network-volumes": ok(fixture(t, "volumes.json")),
		"GET /pods":            ok([]byte(`{"pods":[` + string(fixture(t, "pod-running.json")) + `]}`)),
	})
	vols, err := s.driver(t, runpod.Options{}).ListVolumes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 2 {
		t.Fatalf("ListVolumes = %+v", vols)
	}
	data := vols[0]
	if data.ID != "nv-abc123" || data.Name != "data" || data.SizeGB != 50 || data.Location != "EU-CZ-1" || data.Type != "STANDARD" {
		t.Errorf("data = %+v", data)
	}
	if !data.IsAttached() || !slices.Equal(data.InstanceIDs, []string{"9wxg5cwn7lhd2t"}) || data.Status != "attached" {
		t.Errorf("data should be attached to the running pod: %+v", data)
	}
	if vols[1].IsAttached() || vols[1].Status != "available" || vols[1].Location != "US-TX-3" {
		t.Errorf("scratch = %+v", vols[1])
	}
	if data.MountCommand != "" || data.MkdirCommand != "" {
		t.Errorf("network volumes are mounted at create; commands must be empty: %+v", data)
	}
}

func TestGetVolume(t *testing.T) {
	s := newServer(t, map[string]reply{
		"GET /network-volumes/nv-def456": ok([]byte(`{"id":"nv-def456","name":"scratch","size":10,"dataCenter":"US-TX-3","type":"HIGH_PERFORMANCE"}`)),
		"GET /pods":                      ok([]byte(`{"pods":[]}`)),
	})
	v, err := s.driver(t, runpod.Options{}).GetVolume(t.Context(), "nv-def456")
	if err != nil {
		t.Fatal(err)
	}
	if v.Name != "scratch" || v.Location != "US-TX-3" || v.IsAttached() {
		t.Errorf("GetVolume = %+v", v)
	}
}

func TestCreateVolumeClampsToTenGB(t *testing.T) {
	s := newServer(t, map[string]reply{
		"POST /network-volumes": code(http.StatusCreated, []byte(`{"id":"nv-new","name":"data","size":10,"dataCenter":"EU-CZ-1","type":"STANDARD"}`)),
	})
	d := s.driver(t, runpod.Options{})
	var logged []string
	d.Log = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	id, err := d.CreateVolume(t.Context(), provider.CreateVolumeRequest{Name: "data", SizeGB: 5, Location: "EU-CZ-1"})
	if err != nil || id != "nv-new" {
		t.Fatalf("CreateVolume = %q, %v", id, err)
	}
	body := s.bodies["POST /network-volumes"][0]
	if body["size"] != float64(10) || body["dataCenter"] != "EU-CZ-1" || body["name"] != "data" {
		t.Errorf("body = %v", body)
	}
	if _, has := body["type"]; has {
		t.Errorf("type must be omitted so the datacenter default applies: %v", body)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "10GB") {
		t.Errorf("clamp should be logged once, got %v", logged)
	}
}

func TestAttachDetachUnsupported(t *testing.T) {
	d := newServer(t, nil).driver(t, runpod.Options{})
	if err := d.AttachVolume(t.Context(), "v", "p"); !errors.Is(err, provider.ErrUnsupported) {
		t.Errorf("Attach = %v", err)
	}
	if err := d.DetachVolume(t.Context(), "v", "p"); !errors.Is(err, provider.ErrUnsupported) {
		t.Errorf("Detach = %v", err)
	}
}

func TestEnsureSSHKeyRegistersNothing(t *testing.T) {
	d := newServer(t, nil).driver(t, runpod.Options{})
	id, err := d.EnsureSSHKey(t.Context(), "rig-me-key", "ssh-ed25519 AAAA")
	if err != nil || id != "" {
		t.Errorf("EnsureSSHKey = %q, %v; want \"\", nil", id, err)
	}
}

func TestCapabilities(t *testing.T) {
	caps := newServer(t, nil).driver(t, runpod.Options{}).Capabilities()
	if caps.Spot || !caps.Volumes || !caps.Balance || !caps.DockerImages {
		t.Errorf("Capabilities = %+v", caps)
	}
	want := `. /etc/rp_environment && curl -fsS -X POST "https://api.runpod.io/v2/pods/$RUNPOD_POD_ID/action" -H "Authorization: Bearer $RUNPOD_API_KEY" -H 'Content-Type: application/json' -d '{"action":"stop"}'`
	if caps.SelfStop != want {
		t.Errorf("SelfStop = %s\nwant %s", caps.SelfStop, want)
	}
}

func TestBalanceViaGraphQL(t *testing.T) {
	s := newServer(t, map[string]reply{"POST /graphql": ok(fixture(t, "balance.json"))})
	amount, cur, err := s.driver(t, runpod.Options{}).Balance(t.Context())
	if err != nil || amount != 10.7488301138 || cur != "USD" {
		t.Errorf("Balance = %v %s %v", amount, cur, err)
	}
	if q := s.bodies["POST /graphql"][0]["query"]; q != "{ myself { clientBalance } }" {
		t.Errorf("query = %v", q)
	}
}

func TestBalanceFailureIsUnsupported(t *testing.T) {
	for name, rp := range map[string]reply{
		"http error":    code(http.StatusUnauthorized, []byte(`{"errors":[{"message":"Unauthorized"}]}`)),
		"graphql error": ok([]byte(`{"errors":[{"message":"Cannot query field clientBalance"}],"data":null}`)),
	} {
		s := newServer(t, map[string]reply{"POST /graphql": rp})
		_, _, err := s.driver(t, runpod.Options{}).Balance(t.Context())
		if !errors.Is(err, provider.ErrUnsupported) {
			t.Errorf("%s: err = %v, want ErrUnsupported", name, err)
		}
	}
}

func TestNewRejectsUnknownCloud(t *testing.T) {
	if _, err := runpod.New("k", runpod.Options{Cloud: "hybrid"}); err == nil || !strings.Contains(err.Error(), "secure or community") {
		t.Errorf("err = %v", err)
	}
}

func TestRegisteredFactoryAcceptsEitherKeyName(t *testing.T) {
	var pc config.ProviderConfig
	if err := yaml.Unmarshal([]byte("cloud: community\nimage: me/img:2\ndisk_gb: 30\n"), &pc); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNPOD_API_KEY", "")
	t.Setenv("RUNPOD_KEY", "team-key")
	p, err := provider.Open("runpod", pc)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "runpod" {
		t.Errorf("Name = %s", p.Name())
	}
	imgs, _ := p.Images(t.Context(), "")
	if imgs[0].Name != "me/img:2" {
		t.Errorf("block image not applied: %+v", imgs)
	}

	t.Setenv("RUNPOD_KEY", "")
	_, err = provider.Open("runpod", pc)
	if err == nil || !strings.Contains(err.Error(), "RUNPOD_API_KEY") || !strings.Contains(err.Error(), "RUNPOD_KEY") {
		t.Errorf("missing-key err = %v, want both names mentioned", err)
	}
}

// pod-running-live.json is the smoke pod exactly as GET /pods returned it on
// 2026-09-08 (key scrubbed): mounts is {} rather than null when no volume is
// attached, and runtime.ports carries RunPod's own http port beside 22/tcp.
func TestGetInstanceMapsLiveCapture(t *testing.T) {
	s := newServer(t, map[string]reply{
		"GET /catalog/gpus":        ok(fixture(t, "gpus-secure.json")),
		"GET /pods/wz1qj98m73wfve": ok(fixture(t, "pod-running-live.json")),
	})
	in, err := s.driver(t, runpod.Options{}).GetInstance(t.Context(), "wz1qj98m73wfve")
	if err != nil {
		t.Fatal(err)
	}
	want := provider.Endpoint{Host: "213.192.2.102", Port: 40088, User: "root"}
	if in.Status != provider.Running || in.SSH != want || in.IP != "213.192.2.102" {
		t.Errorf("live pod = %+v", in)
	}
	if in.Type != "1xRTX-4090" || in.PricePerHour != 0.74 || in.Location != "EU-CZ-1" || len(in.VolumeIDs) != 0 {
		t.Errorf("type/price/location/volumes = %s %v %s %v", in.Type, in.PricePerHour, in.Location, in.VolumeIDs)
	}
	if in.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z") != "2026-09-08T11:00:07.841Z" {
		t.Errorf("CreatedAt = %v", in.CreatedAt)
	}
}
