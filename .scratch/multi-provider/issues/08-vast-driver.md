# 08 — Vast.ai driver (REST v0)

**What to build:** `provider: vast` works end to end: `rig types --available` turns the offer
marketplace into a catalogue (one row per `1xRTX-4090`-style token, cheapest price, offer count,
grouped by geolocation); `rig up` picks the cheapest matching offer, retries the next one when
it is taken, connects over direct ssh when the host has open ports and the proxy otherwise, and
installs the footprint with the instance-scoped `CONTAINER_API_KEY` as the self-stop path.
Bids (`spot: true`) work with a markup over `min_bid`. Volumes are refused with a clear message.

**Blocked by:** 01, 02, 03, 05.

**Status:** ready-for-agent

- [ ] `internal/provider/vast`: client for `https://console.vast.ai/api/v0`, Bearer auth, 429
      retry with small backoff (CLI precedent: 3 tries). Credentials: `VAST_API_KEY`, then
      `credentials_file`. Config: `min_reliability` (0.95), `bid_markup` (1.2), `direct_only`
      (false), `image` (default `vastai/pytorch:<pinned>`), `disk_gb` (20), extra offer filters
      as a raw map merged into the query (escape hatch: `cuda_max_good`, `inet_down`, …).
- [ ] Offer search = `POST /bundles/` with `{rentable eq true, verified eq true, external eq
      false, rented eq false, reliability gte <min>, type on-demand|bid, order [[dph_total asc]]}`
      plus token → `gpu_name eq`, `num_gpus eq`; `geolocation eq` when a location is pinned.
- [ ] `InstanceTypes()` groups a broad search by `(num_gpus, gpu_name)`: token, cheapest
      `dph_total`, `SpotPrice` = cheapest `min_bid` from a `type: bid` search, GPU memory from
      `gpu_ram` (MB→GB), description `N offers`. `Availability()` groups the same results by
      `geolocation`. `Locations()` = distinct geolocations seen.
- [ ] `CreateInstance`: search for the token in the location; for each offer in price order
      `PUT /asks/{id}/` with `client_id "me"`, `image`, `disk`, `label = hostname`,
      `runtype "ssh_direc ssh_proxy"`, `env {"-p 22:22":"1"}`, `onstart` = rig's onstart hook,
      `cancel_unavail: true`, `price = min_bid × bid_markup` when spot. A taken/unavailable offer
      → next offer; exhausted → `ErrNoCapacity`. Returns the new contract id as `ID`.
- [ ] `EnsureSSHKey`: `GET /ssh/`, `POST /ssh/` when missing; new instances receive account
      keys (docs conflict on whether existing ones do — irrelevant for rig, which never adds a
      key after create).
- [ ] `ListInstances`/`GetInstance`: `GET /instances/?owner=me`; filter `label` prefix `rig-`;
      status map (`actual_status` loading→Provisioning, running→Running, exited→Stopped,
      offline/unknown→Error, absent→Gone); endpoint direct (`public_ipaddr` +
      `ports["22/tcp"][0].HostPort`) else proxy (`ssh_host`, `ssh_port`); user root;
      `Location` = `geolocation`; `PricePerHour` = `dph_total`; `Spot` = `is_bid`.
- [ ] `ShutdownInstance`/`StartInstance` = `PUT /instances/{id}/ {"state": …}`;
      `DeleteInstance` = `DELETE /instances/{id}/`.
- [ ] Volumes: every method → `ErrUnsupported` with "Vast volumes are machine-bound; use
      `rig fetch`/`rig sync` or cloud sync — see spec follow-up".
- [ ] `Balance()` = `GET /users/current/` → `balance`, currency USD.
- [ ] Self-stop line: `curl -fsS -X PUT https://console.vast.ai/api/v0/instances/$CONTAINER_ID/
      -H "Authorization: Bearer $CONTAINER_API_KEY" -H 'Content-Type: application/json'
      -d '{"state":"stopped"}'`.
- [ ] Tests: fixtures for offers/instances/user; token grouping; direct-vs-proxy endpoint;
      offer-taken retry; bid price maths.
- [ ] Manual smoke (after funding + billing method): as in 06, plus a `spot: true` run and a
      proxy-only host.

## Notes

Spec: ../spec.md — "Vast driver specifics". Gotchas: filter key is `reliability`, response
field `reliability2`; `gpu_ram`/`cpu_ram` in MB; offers are ephemeral — never cache offer ids
across commands; `ports` only present on running instances; Vast's ssh mode replaces the image
entrypoint and drops into tmux by default (`touch ~/.no_auto_tmux` in onstart so `rig ssh -- cmd`
is not wrapped). Stopped instances bill storage and may never get their GPU back.

## Comments
