# 06 — RunPod driver (REST v2)

**What to build:** `provider: runpod` works end to end: `rig types --available` lists GPU shapes
as `1xA100-SXM` tokens with secure/community prices and the datacenters that have stock;
`rig up` creates a pod with sshd on a direct TCP port, an optional network volume at
`/workspace`, rig's public key injected, and the footprint installed; `status/ssh/run/sync/
tunnel/extend/down` behave as on Verda. Spot is refused with a clear message.

**Blocked by:** 01, 02, 03, 05. (07 resolved 2026-09-08.)

**Status:** resolved

- [x] `internal/provider/runpod`: hand-written client for `https://api.runpod.io/v2` (Bearer),
      no GraphQL except `myself { clientBalance }` for `Balance()` (`ErrUnsupported` only when
      the reply says the field is no longer served; a rejected key, an HTTP error or a timeout
      surfaces as a readable error — ADR-0007). Credentials: the environment under either
      `RUNPOD_API_KEY` or `RUNPOD_KEY` first, then `credentials_file` under either name. Config
      keys: `cloud: secure|community` (default secure), `image` (default
      `runpod/pytorch:<pinned>`), `disk_gb` (default 20).
- [x] `InstanceTypes()` from `GET /catalog/gpus?include=AVAILABILITY&product=POD&cloud=<cloud>`:
      one row per `(count 1..maxCount[cloud], gpu)` is too many — emit counts `1, 2, 4, 8` capped
      by `maxCount`; token `<n>x<name with spaces→->`; price = `price[cloud] × n`; GPU memory,
      description = `id`. `Availability()` = datacenter id → tokens whose `dataCenters[]`
      entry for that DC is not `NONE`.
- [x] `Locations()` from `GET /catalog/datacenters` (`id`, `name`).
- [x] `Images()` returns the driver default plus the user-configured image; any string is
      accepted at create (Docker image ref).
- [x] `CreateInstance`: `POST /pods` with `name`, `image`, `gpu {id,count}` (token → `id`),
      `cloud`, `dataCenterIds [loc]`, `disk`, `ports ["22/tcp"]`, `startSsh: true`,
      `env {PUBLIC_KEY: <key>}`, `mounts.network [{volumeId, path:"/workspace"}]`
      when a volume is given. The onstart re-arm hook rides in `args` (an entrypoint wrapper
      that runs `~/.rig/onstart.sh` then execs `/start.sh`), not in an `env.RIG_ONSTART`
      variable: nothing on the instance would read such a variable. Applied to `runpod/*`
      images only; any other image gets a warning that the watchdog will not re-arm.
      `EnsureSSHKey` returns `""` (the key rides in `env.PUBLIC_KEY`). "No available GPU" class
      errors → `ErrNoCapacity`. `spot: true` → `ErrUnsupported`.
- [x] `GetInstance`/`ListInstances` map the v2 pod's `status` field (verified values `RUNNING`,
      `EXITED`; v1's `desiredStatus` is absent in v2) plus runtime to `Status`
      (RUNNING with `ssh.direct` → Running; RUNNING without → Provisioning; EXITED → Stopped;
      TERMINATED/404 → Gone); endpoint = `ssh.direct{host,port}` or `runtime.ports` private 22; user
      root; `PricePerHour` from the pod's cost field; `Location` = `dataCenterId`. `ListInstances`
      keeps only names with the `rig-` prefix (pod `name`).
- [x] `ShutdownInstance`/`StartInstance`/`DeleteInstance` via `POST /pods/{id}/action`
      stop/start/terminate; `DeleteInstance` deletes requested network volumes afterwards.
- [x] Volumes: `GET/POST/DELETE /network-volumes`; `Location` = `dataCenter`; `MountCommand`
      and `MkdirCommand` empty. `CreateVolume` size ≥ 10 GB (clamp with a log line).
- [x] Watchdog self-stop line (issue 07, verified):
      `. /etc/rp_environment && curl -fsS -X POST "https://api.runpod.io/v2/pods/$RUNPOD_POD_ID/action" -H "Authorization: Bearer $RUNPOD_API_KEY" -H 'Content-Type: application/json' -d '{"action":"stop"}'`
- [x] Tests: `httptest` fixtures from the 2026-09-08 probes (catalog shape recorded in the spec);
      token round-trip; status/endpoint mapping; `ErrNoCapacity` wrapping.
- [x] Manual smoke (run 2026-09-08 on the funded account, ~$0.13 — see Comments): `rig --provider runpod types --available`,
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

2026-09-08 (agent): Landed. `internal/provider/runpod/runpod.go`: `init()` registers `runpod`;
the factory decodes `Options{Cloud, Image, DiskGB}` (`cloud`, `image`, `disk_gb`) and resolves
the key with `cfg.Creds` once per name — `RUNPOD_API_KEY` then `RUNPOD_KEY` — so either spelling
works alone; the error is the documented name's with "(RUNPOD_KEY is accepted too)". `New(key,
Options) (*Driver, error)`; exported `BaseURL`/`GraphQLURL` (tests point them at httptest) and
`Log` (nil → `rig:`-prefixed stderr). Constants `DefaultImage`
(`runpod/pytorch:1.2.0-rc.162-cu1290-torch291-ubuntu2404`), `DefaultDiskGB` 20, `MinVolumeGB` 10,
`MountPath` `/workspace`, `AnyLocation` `any`, `SelfStop` (the verified issue-07 line, in
`Capabilities{Spot:false, Volumes:true, Balance:true}`). Hand-written net/http client, problem+json
errors as `runpod: <METHOD> </path>: HTTP <code>: <detail|title>`; a create-time 400 whose detail
contains "no longer any instances available" (or "no available gpu") wraps `ErrNoCapacity`.
Catalog: `GET /catalog/gpus?include=AVAILABILITY&product=POD&count=1&cloud=<CLOUD>`; tokens
`<n>x<name spaces→->` for n in 1,2,4,8 ≤ `maxCount[cloud]`, only GPUs sold on the cloud
(`secure`/`community` flag and maxCount>0 — drops `unknown` and MI300X-on-community); price
`price[cloud]×n`, `GPU` = name, `GPUMemoryGB` = memory×n, `Description` = id; parse is
case-insensitive. Pod → Instance: RUNNING+endpoint → Running, RUNNING without → Provisioning,
PROVISIONING/STARTING → Provisioning, EXITED → Stopped, ERROR → Error, TERMINATED or GET 404 →
Gone (returned as an Instance, not an error); endpoint `ssh.direct{host,port}` else the
`runtime.ports` 22/tcp entry, user root; `Type` rebuilt from `gpu.count` + catalog name (id→name
map cached per driver, filled by any catalog call or lazily once); `VolumeIDs` from
`mounts.network[].volumeId`. Create body: `name image gpu{id,count} cloud disk ports["22/tcp"]
startSsh env{PUBLIC_KEY}`, `dataCenterIds` unless location is empty/`any`, `mounts.network` when
volumes given, and `args` (see below) for `runpod/*` images only. Delete: terminate (404 = done),
then `DELETE /network-volumes/{id}` per requested volume with 5×2s retries. Volumes: list/get
cross-reference `GET /pods` so `InstanceIDs`/`IsAttached` are real (the volume API has no such
field); `Status` attached/available; create clamps <10GB to 10 with a log line; type sent only
when set; Attach/Detach → `ErrUnsupported`. `Balance` = GraphQL `{ myself { clientBalance } }`,
USD, any failure → `ErrUnsupported`. `EnsureSSHKey` → `""`. `Images` = configured image
(default) + rig default. `Locations` = datacenters + `any`.

Deviations / decisions: (1) **Community cloud has no `dataCenters[]`** (verified: the field is
present only for SECURE GPUs with stock; COMMUNITY GPUs carry an overall `availability` level
only). Availability reports such GPUs under the synthetic location `any`; a create there omits
`dataCenterIds`. (2) `env.RIG_ONSTART` from the checklist is not sent — the onstart hook is wired
through `args` instead: `bash -c '[ -f ~/.rig/onstart.sh ] && bash ~/.rig/onstart.sh; exec
/start.sh'`. The v2 OpenAPI (`api.runpod.io/v2/openapi.json`) documents `args` as "Arguments
passed to the container entrypoint"; the official entrypoint is `nvidia_entrypoint.sh`, which
execs its arguments — confirmed live: PID 1 was `docker-init -- nvidia_entrypoint.sh bash -c
'[ -f …'` with `/start.sh` as its exec'd child and sshd up. Applied only to `runpod/*` images
(their CMD is known to be `/start.sh`). (3) `PublicKey string` added to
`provider.CreateInstanceRequest`, filled by `core.provision` from the key it already reads (one
line each). (4) `MemoryGB` is 0 — the catalog does not quote system RAM, so `rig types` shows
`0GB` in MEM (display follow-up for issue 10). (5) Availability is computed for count=1; 2/4/8
tokens are reported wherever the single GPU is, and a create may still hit `ErrNoCapacity`.

Tests (`runpod_test.go`, fixtures under `testdata/` from the 2026-09-08 read-only probes,
`cudaVersions` trimmed): tokens/prices/memory per cloud and count caps, per-datacenter vs `any`
availability (incl. A40: datacenter LOW while overall NONE), locations, images, create body
(literal), placement/args/mounts omission, disk precedence, `ErrNoCapacity`, 422 text, spot and
bad-token refusals, status/endpoint variants (direct, runtime.ports fallback, no port yet,
EXITED, 404, TERMINATED), rig- prefix filter, stop/start/terminate bodies, terminate-404,
volumes with mounting pods, clamp+log, Attach/Detach, EnsureSSHKey, Capabilities.SelfStop
literal, balance and its failure modes, factory with either key name. `pod-running-live.json`
is the smoke pod verbatim (key scrubbed): `mounts` is `{}` when no volume, and `runtime.ports`
has RunPod's own http port beside 22/tcp.

**Smoke run 2026-09-08 (SECURE, isolated `RIG_CONFIG`, balance 10.7488 → 10.6234, $0.125):**
`rig types --available` listed 60+ tokens with datacenters; `rig up --type 1xRTX-2000-Ada` failed
"none available" because the LOW stock shown seconds earlier had gone (not a bug); `rig up --type
1xRTX-A4500,1xRTX-2000-Ada,1xRTX-4090 --name smoke --ttl 30m` picked `1xRTX-4090` in EU-CZ-1 and
was up with the footprint in 3m57s (~1 min to RUNNING+port, rest ssh wait + install). `rig status`
showed `213.192.2.102:40088`, `$0.74`, TTL 30m; `status --json` carried `provider: runpod` and the
`ssh` object. `rig ssh`: deadline set, `bash /root/.rig/ttl.sh` running, tmux 3.4, `/etc/rp_environment`
present (70 lines). `rig run --name hello --wait -- echo hi` → exited 0; `rig logs hello` → `hi`;
`rig ps` ok. Extra: `POST action restart` via the API — the port moved 40088 → 40085 and `rig
status` refreshed it (issue 03 works). `rig down --yes` → "smoke deleted (lived 0.2h, $0.15)";
`GET /pods` empty, `GET /pods/{id}` 404. Not exercised: `sync`, watchdog firing (verified in
07). No driver bugs surfaced.

**Finding — restart wipes the footprint.** After the restart `~/.rig` did not exist: RunPod's
container `disk` is "ephemeral, wiped on restart" (OpenAPI), so ttl.sh, the deadline and
onstart.sh are all gone and the `args` hook finds nothing to re-arm. The pod then runs with **no
watchdog** (`rig status` shows TTL `-`). rig itself has no stop/start commands, so this only
happens after a console/API restart or a start after the watchdog's own stop — but it needs
saying in the docs (issue 11): after any RunPod restart, run `rig adopt <name>`. A durable fix
would place the footprint on the persistent mount (`/workspace/.rig` when a network volume is
attached, symlinked from `~/.rig`) — follow-up, not in scope here. The brief's assumption that
"the deadline file persists on the volume disk" holds only for a pod with a data volume, and
even then only if the footprint lived there.

2026-09-08 (agent, code-review fixes): six review findings fixed in
`internal/provider/runpod/` (no other package touched).

1. **Credential precedence was inverted** (story 4: env vars first, then the KEY=VALUE file).
`credential()` called `cfg.Creds` once per name and `config.Creds` resolves each name
env-then-file, so `RUNPOD_API_KEY` in the credentials file beat `RUNPOD_KEY` in the
environment. It now reads the environment for both spellings first and only then falls back to
`cfg.Creds` per name (still the documented name's error, with "(RUNPOD_KEY is accepted too)").
`TestCredentialPrecedenceEnvBeforeFileForBothNames` covers all six combinations (env-only each
spelling, file-only each spelling, env one + file the other → env wins), observing the chosen
key through the `Authorization` header.

2. **The re-arm gate is loud now.** A non-`runpod/*` image still keeps its own entrypoint (a
foreign image may have no `/start.sh`, and a wrong `args` would break it), but `CreateInstance`
logs through `Driver.Log`: the image, that the TTL watchdog will not re-arm if the instance
restarts, and to run `rig adopt <name>` after a restart.
`TestCreateInstanceWarnsWhenForeignImageSkipsTheRearmHook` asserts it fires for
`myorg/trainer:v3` and not for `DefaultImage`.

3. **`env.RIG_ONSTART` was a ticked box that was never implemented** (nothing sends or reads
it; `args` is the better mechanism and is what ships). No dead config added — the checklist
above now describes the `args` wrapper instead.

4. **`Balance` no longer reports every failure as unsupported.** `ErrUnsupported` is returned
only when the reply says `myself.clientBalance` is not served (`fieldGone`: "cannot query
field" / `GRAPHQL_VALIDATION_FAILED` / "unknown field") or when a 200 carries a null balance.
A 401/403 becomes "runpod rejected the API key reading the account balance (HTTP 401); check
RUNPOD_API_KEY", and anything else (HTTP 500, transport error, other GraphQL error) is returned
as itself so the dashboard shows the reason instead of hiding the tile (ADR-0007). Read-only
live probes (2026-09-08) pinned the shapes: an unknown field answers **HTTP 400** with
`{"errors":[{"message":"Cannot query field ... on type \"User\".","extensions":{"code":
"GRAPHQL_VALIDATION_FAILED"}}]}` (so the field-gone check must run on the HTTP-error path, not
only on a 200), and a bad key answers HTTP 401 with `{"error":{}}` — no message at all, hence
the phrased key error. Tests: `TestBalanceMissingFieldIsUnsupported` (400 and 200 validation
bodies, null balance) and `TestBalanceRealFailuresSurfaceAsErrors` (401, 403, 500, a generic
GraphQL error).

5. **The catalogue query is pinned.** The fake API now records each request's raw query, and
`TestCatalogQueryPinsAvailabilityProductAndCloud` asserts it literally —
`include=AVAILABILITY&product=POD&count=1&cloud=SECURE` and `...&cloud=COMMUNITY`. Verified
live why it matters: with `cloud=SECURE` 23 of 45 GPUs carry `dataCenters[]`, with
`cloud=COMMUNITY` none do, and with the parameter dropped 22 do — deleting it would silently
change which prices and stock rig shows.

6. **Glossary: rig-side names say instance.** `toVolume(v networkVolume, instanceIDs []string)`
(was `pods []string`), `mountedOn`'s doc comment, the `--- instances ---` section header, the
package doc, and the user-facing messages ("runpod v2 has no spot instances", "runpod mounts
network volumes only when an instance is created"). Test names followed:
`TestGetInstanceMapsRunningInstance`, `TestListInstancesKeepsOnlyRigInstances`,
`TestVolumesCarryDatacenterAndMountingInstances`. The wire struct `pod` (with a comment saying
why it keeps RunPod's word), its JSON tags, `listPods` (it fetches `GET /pods` and returns
those wire structs) and the `testdata/pod-*.json` fixture names are unchanged, as agreed.

Left alone as deliberate and already recorded: the synthetic `any` location for community cloud
(re-verified above — community GPUs carry no `dataCenters[]`), `ListVolumes` cross-calling
`GET /pods` for attachments, `InstanceType.MemoryGB` staying 0, and the single-file package
layout. No live pods were created (read-only calls only).
