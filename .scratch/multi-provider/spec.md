# Spec: multi-provider rig (RunPod, Vast.ai, AWS)

Status: needs-triage
Written: 2026-09-08

## Problem Statement

rig is "provider-abstracted" with exactly one driver. Verda's GPU availability is volatile and
its price list is narrow; the team already holds RunPod and Vast.ai accounts (keys in
`secrets/runpod.env`, `vast.env`) and an AWS organisation (SSO profiles in `~/.aws`).
Today none of that capacity is reachable from `rig up`. The `Provider` interface exists, but it
was shaped by Verda and quietly bakes in Verda assumptions that the other three break:

- **Status strings** (`running`, `discontinued`, `no_capacity`, `offline`) are Verda's, compared
  literally in core, tui, cli and instcache.
- **SSH is `user@ip:22`.** RunPod and Vast instances are Docker containers whose sshd is reached
  on a host-mapped port (RunPod remaps it on every restart) or via a proxy host.
- **The footprint assumes systemd and `shutdown -h now`.** Containers have neither; the TTL
  watchdog — rig's whole safety story — silently cannot install.
- **Credentials are `VERDA_CLIENT_ID/SECRET`**, resolved in `config.Credentials()`.
- **Instance types are fixed catalogue names.** Vast has no types at all — it has per-machine
  offers; RunPod's GPU ids are 25-character device strings.
- **Capacity is known before create.** AWS only tells you at `RunInstances` time
  (`InsufficientInstanceCapacity`).
- **Ledger and instance cache have no provider dimension**, so two providers would collide on
  names and burn-rate maths.

## Solution

Keep the core loop (`up → sync → run → logs → down`) and every user-facing contract byte-for-byte
on Verda, and make the provider a first-class dimension: a registry of drivers behind the
existing interface, chosen per command (`--provider`), per profile (`provider:`), or by the
config default. Fix the interface's Verda-isms in a prefactor (normalised status, SSH endpoint,
capacity sentinel, provider-tagged local state), teach the footprint to run without systemd,
then land three drivers: RunPod (REST v2), Vast.ai (REST v0), AWS EC2 (SDK v2). Each driver is a
few hundred lines that owns its own vocabulary mapping; everything above the interface stays
provider-agnostic.

Recommended order: 01–04 (prefactor), 05 (container footprint), 06+07 (RunPod), 08 (Vast),
09 (AWS), 10–11 (TUI, docs). AWS can be pulled ahead of RunPod/Vast if the team's GPU quotas
turn out to be non-zero — it has no container problems at all.

## Facts established (2026-09-08)

**Accounts.** RunPod and Vast API keys work (read-only probes). RunPod (key replaced
2026-09-08, new account) has **~$10.79** of balance — enough for the issue 07 test and a RunPod
smoke run at a cheap GPU. Vast's balance is **$0.00** with no billing method and no SSH key
registered. AWS SSO token is expired (`aws sso login`
needed); `default` and `work` profiles point at the main account, region `ap-southeast-2`.
Vast and AWS cannot be smoke-tested until Vast is funded and `aws sso login` is run.

**RunPod.** REST **v1 is deprecated (retires 2026-11-15)**; **v2 at `api.runpod.io/v2` is GA**;
GraphQL retires early 2027. v2 `GET /catalog/gpus?include=AVAILABILITY&product=POD&cloud=SECURE`
returns per GPU: `id` ("NVIDIA A100-SXM4-80GB"), `name` ("A100 SXM"), `memory`,
`price.{secure,community}`, `maxCount`, and `dataCenters[].{id,availability}` — a direct fit for
`Availability()`. `GET /catalog/datacenters` lists ids like `US-TX-3` with `networkVolumeTypes`.
Pods: `POST /pods` (`name`, `image`, `gpu.{id,count}`, `cloud`, `dataCenterIds`, `disk`,
`mounts.network[{volumeId,path}]`, `ports ["22/tcp"]`, `env`, `startSsh`), `POST /pods/{id}/action`
start/stop/terminate, pod carries `ssh.direct{host,port}`, `runtime.ports[]`, `dataCenterId`.
Network volumes are datacenter-bound (10–4096 GB, $0.07/GB/mo). **v2 has no spot/bid fields**
(v1 had a `interruptible` bool; bidding is GraphQL-only). **No balance endpoint in v2**; GraphQL
`myself.clientBalance` still works. Pods are containers: `start.sh` is PID 1, no systemd, tmux
and sshd present in official images, user root. A pod-scoped `RUNPOD_API_KEY` is injected (on disk in
`/etc/rp_environment`, not in ssh sessions' env) and **verified 2026-09-08 to stop its own pod**
via `POST /v2/pods/{id}/action {"action":"stop"}` (issue 07). Stopped pod:
compute $0, volume disk $0.20/GB/mo, GPU not reserved, port mappings change on restart.

**Vast.ai.** Base `console.vast.ai/api/v0`, `Authorization: Bearer`. Offers: `POST /bundles/`
with a filter DSL (`gpu_name eq`, `num_gpus`, `reliability`, `geolocation`, `rentable`,
`verified`, `dph_total`, `direct_port_count`, `type on-demand|bid`); offer ids are ephemeral, the
only stable handle is `machine_id`. Create: `PUT /asks/{offer_id}/` (`image`, `disk`, `env`
including `"-p 22:22":"1"`, `runtype "ssh_direc ssh_proxy"`, `onstart`, `label`, `price` for
bids, `cancel_unavail`). Response includes `instance_api_key`, injected as `CONTAINER_API_KEY`,
**scoped to start/stop/destroy that one instance** — a sanctioned self-stop path. SSH: direct via
`public_ipaddr` + `ports["22/tcp"][0].HostPort` when the host has open ports, else proxy
`ssh_host:ssh_port`; user root. Lifecycle: `PUT /instances/{id}/ {"state":"stopped|running"}`,
`DELETE /instances/{id}/`; `actual_status` loading/running/exited/offline. Stopped instances bill
storage and have no GPU reservation. Volumes are **machine-bound**. Balance: `GET /users/current/`
→ `balance`. SSH keys: `POST /ssh/` account-level. No Go SDK; rate limits per endpoint (429).

**AWS.** `aws-sdk-go-v2` (ec2 v1.329.0 current) with the default credential chain handles SSO
profiles. EBS is AZ-bound; on-demand capacity is unknowable before `RunInstances`; GPU vCPU
quotas (G/VT: L-DB2E81BA, P: L-417A185B) are often 0 on new accounts; no balance API.

## User Stories

1. As a user, I want `provider: runpod` (or `vast`, `aws`) in config and the rest of my workflow unchanged, so that switching clouds is a one-line edit.
2. As a user, I want `rig --provider vast types --available` to work without touching config, so that I can shop across clouds before committing a profile.
3. As a user, I want a profile to pin its provider (`provider: runpod`), so that `rig up train` and `rig up cheap` can target different clouds from one config.
4. As a user, I want per-provider credentials resolved the way Verda's are today — env vars first, then a KEY=VALUE file — and AWS via the standard SDK chain with an optional profile, so that nothing new has to be learned about secrets.
5. As a user, I want `rig status`, `rig spend` and the dashboard to say which provider each instance is on, so that a $2/h RunPod pod and a $2/h Verda box are never confused.
6. As an agent, I want `rig up` on a container provider to still install the TTL watchdog, so that a forgotten pod stops billing exactly like a forgotten VM.
7. As an agent, I want `rig ssh`, `rig run`, `rig sync`, `rig tunnel` and `rig wait` to just work against a container's mapped ssh port or proxy host, so that no command needs a `--port`.
8. As a user, I want `rig up` to try the next candidate location or type when the provider reports no capacity at create time, so that AWS's late "InsufficientInstanceCapacity" behaves like Verda's early availability list.
9. As a user, I want RunPod types spelled `1xA100-SXM`, `8xH100-SXM` and Vast types spelled the same way, so that a `types:` preference list reads the same across marketplace providers.
10. As a user, I want data volumes on RunPod (network volumes) and AWS (EBS) to follow the same find-or-create-in-location rule as Verda, so that the persistence story is unchanged.
11. As a user, I want clear errors — not silent degradation — when a provider lacks a feature (spot on RunPod v2, volumes on Vast, balance on AWS), so that I know it's the cloud, not rig.
12. As a user, I want `rig up` on AWS to create the key pair and a `rig` security group on first use, so that a fresh account needs no console clicking.
13. As a user, I want AWS instance types to be the native names (`g6e.xlarge`) and images to default to the Deep Learning AMI, so that `rig up --type g6e.xlarge` works with no image research.
14. As a user, I want `rig down` to keep the data volume and destroy everything else, on every provider, so that the cost contract of `down` holds regardless of cloud.
15. As a user, I want the local ledger and instance cache to survive the upgrade, so that `rig spend --since 720h` still shows my Verda history.
16. As a security-conscious user, I want the on-instance self-stop mechanism on containers to use only the provider's instance-scoped token, never my account key, so that ADR-0003 holds on every provider.
17. As a user, I want `rig types` on Vast to show one row per GPU shape with the cheapest current price and offer count, so that a marketplace is readable as a catalogue.
18. As a user, I want Verda behaviour to be unchanged by all of this — same output, same config file — so that existing profiles and scripts keep working.

## Implementation Decisions

- **Registry, not a switch.** `internal/provider` gains `Open(name string, cfg config.ProviderConfig) (Provider, error)` backed by a `map[string]Factory`; each driver package registers itself. `cli.newApp` resolves the provider name from `--provider` → profile pin → `config.provider` → `verda`.
- **Config shape.** New `providers:` map keyed by provider name, each a free-form block decoded by the driver (`credentials_file`, plus driver-specific keys: RunPod `cloud: secure|community`, Vast `min_reliability`, `bid_markup`, `direct_only`, AWS `profile`, `region`, `security_group`). The legacy top-level `credentials_file` keeps meaning "Verda's". Profiles gain `provider:`. Env var names: `VERDA_CLIENT_ID/SECRET`, `RUNPOD_API_KEY` (accepting `RUNPOD_KEY` as in the team's secrets file), `VAST_API_KEY`; AWS uses the SDK chain and honours `AWS_PROFILE`.
- **Normalised status.** `provider.Status` constants: `Provisioning`, `Running`, `Stopped`, `Error`, `Gone`, `NoCapacity`. Drivers map; core/tui/cli/instcache compare constants. Verda's raw string is kept in a new `RawStatus` field for display/debugging.
- **SSH endpoint on the instance.** `Instance.SSH{Host, Port, User}` replaces the implicit `cfg.ssh.user@IP:22`. `sshx.Target` gains `Port` (ssh `-p`, rsync `-e`, `WaitForSSH` dial). The instance cache stores the endpoint; because RunPod remaps ports on restart, any status-bearing command that sees a changed endpoint rewrites the cache entry. Config `ssh.user` becomes an override of the provider's default (root on Verda/RunPod/Vast, `ubuntu` on AWS DLAMI).
- **Capacity sentinel and create retry.** `provider.ErrNoCapacity` (wrapped by drivers on AWS ICE, RunPod "no available GPU", Vast offer-taken races, Verda `no_capacity` status). `core.Pick` returns an ordered candidate list; `Up` walks it, trying `CreateInstance` per candidate and moving on only on `ErrNoCapacity`. All other errors abort as today.
- **Feature gaps are typed.** `provider.ErrUnsupported` for spot on RunPod v2, volumes on Vast, balance on AWS. The dashboard hides the balance tile on `ErrUnsupported`; `up` refuses a `spot: true` / `volume:` profile with the sentinel's message.
- **Type tokens for marketplaces.** RunPod and Vast types are `<count>x<GPU-name>` with spaces replaced by `-` (`1xA100-SXM`, `2xRTX-4090`), derived from the catalogue `name` (RunPod) / `gpu_name` (Vast). Drivers parse the token back; the RunPod driver resolves it to `gpu.id`, the Vast driver into an offer query. Verda and AWS keep native names. Matching is case-insensitive.
- **Locations.** Verda: location code. RunPod: datacenter id (`US-TX-3`). Vast: `geolocation` string as the API returns it (country-ish); an offer id is never a location. AWS: availability zone (`ap-southeast-2a`) inside the configured region — one region per provider block, because EBS and pricing are per-region and multi-region placement is out of scope.
- **Container footprint.** `remote.Init` detects an init system (`[ -d /run/systemd/system ]`). With systemd: today's unit. Without: the same `ttl.sh` runs under `nohup setsid`, pid-filed at `~/.rig/ttl.pid`, re-armed by a one-line `~/.rig/onstart.sh` the driver wires into the container's start (RunPod: `env.RIG_ONSTART` consumed by a wrapper `args`; Vast: `onstart`). The shutdown line becomes driver-supplied: `shutdown -h now` (VMs), `curl -s -X PUT …/instances/$CONTAINER_ID/ -H "Authorization: Bearer $CONTAINER_API_KEY" -d '{"state":"stopped"}'` (Vast), see issue 07 for RunPod. `Init` also installs `tmux` and `rsync` via apt when missing (root in a container), so any Docker image with sshd works.
- **RunPod self-stop: resolved (issue 07, verified on a real pod).** The watchdog sources
  `/etc/rp_environment` and calls `POST /v2/pods/$RUNPOD_POD_ID/action {"action":"stop"}` with the
  injected pod-scoped key. Options considered were: (a) pod-scoped `RUNPOD_API_KEY` against `POST /v2/pods/{id}/action stop` — test first, costs cents; (b) exit the container (kill `start.sh`'s `sleep infinity`) so the pod goes `EXITED` = stopped — zero credentials, image-dependent; (c) push a RunPod *Restricted* key as an env file — an ADR-0003 exception the owner must accept; (d) a workstation-side supervisor (tunnel supervisor pattern) — weakest, fails exactly when the laptop is closed.
- **RunPod driver specifics.** REST v2 only; GraphQL used for one query (`myself.clientBalance`) behind `ErrUnsupported` fallback. `cloud` from config (default `secure`) filters catalogue prices and availability; `--location` pins `dataCenterIds`. Create sets `ports ["22/tcp"]`, `startSsh: true`, `env.PUBLIC_KEY` = rig's key (so `EnsureSSHKey` returns `""` and nothing is registered account-side), `disk` = `os_volume_gb` (default 20), `mounts.network` when a volume is requested (mount path `/workspace`). Default image `runpod/pytorch:<pinned tag>` from the driver, overridable via `image:`. `spot` → `ErrUnsupported`. `Volume.MountCommand` empty (mounted at create).
- **Vast driver specifics.** `InstanceTypes()` = one row per `(num_gpus, gpu_name)` from a broad offer search (`rentable`, `verified`, `reliability ≥ min_reliability` default 0.95), cheapest `dph_total` as the price, offer count in `Description`. `Availability()` groups the same search by `geolocation`. `CreateInstance` re-searches for the token in the location, orders by `dph_total`, tries offers in turn with `cancel_unavail: true`, wrapping a taken offer as `ErrNoCapacity`. `runtype "ssh_direc ssh_proxy"`, `env {"-p 22:22":"1"}`, `label` = hostname (rig's `rig-` prefix lives in the label; `ListInstances` filters on it), `disk` = `os_volume_gb` (default 20). Endpoint: direct when `ports["22/tcp"]` is present, else proxy — `direct_only: true` filters offers to `direct_port_count > 0`. Spot = `type: bid` with `price = min_bid × bid_markup` (default 1.2). Default image `vastai/pytorch:<pinned tag>`. Volumes → `ErrUnsupported` (machine-bound; follow-up). Key registration: `POST /ssh/` once (account-level).
- **AWS driver specifics.** `ec2`, `ssm`, `pricing` clients from the SDK chain plus optional `profile`. Types: `DescribeInstanceTypes` filtered to accelerator families (`g4dn g5 g6 g6e gr6 p3 p4d p5 p5e p5en`) plus any type named explicitly; on-demand price via `pricing.GetProducts` cached for 24h under `~/.cache/rig/`, spot via `DescribeSpotPriceHistory`. Availability: `DescribeInstanceTypeOfferings(availability-zone)` — existence, not capacity; capacity comes from the create-retry. Images: SSM public parameter for the DLAMI base OSS-NVIDIA Ubuntu 24.04 (exact parameter path to verify in the issue), any `ami-…` accepted. Key pair: `ImportKeyPair` matched by fingerprint. Networking: default VPC/subnet in the AZ, `rig` security group (22/tcp inbound) created on first use, public IP on launch. Instances tagged `Name=rig-<name>`, `rig:managed=true`; `ListInstances` filters on the tag. Spot: `InstanceMarketOptions` one-time, interruption `stop`. Volumes: gp3 EBS in the AZ, attached after `running`; driver synthesises `MkdirCommand`/`MountCommand` that finds the NVMe device by volume-id serial, `mkfs.ext4` only when `blkid` finds no filesystem, mounts at `/mnt/<name>`. `DeleteInstance` terminates (root EBS goes with it), waits for data volumes to detach, deletes those requested. Balance → `ErrUnsupported`. `VcpuLimitExceeded` is rewrapped with the quota code to request.
- **Local state.** Ledger events and instance cache entries gain `provider` (missing = `verda`, so old files read unchanged). `Managed()` reconciles only entries of the active provider; `rig spend` totals per provider when more than one appears.
- **Type/availability displays** grow a provider name in the header, not a column; the dashboard shows `provider · balance` and hides balance on `ErrUnsupported`.

## Testing Decisions

- **A fake in-memory `Provider`** in `internal/provider/fake` (scriptable availability, create outcomes, statuses) — the seam core has never had. Core tests cover: candidate walk on `ErrNoCapacity`, `ErrUnsupported` refusals, endpoint propagation to `Target`, provider-tagged ledger/cache writes, unchanged Verda flow.
- **Drivers against `httptest` fixtures** (RunPod, Vast: recorded JSON from the read-only probes above; AWS: the SDK's `ec2` client behind a narrow interface with a hand-rolled fake). Tests assert the vocabulary mapping — token ↔ gpu id, status normalisation, endpoint derivation (RunPod direct, Vast direct vs proxy), mount-command synthesis — never HTTP details.
- **Footprint** tests for the no-systemd branch are pure: the generated `ttl.sh`/`onstart.sh` text with the driver's shutdown line substituted; no real ssh.
- **`sshx` argv builders** get `Port` cases (ssh `-p`, rsync `-e`, tunnel).
- **Not tested automatically:** live clouds. Each driver issue ends with a manual smoke checklist (`types --available`, `up --type <cheapest> --ttl 20m`, `ssh`, `run`, `sync`, watchdog fires, `down`), to be run once the account is funded.

## Out of Scope

- Multi-provider *simultaneous* views (one dashboard over Verda + RunPod). One provider per command.
- Multi-region AWS placement; RunPod global networking; Vast clusters/network volumes.
- Vast volumes (machine-bound; needs placement pinned to a machine) — follow-up spec.
- RunPod spot/bidding — revisit when REST v2 grows it, or accept the GraphQL dependency knowingly.
- RunPod proxy SSH (`ssh.runpod.io`) — no scp/port-forward, so unusable for rig; direct TCP only.
- Serverless/endpoints on RunPod, Vast templates marketplace, AWS SageMaker.
- Cost Explorer integration for an AWS "balance" tile.
- Windows workstations.

## Further Notes

- The RunPod secrets file names its key `RUNPOD_KEY`; the driver accepts both spellings rather
  than asking the team to rename a shared file.
- Vast's `reliability` filter key vs `reliability2` response field, and RunPod's `PUBLIC_KEY` vs
  `SSH_PUBLIC_KEY` docs inconsistency, are recorded in the driver issues so implementers don't
  rediscover them.
