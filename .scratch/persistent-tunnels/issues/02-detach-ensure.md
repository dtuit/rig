# 02 — `rig tunnel --detach`: ensure a supervised tunnel (tracer bullet)

**What to build:** An agent runs `rig tunnel 8000 --detach` (or with no ports, falling back to
the profile's ports exactly like foreground mode) and gets its prompt back with exit 0 only once
the forwards are actually bound and usable. Running the same ensure again exits 0 immediately,
reporting the tunnel is already up. A conflicting local port (held by a live tunnel to a
different instance/remote port, or by a foreign process) or a connection that can't be
established within the bounded startup window exits non-zero with a clear message.

**Blocked by:** 01 — ssh argv builder.

**Status:** resolved

- [x] `--detach` re-execs rig as a hidden supervisor subcommand (new session, stdio to a
      per-tunnel log file), which runs the ssh forward child.
- [x] Ensure blocks until the first successful connection with forwards bound, up to ~30s, then
      exits 0; timeout or ssh failure exits non-zero with the reason.
- [x] Re-ensuring an equivalent live tunnel exits 0 without starting anything.
- [x] A local-port conflict is refused with an error naming the conflict.
- [x] Ensuring additional ports for an already-tunneled instance leaves all ports forwarded.
- [x] Tunnel state (instance name/ID, IP, forwards, supervisor pid, started-at, phase) is
      recorded in one JSON file per tunnel beside the config file; no credentials in state.
- [x] The new `tunnel` package's manager is tested through an injected process launcher (the
      `remote.Runner` precedent) — idempotency, conflict refusal, startup timeout — without real
      ssh.
- [x] The garbled sentence in the tunnel command's long help text is fixed.
- [x] A live tunnel recorded under this instance's name whose ID/IP no longer matches the
      resolved instance is stopped and replaced, not reported already-up (accepted 2026-08-21;
      launcher-seam test: stale tunnel to a recycled IP after name reuse → replaced).
- [x] No reconnect yet: if the ssh child dies, the supervisor exits; the dead tunnel must not
      show as live to a later ensure (pid-liveness check).

## Notes

Spec: ../spec.md — see "Ensure semantics", "Supervisor = rig re-execing itself", "State store".
Reconnect/backoff and give-up are ticket 05; status is 03; stop is 04.

## Comments

2026-08-21 (agent): implemented. `internal/tunnel` owns the feature: `Manager`
(ensure/list, injected Launch/Alive/Stop seams tested via a scripted fake supervisor),
`Supervise` (the hidden `rig tunnel-supervise` body; owns the state file, probes forwards,
SIGTERM = clean stop), state files + logs in `<config-dir>/tunnels/`. Two findings worth
remembering:

- **The ssh child must bypass connection multiplexing** (`sshx.SupervisedTunnelCommand`
  prepends `ControlMaster=no ControlPath=none`): with rig's shared ControlMaster options the
  forward is delegated to the persistent master and the child exits immediately — the smoke
  test showed phase gave-up with the port still (mis)working via the master. Same disease the
  spec rejected `ssh -O forward` for.
- **A bare TCP dial does not prove the tunnel answered** — a foreign listener on the local
  port satisfies the connected probe while ssh is still authenticating (exit 0, wrong
  backend; caught in code review). Fixed with a pre-flight `waitPortFree` (which also gives a
  replaced tunnel's ssh child time to release the port) plus a child-alive re-check before
  writing connected.

Verified end-to-end against localhost sshd: connect → forward serves HTTP → SIGTERM removes
state and releases the port; foreign-listener case refused with a clear message.

2026-08-21 (Darren): accepted — folded into the acceptance list above.

2026-08-21 (agent, from target-user spec review): proposed extra
ensure invariant. Because `rig down`'s tunnel stop is best-effort and supervisors pin the IP,
`down X` (stop fails, warns) followed by `up --name X` can leave a live supervisor forwarding to
a recycled IP — silently the wrong host. Proposal: ensure compares each live tunnel's recorded
instance ID/IP against the resolved instance's current ID/IP and replaces the tunnel on mismatch
instead of reporting already-up. Ensure-time check only; mid-supervision re-resolution stays out
of scope. If accepted, add an acceptance checkbox + a launcher-seam test (stale tunnel to old
IP, ensure after name reuse → replaced, not "already up").
