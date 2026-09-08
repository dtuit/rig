# 06 — RunPod driver (REST v2)

**What to build:** `provider: runpod` works end to end: `rig types --available` lists GPU shapes
as `1xA100-SXM` tokens with secure/community prices and the datacenters that have stock;
`rig up` creates a pod with sshd on a direct TCP port, an optional network volume at
`/workspace`, rig's public key injected, and the footprint installed; `status/ssh/run/sync/
tunnel/extend/down` behave as on Verda. Spot is refused with a clear message.

**Blocked by:** 01, 02, 03, 05. (07 resolved 2026-09-08.)

**Status:** ready-for-agent

- [ ] `internal/provider/runpod`: hand-written client for `https://api.runpod.io/v2` (Bearer),
      no GraphQL except `myself { clientBalance }` for `Balance()` (falls back to
      `ErrUnsupported` on error). Credentials: `RUNPOD_API_KEY` or `RUNPOD_KEY`, then
      `credentials_file`. Config keys: `cloud: secure|community` (default secure), `image`
      (default `runpod/pytorch:<pinned>`), `disk_gb` (default 20).
- [ ] `InstanceTypes()` from `GET /catalog/gpus?include=AVAILABILITY&product=POD&cloud=<cloud>`:
      one row per `(count 1..maxCount[cloud], gpu)` is too many — emit counts `1, 2, 4, 8` capped
      by `maxCount`; token `<n>x<name with spaces→->`; price = `price[cloud] × n`; GPU memory,
      description = `id`. `Availability()` = datacenter id → tokens whose `dataCenters[]`
      entry for that DC is not `NONE`.
- [ ] `Locations()` from `GET /catalog/datacenters` (`id`, `name`).
- [ ] `Images()` returns the driver default plus the user-configured image; any string is
      accepted at create (Docker image ref).
- [ ] `CreateInstance`: `POST /pods` with `name`, `image`, `gpu {id,count}` (token → `id`),
      `cloud`, `dataCenterIds [loc]`, `disk`, `ports ["22/tcp"]`, `startSsh: true`,
      `env {PUBLIC_KEY: <key>, RIG_ONSTART: …}`, `mounts.network [{volumeId, path:"/workspace"}]`
      when a volume is given. `EnsureSSHKey` returns `""` (the key rides in `env.PUBLIC_KEY`).
      "No available GPU" class errors → `ErrNoCapacity`. `spot: true` → `ErrUnsupported`.
- [ ] `GetInstance`/`ListInstances` map the v2 pod's `status` field (verified values `RUNNING`,
      `EXITED`; v1's `desiredStatus` is absent in v2) plus runtime to `Status`
      (RUNNING with `ssh.direct` → Running; RUNNING without → Provisioning; EXITED → Stopped;
      TERMINATED/404 → Gone); endpoint = `ssh.direct{host,port}` or `runtime.ports` private 22; user
      root; `PricePerHour` from the pod's cost field; `Location` = `dataCenterId`. `ListInstances`
      keeps only names with the `rig-` prefix (pod `name`).
- [ ] `ShutdownInstance`/`StartInstance`/`DeleteInstance` via `POST /pods/{id}/action`
      stop/start/terminate; `DeleteInstance` deletes requested network volumes afterwards.
- [ ] Volumes: `GET/POST/DELETE /network-volumes`; `Location` = `dataCenter`; `MountCommand`
      and `MkdirCommand` empty. `CreateVolume` size ≥ 10 GB (clamp with a log line).
- [ ] Watchdog self-stop line (issue 07, verified):
      `. /etc/rp_environment && curl -fsS -X POST "https://api.runpod.io/v2/pods/$RUNPOD_POD_ID/action" -H "Authorization: Bearer $RUNPOD_API_KEY" -H 'Content-Type: application/json' -d '{"action":"stop"}'`
- [ ] Tests: `httptest` fixtures from the 2026-09-08 probes (catalog shape recorded in the spec);
      token round-trip; status/endpoint mapping; `ErrNoCapacity` wrapping.
- [ ] Manual smoke (after funding): `rig --provider runpod types --available`,
      `rig up --type 1xRTX-4090 --ttl 20m`, `ssh`, `run`, `sync`, watchdog stop, `down`.

## Notes

Spec: ../spec.md — "RunPod driver specifics", facts section. Verified v2 pod shape (2026-09-08,
running pod): top-level keys `actions` (e.g. `["stop","restart","terminate"]`), `cloud`, `cost`
(0.5, $/h), `createdAt`, `dataCenterId`, `disk`, `env`, `gpu {count,id,memory,vcpuCount}`, `id`,
`image`, `mounts`, `name`, `ports`, `runtime {ports[{ip,private,public,type}], uptime, gpus…}`,
`ssh {direct{host,port,username,command}, proxy{…}}`, `status`. Create-with-no-stock → HTTP 400
`detail: "There are no longer any instances available with the requested specifications…"` →
`ErrNoCapacity`. Stop → `status: EXITED`, `actions: ["start","terminate"]`; terminate → 204. Gotchas: docs mention both
`PUBLIC_KEY` and `SSH_PUBLIC_KEY`; `start.sh` reads `PUBLIC_KEY`. Public port mappings change on
every restart (issue 03 handles refresh). REST v1 retires 2026-11-15 — do not use it. Stopped
pods bill volume disk at $0.20/GB/mo and are not guaranteed a GPU on restart; `rig status`
should surface `Stopped` plainly so the user runs `down`.

## Comments
