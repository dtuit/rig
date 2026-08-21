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

## Comments

2026-08-21 (agent, from target-user spec review): strong evidence that agent docs alone don't
get read — the reviewing agent worked a full session beside this repo, hit five friction points
that shipped features already solved (`run --wait`, `down -y`, `extend --by`, `sync --exclude`,
`logs -f`), and discovered none of them, because it read `rig --help` / `rig <cmd> --help`, not
AGENTS.md. Implication for this ticket: the ensure/verify/stop contract must be legible from
`rig tunnel --help` itself (Long text: `--detach` exit-0 means ports usable now, idempotent;
verify with `tunnel status --json`; release with `tunnel stop`), with README/agent-docs/ADR as
the secondary layer. Consider also auditing the other commands' help text for the same property
while here — help is the contract surface agents actually read.
