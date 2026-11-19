package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/instcache"
	"github.com/dtuit/rig/internal/provider"
	"github.com/dtuit/rig/internal/provider/fake"
)

// newFleet wires the named fakes into a Fleet whose local state lives under
// a temp dir, so nothing of the developer's is read or written.
func newFleet(t *testing.T, provs ...*fake.Provider) *Fleet {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("RIG_CONFIG", filepath.Join(dir, "config.yml"))
	byName := map[string]provider.Provider{}
	names := make([]string, 0, len(provs))
	for _, p := range provs {
		byName[p.Name()] = p
		names = append(names, p.Name())
	}
	f := NewFleet(&config.Config{}, func(string, ...any) {}, names...)
	f.Open = func(name string) (provider.Provider, error) {
		p, ok := byName[name]
		if !ok {
			return nil, errors.New("no such provider")
		}
		return p, nil
	}
	return f
}

func TestProviderNamesPutsTheDefaultFirst(t *testing.T) {
	cfg := &config.Config{
		Provider: "runpod",
		Providers: map[string]config.ProviderConfig{
			"vast": {}, "verda": {}, "runpod": {}, "aws": {},
		},
	}
	want := []string{"runpod", "aws", "vast", "verda"}
	if got := ProviderNames(cfg); !equalStrings(got, want) {
		t.Errorf("ProviderNames = %v, want %v", got, want)
	}
}

func TestProviderNamesIncludesADefaultWithNoBlockOfItsOwn(t *testing.T) {
	// the single-provider rig: Verda through the legacy top-level
	// credentials_file, or through the environment alone
	if got := ProviderNames(&config.Config{}); !equalStrings(got, []string{"verda"}) {
		t.Errorf("ProviderNames of an empty config = %v, want [verda]", got)
	}
}

func TestSurveyAnswersEveryProviderInOrder(t *testing.T) {
	one, two := fake.Named("verda"), fake.Named("runpod")
	one.Put(provider.Instance{ID: "a", Name: "rig-train", Status: provider.Running, PricePerHour: 1.29})
	two.Put(provider.Instance{ID: "b", Name: "rig-infer", Status: provider.Running, PricePerHour: 0.44})
	two.Bal, two.Cur = 18.22, "USD"
	f := newFleet(t, one, two)

	got := f.Survey(context.Background())
	if len(got) != 2 {
		t.Fatalf("want a survey per provider, got %d", len(got))
	}
	if got[0].Provider != "verda" || got[1].Provider != "runpod" {
		t.Errorf("surveys out of order: %s, %s", got[0].Provider, got[1].Provider)
	}
	for _, s := range got {
		if s.Err != nil {
			t.Fatalf("%s: %v", s.Provider, s.Err)
		}
		if len(s.Instances) != 1 {
			t.Fatalf("%s: want 1 instance, got %d", s.Provider, len(s.Instances))
		}
		if s.Instances[0].Provider != s.Provider {
			t.Errorf("%s: instance stamped %q", s.Provider, s.Instances[0].Provider)
		}
	}
	if !got[1].HasBalance || got[1].Balance != 18.22 || got[1].Currency != "USD" {
		t.Errorf("runpod balance = %v %s (has=%v)", got[1].Balance, got[1].Currency, got[1].HasBalance)
	}
}

func TestSurveyKeepsOneCloudsFailureToItsOwnAnswer(t *testing.T) {
	broken, working := fake.Named("aws"), fake.Named("verda")
	broken.ListErr = errors.New("expired SSO token")
	working.Put(provider.Instance{ID: "a", Name: "rig-train", Status: provider.Running})
	f := newFleet(t, broken, working)

	got := f.Survey(context.Background())
	if got[0].Err == nil {
		t.Error("aws survey reports no error")
	}
	if got[1].Err != nil {
		t.Errorf("verda survey spoiled by aws: %v", got[1].Err)
	}
	if len(got[1].Instances) != 1 {
		t.Errorf("verda lost its instance: %v", got[1].Instances)
	}
}

func TestSurveyReportsADriverThatWillNotOpen(t *testing.T) {
	f := newFleet(t, fake.Named("verda"))
	f.names = append(f.names, "nonesuch")

	got := f.Survey(context.Background())
	if len(got) != 2 || got[1].Err == nil {
		t.Fatalf("an unopenable driver is not reported: %+v", got)
	}
	if got[0].Err != nil {
		t.Errorf("verda spoiled by it: %v", got[0].Err)
	}
}

func TestSurveyOmitsABalanceTheCloudCannotReport(t *testing.T) {
	p := fake.Named("aws")
	p.Caps = provider.Capabilities{}
	f := newFleet(t, p)

	if got := f.Survey(context.Background()); got[0].HasBalance {
		t.Errorf("aws reported a balance of %v", got[0].Balance)
	}
}

// The cache is one file, rewritten whole. Two providers reconciled in
// parallel would lose one another's updates, which is why Survey fans the
// provider calls out but reconciles serially (App.managed).
func TestSurveyRefreshesEveryProvidersCacheEntry(t *testing.T) {
	one, two := fake.Named("verda"), fake.Named("runpod")
	one.Put(provider.Instance{ID: "a", Name: "rig-train", Status: provider.Running, IP: "10.0.0.1"})
	two.Put(provider.Instance{ID: "b", Name: "rig-infer", Status: provider.Running, IP: "10.0.0.2"})
	f := newFleet(t, one, two)
	// as up would have left them, at the addresses they had yesterday
	stale := time.Now().Add(-time.Hour)
	for _, in := range []provider.Instance{
		{ID: "a", Name: "rig-train", Provider: "verda", Status: provider.Running, IP: "10.9.9.9"},
		{ID: "b", Name: "rig-infer", Provider: "runpod", Status: provider.Running, IP: "10.9.9.9"},
	} {
		if err := instcache.Put(instcache.Path(), in, stale); err != nil {
			t.Fatal(err)
		}
	}

	f.Survey(context.Background())

	for _, tc := range []struct{ prov, key, ip string }{
		{"verda", "train", "10.0.0.1"},
		{"runpod", "infer", "10.0.0.2"},
	} {
		a, err := f.App(tc.prov)
		if err != nil {
			t.Fatal(err)
		}
		in := a.cached(tc.key)
		if in == nil {
			t.Errorf("%s lost its cache entry", tc.prov)
			continue
		}
		if in.IP != tc.ip {
			t.Errorf("%s cached %s, want the fresh %s", tc.prov, in.IP, tc.ip)
		}
	}
}

func TestAppIsOpenedOnceAndReused(t *testing.T) {
	f := newFleet(t, fake.Named("verda"))
	opens := 0
	inner := f.Open
	f.Open = func(name string) (provider.Provider, error) {
		opens++
		return inner(name)
	}
	for range 3 {
		if _, err := f.App("verda"); err != nil {
			t.Fatal(err)
		}
	}
	if opens != 1 {
		t.Errorf("driver opened %d times, want 1", opens)
	}
}

func TestFindAppFollowsTheInstancesOwnProvider(t *testing.T) {
	one, two := fake.Named("verda"), fake.Named("runpod")
	f := newFleet(t, one, two)

	a, err := f.FindApp(provider.Instance{Name: "rig-infer", Provider: "runpod"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Provider() != "runpod" {
		t.Errorf("FindApp gave %s", a.Provider())
	}
	if _, err := f.FindApp(provider.Instance{Name: "rig-x"}); err == nil {
		t.Error("an unstamped instance should not resolve to any app")
	}
}

func TestSurveyIgnoresInstancesRigDoesNotManage(t *testing.T) {
	p := fake.Named("verda")
	p.Put(provider.Instance{ID: "a", Name: "rig-train", Status: provider.Running, CreatedAt: time.Now()})
	p.Put(provider.Instance{ID: "b", Name: "someones-laptop", Status: provider.Running})
	f := newFleet(t, p)

	got := f.Survey(context.Background())
	if len(got[0].Instances) != 1 || got[0].Instances[0].Name != "rig-train" {
		t.Errorf("survey picked up more than rig's own: %+v", got[0].Instances)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
