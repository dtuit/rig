# 11 — Docs, ADRs, glossary

**What to build:** README, CONTEXT.md and the ADRs describe rig as multi-provider and record
the decisions this spec made.

**Blocked by:** 01–05 for the wording; update again as drivers land.

**Status:** resolved

- [x] README: provider list with credential variables per provider; `providers:` config
      example; per-provider caveats table (spot, volumes, balance, ssh path, self-stop).
      "Extending to other providers" points at the fake provider and the driver checklist.
- [x] CONTEXT.md glossary: **Provider** (a cloud rig drives through the `Provider` interface),
      **Driver** (the package implementing it — avoid "plugin", "backend"), **Type token**
      (`<n>x<GPU>` spelling for marketplace providers), **Endpoint** (host, port, user rig
      connects to), **Self-stop** (the provider-specific line the watchdog runs at the
      deadline). Note **Location** now spans datacenter / geolocation / availability zone.
- [x] ADR-0007: provider-agnostic core with typed sentinels (`ErrNoCapacity`, `ErrUnsupported`)
      and create-time retry — why capacity is settled at create rather than pre-checked.
- [x] ADR-0008: self-stop on containers — Vast instance-scoped token, RunPod outcome from
      issue 07, how this relates to ADR-0003.
- [x] ADR-0006 wording: cached endpoint refresh by fresh-resolving commands (issue 03).
- [x] `docs/agents/` gain nothing new unless a driver introduces a workflow step; check.

## Notes

Spec: ../spec.md. Use glossary terms: instance (never pod/box) even on RunPod, where the
provider's word is "pod" — the driver maps, the docs speak rig.

## Comments

2026-09-08 (agent): Landed, docs only (no Go code touched).

**README.md.** Intro names the four drivers and "one provider per command"; Install drops
"systemd" for "tmux and either systemd or a Docker container rig can install into" (with the
apt install of tmux/rsync from issue 05); Configure's example grew the `providers:` block for
all four with their credential variables (`VERDA_CLIENT_ID/SECRET`, `RUNPOD_API_KEY` or
`RUNPOD_KEY`, `VAST_API_KEY`, AWS via the SDK chain/`profile`) and a `pod:` profile with a
`provider:` pin, `ssh.user` marked as an override of the provider default — keys checked
against `cli.exampleConfig`, `config.Profile` and each driver's `Options`/`Config`. New
"### Per-provider caveats" table (type spelling, location, spot, data volumes, balance, ssh,
self-stop) plus prose on case-insensitive tokens (core.Pick uses `EqualFold`, so this is true
as of now) and typed refusals, then a bold **RunPod restart wipes the footprint** paragraph
ending in `rig adopt <name>`. Added `rig --provider vast types --available` to the Use block.
"How it holds together": TTL bullet gains the no-systemd path (`nohup setsid`,
`~/.rig/ttl.pid`, `~/.rig/onstart.sh`, provider self-stop line); volumes bullet says
location-bound rather than datacenter-bound; no-local-state bullet says both files tag the
provider (absent = verda) and `rig spend` subtotals. Agent-orchestration section states that
`status --json` carries `provider` and the `ssh` object (both exist, issues 03/04).
"Extending to other providers" rewritten around the registry (`Register` in `init()`, blank
imports), `Capabilities{Spot, Volumes, Balance, DockerImages, SelfStop}` — `DockerImages`
was added by issue 10 while this was being written — `internal/provider/fake`, `httptest` +
`testdata/` as the driver test pattern, and a six-step driver checklist.

**CONTEXT.md.** Intro line now lists the four providers. New terms **Provider**, **Driver**,
**Endpoint**, **Type token**, **Location** (datacenter / geolocation / availability zone) and
**Self-stop**; **Instance** now covers containers and adds `pod` to its _Avoid_ line;
**Watchdog** mentions the non-systemd form. Existing style kept (term, definition, _Avoid_).

**ADRs.** New `docs/adr/0007-provider-agnostic-core-typed-sentinels.md` (normalised status +
`RawStatus`, `ErrUnsupported`/`Capabilities` refusals before a create, `ErrNoCapacity` and the
create-time candidate walk; why capacity cannot be pre-checked — AWS learns at `RunInstances`,
and the 2026-09-08 RunPod smoke run failed on stock the catalogue had just shown; alternatives:
pre-flight check, `Supports(string)`, per-driver retry). New
`docs/adr/0008-self-stop-on-containers.md` (Vast `CONTAINER_API_KEY`, RunPod pod-scoped key
verified 2026-09-08 per issue 07, `/etc/rp_environment` and `/etc/environment` sourcing, nohup
watchdog + `onstart.sh` re-arm, ADR-0003 unchanged, the restart-wipes-disk limitation and
`rig adopt`; alternatives: restricted account key, killing `sleep infinity`, laptop-side
supervisor). ADR-0006 gained the cached-endpoint refresh sentence, ADR-0004 the one about
events carrying the provider.

**docs/agents/.** Nothing needed a provider note — `domain.md` points at CONTEXT.md and
`docs/adr/` generically, `issue-tracker.md` and `triage-labels.md` are provider-neutral. One
incidental fix: `domain.md`'s example of flagging an ADR conflict cited "ADR-0007
(event-sourced orders)", a placeholder that now collides with the real ADR-0007; it points at
ADR-0006 instead.

**Limitations documented rather than features:** RunPod has no spot in the v2 API; Vast has no
data volumes (machine-bound); AWS has no balance; a RunPod restart wipes `~/.rig` and leaves a
pod unwatched until `rig adopt`; AWS spot interruption terminates (one-time request) rather
than stopping. Not claimed in the docs because it is unverified: the AWS driver has never run
live (SSO expired) and Vast has never been smoke-tested (balance $0), so the AWS and Vast
columns of the caveats table describe the code's intent, not observed behaviour.

