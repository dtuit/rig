package cli

import (
	"io"
	"strings"
	"testing"
)

// `rig logs` takes a job, `rig logs --watchdog` takes none, and asking for
// both or neither has to say which — the check runs before any provider is
// opened, so a mistyped command costs no cloud call.
func TestLogsArgumentRules(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"logs"}, "which job?"},
		{[]string{"logs", "fit-7b", "--watchdog"}, "--watchdog takes no job name"},
	} {
		root := NewRoot()
		root.SetArgs(tc.args)
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		err := root.Execute()
		if err == nil {
			t.Errorf("rig %v was accepted", tc.args)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("rig %v said %q, want it to mention %q", tc.args, err, tc.want)
		}
	}
}
