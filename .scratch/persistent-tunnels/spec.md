# Spec: persistent tunnels

Status: ready-for-agent

## Problem Statement

The point of rig is to spin up GPU instances and let an agent use them. Those instances are not
tailnet-reachable, so an SSH port forward is the only access path to services running on them
(vLLM endpoints, metrics, notebooks). But `rig tunnel` is a foreground process that blocks until
^C — a shape an agent cannot hold across its own tool calls. Today an agent's options are hacks:
background the shell call and hope it survives, or re-tunnel around every request. Worse, when a
tunnel drops mid-job (laptop sleep, network blip, 40 minutes into a soak test), the agent sees
connection-refused, misdiagnoses it ("did vLLM crash?"), and burns effort investigating the wrong
thing. There is no way to ask rig whether a tunnel exists, and two sessions wanting the same
forward collide on the local port bind.

## Solution

Make a tunnel a managed, inspectable thing instead of a babysat process. `rig tunnel --detach`
idempotently ensures a supervised tunnel to an instance exists and returns once the forwards are
confirmed live; a rig-owned supervisor process keeps it healthy with keepalives and automatic
reconnect. `rig tunnel status` (with `--json`) reports what is up, and `rig tunnel stop`
tears it down. `rig down` cleans up the instance's tunnels with it. The existing foreground
`rig tunnel` behavior is unchanged — detached mode is additive.

## User Stories

1. As an agent, I want `rig tunnel --detach` to return promptly once the tunnel is live, so that I can open access to an instance in a single tool call without holding a foreground process.
2. As an agent, I want `--detach` to be idempotent — exit 0 immediately when an equivalent tunnel is already up — so that I can "ensure access" at the start of any workflow without tracking what I did in earlier turns.
3. As an agent, I want the tunnel to reconnect automatically after a connection drop, so that a long job (soak test, training run) doesn't fail with confusing connection-refused errors halfway through.
4. As an agent, I want `rig tunnel status --json` to report each tunnel's instance, forwards, health, and supervisor pid, so that I can verify my access path before acting instead of inferring it.
5. As an agent, I want `--detach` to fail with a non-zero exit and a clear message when a requested local port is already taken (by another tunnel or a foreign process), so that I never silently talk to the wrong backend.
6. As an agent, I want `--detach` to fail (not report success) when the SSH connection or a forward cannot actually be established within a bounded startup window, so that exit 0 always means the ports are usable now.
7. As a human user, I want plain `rig tunnel` to keep its current blocking, ^C-to-stop behavior byte-for-byte, so that my muscle memory and existing scripts keep working.
8. As a user, I want `rig tunnel --detach` with no port arguments to fall back to the profile's ports, exactly like foreground mode, so that both modes resolve ports the same way.
9. As a user, I want `rig tunnel stop` to stop the resolved instance's detached tunnels (all of them, or just the named ports), so that I can release local ports without hunting for pids.
10. As a user, I want `rig down` to stop any detached tunnels for the instance it is destroying, so that no orphaned supervisor keeps retrying against a dead IP.
11. As a user, I want `rig tunnel status` to render a human table consistent with rig's other list commands, so that checking my tunnels feels like checking `rig ps`.
12. As a user, I want a tunnel whose connection is currently down to show as "reconnecting" (with last error and retry info) rather than disappearing, so that I can tell a healing tunnel from a stopped one.
13. As a user, I want a supervisor that has failed continuously for a sustained window to give up and record why, so that a deleted instance doesn't leave a process retrying forever.
14. As a user, I want stale state (a supervisor that was killed, a machine reboot) reaped automatically when tunnels are listed or ensured, so that status never shows tunnels that don't exist.
15. As a user, I want detached tunnels to different instances to coexist, each independently ensured and stopped, so that running two GPU instances doesn't force me to choose one access path.
16. As a user, I want ensuring the same instance with an additional port to extend cleanly (a second supervisor or an updated one — invisible either way), so that "ensure 8000" followed by "ensure 9090" leaves both forwarded.
17. As an agent, I want the tunnel commands and their contract documented in the repo's agent docs, so that any agent session discovers "ensure, verify, stop" without being told.
18. As an agent, I want ensure/stop/status exit codes to be unambiguous (0 success or already-in-desired-state; non-zero with a message otherwise), so that I can chain them with `&&` per rig's existing conventions.
19. As a user, I want the supervisor to be rig itself (no autossh or other new dependency), so that installing rig remains a single binary.
20. As a security-conscious user, I want tunnel state to contain only connection metadata (instance, ip, ports, pid, timestamps, last error) and never credentials, so that the state directory stays as safe as the ledger.

## Implementation Decisions

- **New `tunnel` package** owning the whole feature: the manager (ensure / list / stop), the
  supervision loop, and the state store. The CLI command stays a thin wrapper, like every other
  rig command.
- **Supervisor = rig re-execing itself.** `--detach` spawns `rig` with a hidden internal
  subcommand as a detached process (new session, stdio to a per-tunnel log file in the state
  directory). The supervisor runs the ssh forward process and restarts it on exit with capped
  exponential backoff. No autossh, no systemd, no new dependency.
- **The ssh child is the existing system ssh**, invoked with the same base options rig always
  uses, plus: no remote command (`-N`), the `-L` forwards, `ExitOnForwardFailure=yes`, and
  `ServerAliveInterval`/`ServerAliveCountMax` keepalives so a dead peer is detected within
  ~30–60s. The argv construction is extracted as a pure function in the ssh layer, shared with
  foreground mode (foreground gains keepalives too; its observable behavior is otherwise
  unchanged).
- **Ensure semantics.** Resolve the instance (existing resolution rules, profile-port fallback
  included). If a live tunnel for this instance already covers the requested forwards → exit 0,
  say so. If a requested local port belongs to a live tunnel for a *different* instance or a
  different remote port → error naming the conflict. Otherwise start a supervisor and block until
  the first successful connection with forwards bound, up to a bounded startup timeout (default
  ~30s) — success is only reported once the ports are actually usable. Additional ports for an
  already-tunneled instance start an additional supervisor; ensure/stop/status treat the
  instance's tunnels as one logical set.
- **State store**: one JSON state file per tunnel in a directory beside the config file (the
  ledger precedent). Contents: instance name and ID, IP, forwards, supervisor pid, started-at,
  phase (connected / reconnecting / gave-up), last error, last-change time. The supervisor owns
  its file's lifecycle: writes on phase change, removes it on clean stop. Readers treat a state
  file whose pid is not alive as stale and reap it. No credentials in state (ADR-0003 spirit).
- **Give-up rule**: the supervisor retries with capped backoff (cap ~1 minute) and exits after a
  sustained continuous-failure window (default 15 minutes), recording the final error in state so
  status can explain what happened.
- **CLI surface**: the tunnel command grows subcommands — `rig tunnel status` and
  `rig tunnel stop [ports…]` — while ensure stays a `--detach` flag on the base command, because
  ensure is the same operation as foreground tunneling (same port resolution, same shape) with a
  different lifecycle. Subcommands over verb-flags: each verb gets its own help, args, and flags,
  and `rig tunnel stop` reads cleanly beside the existing top-level `rig stop` (jobs). Port specs
  are numeric so subcommand names never collide with positionals. `status` honors the persistent
  `--json` flag by rendering the tunnel state struct directly with lowercase json tags (ADR-0002:
  the structs are the schema; JSON list output is `[]` when empty, never prose).
- **`rig down` integration**: tearing down an instance stops its tunnels best-effort (a failure
  to stop warns, never blocks the down).
- **Stop semantics**: `rig tunnel stop` with no ports stops all of the resolved instance's
  tunnels; with ports, only supervisors covering those local ports. Stopping something already
  stopped is success, not an error.
- **Docs**: agent docs and README gain the ensure/verify/stop contract; a new ADR records the
  supervision model (rig-as-supervisor, state-file liveness, give-up rule) alongside the existing
  ADRs; the domain glossary gains **Tunnel** — a supervised local port forward from the
  workstation to an instance, surviving connection drops until stopped.

## Testing Decisions

- **Test external behavior only**: what ensure/list/stop return and what a subsequent list
  observes — never state-file schemas, backoff internals, or argv details (except the pure argv
  builder, whose output *is* its behavior).
- **Primary seam (the one new seam)**: the tunnel manager's public API with an injected process
  launcher, directly analogous to how the remote wait loop is tested through the `Runner`
  interface with the injected `sleep`. The fake launcher spawns stub processes / scripted
  lifecycles, covering: idempotent ensure, port-conflict refusal, startup-timeout failure,
  liveness detection, stale-state reaping, reconnect-after-death, and the give-up window
  (with an injected clock/sleep, as in the wait tests).
- **Pure-function tests** for the ssh argv builder: forwards, `-N`, keepalives,
  `ExitOnForwardFailure` present; base options unchanged.
- **CLI-level tests** follow the existing thin style (the exit-propagation and json render
  tests as prior art): flag parsing, port-spec parsing (already exercised), human/JSON render of
  a canned tunnel list — human table stable, JSON emits `[]` when empty.
- **Not tested**: real ssh processes, real network reconnects — the launcher seam makes the
  supervision logic testable without them.

## Out of Scope

- Remote forwards (`-R`), dynamic/SOCKS forwards (`-D`), and forwarding to hosts other than
  localhost on the instance.
- Any tailnet/VPN integration — tunnels exist precisely because instances are not tailnet-reachable.
- Tunnels in the TUI dashboard (status is CLI-only for now).
- Auto-ensuring tunnels on `rig up` or on `rig run` (explicit ensure only; can layer on later).
- Re-resolving a changed instance IP mid-supervision — the supervisor pins the IP recorded at
  ensure time; rig instances keep their IP for their lifetime.
- Windows support (rig already assumes a POSIX workstation).
- Driving forwards over the existing ControlMaster with `ssh -O forward` — rejected because the
  control master's persistence window and lifecycle are wrong for a supervised long-lived tunnel.

## Further Notes

- The feature is the missing fourth verb in rig's agent contract: create (`up`), execute
  (`run`/`wait`), transfer (`sync`/`fetch`), and now *access* (`tunnel --detach`).
- Foreground `rig tunnel` remains the human-shaped, deliberate, temporary tool; detached mode is
  the agent-shaped primitive. Both resolve ports identically.
- The existing tunnel command's `Long` help text contains a garbled sentence ("forwards the ports
  of the profile the instance was created from is unknown") — fix it while touching the command.
- Seam choice and the supervisor model (rig re-exec + auto-reconnect, over plain detached ssh)
  were confirmed with the user before this spec was written.

## Comments

2026-08-21 — target-user review (agent that drove rig for a full training-matrix session),
triaged by agent; decisions pending Darren:

1. **Ground truth on demand:** that session never needed a tunnel — every workload was a batch
   job writing to disk. The spec solves a real access gap, but the session's actual friction was
   *discoverability*: nearly every pain point hit already had a shipped solution (`run --wait`,
   `down -y`, `extend --by`, `sync --exclude`, `logs -f`) that went unused because the agent
   read `--help`, not AGENTS.md/docs.
2. **Story 17 is under-specified (accepted into 06 as a comment):** the ensure/verify/stop
   contract must be legible from `rig tunnel --help` itself (and the subcommands' help), not
   only from agent docs — otherwise agents will hand-roll tunnels the way `--wait` was missed.
3. **Give-up rule vs story 3 (recorded on 05):** a 15-minute continuous-failure window kills the
   tunnel under a 20-minute network partition during an 8-hour job — exactly the confusion story
   3 promises to prevent. Proposal: configurable window, and/or default it longer than a typical
   job, or give up only once the instance's deadline has passed. Needs a decision before 05.
4. **IP pinning + name reuse hazard (recorded on 02):** best-effort stop in `rig down` plus
   pinned IPs means `down X` (stop fails, warns) → `up --name X` can leave a live supervisor
   forwarding to a recycled IP — story 5's "never silently talk to the wrong backend" enforced
   for ports but not hosts. Proposal: ensure compares the recorded IP against the resolved
   instance's current IP and replaces the tunnel on mismatch (ensure-time check only; no
   mid-supervision re-resolution, Out of Scope stands).
5. **Story 6 confirmed load-bearing:** exit-0-means-usable-now is the highest-value invariant in
   the document per the reviewer; keep it pinned by the launcher-seam tests (02 already does).

Separately filed: `rig sync --quiet` (rsync progress output blew a 50–95KB hole in the agent's
context twice) → `.scratch/agent-ergonomics/issues/09-sync-quiet.md`, needs-triage.
