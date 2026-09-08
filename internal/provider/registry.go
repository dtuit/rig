package provider

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dtuit/rig/internal/config"
)

// Factory builds a Provider from its providers: block; it resolves the
// driver's credentials via cfg.Creds and decodes its own keys via cfg.Decode.
type Factory func(cfg config.ProviderConfig) (Provider, error)

var registry = map[string]Factory{}

// Register makes a driver available to Open under name. Drivers call it from
// init(); the CLI blank-imports the driver packages it ships.
func Register(name string, f Factory) {
	if _, dup := registry[name]; dup {
		panic("provider: Register called twice for " + name)
	}
	registry[name] = f
}

// Open builds the named provider, or errors listing the registered names.
func Open(name string, cfg config.ProviderConfig) (Provider, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (have: %s)", name, strings.Join(Names(), ", "))
	}
	return f(cfg)
}

// Names lists the registered providers, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
