# 03 — `rig tunnel status`: inspect tunnels, human and --json

**What to build:** A user or agent runs `rig tunnel status` and sees each detached tunnel —
instance, forwards, health/phase, supervisor pid, age — as a human table consistent with rig's
other list commands. With the persistent `--json` flag it emits the tunnel state structs directly
(ADR-0002: the structs are the schema), `[]` when there are none. Tunnels whose supervisor is no
longer alive are reaped on read and never shown.

**Blocked by:** 02 — detach/ensure.

**Status:** resolved

- [x] `rig tunnel status` renders a human table; empty state prints prose, JSON prints `[]`.
- [x] `--json` renders the existing tunnel state struct with lowercase json tags — no parallel
      schema.
- [x] Stale state files (dead pid) are reaped when listing; a killed supervisor disappears from
      status.
- [x] Tunnels to multiple instances list together and are distinguishable.
- [x] Render follows the gather/render seam: rows built first, then handed to exactly one of the
      two renderers (json render tests as prior art).

## Notes

Spec: ../spec.md. Phase values beyond "connected" (reconnecting / gave-up) arrive in ticket 05 —
render whatever phase the state carries.

## Comments

2026-08-21 (agent): implemented. `rig tunnel status` lists via `Manager.List` (reap-on-read)
and hands the states to exactly one renderer — `renderJSON([]tunnel.State)` under `--json`
(structs are the schema, `[]` when empty) or a tabwriter table (INSTANCE, IP, FORWARDS, PHASE,
PID, AGE; IP column added beyond the issue's list for multi-instance distinguishability).
Status needs no provider call or credentials. Verified live against a real supervisor.
