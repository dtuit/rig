# rig

GPU cloud workbench. `rig` provisions ephemeral GPU instances with persistent
volumes, gets you (or your AI agent) onto them, and runs detached jobs that
survive disconnects — then makes sure the instance kills itself before it
burns a hole in the bill.

Provider-abstracted; [Verda](https://verda.com) (formerly DataCrunch) is the
first driver, via the official Go SDK.

## Install

```bash
go install github.com/dtuit/rig/cmd/rig@latest
```

Requires `ssh`, `rsync` locally; instances need `tmux` + `systemd` (Verda's
stock Ubuntu images qualify).

## Configure

`~/.config/rig/config.yml` (see `rig config` for an example):

```yaml
provider: verda
credentials_file: ~/secrets/verda.env   # VERDA_CLIENT_ID / VERDA_CLIENT_SECRET
ssh:
  user: root
  public_key: ~/.ssh/id_ed25519.pub     # registered with the provider on first up
  forward_agent: true
defaults:
  ttl: 12h
profiles:
  train:
    types: [1H100.80S.32V, 1A100.22V]   # preference order, first available wins
    spot: false
    volume: { name: rig-data, size_gb: 500 }
    ttl: 24h
    ports: [8000]
    env_files: [~/.config/rig/train.env]  # pushed to ~/.rig/env, sourced by jobs
```

Environment variables (`VERDA_CLIENT_ID`/`VERDA_CLIENT_SECRET`) override the
credentials file. Get credentials from the Verda console under
*Keys → REST API Credentials*.

## Use

```bash
rig                      # dashboard TUI: instances, volumes, burn rate, balance
rig up                   # interactive picker over LIVE availability + pricing
rig up train             # headless: first available type from the profile
rig up --type CPU.4V.16G --name smoke --ttl 1h   # fully ad-hoc

rig status               # table: status, IP, $/h, age, time-to-self-destruct
rig types --available    # what can actually be deployed right now
rig ssh [-- cmd]         # interactive shell or one-off command (agent forwarded)
rig sync ~/code/project    # rsync a working tree to the box
rig tunnel 8000 50051    # forward local ports (^C to stop)

rig run --name train -- bash train.sh   # detached tmux job, survives disconnect
rig ps                   # running / exited jobs
rig logs train -f        # follow the job log
rig stop train           # kill a job

rig extend --by 6h       # push out the self-destruct deadline
rig down                 # delete instance (+ its OS volume); data volumes kept
rig down --delete-volumes  # ... and destroy data volumes too
rig volumes              # list volumes; --rm <name> deletes a detached one
```

## How it holds together

- **Ephemeral instances, persistent volumes.** Instances are disposable;
  the expensive-to-rebuild state (venvs, model weights, datasets) lives on a
  named volume that `rig up` finds-or-creates in whichever datacenter the
  instance lands in. Volumes are datacenter-bound, so a profile may
  accumulate one volume per location; locations that already hold the
  profile's volume are preferred during placement.
- **Preference lists, not fixed types.** GPU availability is volatile.
  Profiles declare `types: [A, B, C]` and rig takes the first that is
  actually deployable, optionally pinned to a location.
- **TTL self-destruct.** `rig up` installs a systemd watchdog on the box
  that shuts it down when the deadline in `~/.rig/deadline` passes —
  deferring an hour at a time while any rig job is still running, or while
  any executable hook in `~/.rig/busy.d/` exits 0 (drop a script there to
  gate shutdown on a wandb run, a health endpoint, whatever). A shut-down
  instance stops billing for compute.
- **Jobs are tmux sessions.** `rig run` wraps the command in a named tmux
  session, logs to `~/.rig/logs/<name>.log`, records the exit code, and
  sources every file in `~/.rig/env/` first. Nothing depends on your laptop
  staying connected.
- **No credentials at rest on instances.** SSH agent forwarding handles
  private git dependencies (`git+ssh` installs); secrets arrive only as
  explicitly pushed env files.
- **No local state.** Everything is derived from the provider API; rig
  instances are recognized by the `rig-` hostname prefix. (Note: on a shared
  cloud account, that prefix — and instance names — are global to the team.)

## Agent orchestration

Every command is non-interactive when fully specified, which makes rig
drivable by an AI agent end-to-end:

```bash
rig up train                          # blocks until ssh is ready
rig sync ~/code/project
rig run --name exp42 -- bash provision/train.sh
rig logs exp42 -f                     # poll progress
rig down --yes                        # tear down when done
```

The TTL watchdog is the safety net if the agent (or the human) forgets the
last step.

## Extending to other providers

`internal/provider` defines the interface (instances, volumes, images, SSH
keys, live availability, balance); `internal/provider/verda` is ~300 lines
against the official SDK. Everything above the interface — placement,
volumes, TTL, jobs, TUI — is provider-agnostic.
