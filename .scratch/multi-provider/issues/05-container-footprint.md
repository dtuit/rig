# 05 — Footprint without systemd (container providers)

**What to build:** `rig up` and `rig adopt` install a working TTL watchdog on a Docker-container
instance (RunPod pod, Vast instance): no systemd, no `shutdown`. The deadline, busy hooks and
job-deferral behave exactly as on a VM; only the "stop the instance" line differs, and the
provider supplies it. Missing `tmux`/`rsync` are installed so any sshd-bearing image works.

**Blocked by:** 02 (for `Capabilities`/traits plumbing) — can be drafted in parallel.

**Status:** resolved

- [x] `remote.Init` detects an init system on the box (`[ -d /run/systemd/system ]`). Systemd
      path is unchanged. Otherwise `ttl.sh` is started with `nohup setsid … & echo $! > ~/.rig/ttl.pid`,
      and `Init` is idempotent (kills a previous pid before restarting).
- [x] `~/.rig/onstart.sh` re-arms the watchdog after a container restart; drivers wire it into
      the container's start hook (RunPod `args`/env wrapper, Vast `onstart`) — the driver issues
      own that wiring, this issue provides the script and documents the contract.
- [x] The shutdown line in `ttl.sh` is a template parameter supplied by the provider
      (`Provider.SelfStopCommand()` or part of `Capabilities`): `shutdown -h now` for VMs; the
      Vast `curl` against `$CONTAINER_API_KEY`; RunPod per issue 07 (placeholder that logs and
      exits non-zero until decided).
- [x] `Init` runs `command -v tmux rsync || apt-get install -y tmux rsync` (quietly, only when
      apt exists and the user is root or has passwordless sudo); failure is a warning that
      names what `rig run`/`rig sync` will need.
- [x] `ttl.sh`'s job detection already scans every tmux socket; verify it under a container where
      `/tmp/tmux-0/default` is the only socket. `logger` absence is tolerated (already `|| true`).
- [x] Pure tests: rendered `ttl.sh`/unit/onstart text for systemd and non-systemd with a given
      shutdown line. No real ssh.

## Notes

Spec: ../spec.md — "Container footprint". ADR-0003 stance: the Vast token is instance-scoped
(start/stop/destroy of that one instance), so placing nothing else on the box keeps the ADR
intact; record this in the new ADR (issue 11).

## Comments

2026-09-08 (agent): Landed. `internal/provider/provider.go`: `Capabilities.SelfStop string` — the
shell line the watchdog runs at the deadline; empty means the VM default `shutdown -h now`
(Verda and the fake leave it empty; the RunPod/Vast drivers set theirs in 06/08).
`internal/remote/remote.go`: `remote.Init(t, ttl)` is now `remote.Init(t sshx.Target, f
remote.Footprint) error` with `Footprint{TTL time.Duration; SelfStop string; Log func(format
string, a ...any)}` (nil Log discards warnings); `RenderTTLScript(selfStop string) string`
(pure; `DefaultSelfStop = "shutdown -h now"` substituted for a `@RIG_SELF_STOP@` placeholder via
`strings.Replace`, so the script's `date +%s` needs no escaping); `RenderOnstart() string` →
`~/.rig/onstart.sh` (contract in its doc comment: drivers wire `[ -f ~/.rig/onstart.sh ] && bash
~/.rig/onstart.sh` into the container start hook; a no-op while the pidfile names a live ttl.sh).
Init order: mkdir → best-effort tools (`command -v tmux && command -v rsync || { apt-get … }`, root
directly or `sudo -n`, `DEBIAN_FRONTEND=noninteractive`; the same command then echoes
`MISSING:<tools>` and Init logs `warning: tmux is not installed on the instance and could not be
installed; rig run needs it` per tool — never an error) → write rendered ttl.sh → `[ -d
/run/systemd/system ] && echo systemd || echo none` (last output line wins, so a login banner
does not break detection) → systemd: unchanged unit install; else write onstart.sh, then
`{ p=$(cat ~/.rig/ttl.pid); grep -qa ttl.sh /proc/$p/cmdline && kill "$p"; } 2>/dev/null; nohup
setsid ~/.rig/ttl.sh >~/.rig/ttl.log 2>&1 & echo $! > ~/.rig/ttl.pid` (the pid is checked against
/proc cmdline because a container restart recycles pids) → SetDeadline. onstart.sh is written only
on the no-systemd path so a VM never grows a second watchdog beside its unit. `internal/core/core.go`:
`App.Footprint(ttl) remote.Footprint` (TTL, `Prov.Capabilities().SelfStop`, `a.logf`) used by `Up`;
`internal/cli/adopt.go` uses the same. Tests (`internal/remote/footprint_test.go`):
`RenderTTLScript` default/verbatim-once/only-the-stop-line-differs, both scripts parse under
`bash -n`, `RenderOnstart` content, a real-shell run of onstart.sh + the restart line against a fake
$HOME (pidfile names a live process, second onstart is a no-op, restart replaces the pid), and two
stub-`ssh`-on-PATH Init tests (no systemd: nohup/pidfile/onstart present and no `systemctl`;
systemd: `sudo bash -c '… systemctl …'` present and no nohup) — the stub cost ~20 lines, so the
optional Init test was done. ttl.sh's only text change beyond the stop line is its header comment
(mentions nohup/setsid). No existing test changed. Notes for 06/08: the start line records `$!` of
`nohup setsid`, which is ttl.sh itself only in a non-interactive shell (setsid execs in place when
not a group leader) — start hooks are non-interactive, so fine; RunPod's `/etc/rp_environment`
sourcing belongs inside the driver's `SelfStop` line as issue 07 wrote it. Follow-ups: README lines
17 and 108 still say instances need systemd (docs, issue 11); `ttl.log` is never rotated (the
watchdog is silent except at the deadline, so it stays tiny).
