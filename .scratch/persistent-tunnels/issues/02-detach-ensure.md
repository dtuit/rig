# 02 — `rig tunnel --detach`: ensure a supervised tunnel (tracer bullet)

**What to build:** An agent runs `rig tunnel 8000 --detach` (or with no ports, falling back to
the profile's ports exactly like foreground mode) and gets its prompt back with exit 0 only once
the forwards are actually bound and usable. Running the same ensure again exits 0 immediately,
reporting the tunnel is already up. A conflicting local port (held by a live tunnel to a
different instance/remote port, or by a foreign process) or a connection that can't be
established within the bounded startup window exits non-zero with a clear message.

**Blocked by:** 01 — ssh argv builder.

**Status:** ready-for-agent

- [ ] `--detach` re-execs rig as a hidden supervisor subcommand (new session, stdio to a
      per-tunnel log file), which runs the ssh forward child.
- [ ] Ensure blocks until the first successful connection with forwards bound, up to ~30s, then
      exits 0; timeout or ssh failure exits non-zero with the reason.
- [ ] Re-ensuring an equivalent live tunnel exits 0 without starting anything.
- [ ] A local-port conflict is refused with an error naming the conflict.
- [ ] Ensuring additional ports for an already-tunneled instance leaves all ports forwarded.
- [ ] Tunnel state (instance name/ID, IP, forwards, supervisor pid, started-at, phase) is
      recorded in one JSON file per tunnel beside the config file; no credentials in state.
- [ ] The new `tunnel` package's manager is tested through an injected process launcher (the
      `remote.Runner` precedent) — idempotency, conflict refusal, startup timeout — without real
      ssh.
- [ ] The garbled sentence in the tunnel command's long help text is fixed.
- [ ] No reconnect yet: if the ssh child dies, the supervisor exits; the dead tunnel must not
      show as live to a later ensure (pid-liveness check).

## Notes

Spec: ../spec.md — see "Ensure semantics", "Supervisor = rig re-execing itself", "State store".
Reconnect/backoff and give-up are ticket 05; status is 03; stop is 04.
