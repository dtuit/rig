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

const ttlScript = `#!/usr/bin/env bash
# rig TTL watchdog: shuts the instance down at the deadline in $RIG_DIR/deadline
# (unix epoch). Deferred 1h at a time while busy: any rig-* tmux session alive
# (any user's socket; this runs under systemd as root), or any executable hook
# in $RIG_DIR/busy.d exiting 0.
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
  shutdown -h now
done
`

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

func homeOf(user string) string {
	if user == "root" {
		return "/root"
	}
	return "/home/" + user
}

// Init creates the ~/.rig layout, installs the TTL watchdog as a systemd
// unit, and sets the initial deadline.
func Init(t sshx.Target, ttl time.Duration) error {
	if _, err := t.Run("mkdir -p ~/.rig/busy.d ~/.rig/logs ~/.rig/jobs ~/.rig/env"); err != nil {
		return fmt.Errorf("creating ~/.rig: %w", err)
	}
	if out, err := t.RunWithStdin(ttlScript, "cat > ~/.rig/ttl.sh && chmod +x ~/.rig/ttl.sh"); err != nil {
		return fmt.Errorf("installing ttl.sh: %v: %s", err, out)
	}
	unit := fmt.Sprintf(ttlUnit, homeOf(t.User))
	install := "cat > /etc/systemd/system/rig-ttl.service && systemctl daemon-reload && systemctl enable rig-ttl.service && systemctl restart rig-ttl.service"
	cmd := install
	if t.User != "root" {
		cmd = fmt.Sprintf("sudo bash -c '%s'", install)
	}
	if out, err := t.RunWithStdin(unit, cmd); err != nil {
		return fmt.Errorf("installing rig-ttl unit: %v: %s", err, out)
	}
	return SetDeadline(t, time.Now().Add(ttl))
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
	Name     string
	Running  bool
	ExitCode int // valid when !Running
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
	out, err := t.Run(fmt.Sprintf("tmux kill-session -t rig-%s", name))
	if err != nil {
		return fmt.Errorf("stopping job %s: %v: %s", name, err, out)
	}
	return nil
}
