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
