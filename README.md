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
rig up --type 1H100.80S.32V --env-file ~/secrets/hf.env   # push KEY=VALUE files ad hoc (repeatable)

rig status               # table: status, IP, $/h, age, time-to-self-destruct
rig types --available    # what can actually be deployed right now
rig ssh [-- cmd]         # interactive shell or one-off command (agent forwarded)
rig sync ~/code/project    # rsync a working tree to the box
rig sync data --exclude images/          # skip subtrees (repeatable, stacks on defaults)
rig sync ./data:/root/data ./hf.env:/root/exp/hf.env --jobs 3   # per-path dest, single files, parallel streams
rig fetch s3://bucket/set.tar            # download ON the instance, not through your machine
rig tunnel 8000 50051    # forward local ports (^C to stop)

rig run --name train -- bash train.sh   # detached tmux job, survives disconnect
rig ps                   # running / exited jobs
rig logs train -f        # follow the job log
rig wait train           # block until it finishes; exit with ITS exit code (124 = --timeout)
rig wait train --for 'stage: train'   # block until a log line matches; prints it (125 = job ended first)
rig run --wait -- bash train.sh          # start + wait in one command
rig stop train           # kill a job

rig extend 6h            # push out the self-destruct deadline (or: rig extend <name> 6h)
rig down                 # delete instance (+ its OS volume); prints lifetime + cost
rig down --delete-volumes  # ... and destroy data volumes too
rig volumes              # list volumes; --rm <name> deletes a detached one
rig spend --since 168h   # spend summary from the local ledger (advisory)
```

`rig ssh -- <cmd>` exits with the remote command's exit code; 255 means ssh
itself failed *or* the remote command killed its own connection (e.g. a bare
`pkill`), so don't chain on commands that tear down their own session.
`rig run` shell-quotes each argument, so `rig run -- bash job.sh 700 32 "A D E"` hands
the script `A D E` as one third argument; `rig ssh -- 'a && b'` stays a raw string on purpose.

`rig wait` exit codes: the job's own code; 124 when `--timeout` expires; with
`--for`, 0 and the matching line on stdout, or 125 if the job ends before any
line matches (ADR-0005).

`rig sync` takes directories or single files. Each argument may carry its own
destination as `src:dest` (the part after the last colon must start with `/`
or `~`); `--dest` covers the rest (default `~/<basename>`). `--jobs N` runs
transfers concurrently; three or four streams saturate a typical link. Progress
output shows only on a terminal running a single stream; `--quiet` drops it,
`--progress` forces it on.

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
- **No long-lived credentials at rest on instances.** SSH agent forwarding
  handles private git dependencies (`git+ssh` installs); secrets arrive only
  as explicitly pushed env files. `rig fetch s3://…` forwards short-lived
  session credentials (minting them via STS when your local key is
  long-lived) to the one remote command over stdin — nothing persists.
- **(Almost) no local state.** Everything is derived from the provider API;
  rig instances are recognized by the `rig-` hostname prefix. (Note: on a
  shared cloud account, that prefix — and instance names — are global to the
  team.) The one exception is the spend ledger (`~/.config/rig/ledger.jsonl`),
  an advisory append-only record behind `rig spend`; the provider's billing
  stays authoritative, and TTL-torn instances appear as estimates. The other is
  the instance cache (`~/.config/rig/instances.json`, ADR-0006): written on `up`
  and `adopt`, cleared on `down`, entries trusted for 12h, so `ssh`, `run`,
  `logs`, `wait`, `sync` and friends resolve a named instance without a provider
  call. `status`, `down`, `extend` and `adopt` always ask the provider, and any
  fresh listing drops entries that have gone stale.

## Agent orchestration

Every command is non-interactive when fully specified, and the read commands
(`status`, `ps`, `types`, `volumes`, `spend`) take a `--json` flag, which
makes rig drivable by an AI agent end-to-end — no table scraping, no log
watching:

```bash
rig up train                          # blocks until ssh is ready
rig sync ~/code/project
rig fetch s3://bucket/dataset.tar --dest /data/
rig run --wait --name exp42 -- bash provision/train.sh   # blocks; exits with the job's code
rig wait exp42 --for '^epoch 1/' --timeout 30m   # wake on a log line, not on completion
rig status --json | jq -r '.[0].ttl'  # machine-readable everything
rig down --yes                        # tear down when done
```

The TTL watchdog is the safety net if the agent (or the human) forgets the
last step.

## Extending to other providers

`internal/provider` defines the interface (instances, volumes, images, SSH
keys, live availability, balance); `internal/provider/verda` is ~300 lines
against the official SDK. Everything above the interface — placement,
volumes, TTL, jobs, TUI — is provider-agnostic.
