# The instance cache is advisory, not authoritative

`App.Resolve` with a named instance answers from a local cache
(`~/.config/rig/instances.json`, written on up and adopt, removed on down) before asking the
provider, so the ssh-heavy commands (ssh, run, logs, ps, stop, wait, sync, fetch, tunnel) skip
a provider listing per call — in a polling loop that was a second of latency and one API call
per tick. This is the second deliberate break in the "no local state" rule after the spend
ledger (ADR-0004) and carries the same stance: the cache is advisory and the provider is the
source of truth. Entries are trusted for 12h because instances are ephemeral; every fresh listing
(`Managed`, which `status`, `up`, `down --all`, every `ResolveFresh` and every cache miss run)
reconciles the cache — refreshing entries still running, dropping the rest. A fresh resolve also
rewrites a cached entry whose ssh endpoint has moved, because a container's host-mapped ssh port
is reassigned on every restart, so one status-bearing command makes such an instance reachable
again. The status-bearing commands never read it: `down`, `extend` and `adopt` go through
`ResolveFresh`, `status` and `up` through `Managed` directly. A stale hit therefore costs one
failed ssh (ConnectTimeout is 10s), fixed by `rig status`, and `rig down` forgets an entry even
when the provider answers "not found". We chose this over ssh config stanzas (`~/.ssh/rig.d/<name>`,
which need a one-time `Include` edit and leave rsync/tunnel resolution unsolved) and over
trusting the cache (it cannot see teardowns by the watchdog or from another machine).
