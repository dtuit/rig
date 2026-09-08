# 10 — TUI, `types`, `images`, `config` across providers

**What to build:** The dashboard and picker name the provider they are showing, hide the
balance tile when the provider cannot report one, and render marketplace type tokens without
layout breakage; `rig types`/`rig images` headers name the provider; `rig config` prints a
multi-provider example.

**Blocked by:** 01, 02; polish after 06/08/09 land.

**Status:** resolved

- [x] Dashboard header: `rig · <provider>`; balance tile hidden on `ErrUnsupported`.
- [x] Picker: works with 45+ RunPod tokens and Vast's grouped rows; description column shows
      `N offers` / `id`; spot column blank when unsupported.
- [x] `rig types` / `rig types --available` human output carries the provider in the header
      line; `--json` unchanged apart from fields already added in 02/03.
- [x] `rig images` on Docker-image providers shows the default and configured images and a
      hint that any image reference is accepted.
- [x] `rig config` example includes `providers:` blocks for all four with the documented keys.

## Notes

Spec: ../spec.md — "Type/availability displays". Keep ADR-0002: JSON renders existing structs.

## Comments

2026-09-08 (agent): Landed. `internal/provider/provider.go`: `Capabilities.DockerImages bool` —
the cloud takes any Docker reference, so its `Images` list is only rig's default plus the
configured one; set true in `internal/provider/runpod/runpod.go` and
`internal/provider/vast/vast.go` (their `TestCapabilities` assertions extended).
`internal/tui/dashboard.go`: header is `rig · <provider>` from `App.Provider()`; `fetch` asks
for a balance only when `Capabilities().Balance`, so AWS shows no tile and an `ErrUnsupported`
answer still shows none; new `endpointText(in)` renders the IP column as `host:port` for a
container's mapped sshd, the bare host otherwise (the same reading as `rig status` — `cli`'s
`hostPort` cannot be imported from `tui`, which `cli` imports); columns rebalanced
(NAME 14, TYPE 16, IP 21, LOC 15, and the volume table's LOC 15) so a mapped port and an AWS
availability zone fit without pushing `$/H` off its column. `internal/tui/picker.go`: the
Pricing question is skipped when `!Capabilities().Spot` (RunPod) and the persistent-volume
question when `!Capabilities().Volumes` — Vast's `ListVolumes` answers `ErrUnsupported`, which
aborted the whole picker before this; the type option is
`%-16s %-14s %-6s $%-7.2f %-14s %s` = type, GPU (clipped), system RAM (`-` when the catalogue
quotes none), price, `InstanceType.Description` (Vast's `N offers`, RunPod's gpu id, clipped),
locations; new `memText`, `clip` and `locsText` helpers, the last collapsing a long list to
`<first> +N` because Vast reports one GPU in twenty countries named "United States, US"; the
title counts the options and names `/` as the filter; `Height` on the datacenter and volume
selects as well as the type select, so every long list scrolls in a viewport.
`internal/cli/inventory.go`: `rig types` and `rig images` print `provider: <name>` before the
table; the MEM column uses new `memGB` in `internal/cli/render.go` (`-` for 0, RunPod's
catalogue has no system RAM); the SPOT$/H cell is empty when the cloud has no spot capacity
rather than quoting `0.00`; `rig images` prints
`any image reference is accepted with --image / image:` after the table when
`Capabilities().DockerImages`; the `providers:` example now lists every key each driver decodes
(runpod `cloud`/`image`/`disk_gb`, vast `min_reliability`/`bid_markup`/`direct_only`/`image`/
`disk_gb`/`filters`, aws `profile`/`region`/`security_group`/`default_image`/`volume_type`/
`families`). `internal/cli/root.go`: root `Short` is now "rig — GPU cloud workbench" (the
"(Verda)" flagged by issue 01).

Tests: `internal/cli/inventory_test.go` (new) — the provider line opens `types` and
`types --available` and the Verda-shaped row is unchanged beside it; a RunPod-shaped row renders
`-` for MEM with an empty spot cell (asserted through `strings.Fields`, so an empty cell
disappears) and no `0.00` anywhere; the `images` hint appears only when `DockerImages` is set;
the `rig config` example loads and `runpod.Options`/`vast.Config`/`aws.Config` decode their
documented keys from it. `internal/cli/json_test.go`'s `fakeProvider` gained `caps
*provider.Capabilities` (nil = spot+volumes+balance, as Verda) and `images []provider.Image`,
and its `Images` now returns them instead of "not implemented" — no existing assertion changed.

Deviations / notes: (1) The human `rig types` and `rig images` tables are no longer
byte-identical for Verda — both gained the `provider: <name>` line the issue asks for; `--json`
is untouched (ADR-0002, no display-only fields added to any struct). (2) `rig images` got the
provider line too, matching the issue's "What to build"; it has no `--json` path. (3) The
picker's volume-question skip is beyond the checklist but the picker was unusable on Vast
without it. (4) `endpointText` in `tui` duplicates four lines of `cli.hostPort`; the shared home
would be a method on `provider.Endpoint`, left alone as out of scope. (5) `rig types`'
AVAILABLE column is still the full location list, so a Vast row can be very wide — the picker
collapses it, the table does not, because that column is what the user asked to see.

TUI verified by rendering, not by committed tests (the repo unit-tests no TUI): a temporary
test in `internal/tui` printed `dashModel.View()` for a Verda-, RunPod- and AWS-shaped fake
(`fake.Named` with `Caps`) — `rig · <name>`, the balance tile present for the first two and
absent for AWS, `213.192.2.102:40088` in the IP column, `ap-southeast-2b` in LOC with `$/H`
still aligned — and a scratch program drove a real `huh` Select of 180 such options through
`tea.WindowSizeMsg` + 25 `j` presses at 100x30 and 80x24: the viewport stays 20 lines and
scrolls at both sizes; at exactly 80 columns a row with two or more locations wraps onto a
second line (huh wraps the option text, the list does not break), which is the accepted cost of
keeping the description column. Both were deleted afterwards.
