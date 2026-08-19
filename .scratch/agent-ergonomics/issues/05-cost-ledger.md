# 05 — cost accounting that survives teardown

Status: resolved
Source: ../HANDOFF.md §05

## Problem

`rig status` shows burn rate while an instance lives; the number disappears at `rig down`.
Reporting spend meant reconstructing `$1.79/h × 6.1h` by hand. A standalone audit exercise has
already been done reverse-engineering spend from provider audit logs — unnecessary if rig recorded
it at source.

## Shape

1. `rig down` prints the instance's lifetime and cost as it deletes.
2. A local append-only ledger (`~/.config/rig/ledger.jsonl`, record on up and on down) plus
   `rig spend [--since D]` to summarise.

## Where

`internal/cli/lifecycle.go:112` (down); price fields already used by the status renderer at
`:241–251`.

## Notes

Ledger is advisory, not authoritative — provider billing is the source of truth. It will miss
instances torn down by TTL rather than `rig down`; handle that explicitly (e.g. mark records with
no `down` event as `unclosed`, estimate via TTL).
