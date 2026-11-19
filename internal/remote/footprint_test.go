package remote

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dtuit/rig/internal/sshx"
)

// The watchdog's stop line is the only provider-specific text in ttl.sh: an
// empty SelfStop renders the VM default, a driver's line lands verbatim
// exactly once, and nothing else in the script moves.
func TestRenderTTLScriptSelfStopLine(t *testing.T) {
	def := RenderTTLScript("")
	if strings.Count(def, "shutdown -h now") != 1 {
		t.Fatalf("default script should stop with `shutdown -h now` exactly once:\n%s", def)
	}
	if strings.Contains(def, ttlSelfStopPlaceholder) {
		t.Errorf("placeholder leaked into the rendered script")
	}
	for _, want := range []string{"#!/usr/bin/env bash", `DIR="${RIG_DIR:-${HOME:-/root}/.rig}"`, "rig_jobs_running", "/tmp/tmux-*/default", `"$DIR"/busy.d/*`, "logger -t rig-ttl", "ttl_log", `LOG="$DIR/ttl.log"`} {
		if !strings.Contains(def, want) {
			t.Errorf("default script lacks %q", want)
		}
	}

	line := `. /etc/rp_environment && curl -fsS -X POST "https://api.runpod.io/v2/pods/$RUNPOD_POD_ID/action" -H "Authorization: Bearer $RUNPOD_API_KEY" -H 'Content-Type: application/json' -d '{"action":"stop"}'`
	got := RenderTTLScript(line)
	if strings.Count(got, line) != 1 {
		t.Errorf("driver line should appear verbatim exactly once:\n%s", got)
	}
	if strings.Contains(got, "shutdown -h now") {
		t.Errorf("driver line must replace the VM default, not sit beside it")
	}
	if want := strings.Replace(def, "shutdown -h now", line, 1); got != want {
		t.Errorf("only the stop line may differ between renders\n got: %s\nwant: %s", got, want)
	}
}

// Both scripts must parse as bash on the box they land on.
func TestRenderedScriptsAreValidBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash on PATH")
	}
	for name, script := range map[string]string{
		"ttl.sh default": RenderTTLScript(""),
		"ttl.sh runpod":  RenderTTLScript(`. /etc/rp_environment && curl -d '{"action":"stop"}'`),
		"onstart.sh":     RenderOnstart(),
	} {
		c := exec.Command("bash", "-n")
		c.Stdin = strings.NewReader(script)
		if out, err := c.CombinedOutput(); err != nil {
			t.Errorf("%s does not parse: %v: %s", name, err, out)
		}
	}
}

// onstart.sh is the re-arm hook a container driver runs at start: it must
// restart ttl.sh under nohup/setsid and record the pid, and do nothing when
// the pidfile names a live watchdog.
func TestRenderOnstartRearmsWatchdog(t *testing.T) {
	s := RenderOnstart()
	for _, want := range []string{
		"#!/usr/bin/env bash",
		// >> not >: a container restart must not truncate the decision log
		// the watchdog keeps in that same file.
		"nohup setsid ~/.rig/ttl.sh >>~/.rig/ttl.log 2>&1 & echo $! > ~/.rig/ttl.pid",
		"/proc/$p/cmdline",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("onstart.sh lacks %q:\n%s", want, s)
		}
	}
}

// TestDetachedWatchdogUnderBash runs the no-systemd start line and onstart.sh
// in a real shell against a fake $HOME: the pidfile names a live ttl.sh,
// onstart.sh on a live watchdog is a no-op, and the restart line replaces
// the old process with a new one.
func TestDetachedWatchdogUnderBash(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid on PATH")
	}
	home := t.TempDir()
	rig := filepath.Join(home, ".rig")
	if err := os.MkdirAll(rig, 0o755); err != nil {
		t.Fatal(err)
	}
	// stands in for ttl.sh: long-lived, and named ttl.sh in /proc/<pid>/cmdline
	if err := os.WriteFile(filepath.Join(rig, "ttl.sh"), []byte("#!/usr/bin/env bash\nexec -a ttl.sh sleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rig, "onstart.sh"), []byte(RenderOnstart()), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(script string) {
		c := exec.Command("bash", "-c", script)
		c.Env = append(os.Environ(), "HOME="+home)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", script, err, out)
		}
	}
	pid := func() int {
		b, err := os.ReadFile(filepath.Join(rig, "ttl.pid"))
		if err != nil {
			t.Fatal(err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			t.Fatalf("ttl.pid = %q", b)
		}
		return n
	}
	alive := func(p int) bool { return syscall.Kill(p, 0) == nil }

	run("bash ~/.rig/onstart.sh")
	first := pid()
	t.Cleanup(func() { syscall.Kill(first, syscall.SIGKILL) })
	if !alive(first) {
		t.Fatalf("onstart.sh did not leave ttl.sh (pid %d) running", first)
	}
	run("bash ~/.rig/onstart.sh")
	if pid() != first {
		t.Errorf("onstart.sh on a live watchdog started another: pid %d, then %d", first, pid())
	}
	run(ttlRestartCmd)
	second := pid()
	t.Cleanup(func() { syscall.Kill(second, syscall.SIGKILL) })
	time.Sleep(50 * time.Millisecond)
	if second == first || alive(first) || !alive(second) {
		t.Errorf("restart: pid %d -> %d, old alive %v, new alive %v; want a new live pid and the old one gone",
			first, second, alive(first), alive(second))
	}
}

// stubSSH puts a fake ssh on PATH that logs each remote command (ssh's last
// argv element) to cmdlog, answers the init-system probe with initAnswer,
// and reports every tool present.
func stubSSH(t *testing.T, initAnswer string) (cmdlog string) {
	t.Helper()
	dir := t.TempDir()
	cmdlog = filepath.Join(dir, "cmds")
	stub := `#!/bin/sh
for last; do :; done
printf '%s\n' "$last" >> ` + cmdlog + `
cat >/dev/null
case "$last" in
  *'/run/systemd/system'*) echo ` + initAnswer + ` ;;
  *MISSING*) echo 'MISSING:' ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return cmdlog
}

// On a box without /run/systemd/system (a RunPod pod: it has a systemctl
// binary but no systemd) Init must start ttl.sh under nohup/setsid with a
// pidfile, kill a previous watchdog first, write onstart.sh, and never touch
// systemctl.
func TestInitWithoutSystemdUsesNohupAndPidfile(t *testing.T) {
	cmdlog := stubSSH(t, "none")
	target := sshx.Target{User: "root", Host: "127.0.0.1", NoMultiplex: true}
	if err := Init(target, Footprint{TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(cmdlog)
	if err != nil {
		t.Fatal(err)
	}
	cmds := string(b)
	for _, want := range []string{
		// >> not >: a container restart must not truncate the decision log
		// the watchdog keeps in that same file.
		"nohup setsid ~/.rig/ttl.sh >>~/.rig/ttl.log 2>&1 & echo $! > ~/.rig/ttl.pid",
		"/proc/$p/cmdline",
		"kill",
		"cat > ~/.rig/onstart.sh",
		"cat > ~/.rig/ttl.sh",
		"echo ",
	} {
		if !strings.Contains(cmds, want) {
			t.Errorf("Init on a no-systemd box did not issue %q:\n%s", want, cmds)
		}
	}
	if strings.Contains(cmds, "systemctl") {
		t.Errorf("Init on a no-systemd box must not install the unit:\n%s", cmds)
	}
}

// With systemd present the unit install is unchanged and no nohup fallback runs.
func TestInitWithSystemdInstallsUnit(t *testing.T) {
	cmdlog := stubSSH(t, "systemd")
	target := sshx.Target{User: "ubuntu", Host: "127.0.0.1", NoMultiplex: true}
	if err := Init(target, Footprint{TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(cmdlog)
	if err != nil {
		t.Fatal(err)
	}
	cmds := string(b)
	if !strings.Contains(cmds, "sudo bash -c 'cat > /etc/systemd/system/rig-ttl.service && systemctl daemon-reload && systemctl enable rig-ttl.service && systemctl restart rig-ttl.service'") {
		t.Errorf("systemd box should install the unit via sudo for a non-root user:\n%s", cmds)
	}
	if strings.Contains(cmds, "nohup") || strings.Contains(cmds, "onstart.sh") {
		t.Errorf("systemd box must not use the nohup fallback:\n%s", cmds)
	}
}

// The watchdog's record is the answer to "why is this instance still up",
// and it is shell, so it is tested by running it: a real bash against a
// fake RIG_DIR, with the poll interval turned down.
func runWatchdog(t *testing.T, dir string, wait time.Duration) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash on PATH")
	}
	script := filepath.Join(dir, "ttl.sh")
	// "exit 0" stands in for the self-stop line: the test must not run
	// `shutdown -h now` on the machine running the test.
	if err := os.WriteFile(script, []byte(RenderTTLScript("exit 0")), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	c := exec.CommandContext(ctx, "bash", script)
	c.Env = append(os.Environ(), "RIG_DIR="+dir, "RIG_TTL_POLL=1")
	c.Run() // it either self-stops or is killed by the deadline; both are fine
	b, err := os.ReadFile(filepath.Join(dir, "ttl.log"))
	if err != nil {
		t.Fatalf("the watchdog wrote no log: %v", err)
	}
	return string(b)
}

func TestWatchdogRecordsWhyItDeferred(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "busy.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	// a deadline already passed, and a hook that says the box is busy
	if err := os.WriteFile(filepath.Join(dir, "deadline"),
		[]byte(fmt.Sprint(time.Now().Add(-time.Minute).Unix())), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "busy.d", "keepalive"),
		[]byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	log := runWatchdog(t, dir, 4*time.Second)

	for _, want := range []string{"watchdog started", "armed for", "deferred 1h", "busy.d/keepalive"} {
		if !strings.Contains(log, want) {
			t.Errorf("log does not say %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "deadline reached") {
		t.Errorf("it stopped an instance a hook said was busy:\n%s", log)
	}
	// the deferral has to move the deadline, not just talk about it
	b, _ := os.ReadFile(filepath.Join(dir, "deadline"))
	moved, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if moved <= time.Now().Unix() {
		t.Errorf("deadline is still in the past: %d", moved)
	}
}

func TestWatchdogRecordsFiring(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deadline"),
		[]byte(fmt.Sprint(time.Now().Add(-time.Minute).Unix())), 0o644); err != nil {
		t.Fatal(err)
	}

	log := runWatchdog(t, dir, 6*time.Second)

	if !strings.Contains(log, "deadline reached, stopping the instance") {
		t.Errorf("nothing recorded the stop:\n%s", log)
	}
}
