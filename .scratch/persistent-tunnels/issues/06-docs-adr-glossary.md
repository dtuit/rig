# 06 — Docs, ADR, and glossary

**What to build:** Any fresh agent session discovers the tunnel contract without being told: the
agent docs and README describe ensure / verify / stop (`rig tunnel --detach`, `rig tunnel
status --json`, `rig tunnel stop`) alongside the existing agent verbs. The supervision model is
recorded as an ADR, and **Tunnel** joins the domain glossary.

**Blocked by:** 04 — stop/down; 05 — auto-reconnect.

**Status:** ready-for-agent

- [ ] README's command overview covers detached tunnels with the ensure/verify/stop one-liners.
- [ ] Agent docs (AGENTS.md or the docs it points to) describe the contract: exit-0 means ports
      usable now, ensure is idempotent, status is the way to verify access before acting.
- [ ] A new ADR records the supervision decisions: rig re-execs itself as supervisor (no
      autossh/systemd), state-file pid-liveness, capped backoff, the give-up window, and why
      `ssh -O forward` over the control master was rejected.
- [ ] CONTEXT.md gains **Tunnel**: a supervised local port forward from the workstation to an
      instance, surviving connection drops until stopped (with the usual _Avoid_ line).
- [ ] Docs reflect the shipped behavior — verify against the implemented commands, not the spec.

## Notes

Spec: ../spec.md. Last ticket — everything behavioral must be merged first.
