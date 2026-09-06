# 16 — `rig types` default output is too long for an agent to read whole

Status: resolved
Source: agent session d301490d (training run on one H100, 2026-09-06); code read against ec9471b

## Problem

`rig types` with no flag prints every type (more than 40 rows, GB300 first). The agent piped it
through `head -60`, and the A100 rows it was choosing between fell off the end. `--available`
exists and would have been the right call, but nothing in the output points to it.

## Shape

One of:

- Sort available types first (then by price), so a truncated read still shows what can be
  deployed.
- Print a one-line footer when unavailable rows were shown: "N types unavailable now; rig types
  --available hides them".
- Make `--available` the default and add `--all`.

Low effort; pick in triage.

## Where

`internal/cli/inventory.go` `newTypesCmd`.

## Comments

**Fixed (2026-09-06, agent):** took the first two options together in
`internal/cli/inventory.go`. Rows are stable-sorted with available types first (provider
order kept within each group) before rendering, so `--json` gets the same order without a
shape change (ADR-0002). When unavailable rows were printed and `--available` was not given,
a one-line footer follows the table: `N types unavailable now; rig types --available hides
them`. `--available` did not become the default, so existing scripts see the same rows.
