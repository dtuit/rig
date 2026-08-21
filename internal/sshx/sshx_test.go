package sshx

import (
	"reflect"
	"slices"
	"testing"
)

// flagValues collects the value following every occurrence of flag in args.
func flagValues(args []string, flag string) []string {
	var vals []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			vals = append(vals, args[i+1])
		}
	}
	return vals
}

func TestTunnelArgs(t *testing.T) {
	target := Target{User: "ubuntu", Host: "10.0.0.5"}
	forwards := [][2]int{{8000, 8000}, {9090, 80}}
	args := target.TunnelArgs(forwards)

	base := target.baseArgs()
	if len(args) < len(base) || !reflect.DeepEqual(args[:len(base)], base) {
		t.Errorf("args should start with the unchanged base options\ngot  %v\nbase %v", args, base)
	}

	opts := flagValues(args, "-o")
	for _, want := range []string{"ExitOnForwardFailure=yes", "ServerAliveInterval=15", "ServerAliveCountMax=3"} {
		if !slices.Contains(opts, want) {
			t.Errorf("missing -o %s in %v", want, args)
		}
	}

	want := []string{"8000:localhost:8000", "9090:localhost:80"}
	if ls := flagValues(args, "-L"); !reflect.DeepEqual(ls, want) {
		t.Errorf("-L forwards = %v, want %v", ls, want)
	}

	n := len(args)
	if n < 2 || args[n-2] != "-N" || args[n-1] != "ubuntu@10.0.0.5" {
		t.Errorf("args should end with -N user@host, got %v", args[max(0, n-2):])
	}
}

// A supervised tunnel's ssh child must own its connection: multiplexing
// options must come first (ssh takes the first value per keyword), or the
// forward is delegated to the persistent ControlMaster and the child exits
// immediately — unsupervivable.
func TestSupervisedTunnelCommandDisablesMultiplexing(t *testing.T) {
	target := Target{User: "ubuntu", Host: "10.0.0.5"}
	args := target.SupervisedTunnelCommand([][2]int{{8000, 8000}}).Args
	want := []string{"ssh", "-o", "ControlMaster=no", "-o", "ControlPath=none"}
	if len(args) < len(want) || !reflect.DeepEqual(args[:len(want)], want) {
		t.Errorf("args = %v, want prefix %v", args, want)
	}
	rest := args[len(want):]
	if !reflect.DeepEqual(rest, target.TunnelArgs([][2]int{{8000, 8000}})) {
		t.Errorf("after the multiplexing override, args should be TunnelArgs unchanged, got %v", rest)
	}
}

// TunnelArgs must be pure: same inputs, same output, no input mutated.
func TestTunnelArgsPure(t *testing.T) {
	target := Target{User: "ubuntu", Host: "10.0.0.5", ForwardAgent: true, IdentityFile: "/k"}
	forwards := [][2]int{{8000, 8000}}
	orig := [][2]int{{8000, 8000}}
	a := target.TunnelArgs(forwards)
	b := target.TunnelArgs(forwards)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("not deterministic:\n%v\n%v", a, b)
	}
	if !reflect.DeepEqual(forwards, orig) {
		t.Errorf("forwards mutated: %v", forwards)
	}
}
