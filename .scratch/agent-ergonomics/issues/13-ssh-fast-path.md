# 13 — `rig ssh` resolves the instance through the provider on every call

Status: resolved
Source: agent session d301490d (training run on one H100, 2026-09-06); code read against ec9471b

## Problem

Every `rig ssh`/`rig run`/`rig logs` calls `App.Resolve`, which lists instances via the provider
API before opening ssh. That is fine once; inside a polling loop it is a second of latency and one
API call per tick (240 calls over a four-hour watch at one per minute). This is why the agent called
ssh directly 36 times with `-i ~/.ssh/rig_ed25519 -o StrictHostKeyChecking=no -o
UserKnownHostsFile=/dev/null -o LogLevel=ERROR` spelled out each time.

## Shape

Either or both:

- **ssh config stanza.** `rig up` (and `rig adopt`) write `~/.ssh/rig.d/<name>` with
  `Host rig-<name>`, HostName, User, IdentityFile and the ControlMaster options rig already uses;
  `rig down` removes it. Plain `ssh rig-exp42b` then works, and so does
  `rsync -e ssh ... rig-exp42b:/path`. Needs an `Include ~/.ssh/rig.d/*` line in
  `~/.ssh/config`; `rig config` can print the one-time instruction.
- **local name-to-IP cache.** `Resolve` consults `~/.config/rig/instances.json` (written on up,
  cleared on down, short TTL) before the provider, falling back on a miss or a connection failure.

Also print the resolved IP in `rig status --json` (already there) and mention `rig ssh --ip` or
`rig ip <name>` as a cheaper primitive if the stanza route is rejected.

## Where

`internal/core/core.go` `Resolve`/`Up`/`Down`, `internal/sshx/sshx.go` `baseArgs` (shared with
the stanza), `internal/cli/lifecycle.go`.

## Comments

**Local cache landed (2026-09-06, agent):** took the name-to-IP cache route, not the ssh
stanza (ADR-0006 records why). New package `internal/instcache` keeps
`~/.config/rig/instances.json` (beside the config, like the ledger and tunnel state): `Put` on
`rig up` (`core.Up`) and `rig adopt`, `Remove` on `rig down` (`core.Down` — also when the
provider answers "not found"), entries expire after 12h, and `core.Managed` reconciles the file
against every fresh listing (refresh running entries, drop the rest; never adds). Seam:
`App.Resolve(key)` with a non-empty key consults the cache first and returns the cached
`provider.Instance` without a provider call; `App.ResolveFresh` is the provider-only path and
is what `down`, `extend` and `adopt` now use (`status`/`up` already go through `Managed`).
ssh/run/logs/ps/stop/wait/sync/fetch/tunnel are on the cached path unchanged. The cache is
advisory: a stale hit costs one ssh failure (10s ConnectTimeout), `rig status` heals it.
Unit tests: `internal/instcache/instcache_test.go` (write/read/replace/expiry/remove/reconcile/
corrupt file, temp dir). Not done: ssh config stanzas, `rig ip`.
