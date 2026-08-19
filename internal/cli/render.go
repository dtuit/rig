package cli

import (
	"encoding/json"
	"os"
	"reflect"
)

// renderJSON emits v for machine consumption (ADR-0002): the existing structs,
// one JSON document, no prose. A nil slice renders as [] — empty lists are
// data, not null.
func renderJSON(v any) error {
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Slice && rv.IsNil() {
		v = []struct{}{}
	}
	return json.NewEncoder(os.Stdout).Encode(v)
}
