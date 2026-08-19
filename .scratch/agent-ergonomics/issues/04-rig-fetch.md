# 04 — `rig fetch`: pull data onto the instance directly

Status: resolved
Source: ../HANDOFF.md §04

## Problem

Biggest wall-clock waste of the exercise: a 1.2 GB S3 dataset pushed *through the operator's box*
to the instance at ~50 MB/min (~20 minutes). The instance has far better bandwidth to S3.

## Shape

`rig fetch <url> [--dest]` running the transfer on the instance (curl for http(s), the
provider/AWS CLI for s3://).

## Credential decision (Darren, 2026-08-18)

**Short-lived creds.** Rationale: fetching is not the only use case — scripts and tools on the
box need credentials too. AWS creds are minted via STS from the operator's local identity and
forwarded; they expire on their own, so the no-long-lived-secrets-on-box stance holds.

Static API keys (wandb, HuggingFace, …) are a different lifecycle — rig can only transport them,
not shorten them — but should share the same transport. That general mechanism is split out as
issue `08-env-creds-transport.md`; `rig fetch` only needs the STS minting for its own command.

## Implementation shape

- `rig fetch <url> [--dest] [-i inst]`
  - `http(s)://` → run `curl -fL -o <dest>` on the instance. No creds involved.
  - `s3://` → mint STS session creds locally (from the operator's AWS profile), export them into
    the environment of the single remote command, run `aws s3 cp` on the instance. Nothing
    persisted on the box.
