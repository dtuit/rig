# 04 — `rig tunnel stop` and `rig down` cleanup

**What to build:** `rig tunnel stop` stops all of the resolved instance's detached tunnels;
`rig tunnel stop 8000` stops only the supervisors covering those local ports. Stopping something
already stopped succeeds silently — the command is idempotent like ensure. `rig down` stops the
doomed instance's tunnels best-effort so no orphaned supervisor keeps retrying against a dead IP;
a failure to stop warns but never blocks the down.

**Blocked by:** 02 — detach/ensure.

**Status:** ready-for-agent

- [ ] `rig tunnel stop` (no ports) stops every tunnel for the resolved instance; supervisor and
      ssh child both terminate and the state files are removed.
- [ ] `rig tunnel stop <port…>` stops only the matching tunnels, leaving others running.
- [ ] Stopping when nothing is running exits 0.
- [ ] `rig down` stops the instance's tunnels before/alongside deletion; tunnel-stop failure is a
      warning, not an error.
- [ ] Stop honors rig's exit-code convention: 0 for reached-desired-state, non-zero with a
      message otherwise.
- [ ] Manager-level behavior covered through the injected launcher seam (no real ssh in tests).

## Notes

Spec: ../spec.md — see "Stop semantics" and "`rig down` integration". Can run in parallel with
ticket 03 once 02 lands.
