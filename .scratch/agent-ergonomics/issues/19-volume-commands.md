# 19 — standalone volume commands: create, attach, detach

Status: needs-triage
Source: review of agent session d301490d (2026-09-06) and the 2026-09-04 lab run, both of which re-uploaded 7.9 GB of datasets and rebuilt the environment; code read against 01b90d0

## Problem

A data volume can only come into being as a side effect of `rig up`, and can only be attached at
launch. There is no way to: create and fill a volume ahead of an experiment on a cheap CPU
instance; attach a volume to an instance that is already running (for example after realising the
datasets are needed); detach one to move it to another instance in the same datacenter without
tearing the first down. The provider interface already has `AttachVolume` and `DetachVolume`
(`internal/provider/provider.go:104`) and the Verda driver implements them; nothing in the CLI
exposes them. `rig volumes` (`internal/cli/inventory.go:135`) only lists and deletes.

## Shape

Make `volumes` a verb group, keeping today's list as the default and the flag form of delete for
one release:

- `rig volumes create NAME --size GB --location LOC [--type NVMe_Shared]`
- `rig volumes attach NAME [-i instance]`: attach, then run the provider's mkdir and mount
  commands (the `MountVolume` path `rig up` already uses); refuse if the volume is attached
  elsewhere or in another datacenter, naming the mismatch.
- `rig volumes detach NAME [-i instance]`: unmount best-effort (`umount` on the mount path from
  the provider's mount command), then detach.
- `rig volumes rm NAME` as the verb form of `--rm`; the issue 06 audit already flagged the flag
  form as the one destructive action that is not verb-shaped.
- `--json` on the list renders the existing struct (ADR-0002); the new verbs print one line.

## Where

`internal/cli/inventory.go`, `internal/core/core.go` (`MountVolume`, a new `Unmount`),
`internal/provider/provider.go` (no interface change needed), README volumes section.

## Open question

Whether an attached volume should defer the TTL watchdog or be unaffected by it. Today shutdown
does not touch data volumes, so unaffected is the sensible default; state it in the README.
