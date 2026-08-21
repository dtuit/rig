# 01 — Prefactor: pure ssh argv builder with keepalives

**What to build:** Foreground `rig tunnel` behaves exactly as today, except a dead peer is now
detected within about a minute (keepalives) and a forward that fails to bind kills the session
instead of limping on. Under the hood, the ssh argument construction becomes a pure function the
detached mode can share.

**Blocked by:** None — can start immediately.

**Status:** resolved

- [x] The ssh argv for a tunnel is built by a pure function, unit-tested directly: base options
      unchanged, `-N`, one `-L` per forward, `ExitOnForwardFailure=yes`,
      `ServerAliveInterval`/`ServerAliveCountMax` present.
- [x] Foreground `rig tunnel` output and ^C behavior are unchanged (byte-identical human output).
- [x] Non-tunnel ssh invocations (run/ssh/sync/fetch) are untouched.

## Notes

Spec: ../spec.md. This is the "make the change easy" step — no user-visible feature beyond
keepalives.

## Comments

2026-08-21 (agent): Implemented as `sshx.Target.TunnelArgs` (exported — the detached tunnel
package in issue 02 shares it). Keepalives: `ServerAliveInterval=15` / `ServerAliveCountMax=3`
(dead peer detected in 45–60s). `Tunnel` now delegates to the builder; no other sshx method or
CLI code changed. Unit tests in `internal/sshx/sshx_test.go` (base-options prefix, keepalive and
ExitOnForwardFailure options, per-forward `-L`, trailing `-N user@host`, purity/determinism).
