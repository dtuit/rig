# rig

GPU cloud workbench: provisions ephemeral GPU instances on a provider (Verda, RunPod, Vast.ai or
AWS EC2 — one per command), gets you — or your AI agent — onto them, and runs detached jobs that
survive disconnects.

## Language

**Provider**:
A cloud rig drives through the `Provider` interface, chosen per command (`--provider`), per
profile (`provider:`) or by the config default.
_Avoid_: cloud (in code and command names), vendor, host

**Driver**:
The package implementing `Provider` for one cloud (`internal/provider/runpod`), registered by
name in its `init()`.
_Avoid_: plugin, backend, adapter, integration

**Instance**:
An ephemeral cloud VM or container created and managed by rig, identified by a `rig-` hostname
prefix (Vast.ai carries the name in the instance label).
_Avoid_: box, machine, server, node, pod (RunPod's word for it — the driver maps, the docs and
code say instance)

**Fleet**:
Every provider one config drives, opened lazily and surveyed together — what the dashboard shows
and what a burn rate sums over. An `App` is one cloud; a `Fleet` is the whole estate.
_Avoid_: cluster, estate (in code), group

**Endpoint**:
The host, port and user rig's ssh, rsync and tunnel commands connect to. A VM's is `IP:22`; a
container's is a host-mapped port or the provider's proxy host, and it can move on restart.
_Avoid_: address, IP (an IP is for display; the endpoint is what rig dials)

**Type token**:
The `<count>x<GPU>` spelling of an instance type on a marketplace provider — `1xA100-SXM`,
`2xRTX-4090` — GPU-name spaces replaced by dashes, matched case-insensitively. Verda and AWS use
their native type names instead.
_Avoid_: SKU, shape, flavor

**Location**:
Where an instance and its volumes live: a Verda location code, a RunPod datacenter id, a Vast.ai
geolocation, or an AWS availability zone inside the provider block's region.
_Avoid_: region (AWS-specific and one level up), zone, site

**Job**:
A named detached command on an instance, running in a tmux session, with its log and recorded
exit code kept on the instance.
_Avoid_: task, process, session

**TTL**:
The duration from now until an instance self-destructs.
_Avoid_: lease, expiry

**Deadline**:
The absolute self-destruct time stored on the instance. The watchdog defers it while jobs run.
_Avoid_: timeout (a timeout bounds a wait; a deadline ends an instance)

**Watchdog**:
The on-instance service that enforces the deadline — a systemd unit, or a `nohup setsid` process
re-armed on start where there is no systemd.

**Self-stop**:
The provider-specific shell line the watchdog runs at the deadline: `shutdown -h now` on a VM, or
the API call that stops a container using only the instance-scoped token the provider injects
(ADR-0008).
_Avoid_: shutdown command, kill switch

**Profile**:
A named launch preset in the user's config: instance-type preference list, spot choice, volume,
TTL, env files.
_Avoid_: template, preset

**Data volume**:
A persistent volume that survives `rig down`.
_Avoid_: disk

**OS volume**:
The boot volume, deleted with its instance.

**Env file**:
A KEY=VALUE file pushed to the instance; every job sources all pushed env files before running.

**Footprint**:
Everything rig installs on an instance: the `~/.rig` layout, the watchdog, pushed env files.

**Adopt**:
Installing or repairing the footprint on an instance rig did not just create.

**Spot**:
Provider spot-priced capacity — cheaper, reclaimable.

**Burn rate**:
Total dollars per hour across currently running instances.

**Ledger**:
The local append-only record of instance lifecycle events used for spend reporting. Advisory
only — the provider's billing is authoritative.
