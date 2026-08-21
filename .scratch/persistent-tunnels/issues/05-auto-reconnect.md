# 05 — Auto-reconnect supervision

**What to build:** A tunnel survives connection drops. When the ssh child dies (network blip,
laptop sleep, keepalive timeout), the supervisor restarts it with capped exponential backoff and
the tunnel heals without anyone noticing. While down, `rig tunnel status` shows the tunnel as
"reconnecting" with the last error rather than it disappearing. After a sustained
continuous-failure window (default 15 minutes) the supervisor gives up, records why, and exits —
so a deleted instance never leaves a process spinning forever.

**Blocked by:** 02 — detach/ensure; 03 — status (renders the new phases).

**Status:** ready-for-agent

- [ ] A killed ssh child is restarted; the forward works again without re-running ensure.
- [ ] Backoff is capped (~1 minute); a reconnect success resets it.
- [ ] State tracks phase transitions (connected → reconnecting → connected / gave-up), last
      error, and last-change time; status renders them.
- [ ] After the sustained failure window the supervisor exits with the final error recorded; the
      gave-up state is visible in status until reaped/stopped.
- [ ] Ensure on a reconnecting tunnel does not stack a duplicate; ensure on a gave-up tunnel
      replaces it with a fresh supervisor.
- [ ] Supervision loop tested through the injected launcher plus injected sleep/clock (the
      `WaitConfig.sleep` precedent) — no real timing, no real ssh.

## Notes

Spec: ../spec.md — see "Give-up rule". This completes the agent story: connection-refused
mid-job stops being a misdiagnosis trap.
