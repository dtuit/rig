package cli

import (
	"encoding/json"
	"os"
	"reflect"

	"github.com/dtuit/rig/internal/config"
)

// severalProviders reports whether a human table must name each row's
// provider: true when more than one cloud is in play. One rule, two
// readings, because the two tables see different halves of it — `rig status`
// lists one provider's instances at a time, so only the config knows the
// user drives others, while `rig spend` reads a ledger that outlives a
// provider block. rowProviders is the provider of every row on screen.
func severalProviders(cfg *config.Config, rowProviders []string) bool {
	if cfg != nil && len(cfg.Providers) > 1 {
		return true
	}
	seen := map[string]bool{}
	for _, p := range rowProviders {
		seen[p] = true
	}
	return len(seen) > 1
}

// renderJSON emits v for machine consumption (ADR-0002): the existing structs,
// one JSON document, no prose. A nil slice renders as [] — empty lists are
// data, not null.
func renderJSON(v any) error {
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Slice && rv.IsNil() {
		v = []struct{}{}
	}
	return json.NewEncoder(os.Stdout).Encode(v)
}
