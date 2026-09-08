# On containers the watchdog stops the instance with the provider's instance-scoped token

The TTL watchdog is rig's whole safety story, and on a VM it ends in `shutdown -h now`. A
container has no init to shut down: RunPod pods and Vast.ai instances stop only through an API
call, which needs a credential on the instance — exactly what ADR-0003 restricts to short-lived
or scoped material. So the watchdog uses the token the provider itself injects, never the
operator's account key. `Capabilities.SelfStop` is the driver-supplied shell line rendered into
`~/.rig/ttl.sh` at install time; empty keeps the VM default.

Vast.ai injects `CONTAINER_API_KEY`, documented as scoped to start/stop/destroy that one
instance: `PUT /instances/$CONTAINER_ID/ {"state":"stopped"}`. RunPod injects a pod-scoped
`RUNPOD_API_KEY`, verified against a live pod on 2026-09-08 (issue 07): `POST
/v2/pods/$RUNPOD_POD_ID/action {"action":"stop"}` returned 200 and the pod reached `EXITED`
within seconds. Neither key is in an ssh session's environment — both providers write the
injected variables to a file — so each line sources it first (`/etc/rp_environment` on RunPod,
`/etc/environment` on Vast) before curling. Without systemd the same `ttl.sh` runs under `nohup
setsid` with its pid in `~/.rig/ttl.pid`, and a one-line `~/.rig/onstart.sh` re-arms it on
container start, wired in by the driver (RunPod: `args` wrapping the image's entrypoint; Vast:
`onstart`).

ADR-0003 holds unchanged: nothing long-lived rig controls lands on an instance. These tokens are
the provider's own, cannot touch another instance, and die with the container. The known
limitation is RunPod's ephemeral container disk: a restart from the console or API — or a start
after the watchdog stopped the pod — wipes `~/.rig`, taking the deadline, `ttl.sh` and
`onstart.sh` with it, so the pod runs unwatched (`rig status` shows TTL `-`) until `rig adopt
<name>` reinstalls the footprint. Placing the footprint on an attached network volume would fix
it and is a follow-up. Decided with the repo owner (2026-09-08) over pushing a restricted account
key as an env file (an ADR-0003 exception), killing the container's `sleep infinity` so it exits
(no credentials at all, but image-dependent; kept as a documented fallback), and a
workstation-side supervisor (fails exactly when the laptop is closed).
