# 08 — general env/credential transport to instances

Status: needs-triage
Source: conversation with Darren, 2026-08-18 (spun out of issue 04)

## Motivation

Fetching is not the only thing that needs credentials on the box — scripts and tools do too
(AWS SDKs, wandb, HuggingFace, …). Item 04's STS decision covers AWS for a single `rig fetch`
command; this issue is the general mechanism.

## Two credential classes, one transport

- **Mintable, short-lived (AWS via STS):** rig generates them from the operator's local identity,
  they self-expire; expiry can be aligned with the instance TTL. rig owns the lifecycle.
- **Static API keys (wandb, HF, …):** rig can only transport, not shorten. Mitigation is scoping
  (e.g. read-only HF token) plus the box TTL bounding exposure. User owns the lifecycle; docs say
  so.

## Proposed shape (unconfirmed)

- Transport: `~/.rig/env` on the instance, mode 0600, sourced by `rig run` jobs and `rig ssh`
  sessions, never synced back (extends the `.env` rsync exclusion from `c126e34`).
- `rig env push KEY=VALUE ...` / `rig run --env-file FILE` — explicit static-key transport.
- `rig creds aws [--duration D]` — mints STS session creds and pushes them via the same
  transport; default duration matched to instance TTL.

## Open questions

- Command naming (`env push` vs `creds`), and whether `--env-file` on `run` is enough on its own.
- Should `rig up` warn if `~/.rig/env` creds outlive the TTL extension story?

## Comments

**New evidence (2026-09-06, agent session d301490d):** an ad-hoc `rig up --type ... --name ...`
has no way to push an env file at all; `EnvFiles` only flows from a profile
(`core.SpecFromProfile`). The session needed a HuggingFace token on the instance and rsynced
`huggingface.env` by hand to a bespoke path that its scripts then sourced, bypassing
`~/.rig/env` entirely. Minimum viable slice of this issue: `rig up --env-file FILE` (repeatable)
and `rig env push FILE` for a running instance, both landing in `~/.rig/env` via the existing
`remote.PushEnvFile`. The STS half can follow.
**`--env-file` slice landed (2026-09-06, agent):** the minimum transport for ad-hoc launches:
`rig up --env-file FILE` (repeatable `StringArray`, `internal/cli/lifecycle.go`) appends to
`spec.EnvFiles`, so the files ride the existing `remote.PushEnvFile` path exactly like a
profile's `env_files` (stacking on them when a profile is used). Nothing else here is
implemented — `rig env push`, `rig run --env-file`, `rig creds aws`/STS and the naming
question stay open for triage; status left as needs-triage.
