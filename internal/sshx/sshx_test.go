package sshx

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
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

// Progress output is opt-in (issue 09); everything else about the argv is
// unchanged by the knob, and src/dest reach rsync exactly as given.
func TestRsyncArgsProgressKnob(t *testing.T) {
	target := Target{User: "root", Host: "10.0.0.5"}
	excl := []string{".git", "*.ckpt"}
	loud := target.rsyncArgs("./data/", "~/data/", excl, true, true)
	quiet := target.rsyncArgs("./data/", "~/data/", excl, true, false)

	if !slices.Contains(loud, "--info=progress2") {
		t.Errorf("progress=true should add --info=progress2: %v", loud)
	}
	if slices.Contains(quiet, "--info=progress2") {
		t.Errorf("progress=false must drop --info=progress2: %v", quiet)
	}
	if len(loud) != len(quiet)+1 {
		t.Errorf("the knob should change exactly one argument:\nloud  %v\nquiet %v", loud, quiet)
	}
	if got := flagValues(quiet, "--exclude"); !reflect.DeepEqual(got, excl) {
		t.Errorf("excludes = %v, want %v", got, excl)
	}
	if !slices.Contains(quiet, "--delete") {
		t.Errorf("delete=true should add --delete: %v", quiet)
	}
	n := len(quiet)
	if quiet[n-2] != "./data/" || quiet[n-1] != "root@10.0.0.5:~/data/" {
		t.Errorf("args should end with src user@host:dest as given, got %v", quiet[n-2:])
	}

	file := target.rsyncArgs("/tmp/secrets.env", "~/secrets.env", nil, false, false)
	if file[len(file)-2] != "/tmp/secrets.env" || slices.Contains(file, "--delete") {
		t.Errorf("a file source must pass through untouched and without --delete: %v", file)
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

func TestNoMultiplexReplacesControlMaster(t *testing.T) {
	shared := Target{User: "root", Host: "h"}.baseArgs()
	own := Target{User: "root", Host: "h", NoMultiplex: true}.baseArgs()
	has := func(args []string, s string) bool {
		for _, a := range args {
			if a == s {
				return true
			}
		}
		return false
	}
	if !has(shared, "ControlMaster=auto") || has(shared, "ControlMaster=no") {
		t.Fatalf("shared target should multiplex: %v", shared)
	}
	if !has(own, "ControlMaster=no") || !has(own, "ControlPath=none") || has(own, "ControlMaster=auto") {
		t.Fatalf("NoMultiplex target should own its connection: %v", own)
	}
}

// A provider endpoint on the default port must leave every argv exactly as
// before: no -p, and Port 0 and Port 22 are the same target.
func TestDefaultPortLeavesArgvUnchanged(t *testing.T) {
	zero := Target{User: "root", Host: "10.0.0.5"}
	std := Target{User: "root", Host: "10.0.0.5", Port: 22}
	if got := zero.baseArgs(); slices.Contains(got, "-p") {
		t.Errorf("Port 0 must not emit -p: %v", got)
	}
	if !reflect.DeepEqual(zero.baseArgs(), std.baseArgs()) {
		t.Errorf("Port 22 must equal Port 0:\n%v\n%v", zero.baseArgs(), std.baseArgs())
	}
	if !reflect.DeepEqual(zero.rsyncArgs("./d/", "~/d/", nil, false, false), std.rsyncArgs("./d/", "~/d/", nil, false, false)) {
		t.Errorf("rsync argv differs between Port 0 and Port 22")
	}
	if !reflect.DeepEqual(zero.TunnelArgs([][2]int{{8000, 8000}}), std.TunnelArgs([][2]int{{8000, 8000}})) {
		t.Errorf("tunnel argv differs between Port 0 and Port 22")
	}
}

// A container's host-mapped sshd port (RunPod, Vast) reaches every builder:
// ssh gets -p, rsync's -e transport carries the same, the tunnel argv too.
func TestPortOnEveryArgvBuilder(t *testing.T) {
	target := Target{User: "root", Host: "1.2.3.4", Port: 10341}

	if got := flagValues(target.baseArgs(), "-p"); !reflect.DeepEqual(got, []string{"10341"}) {
		t.Errorf("ssh -p = %v, want [10341]", got)
	}

	tun := target.TunnelArgs([][2]int{{8000, 8000}})
	if got := flagValues(tun, "-p"); !reflect.DeepEqual(got, []string{"10341"}) {
		t.Errorf("tunnel -p = %v, want [10341]: %v", got, tun)
	}
	if n := len(tun); tun[n-2] != "-N" || tun[n-1] != "root@1.2.3.4" {
		t.Errorf("tunnel argv should still end with -N user@host (port travels in -p), got %v", tun[n-2:])
	}

	sup := target.SupervisedTunnelCommand([][2]int{{8000, 8000}}).Args
	if got := flagValues(sup, "-p"); !reflect.DeepEqual(got, []string{"10341"}) {
		t.Errorf("supervised tunnel -p = %v, want [10341]", got)
	}

	rs := target.rsyncArgs("./data/", "~/data/", nil, false, false)
	e := flagValues(rs, "-e")
	if len(e) != 1 || !strings.HasPrefix(e[0], "ssh ") || !strings.HasSuffix(e[0], " -p 10341") {
		t.Errorf(`rsync -e = %v, want "ssh <base opts> -p 10341"`, e)
	}
	if rs[len(rs)-1] != "root@1.2.3.4:~/data/" {
		t.Errorf("rsync dest must stay user@host:path (no port), got %s", rs[len(rs)-1])
	}
}

// WaitForSSH must probe the endpoint's port, not 22, and hand the same port
// to the ssh it then runs. A local listener stands in for the sshd; a stub
// ssh on PATH records its argv.
func TestWaitForSSHDialsEndpointPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	accepted := make(chan struct{}, 1)
	go func() {
		if c, err := l.Accept(); err == nil {
			c.Close()
			accepted <- struct{}{}
		}
	}()

	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argv + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	target := Target{User: "root", Host: "127.0.0.1", Port: port}
	if err := target.WaitForSSH(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-accepted:
	default:
		t.Errorf("WaitForSSH never dialled 127.0.0.1:%d", port)
	}
	b, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal("stub ssh was not run:", err)
	}
	args := strings.Split(strings.TrimSpace(string(b)), "\n")
	if got := flagValues(args, "-p"); !reflect.DeepEqual(got, []string{strconv.Itoa(port)}) {
		t.Errorf("ssh -p = %v, want [%d]: %v", got, port, args)
	}
}
