# rig

GPU cloud workbench: provisions ephemeral GPU instances on a provider (Verda today), gets you —
or your AI agent — onto them, and runs detached jobs that survive disconnects.

## Language

**Instance**:
An ephemeral cloud VM created and managed by rig, identified by a `rig-` hostname prefix.
_Avoid_: box, machine, server, node

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
The on-instance service that enforces the deadline.

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
