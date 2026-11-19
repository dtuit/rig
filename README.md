# rig

GPU cloud workbench. `rig` provisions ephemeral GPU instances with persistent
volumes, gets you (or your AI agent) onto them, and runs detached jobs that
survive disconnects — then makes sure the instance kills itself before it
burns a hole in the bill.

Provider-abstracted, with drivers for [Verda](https://verda.com) (formerly
DataCrunch), [RunPod](https://runpod.io), [Vast.ai](https://vast.ai) and AWS
EC2. One provider per command: set the default in config, pin it per profile,
or pass `--provider`.

## Install

```bash
go install github.com/dtuit/rig/cmd/rig@latest
```

Requires `ssh`, `rsync` locally. Instances need `tmux` and either systemd or a
Docker container rig can install into: stock Ubuntu images (Verda, the AWS Deep
Learning AMI) take the systemd path, RunPod pods and Vast instances the
container one. `rig up` installs `tmux` and `rsync` with apt when they are
missing, so any image with sshd works.

## Configure

`~/.config/rig/config.yml` (see `rig config` for an example):

```yaml
provider: verda                          # default; a profile may pin its own, --provider wins
providers:                               # per-provider blocks; env vars always beat credentials_file
  verda:
    credentials_file: ~/secrets/verda.env  # VERDA_CLIENT_ID / VERDA_CLIENT_SECRET
  runpod:
    credentials_file: ~/secrets/runpod.env # RUNPOD_API_KEY (or RUNPOD_KEY)
    cloud: secure                          # or community; also image, disk_gb
  vast:
    credentials_file: ~/secrets/vast.env   # VAST_API_KEY
    min_reliability: 0.95                  # also bid_markup, direct_only, image, disk_gb, filters
  aws:
    profile: default                       # SDK credential chain; AWS_PROFILE honoured
    region: ap-southeast-2                 # one region per block
    ssh_cidrs: []                          # who may ssh in; empty = your current public IP, /32
    vpc: vpc-0123456789abcdef0             # needed when the account has no default VPC
    subnets: [subnet-0aaa, subnet-0bbb]    # one per zone rig may use; imply their VPC
ssh:
  user: root                             # optional override of the provider's login user
  public_key: ~/.ssh/id_ed25519.pub      # registered with the provider on first up
  forward_agent: true
defaults:
  ttl: 12h
profiles:
  train:
    types: [1H100.80S.32V, 1A100.22V]    # preference order, first available wins
    spot: false
    volume: { name: rig-data, size_gb: 500 }
    ttl: 24h
    ports: [8000]
    env_files: [~/.config/rig/train.env]   # pushed to ~/.rig/env, sourced by jobs
  pod:
    provider: runpod                       # pin a profile to another cloud
    types: [1xA100-SXM]
```

Credentials resolve one variable at a time: the environment first, then that
provider's `credentials_file`. A top-level `credentials_file` still means
Verda's. AWS holds no rig-managed secret at all — the SDK's default chain does
it, so `aws sso login --profile <name>` is the whole story. Get the keys from
the Verda console (*Keys → REST API Credentials*), RunPod (*Settings → API
Keys*) and Vast (*Account → API keys*).

**Who may ssh in on AWS.** The `rig` security group rig creates allows 22/tcp
from your current public IP only — discovered from `checkip.amazonaws.com` when
the group is created, and written as a `/32`. Set `ssh_cidrs` to pin it
yourself (`[10.20.0.0/16]`, a bare address means `/32`, and `[0.0.0.0/0]` is
honoured if you really want the whole internet); with `ssh_cidrs` empty and no
way to determine your address, `rig up` fails and tells you to set the key
rather than opening the port to everybody. Your address changing is expected:
rig adds the new `/32` to its own group — the one tagged `rig:managed=true` —
and says so. It never edits a group it did not create; if you point
`security_group` at your own group and that group does not admit you, `rig up`
refuses with the `authorize-security-group-ingress` line to run. Nothing is
ever revoked, so prune stale `/32`s yourself, and rig will warn you if its
group still stands open to `0.0.0.0/0` from an earlier release.

**rig will not invent a VPC.** AWS launches go into the region's default VPC
and the chosen zone's subnet, with a public IP. An account that never had a
default VPC, or had it deleted for compliance, must pin `providers.aws.vpc`
(rig then picks a public subnet in the launch zone) or `providers.aws.subnets`
(explicit ids, at most one per zone; a zone none of them covers is an error,
not a launch somewhere else). rig creates no VPC, subnet, gateway or route of
its own. Pin a **dedicated** VPC rather than a production one: rig creates its
security group wherever you point it, and an internet-facing ssh port does not
belong in a production network.

### Per-provider caveats

|                | Verda | RunPod | Vast.ai | AWS EC2 |
| -------------- | ----- | ------ | ------- | ------- |
| **Type**       | catalogue name (`1H100.80S.32V`) | token (`1xA100-SXM`) | token (`1xRTX-4090`) | native name (`g6e.xlarge`) |
| **Location**   | location code (`FIN-01`) | datacenter id (`US-TX-3`); `any` on the community cloud | geolocation, as the API spells it | availability zone in the block's region |
| **Spot**       | yes | no — the v2 API has no bid fields | yes: bid = min bid × `bid_markup` | yes: one-time request, an interruption terminates |
| **Data volume**| yes | network volume, mounted at `/workspace` | no — Vast volumes are machine-bound | EBS, formatted and mounted at `/mnt/<name>` |
| **Balance**    | yes | yes | yes | no |
| **ssh**        | `root@IP:22` | container on a host-mapped port, user `root` | mapped port or Vast's proxy host, user `root` | `ubuntu@IP:22` |
| **Self-stop**  | `shutdown -h now` | pod-scoped key → `POST /pods/{id}/action stop` | `CONTAINER_API_KEY` → `PUT /instances/{id}/ state=stopped` | `shutdown -h now` |

**What has been run for real.** Verda is the cloud rig has driven since its
first commit, and its columns rest on that use plus the tests around it.
RunPod was smoke-tested end to end on 2026-09-08 — up, ssh, a job, the
pod-scoped self-stop, down. AWS EC2 was live-verified the same day, on
`t3.medium` (with an EBS data volume created, mounted, kept across `down` and
deleted on request) and on `g4dn.xlarge` (the Deep Learning AMI, Tesla T4):
up, ssh, a job, `sync`, a tunnel, live prices, the watchdog under systemd,
down. Spot launches and a deadline actually firing are the AWS parts still
unexercised. On Vast.ai the read half has now
talked to the live cloud — on 2026-09-09 the marketplace crawl, availability
and the account balance all answered, and `rig types` renders them — but
nothing has ever been rented there (no billing method on the account), so the
launch, footprint and self-stop parts of that column are still what rig
intends to do rather than observed behaviour.

Marketplace type tokens are `<count>x<GPU name with spaces as dashes>`, and
matching is case-insensitive, so `--type 1xrtx-4090` finds the same offers as
`--type 1xRTX-4090`. What a cloud cannot do fails loudly rather than quietly:
`spot: true` on RunPod and a `volume:` on Vast are refused before anything is
created, and the dashboard shows no balance for AWS.

**A RunPod restart wipes the footprint.** A pod's container disk is ephemeral,
so a restart from the RunPod console or API — or a start after the watchdog
stopped the pod — deletes `~/.rig` with the deadline, `ttl.sh` and the re-arm
hook in it. The pod then runs with **no watchdog** and `rig status` shows its
TTL as `-`. Run `rig adopt <name>` after any such restart.

## Use

```bash
rig                      # dashboard TUI: every configured cloud's instances, volumes, burn rate, balance
rig --provider vast      # dashboard for one cloud only
rig up                   # interactive picker over LIVE availability + pricing
rig up train             # headless: first available type from the profile
rig up --type CPU.4V.16G --name smoke --ttl 1h   # fully ad-hoc
rig up --type 1H100.80S.32V --env-file ~/secrets/hf.env   # push KEY=VALUE files ad hoc (repeatable)

rig status               # table: status, ssh endpoint, $/h, age, time-to-self-destruct
rig types --available    # what can actually be deployed right now
rig --provider vast types --available    # shop another cloud without touching config
rig ssh [-- cmd]         # interactive shell or one-off command (agent forwarded)
rig sync ~/code/project    # rsync a working tree to the box
rig sync data --exclude images/          # skip subtrees (repeatable, stacks on defaults)
rig sync ./data:/root/data ./hf.env:/root/exp/hf.env --jobs 3   # per-path dest, single files, parallel streams
rig fetch s3://bucket/set.tar            # download ON the instance, not through your machine
rig tunnel 8000 50051    # forward local ports (^C to stop)

rig run --name train -- bash train.sh   # detached tmux job, survives disconnect
rig ps                   # jobs: latest run of each (--all for every recorded run)
rig logs train -f        # follow the job log (--run <id> for an earlier run)
rig logs --watchdog      # the TTL watchdog's own record: what it deferred, and why
rig wait train           # block until it finishes; exit with ITS exit code (124 = --timeout)
rig wait train --for 'stage: train'   # block until a log line matches; prints it (125 = job ended first)
rig run --wait -- bash train.sh          # start + wait in one command
rig stop train           # kill a job

rig extend 6h            # push out the self-destruct deadline (or: rig extend <name> 6h)
rig down                 # delete instance (+ its OS volume); prints lifetime + cost
rig down --delete-volumes  # ... and destroy data volumes too
rig volumes              # list volumes; --rm <name> deletes a detached one
rig spend --since 168h   # spend summary from the local ledger (advisory)
rig spend -i train       # one instance's timeline: up, every extend, down and cost
```

### The dashboard

`rig` on its own opens the dashboard over **every provider the config knows**
— each `providers:` block plus the default one — asking them in parallel. Rows
carry a `PROVIDER` column whenever more than one cloud is in play, the burn
rate sums across all of them, and each cloud's balance is named in the header.
A cloud that cannot be reached (missing credentials, an expired token, an
outage) gets one error line of its own and leaves the other clouds' rows
alone. `rig --provider <name>` pins the dashboard to one cloud, as it pins
every other command.

| key | does |
| --- | --- |
| `↑`/`↓`, `k`/`j` | move the selection (scroll the log, on a run) |
| `enter` | open the selected instance, then one of its runs |
| `esc` | back up a level, with the selection and scroll where you left them |
| `n` | create an instance: which cloud, then that cloud's picker |
| `d` | delete the selected instance (confirms; data volumes are kept) |
| `f` | on a run: follow its log live, leaving the dashboard until ctrl-C |
| `s` | on a run: stop it |
| `r` | refresh now (it also refreshes every 15s) |
| `w` | split panes — instance list beside the detail, at 120 columns or wider |
| `?` | every action, and what the columns mean |
| `q` | quit |

Three levels. The **instances** screen is five complete columns — INSTANCE, STATUS,
`$/H`, SPENT, EXPIRES, JOBS — with the type, endpoint and location on the instance
itself rather than truncated to fit beside them. `enter` opens an **instance**: what it
is, its storage and what happens to each volume when the instance goes, and its runs.
`enter` again opens a **run**: its command, how it went, and its log.

Only the provider API is asked for the first paint, so the estate appears as fast as
`rig status` does. EXPIRES and JOBS need the instance itself — one ssh each, shared
between them — so they show `…` and fill in as the answers arrive. A stopped instance is
never asked, because it cannot answer. One the cloud calls running but ssh cannot reach
shows everything the API knows and says why the rest is missing: `running · no ssh`,
an unreadable expiry, and the ssh error itself against its jobs with a `rig adopt` hint.
"No jobs" and "could not ask" are never allowed to look alike.

Columns are sized to their contents and fitted to the terminal: nothing runs off the
side, a value too long for its column is clipped from the *middle* — names, mount paths
and commands differ at the end — and on a narrow window columns are squeezed and then
given up in a fixed order, cost last. The footer never clips a verb; anything that will
not fit moves into `?`. Selection is a background band, so a failed run stays red on the
row you are looking at, and `tui.theme: light` in the config swaps it for a pale band
with dark text.

`n` and `f` both leave the dashboard and come back: `n` runs the same picker `rig up`
uses, `f` runs the same follower `rig logs -f` does, because the ssh that follows a log
needs the terminal the dashboard is drawing on.

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
  named volume that `rig up` finds-or-creates in whichever location the
  instance lands in. Volumes are location-bound (datacenter on Verda and
  RunPod, availability zone on AWS), so a profile may accumulate one volume
  per location; locations that already hold the profile's volume are
  preferred during placement.
- **Preference lists, not fixed types.** GPU availability is volatile.
  Profiles declare `types: [A, B, C]` and rig takes the first that is
  actually deployable, optionally pinned to a location.
- **The watchdog writes down what it does.** Every deadline it defers, and
  the job or `busy.d` hook it deferred for, goes to `~/.rig/ttl.log` with a
  timestamp, along with arming and firing. `rig logs --watchdog` reads it.
  That is what answers "why is this instance still up, hours past its TTL" —
  before this, the deferral rewrote `~/.rig/deadline` silently and nothing
  recorded the reason. The log is capped and lives in the same file whether
  the watchdog runs under systemd or `nohup`, so reading it needs no
  knowledge of which.
- **TTL self-destruct.** `rig up` installs a systemd watchdog on the box
  that shuts it down when the deadline in `~/.rig/deadline` passes —
  deferring an hour at a time while any rig job is still running, or while
  any executable hook in `~/.rig/busy.d/` exits 0 (drop a script there to
  gate shutdown on a wandb run, a health endpoint, whatever). A shut-down
  instance stops billing for compute. On a container with no systemd the
  same script runs under `nohup setsid`, pid-filed at `~/.rig/ttl.pid` and
  re-armed on container start by `~/.rig/onstart.sh`; the line it runs at
  the deadline is the provider's self-stop call instead of `shutdown`
  (ADR-0008).
- **Jobs are tmux sessions, and every run is written down.** `rig run` wraps
  the command in a named tmux session, sources every file in `~/.rig/env/`
  first, and logs to `~/.rig/logs/<name>.<run>.log` with `<name>.log`
  pointing at the current run. Each run appends its start and its exit code
  to `~/.rig/jobs.jsonl`, so retrying a job no longer destroys the log of
  the run you are retrying: `rig ps` shows the latest run of each job,
  `rig ps --all` every run it has, and `rig logs <job> --run <id>` an
  earlier one. A run the record never saw finish — the instance stopped
  under it — reads as `interrupted`, not as exited. Nothing depends on your
  laptop staying connected.
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
  fresh listing drops entries that have gone stale and refreshes the cached ssh
  endpoint (host, port, user) of the rest — so a container whose mapped port
  moved on restart is reachable again after one such command. Both files tag
  every record with the provider it belongs to (absent = Verda, so files
  written before the other drivers existed read unchanged), so two clouds'
  `rig-train` never collide and `rig spend` subtotals per provider once more
  than one appears.

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

`rig status --json` carries the `provider` each instance is on and its `ssh`
endpoint (`host`, `port`, `user`), so an agent needs no per-cloud knowledge to
reach a box. The TTL watchdog is the safety net if the agent (or the human)
forgets the last step.

## Extending to other providers

`internal/provider` defines the interface (instances, volumes, images, SSH
keys, live availability, balance) and a registry: a driver calls
`provider.Register("<name>", factory)` from its `init()`, and `internal/cli`
blank-imports the drivers it ships. `Capabilities{Spot, Volumes, Balance,
DockerImages, SelfStop}` is how a driver declares what its cloud cannot do
(ADR-0007), whether an image is any Docker reference, and what the TTL
watchdog runs at the deadline (ADR-0008). Everything above the interface —
placement, volumes, TTL, jobs, TUI — is provider-agnostic;
`internal/provider/verda` is ~330 lines against the official SDK, and the
marketplace and EC2 drivers 900–1500 against plain `net/http` or the AWS SDK.

A new driver, in order:

1. `New(creds, Config)` and an `init()` registering a factory that resolves
   secrets with `cfg.Creds([]string{"X_API_KEY"})` and its own keys with
   `cfg.Decode(&Config{})` — the `providers:` block is the driver's.
2. The shopping half — `Locations`, `InstanceTypes`, `Availability`,
   `Images` — speaking rig's vocabulary: normalised `Status` constants,
   type tokens, location codes.
3. `CreateInstance`, `GetInstance`, `ListInstances`, `DeleteInstance`,
   filling `Instance.SSH` and wrapping "no stock" as `ErrNoCapacity` so `up`
   walks to the next candidate instead of failing.
4. Volumes and `Balance`, or `ErrUnsupported` plus the matching
   `Capabilities` false — never a silent no-op.
5. For a container cloud, `Capabilities.SelfStop`: a line that stops the
   instance using only the token the provider injects (ADR-0003, ADR-0008).
6. Tests. `internal/provider/fake` is the in-memory Provider the core tests
   drive (scriptable availability, create errors, status sequences,
   endpoints). A driver's own tests run it against `httptest` with recorded
   JSON under `testdata/` and assert the vocabulary mapping — token ↔ GPU
   id, status normalisation, endpoint derivation, mount-command synthesis —
   never HTTP details; the AWS driver puts the SDK clients behind narrow
   interfaces with hand-rolled fakes instead.

## License

MIT — see [LICENSE](LICENSE).
