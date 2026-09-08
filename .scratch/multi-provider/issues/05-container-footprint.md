# 05 — Footprint without systemd (container providers)

**What to build:** `rig up` and `rig adopt` install a working TTL watchdog on a Docker-container
instance (RunPod pod, Vast instance): no systemd, no `shutdown`. The deadline, busy hooks and
job-deferral behave exactly as on a VM; only the "stop the instance" line differs, and the
provider supplies it. Missing `tmux`/`rsync` are installed so any sshd-bearing image works.

**Blocked by:** 02 (for `Capabilities`/traits plumbing) — can be drafted in parallel.

**Status:** ready-for-agent

- [ ] `remote.Init` detects an init system on the box (`[ -d /run/systemd/system ]`). Systemd
      path is unchanged. Otherwise `ttl.sh` is started with `nohup setsid … & echo $! > ~/.rig/ttl.pid`,
      and `Init` is idempotent (kills a previous pid before restarting).
- [ ] `~/.rig/onstart.sh` re-arms the watchdog after a container restart; drivers wire it into
      the container's start hook (RunPod `args`/env wrapper, Vast `onstart`) — the driver issues
      own that wiring, this issue provides the script and documents the contract.
- [ ] The shutdown line in `ttl.sh` is a template parameter supplied by the provider
      (`Provider.SelfStopCommand()` or part of `Capabilities`): `shutdown -h now` for VMs; the
      Vast `curl` against `$CONTAINER_API_KEY`; RunPod per issue 07 (placeholder that logs and
      exits non-zero until decided).
- [ ] `Init` runs `command -v tmux rsync || apt-get install -y tmux rsync` (quietly, only when
      apt exists and the user is root or has passwordless sudo); failure is a warning that
      names what `rig run`/`rig sync` will need.
- [ ] `ttl.sh`'s job detection already scans every tmux socket; verify it under a container where
      `/tmp/tmux-0/default` is the only socket. `logger` absence is tolerated (already `|| true`).
- [ ] Pure tests: rendered `ttl.sh`/unit/onstart text for systemd and non-systemd with a given
      shutdown line. No real ssh.

## Notes

Spec: ../spec.md — "Container footprint". ADR-0003 stance: the Vast token is instance-scoped
(start/stop/destroy of that one instance), so placing nothing else on the box keeps the ADR
intact; record this in the new ADR (issue 11).

## Comments
