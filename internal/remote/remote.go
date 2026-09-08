// Package remote manages rig's footprint on an instance: ~/.rig with the TTL
// self-destruct watchdog, detached tmux jobs, pushed env files.
package remote

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dtuit/rig/internal/sshx"
)

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ttlSelfStopPlaceholder marks where the provider's stop line goes in ttlScript.
const ttlSelfStopPlaceholder = "@RIG_SELF_STOP@"

// DefaultSelfStop stops a VM at its deadline; a container provider supplies
// its own line through provider.Capabilities.SelfStop.
const DefaultSelfStop = "shutdown -h now"

const ttlScript = `#!/usr/bin/env bash
# rig TTL watchdog: stops the instance at the deadline in $RIG_DIR/deadline
# (unix epoch). Deferred 1h at a time while busy: any rig-* tmux session alive
# (any user's socket; this runs as root, under systemd on a VM or under
# nohup/setsid in a container), or any executable hook in $RIG_DIR/busy.d
# exiting 0.
set -u
DIR="${RIG_DIR:-${HOME:-/root}/.rig}"
rig_jobs_running() {
  for s in /tmp/tmux-*/default; do
    [ -S "$s" ] || continue
    tmux -S "$s" ls -F '#{session_name}' 2>/dev/null | grep -q '^rig-' && return 0
  done
  return 1
}
while true; do
  sleep 60
  [ -f "$DIR/deadline" ] || continue
  d=$(cat "$DIR/deadline" 2>/dev/null) || continue
  now=$(date +%s)
  [ "$now" -lt "$d" ] && continue
  if rig_jobs_running; then
    echo $((now + 3600)) > "$DIR/deadline"
    continue
  fi
  busy=0
  if [ -d "$DIR/busy.d" ]; then
    for h in "$DIR"/busy.d/*; do
      [ -x "$h" ] && "$h" >/dev/null 2>&1 && busy=1 && break
    done
  fi
  if [ "$busy" = 1 ]; then
    echo $((now + 3600)) > "$DIR/deadline"
    continue
  fi
  logger -t rig-ttl "deadline reached, shutting down" 2>/dev/null || true
  ` + ttlSelfStopPlaceholder + `
done
`

// RenderTTLScript returns ~/.rig/ttl.sh with selfStop as the line the
// watchdog runs at the deadline; empty means DefaultSelfStop. Pure.
func RenderTTLScript(selfStop string) string {
	if selfStop == "" {
		selfStop = DefaultSelfStop
	}
	return strings.Replace(ttlScript, ttlSelfStopPlaceholder, selfStop, 1)
}

const ttlUnit = `[Unit]
Description=rig TTL self-destruct watchdog

[Service]
Environment=RIG_DIR=%[1]s/.rig
ExecStart=%[1]s/.rig/ttl.sh
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
`

// Without systemd the watchdog is a detached process: nohup/setsid so the
// ssh session's end does not take it, its pid in ~/.rig/ttl.pid so Init
// and onstart.sh can tell a live watchdog from a stale file.
const (
	ttlStartLine = "nohup setsid ~/.rig/ttl.sh >~/.rig/ttl.log 2>&1 & echo $! > ~/.rig/ttl.pid"
	// ttlAliveCheck sets p to the recorded pid and succeeds only when that
	// pid is still our ttl.sh (a container restart recycles pids).
	ttlAliveCheck = `p=$(cat ~/.rig/ttl.pid 2>/dev/null); [ -n "$p" ] && grep -qa ttl.sh "/proc/$p/cmdline" 2>/dev/null`
	// ttlRestartCmd is what Init runs without systemd: kill the previous
	// watchdog if its pid is still ours, then start a fresh one.
	ttlRestartCmd = "{ " + ttlAliveCheck + " && kill \"$p\"; } 2>/dev/null; " + ttlStartLine
)

// RenderOnstart returns ~/.rig/onstart.sh, the re-arm hook for container
// providers, whose instances lose every process on restart. Contract for
// drivers: wire `[ -f ~/.rig/onstart.sh ] && bash ~/.rig/onstart.sh` into the
// container's start hook (RunPod args wrapper, Vast onstart). Running it
// while the watchdog is alive is a no-op. Pure.
func RenderOnstart() string {
	return "#!/usr/bin/env bash\n" +
		"# rig: re-arm the TTL watchdog after a container restart. Drivers run\n" +
		"# `bash ~/.rig/onstart.sh` from the container's start hook when it exists.\n" +
		ttlAliveCheck + " && exit 0\n" +
		ttlStartLine + "\n"
}

func homeOf(user string) string {
	if user == "root" {
		return "/root"
	}
	return "/home/" + user
}

// Footprint is what Init installs beyond the ~/.rig layout.
type Footprint struct {
	TTL time.Duration // initial deadline, from now
	// SelfStop is the shell line the watchdog runs at the deadline; empty
	// means DefaultSelfStop (a VM). Container drivers supply theirs via
	// provider.Capabilities.SelfStop.
	SelfStop string
	// Log receives warnings Init does not fail on (missing tmux/rsync); nil
	// discards them.
	Log func(format string, a ...any)
}

func (f Footprint) logf(format string, a ...any) {
	if f.Log != nil {
		f.Log(format, a...)
	}
}

// Init creates the ~/.rig layout, installs the TTL watchdog and sets the
// initial deadline. With systemd (/run/systemd/system present) the watchdog
// is a systemd unit; otherwise — a container — ttl.sh runs under
// nohup/setsid with its pid in ~/.rig/ttl.pid, and ~/.rig/onstart.sh
// re-arms it after a restart. Init is idempotent: a previous watchdog is
// restarted, not duplicated. Missing tmux/rsync are installed via apt when
// possible; failure there is a warning, never an error.
func Init(t sshx.Target, f Footprint) error {
	if _, err := t.Run("mkdir -p ~/.rig/busy.d ~/.rig/logs ~/.rig/jobs ~/.rig/env"); err != nil {
		return fmt.Errorf("creating ~/.rig: %w", err)
	}
	ensureTools(t, f)
	if out, err := t.RunWithStdin(RenderTTLScript(f.SelfStop), "cat > ~/.rig/ttl.sh && chmod +x ~/.rig/ttl.sh"); err != nil {
		return fmt.Errorf("installing ttl.sh: %w: %s", err, out)
	}
	out, err := t.Run("[ -d /run/systemd/system ] && echo systemd || echo none")
	if err != nil {
		return fmt.Errorf("detecting init system: %w: %s", err, out)
	}
	// last line: an image's login banner may precede the answer
	if lines := strings.Fields(out); len(lines) > 0 && lines[len(lines)-1] == "systemd" {
		if err := installUnit(t); err != nil {
			return err
		}
	} else if err := startDetached(t); err != nil {
		return err
	}
	return SetDeadline(t, time.Now().Add(f.TTL))
}

// installUnit is the systemd path: the unit file, enabled and (re)started.
func installUnit(t sshx.Target) error {
	unit := fmt.Sprintf(ttlUnit, homeOf(t.User))
	install := "cat > /etc/systemd/system/rig-ttl.service && systemctl daemon-reload && systemctl enable rig-ttl.service && systemctl restart rig-ttl.service"
	cmd := install
	if t.User != "root" {
		cmd = fmt.Sprintf("sudo bash -c '%s'", install)
	}
	if out, err := t.RunWithStdin(unit, cmd); err != nil {
		return fmt.Errorf("installing rig-ttl unit: %w: %s", err, out)
	}
	return nil
}

// startDetached is the no-systemd path: write onstart.sh, kill the previous
// watchdog if its pid is still ours, start a fresh one under nohup/setsid.
func startDetached(t sshx.Target) error {
	if out, err := t.RunWithStdin(RenderOnstart(), "cat > ~/.rig/onstart.sh && chmod +x ~/.rig/onstart.sh"); err != nil {
		return fmt.Errorf("installing onstart.sh: %w: %s", err, out)
	}
	if out, err := t.Run(ttlRestartCmd); err != nil {
		return fmt.Errorf("starting ttl.sh: %w: %s", err, out)
	}
	return nil
}

// ensureTools installs tmux and rsync via apt when either is missing (root
// directly, otherwise passwordless sudo) and warns, naming the rig command
// that needs each, when one is still absent. Best effort: any Docker image
// with sshd should work, and a failure here must not fail Init.
func ensureTools(t sshx.Target, f Footprint) {
	sudo := ""
	if t.User != "root" {
		sudo = "sudo -n "
	}
	cmd := "command -v tmux >/dev/null && command -v rsync >/dev/null || { command -v apt-get >/dev/null && " +
		"(" + sudo + "apt-get update -qq && DEBIAN_FRONTEND=noninteractive " + sudo + "apt-get install -y -qq tmux rsync) >/dev/null 2>&1; }; " +
		`m=""; command -v tmux >/dev/null || m="$m tmux"; command -v rsync >/dev/null || m="$m rsync"; echo "MISSING:$m"`
	out, err := t.Run(cmd)
	if err != nil {
		f.logf("warning: checking for tmux/rsync on the instance: %v: %s", err, strings.TrimSpace(out))
		return
	}
	needs := map[string]string{"tmux": "rig run", "rsync": "rig sync"}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "MISSING:") {
			continue
		}
		for _, tool := range strings.Fields(strings.TrimPrefix(line, "MISSING:")) {
			f.logf("warning: %s is not installed on the instance and could not be installed; %s needs it", tool, needs[tool])
		}
	}
}

func SetDeadline(t sshx.Target, deadline time.Time) error {
	out, err := t.Run(fmt.Sprintf("echo %d > ~/.rig/deadline", deadline.Unix()))
	if err != nil {
		return fmt.Errorf("setting deadline: %v: %s", err, out)
	}
	return nil
}

func Deadline(t sshx.Target) (time.Time, error) {
	out, err := t.Run("cat ~/.rig/deadline 2>/dev/null")
	if err != nil {
		return time.Time{}, err
	}
	s := strings.TrimSpace(out)
	if s == "" {
		return time.Time{}, fmt.Errorf("no deadline set")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad deadline %q", s)
	}
	return time.Unix(n, 0), nil
}

// PushEnvFile copies a local env file to ~/.rig/env/<base>; rig jobs source
// every file there before running.
func PushEnvFile(t sshx.Target, content, base string) error {
	if !nameRe.MatchString(strings.ReplaceAll(base, ".", "")) {
		return fmt.Errorf("invalid env file name %q", base)
	}
	out, err := t.RunWithStdin(content, fmt.Sprintf("cat > ~/.rig/env/%s && chmod 600 ~/.rig/env/%s", base, base))
	if err != nil {
		return fmt.Errorf("pushing env %s: %v: %s", base, err, out)
	}
	return nil
}

type Job struct {
	Name     string `json:"name"`
	Running  bool   `json:"running"`
	ExitCode int    `json:"exit_code"` // valid when !Running
}

// StartJob runs command detached in tmux session rig-<name>, logging to
// ~/.rig/logs/<name>.log and recording the exit code on completion.
func StartJob(t sshx.Target, name, workdir, command string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("job name must match %s", nameRe)
	}
	script := "#!/bin/bash\n"
	script += "export PATH=\"$HOME/.local/bin:$PATH\"\n"
	if workdir != "" {
		script += fmt.Sprintf("cd %q || exit 1\n", workdir)
	}
	script += "set -a; for f in \"$HOME\"/.rig/env/*; do [ -f \"$f\" ] && . \"$f\"; done; set +a\n"
	script += command + "\n"
	if out, err := t.RunWithStdin(script,
		fmt.Sprintf("cat > ~/.rig/jobs/%s.sh && chmod +x ~/.rig/jobs/%s.sh", name, name)); err != nil {
		return fmt.Errorf("writing job script: %v: %s", err, out)
	}
	// fresh log/exit per start
	start := fmt.Sprintf(
		`rm -f ~/.rig/logs/%[1]s.exit && : > ~/.rig/logs/%[1]s.log && tmux new-session -d -s rig-%[1]s `+
			`'"$HOME"/.rig/jobs/%[1]s.sh >> "$HOME"/.rig/logs/%[1]s.log 2>&1; echo $? > "$HOME"/.rig/logs/%[1]s.exit'`,
		name)
	if out, err := t.Run(start); err != nil {
		if strings.Contains(out, "duplicate session") {
			return fmt.Errorf("job %q is already running", name)
		}
		return fmt.Errorf("starting job: %v: %s", err, out)
	}
	return nil
}

func ListJobs(t sshx.Target) ([]Job, error) {
	out, err := t.Run(`tmux ls -F '#{session_name}' 2>/dev/null | grep '^rig-' | sed 's/^rig-/RUN /'; ` +
		`for f in "$HOME"/.rig/logs/*.exit; do [ -f "$f" ] || continue; echo "EXIT $(basename "$f" .exit) $(cat "$f")"; done; true`)
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %v: %s", err, out)
	}
	var jobs []Job
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 2 && f[0] == "RUN":
			jobs = append(jobs, Job{Name: f[1], Running: true})
			seen[f[1]] = true
		case len(f) == 3 && f[0] == "EXIT" && !seen[f[1]]:
			code, _ := strconv.Atoi(f[2])
			jobs = append(jobs, Job{Name: f[1], ExitCode: code})
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
	return jobs, nil
}

func TailLog(t sshx.Target, name string, follow bool, lines int) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid job name %q", name)
	}
	cmd := fmt.Sprintf("tail -n %d ~/.rig/logs/%s.log", lines, name)
	if follow {
		cmd += " -f"
	}
	return t.Stream(cmd)
}

func StopJob(t sshx.Target, name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid job name %q", name)
	}
	// =rig-<name> is tmux's exact-match target; without it rig-<name> would
	// also match a prefix sibling like rig-<name>2 (issue 11).
	out, err := t.Run(fmt.Sprintf("tmux kill-session -t =rig-%s", name))
	if err != nil {
		return fmt.Errorf("stopping job %s: %v: %s", name, err, out)
	}
	return nil
}
