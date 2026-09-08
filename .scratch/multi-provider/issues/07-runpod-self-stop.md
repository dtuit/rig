# 07 — Decide and verify RunPod self-stop from inside a pod

**What to build:** A decision, verified on a real pod, for the one line of `ttl.sh` that stops
a RunPod pod at its deadline — without an account-level key on the box (ADR-0003).

**Blocked by:** 06 (needs a pod to test against). RunPod balance ~$10.79 as of 2026-09-08 — funded.

**Status:** resolved

Options, in preference order:

- [x] (a) **Pod-scoped `RUNPOD_API_KEY`.** VERIFIED 2026-09-08 — works, see Comments. From inside a pod, call
      `POST https://api.runpod.io/v2/pods/$RUNPOD_POD_ID/action {"action":"stop"}` with the
      injected key. Docs call it "pod-scoped" but do not list its rights; a community thread
      reports 403 and staff saying self-stop was "on the roadmap". Costs cents to test. If it
      works, this is the answer and the ADR stance holds.
- [ ] (b) **Exit the container.** Official images run `start.sh` as PID 1 ending in
      `sleep infinity`; killing that child makes PID 1 exit and the pod goes `EXITED`
      (= stopped, compute billing ends). Zero credentials; depends on the image's entrypoint.
      Verify on `runpod/pytorch`, and decide whether rig's own `args` wrapper (issue 05's onstart
      wiring) should make this robust by owning PID 1's wait loop.
- [ ] (c) **Restricted account key pushed as an env file.** Works for sure; is an explicit
      ADR-0003 exception (long-lived key on the instance, scoped only as far as RunPod's key
      scopes go). Owner decision required.
- [ ] (d) **Workstation-side supervisor** enforcing the deadline via the API (tunnel-supervisor
      pattern). Only as a belt-and-braces addition — fails exactly when the laptop is closed.

Record the outcome in this file, set the shutdown line in the RunPod driver, and add the
result to the ADR in issue 11.

## Notes

Spec: ../spec.md — "RunPod self-stop is an open question". `start.sh` writes every env var
except `PUBLIC_KEY` to `/etc/rp_environment`, so whatever key exists in the pod is already on
its disk — worth stating in the ADR regardless of the option chosen.

## Comments

2026-09-08 (agent): the owner must choose between (c) and shipping RunPod without a guaranteed
watchdog if both (a) and (b) fail. Test (a) then (b) first; they need ~$1 of balance.

2026-09-08 (Darren): RunPod key replaced with one on a funded account (~$10.79); "ok for rest" —
the balance is available for testing (a)/(b) and the driver smoke run.

2026-09-08 (agent): **Resolved with option (a), verified on a real pod** (RTX 3090, SECURE,
EU-CZ-1, image `runpod/pytorch:1.2.0-rc.162-cu1290-torch291-ubuntu2404`, ~7 minutes, ~$0.06).
Findings:

- The pod-scoped key is NOT in an ssh session's environment. `start.sh` writes every injected
  variable to `/etc/rp_environment` (mode 644, root), so the watchdog must `. /etc/rp_environment`
  first. Variables present: `RUNPOD_API_KEY`, `RUNPOD_POD_ID`, `RUNPOD_DC_ID`,
  `RUNPOD_POD_HOSTNAME`, `RUNPOD_PUBLIC_IP`, `RUNPOD_TCP_PORT_22`, `RUNPOD_GPU_NAME`, counts.
- `POST https://api.runpod.io/v2/pods/$RUNPOD_POD_ID/action` with `{"action":"stop"}` and
  `Authorization: Bearer $RUNPOD_API_KEY` returned **HTTP 200**; the pod went to `EXITED` within
  seconds and the ssh connection was closed by the host. No account key involved — ADR-0003 holds.
- Self-stop line for the RunPod driver (issue 06):
  `. /etc/rp_environment && curl -fsS -X POST "https://api.runpod.io/v2/pods/$RUNPOD_POD_ID/action" -H "Authorization: Bearer $RUNPOD_API_KEY" -H 'Content-Type: application/json' -d '{"action":"stop"}'`
- Option (b) remains a documented fallback: PID 1 is `docker-init -- nvidia_entrypoint.sh /start.sh`,
  `start.sh` (pid ~493) ends in `sleep infinity`; killing that child would exit the container.
  Not exercised.
- Useful for issue 06: official image has `tmux`, `rsync`, `curl`, `runpodctl` (unconfigured),
  a `systemctl` binary but no `/run/systemd/system` — the systemd-detection rule in issue 05 is
  correct. Create with no stock returns HTTP 400 `{"detail":"There are no longer any instances
  available with the requested specifications. Please refresh and try again."}` — map to
  `ErrNoCapacity`. The pod's v2 `runtime.ports` entry for ssh was
  `{"ip":"213.192.2.92","private":22,"public":40076,"type":"tcp"}` and `ssh.direct` was populated
  once running; `publicIp`/`portMappings` (v1 fields) were null in v2.
