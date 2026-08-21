# 04 — `rig tunnel stop` and `rig down` cleanup

**What to build:** `rig tunnel stop` stops all of the resolved instance's detached tunnels;
`rig tunnel stop 8000` stops only the supervisors covering those local ports. Stopping something
already stopped succeeds silently — the command is idempotent like ensure. `rig down` stops the
doomed instance's tunnels best-effort so no orphaned supervisor keeps retrying against a dead IP;
a failure to stop warns but never blocks the down.

**Blocked by:** 02 — detach/ensure.

**Status:** resolved

- [x] `rig tunnel stop` (no ports) stops every tunnel for the resolved instance; supervisor and
      ssh child both terminate and the state files are removed.
- [x] `rig tunnel stop <port…>` stops only the matching tunnels, leaving others running.
- [x] Stopping when nothing is running exits 0.
- [x] `rig down` stops the instance's tunnels before/alongside deletion; tunnel-stop failure is a
      warning, not an error.
- [x] Stop honors rig's exit-code convention: 0 for reached-desired-state, non-zero with a
      message otherwise.
- [x] Manager-level behavior covered through the injected launcher seam (no real ssh in tests).

## Notes

Spec: ../spec.md — see "Stop semantics" and "`rig down` integration". Can run in parallel with
ticket 03 once 02 lands.

## Comments

2026-08-21 (agent): implemented. `Manager.StopInstance(instance, ports)` stops matching
supervisors via the shared `stopAndReap` (SIGTERM, bounded wait for death so the ssh child has
released the ports, state file removed; a supervisor that outlives the wait is a non-zero
error, and ensure's stale-replacement path now shares this contract instead of racing the old
child). One deliberate deviation from "existing resolution rules": `rig tunnel stop` resolves
against the recorded tunnel state (name or ID prefix, sole-instance fallback) with no provider
call — so ports can be released after the instance is gone or the provider is unreachable,
which is the story-10 cleanup case. Stopping what is already stopped exits 0. `rig down` calls
the stop best-effort *before* deletion (per spec "before/alongside"; a stop failure warns,
never blocks). Verified live: stop by bare name, by ID prefix, idempotent re-stop, port
released.
