package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
)

// hostPort renders an ssh endpoint for a human table: the host alone on the
// default port (0 or 22, as every VM is), host:port for a container's mapped
// sshd.
func hostPort(host string, port int) string {
	if port == 0 || port == 22 {
		return host
	}
	return fmt.Sprintf("%s:%d", host, port)
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
