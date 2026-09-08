# 03 — Prefactor: SSH endpoint (host, port, user) on the instance

**What to build:** Every ssh-based command connects to the endpoint the provider reports, not
to `cfg.ssh.user@IP:22`. A container reachable at `1.2.3.4:10341` or through a proxy host, and
an AWS box whose user is `ubuntu`, work with no new flags. Verda instances keep `root@ip:22`.

**Blocked by:** None — can start immediately; 06/08/09 depend on it.

**Status:** resolved

- [x] `provider.Instance` gains `SSH Endpoint{Host string; Port int; User string}`; `IP` stays
      for display. Verda fills `{IP, 22, "root"}`.
- [x] `sshx.Target.Port` (0 = 22): ssh/scp/rsync/tunnel argv builders emit `-p`/`-e "ssh -p N …"`
      accordingly; `WaitForSSH` dials `host:port`. Unit tests for each builder.
- [x] `core.Target(in)` uses `in.SSH`; config `ssh.user` becomes an override applied only when
      set explicitly (the default `root` in `config.Load` is removed so absence is detectable).
- [x] Instance cache entries persist the endpoint; `status`, `extend`, `down`, `adopt`
      (fresh-resolving commands) rewrite the cached entry when the endpoint changed. README's
      cache paragraph gains one sentence about this.
- [x] Tunnel state records host+port; `rig tunnel status` shows `host:port` when port ≠ 22.
- [x] `rig status` human table shows `host:port` in the IP column when port ≠ 22; JSON adds the
      `ssh` object.

## Notes

Spec: ../spec.md — "SSH endpoint on the instance". RunPod remaps the public port on every
restart, which is why fresh-resolving commands must refresh the cache (ADR-0006 already says
cached entries carry the endpoint "as recorded at up/adopt time"; extend that ADR's wording in
issue 11).

## Comments

2026-09-08 (agent): Landed. `internal/provider/provider.go`: `type Endpoint struct{Host string
"host"; Port int "port"; User string "user"}` and `Instance.SSH Endpoint "ssh"` (IP stays for
display). `internal/provider/verda/verda.go`: `fromSDKInstance` fills `{IP, 22, "root"}`.
`internal/provider/fake/fake.go`: creates fill `{IP, 22, "root"}`; new scriptable `SSH
map[id]Endpoint` overrides what List/GetInstance report (a remapped container port).
`internal/sshx/sshx.go`: `Target.Port int` (0 = 22); a non-default port is appended to
`baseArgs` as `-p N`, so every ssh argv, rsync's `-e "ssh <base opts> -p N"`, `TunnelArgs` and
`SupervisedTunnelCommand` carry it, and `WaitForSSH` dials `JoinHostPort(host, port)`; port 0/22
argv is byte-identical (no scp path exists). `internal/core/core.go`: `Target(in)` = user
`cfg.SSH.User` → `in.SSH.User` → `root`, host `in.SSH.Host` → `in.IP`, port `in.SSH.Port`;
`ResolveFresh` on a named hit calls `refreshCached` (rewrites the entry via `Remember` when its
`SSH` differs — belt and braces over `Managed`'s `Reconcile`, which already rewrites running
entries from the fresh listing). `internal/config/config.go`: the `root` default for `ssh.user`
is gone (nothing else read it; `remote.homeOf` gets the resolved user via Target); the
`rig config` example marks `ssh.user` as an override. `internal/tunnel/state.go`:
`Endpoint.Port int "port,omitempty"` (22 is stored as 0/omitted, so existing state files and the
JSON shape are unchanged); `Endpoint.IP` now documented as the ssh host (IP or proxy host) and is
filled from `Target(in).Host`, so `Ensure`'s stale-endpoint replacement also fires on a port
remap. `internal/cli/access.go`: `--detach` records host+port; the supervisor rebuilds its Target
from `{IP: st.IP, SSH: {Host: st.IP, Port: st.Port}}`; `rig tunnel status` IP column shows
`host:port` when port ≠ 22 via `hostPort` (`internal/cli/render.go`). `internal/cli/lifecycle.go`:
`rig status` IP column likewise. README instance-cache paragraph: one sentence on the refresh.
Tests: `sshx_test.go` (default port leaves argv unchanged; `-p` on ssh/tunnel/supervised/rsync `-e`;
`WaitForSSH` dials the endpoint port and runs ssh with `-p`, via a local listener and a stub `ssh`
on PATH), `core_test.go` (`Target` precedence/fallback; `ResolveFresh` refreshes the cached
endpoint and the next cached `Resolve` sees it), `tunnel/manager_test.go` (port round-trip and
remap replacement through `Ensure`/`List`; default port omitted from the file), `cli/tunnel_test.go`
and `cli/json_test.go` (`host:port` rendering; `ssh` object in `status --json`). No existing test
changed; `json_test.go`'s existing golden assertions do not render the full struct, so the new
`ssh` object needed no expectation update. Deviations: tunnel state records port but not user —
the supervisor still resolves user from config/root, so an AWS `ubuntu` instance's detached
tunnel needs issue 09 to add `user` to the state (omitempty) or accept `ssh.user: ubuntu` in
config. Dashboard IP column left for issue 10.
