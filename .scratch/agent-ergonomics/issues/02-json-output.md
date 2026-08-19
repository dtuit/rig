# 02 — machine-readable output (`--json`)

Status: resolved
Source: ../HANDOFF.md §02

## Problem

Every status read has to be scraped (`rig ps | awk '/^train/{print $2}' | tr -d ' \r'`) —
brittle against any formatting change, and the only option available.

## Shape

A persistent `--json` flag on the root command (`internal/cli/root.go:49`), honoured by `status`,
`ps`, `types`, and `volumes`. Emit the existing structs; do not invent a parallel schema.

## Where

- `internal/cli/lifecycle.go:217` — status render; burn-rate calc at `:233–251`
- `internal/cli/jobs.go:71` — ps
- `internal/cli/inventory.go:20` — types; `:104` — volumes

## Done when

- `rig status --json | jq -r '.[0].ttl'` works.
- Human tables are byte-identical to today when `--json` is absent.

## Notes

- This is a seam question (where rendering ends and data begins), not a formatting question —
  handoff suggests the `codebase-design` skill.
- CLI-contract change → deserves an ADR.
