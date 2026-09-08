# 11 — Docs, ADRs, glossary

**What to build:** README, CONTEXT.md and the ADRs describe rig as multi-provider and record
the decisions this spec made.

**Blocked by:** 01–05 for the wording; update again as drivers land.

**Status:** ready-for-agent

- [ ] README: provider list with credential variables per provider; `providers:` config
      example; per-provider caveats table (spot, volumes, balance, ssh path, self-stop).
      "Extending to other providers" points at the fake provider and the driver checklist.
- [ ] CONTEXT.md glossary: **Provider** (a cloud rig drives through the `Provider` interface),
      **Driver** (the package implementing it — avoid "plugin", "backend"), **Type token**
      (`<n>x<GPU>` spelling for marketplace providers), **Endpoint** (host, port, user rig
      connects to), **Self-stop** (the provider-specific line the watchdog runs at the
      deadline). Note **Location** now spans datacenter / geolocation / availability zone.
- [ ] ADR-0007: provider-agnostic core with typed sentinels (`ErrNoCapacity`, `ErrUnsupported`)
      and create-time retry — why capacity is settled at create rather than pre-checked.
- [ ] ADR-0008: self-stop on containers — Vast instance-scoped token, RunPod outcome from
      issue 07, how this relates to ADR-0003.
- [ ] ADR-0006 wording: cached endpoint refresh by fresh-resolving commands (issue 03).
- [ ] `docs/agents/` gain nothing new unless a driver introduces a workflow step; check.

## Notes

Spec: ../spec.md. Use glossary terms: instance (never pod/box) even on RunPod, where the
provider's word is "pod" — the driver maps, the docs speak rig.

## Comments
