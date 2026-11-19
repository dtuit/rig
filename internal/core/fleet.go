package core

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/provider"
)

// Fleet drives every provider one config knows about. An App is one cloud;
// a Fleet is the workstation's whole rig estate, which is what the
// dashboard shows and what a burn rate has to sum over. Drivers are opened
// lazily and once: a provider whose credentials are missing costs one error
// on the row it belongs to, not the whole view.
type Fleet struct {
	Cfg *config.Config
	Log func(format string, a ...any)

	// Open builds the driver for a provider name. Nil is the production
	// path: provider.Open against this config's block for it. Tests
	// substitute fakes, which the registry cannot hold (a driver registers
	// itself from init(), and only the CLI imports the real ones).
	Open func(name string) (provider.Provider, error)

	names []string
	mu    sync.Mutex
	apps  map[string]*App
}

// NewFleet returns a Fleet over the named providers, in the order given.
// With no names it drives every provider the config knows (ProviderNames).
func NewFleet(cfg *config.Config, log func(string, ...any), names ...string) *Fleet {
	if len(names) == 0 {
		names = ProviderNames(cfg)
	}
	return &Fleet{Cfg: cfg, Log: log, names: names, apps: map[string]*App{}}
}

// ProviderNames lists the providers a config drives: the default one first
// — it is the one every command without --provider uses — then every other
// providers: block alphabetically. The default is included even with no
// block of its own, because a single-provider rig configures Verda through
// the legacy top-level credentials_file, or through the environment alone.
func ProviderNames(cfg *config.Config) []string {
	dflt := cfg.Provider
	if dflt == "" {
		dflt = "verda"
	}
	rest := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		if name != dflt {
			rest = append(rest, name)
		}
	}
	slices.Sort(rest)
	return append([]string{dflt}, rest...)
}

// Names lists the providers this Fleet drives, in display order.
func (f *Fleet) Names() []string { return slices.Clone(f.names) }

// App returns the App driving name, opening its driver on first use. The
// same App is returned for every later call, so a survey and the delete it
// leads to talk to one driver.
func (f *Fleet) App(name string) (*App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.apps[name]; ok {
		return a, nil
	}
	open := f.Open
	if open == nil {
		open = func(n string) (provider.Provider, error) {
			return provider.Open(n, f.Cfg.ProviderConfig(n))
		}
	}
	prov, err := open(name)
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: f.Cfg, Prov: prov, Log: f.Log}
	if f.apps == nil {
		f.apps = map[string]*App{}
	}
	f.apps[name] = a
	return a, nil
}

// Survey is one provider's answer to "what have I got running here". Err
// set means the provider could not be asked at all — a driver that would
// not open, or a listing that failed — and the other fields are empty;
// every one of them is otherwise filled as far as the cloud allows, so a
// balance a provider cannot report is absent rather than zero.
type Survey struct {
	Provider   string
	Instances  []provider.Instance
	Volumes    []provider.Volume
	Balance    float64
	Currency   string
	HasBalance bool
	Err        error
}

// Survey asks every provider in the Fleet what it is running, in parallel,
// and answers in Names() order. It is the whole question the dashboard has:
// instances, volumes and balances across the clouds, with each cloud's
// failure kept to its own row.
//
// Only the instance listing can fail a Survey. Volumes and balance are
// decoration — a driver that answers ErrUnsupported (or refuses the call)
// simply contributes none, exactly as the single-provider dashboard did.
func (f *Fleet) Survey(ctx context.Context) []Survey {
	out := make([]Survey, len(f.names))
	var wg sync.WaitGroup
	for i, name := range f.names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = f.survey(ctx, name)
		}()
	}
	wg.Wait()
	// The cache is reconciled here rather than inside the goroutines: it is
	// one file, rewritten whole (see App.managed).
	for _, s := range out {
		if s.Err != nil {
			continue
		}
		if a, err := f.App(s.Provider); err == nil {
			a.reconcile(s.Instances)
		}
	}
	return out
}

func (f *Fleet) survey(ctx context.Context, name string) Survey {
	s := Survey{Provider: name}
	a, err := f.App(name)
	if err != nil {
		s.Err = err
		return s
	}
	if s.Instances, err = a.managed(ctx); err != nil {
		s.Err = err
		return s
	}
	s.Volumes, _ = a.Prov.ListVolumes(ctx)
	if a.Prov.Capabilities().Balance {
		if amt, cur, err := a.Prov.Balance(ctx); err == nil {
			s.Balance, s.Currency, s.HasBalance = amt, cur, true
		}
	}
	return s
}

// FindApp returns the App that owns in — the one driving the cloud the
// instance was listed from. An instance carries its provider from the
// moment core stamped it (App.stamp), which is what lets one dashboard
// act on rows from several clouds.
func (f *Fleet) FindApp(in provider.Instance) (*App, error) {
	if in.Provider == "" {
		return nil, fmt.Errorf("instance %s carries no provider", in.Name)
	}
	return f.App(in.Provider)
}
