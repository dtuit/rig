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

2026-09-08 (agent, code-review fixes): a two-axis review of the multi-provider
work found eight things in this issue's blast radius; all eight are fixed.

**Triplicated endpoint and duplicated memory rendering → methods on the domain
types.** `internal/provider/provider.go` gains `Endpoint.String()` (host alone
on port 0 or 22, `host:port` otherwise), `Instance.Endpoint()` (the ssh
endpoint, with the display IP standing in as the host for a driver that fills
none) and `InstanceType.MemoryText()` (`-` for a catalogue that quotes no
system RAM). `cli.hostPort`, `cli.orIP`, `cli.memGB`, `tui.endpointText` and
`tui.memText` are deleted; `rig status`, `rig tunnel status`, the dashboard,
`rig types` and the picker all call the methods, so the five readings cannot
drift again and `tui` no longer needs to copy what it cannot import. New
`internal/provider/render_test.go` covers no port, port 22, port 40085, empty
host, the IP fallback and both memory cases. Renderings are byte-identical
apart from the column header (below).

**One "more than one provider" rule.** `cli.severalProviders(cfg,
rowProviders)` in `internal/cli/render.go` is now the only predicate: more
than one provider block in the config, or more than one provider among the
rows on screen. `rig status` (`lifecycle.go`) and `rig spend` (`spend.go`)
both use it, so a user with two provider blocks and one cloud's history sees
the provider named in both tables instead of a PROVIDER column in one and no
subtotals in the other. It is the union rather than either half because the
two tables see different halves of the same question: status lists one
provider's instances at a time, so only the config knows the user drives
others (a pure data rule would make its column dead code and drop story 5),
while spend reads a ledger that outlives a provider block (a pure config rule
would hide a RunPod pod's cost among Verda's). `rig spend` drives no provider,
so it takes the config through new `cli.configForDisplay()` — never opening a
driver, warning rather than failing when the config will not load. New test
`TestSpendNamesProvidersWhenConfigKnowsSeveral`; the existing single-cloud and
two-cloud spend tests pass unchanged. Both tables now build their row as a
`[]string` joined with tabs (`slices.Insert` for the optional PROVIDER cell)
instead of hiding a `\t` inside a `%s` value: the old form could not
misalign — tabwriter splits the finished bytes, and a registry provider name
holds no tab — but the row shape is now visible in the code.

**`%v` where the convention is `%w`.** `internal/remote/remote.go`: the five
error returns in the hunks this work added or touched — `installing ttl.sh`,
`detecting init system`, `installing rig-ttl unit`, `installing onstart.sh`,
`starting ttl.sh` — wrap with `%w`, so `errors.Is`/`errors.As` reach the ssh
error. Messages are unchanged. The `ensureTools` warning keeps `%v` (it logs,
it does not return). Follow-up, untouched because it predates this work:
`SetDeadline`, `PushEnv` and the four job functions still use `%v`.

**The picker names its provider.** `internal/tui/picker.go`: the pricing
question is `Pricing on <provider>`, the type select is
`Instance type on <provider> (N available now, / filters)`, and the final
confirm summary reads `<type> on <provider> in <loc>, ...` — the flow says
which cloud it is shopping at the start, at the catalogue and at the commit,
consistent with the dashboard's `rig · <provider>` and `rig types`'
`provider: <name>`. It matters most when `--provider` or a profile pin makes
it a cloud other than the config default.

**Dashboard header matches the spec's shape.** `internal/tui/dashboard.go`
renders `rig · <provider> · <balance>` (dim balance), not a separate
`"  balance X"` tile; a cloud that cannot report one still shows nothing.

**README honesty.** `README.md` grows a short paragraph under the per-provider
caveats table saying what has actually been run: Verda is the cloud rig has
driven since its first commit, RunPod was smoke-tested end to end on
2026-09-08 including the pod-scoped self-stop, and the Vast.ai and AWS EC2
columns describe drivers unit-tested against recorded responses that have
never talked to the live cloud (no Vast billing method, an expired AWS SSO
token) — intent, not observation. No claim that Verda was re-smoke-tested
here. `rig status`' README line now says "ssh endpoint" instead of "IP".

**Glossary: the field rig dials is not an "IP".** `internal/tunnel/state.go`'s
`Endpoint.IP` is now `Endpoint.Host`, keeping `json:"ip"` so tunnel state
files written before the rename still load — `TestStateFileKeepsTheIPKey`
loads a pre-rename file through `Manager.List()` and checks a rewrite still
emits `"ip"`. The user-facing columns are now `ENDPOINT` in `rig status`,
`rig tunnel status` and the dashboard (they show `host:port`, which no IP
does). `provider.Instance.IP` keeps its name and json key: it *is* a display
IP, which the glossary allows.

**Middle Man and dead surface.** `internal/core/core.go`: private `footprint`
is gone, `Footprint(ttl)` is the only one and `Up` calls it; `a.Prov.Name()`
inside core is now `a.Provider()` everywhere, so the accessor is used
consistently rather than bypassed; `Provision`'s doc comment now says outright
that it is the exported test seam from issue 02 and to keep it exported.

**Vast's bid-only marker (asked for mid-pass by the Vast agent).** The driver
now describes a shape only the bid market carries as `N offers (bid only)`.
The picker's description column was 14 wide, which cut exactly the marker, so
it is 20 wide (`12 offers (bid only)` is 20 characters and survives); the
widest picker row is now 90 columns rather than 84, so the pre-existing wrap
at exactly 80 columns hits a little sooner — accepted, same trade as before.
`rig types` does not print `Description` and should not start: Verda fills it
too, so a NOTE column would rewrite the Verda table, and sniffing `(bid only)`
in cli would parse a driver's prose. Instead, when a spot-capable cloud has
unavailable rows and `--spot` was not given, the table's tail adds
`availability is per market: rig types --spot checks the spot one` — which
answers the actual question a priced row with `AVAILABLE -` raises, on every
provider. New test `TestTypesSaysAvailabilityIsPerMarket`.

Verified: `gofmt -l ./internal ./cmd` silent, `go build ./...`, `go vet ./...`,
and `go test` green for `internal/{cli,core,remote,tunnel,provider,ledger,
config,instcache,sshx}`. The dashboard and the picker's type labels were
checked by a temporary `internal/tui` test that printed `dashModel.View()` and
the label format for RunPod-, Verda- and Vast-shaped rows (header
`rig · runpod · 123.45 USD`, balance absent when empty, `ENDPOINT` column
carrying `213.192.2.102:40085`, `12 offers (bid only)` intact); it was deleted
afterwards, as the repo unit-tests no TUI.
