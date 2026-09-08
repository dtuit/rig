# 08 — Vast.ai driver (REST v0)

**What to build:** `provider: vast` works end to end: `rig types --available` turns the offer
marketplace into a catalogue (one row per `1xRTX-4090`-style token, cheapest price, offer count,
grouped by geolocation); `rig up` picks the cheapest matching offer, retries the next one when
it is taken, connects over direct ssh when the host has open ports and the proxy otherwise, and
installs the footprint with the instance-scoped `CONTAINER_API_KEY` as the self-stop path.
Bids (`spot: true`) work with a markup over `min_bid`. Volumes are refused with a clear message.

**Blocked by:** 01, 02, 03, 05.

**Status:** resolved

- [x] `internal/provider/vast`: client for `https://console.vast.ai/api/v0`, Bearer auth, 429
      retry with small backoff (CLI precedent: 3 tries). Credentials: `VAST_API_KEY`, then
      `credentials_file`. Config: `min_reliability` (0.95), `bid_markup` (1.2), `direct_only`
      (false), `image` (default `vastai/pytorch:<pinned>`), `disk_gb` (20), extra offer filters
      as a raw map merged into the query (escape hatch: `cuda_max_good`, `inet_down`, …).
- [x] Offer search = `POST /bundles/` with `{rentable eq true, verified eq true, external eq
      false, rented eq false, reliability gte <min>, type on-demand|bid, order [[dph_total asc]]}`
      plus token → `gpu_name eq`, `num_gpus eq`; `geolocation eq` when a location is pinned.
- [x] `InstanceTypes()` groups a broad search by `(num_gpus, gpu_name)`: token, cheapest
      `dph_total`, `SpotPrice` = cheapest `min_bid` from a `type: bid` search, GPU memory from
      `gpu_ram` (MB→GB), description `N offers`. `Availability()` groups the same results by
      `geolocation`. `Locations()` = distinct geolocations seen.
- [x] `CreateInstance`: search for the token in the location; for each offer in price order
      `PUT /asks/{id}/` with `client_id "me"`, `image`, `disk`, `label = hostname`,
      `runtype "ssh_direc ssh_proxy"`, `env {"-p 22:22":"1"}`, `onstart` = rig's onstart hook,
      `cancel_unavail: true`, `price = min_bid × bid_markup` when spot. A taken/unavailable offer
      → next offer; exhausted → `ErrNoCapacity`. Returns the new contract id as `ID`.
- [x] `EnsureSSHKey`: `GET /ssh/`, `POST /ssh/` when missing; new instances receive account
      keys (docs conflict on whether existing ones do — irrelevant for rig, which never adds a
      key after create).
- [x] `ListInstances`/`GetInstance`: `GET /instances/?owner=me`; filter `label` prefix `rig-`;
      status map (`actual_status` loading→Provisioning, running→Running, exited→Stopped,
      offline/unknown→Error, absent→Gone); endpoint direct (`public_ipaddr` +
      `ports["22/tcp"][0].HostPort`) else proxy (`ssh_host`, `ssh_port`); user root;
      `Location` = `geolocation`; `PricePerHour` = `dph_total`; `Spot` = `is_bid`.
- [x] `ShutdownInstance`/`StartInstance` = `PUT /instances/{id}/ {"state": …}`;
      `DeleteInstance` = `DELETE /instances/{id}/`.
- [x] Volumes: every method → `ErrUnsupported` with "Vast volumes are machine-bound; use
      `rig fetch`/`rig sync` or cloud sync — see spec follow-up".
- [x] `Balance()` = `GET /users/current/` → `balance`, currency USD.
- [x] Self-stop line: `curl -fsS -X PUT https://console.vast.ai/api/v0/instances/$CONTAINER_ID/
      -H "Authorization: Bearer $CONTAINER_API_KEY" -H 'Content-Type: application/json'
      -d '{"state":"stopped"}'`.
- [x] Tests: fixtures for offers/instances/user; token grouping; direct-vs-proxy endpoint;
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

2026-09-08 (agent): Landed. `internal/provider/vast/`: `client.go` (hand-written net/http: Bearer
auth, JSON in/out, 429 retried 3× with 0.5s/1s/2s backoff — `retryWait` field for tests;
`*apiError` text is `vast: PUT /asks/1/: HTTP 404 Not Found: <code>: <msg>` so core's `isNotFound`
heuristic fires on a vanished instance), `vast.go` (`init()` registers `vast` via
`cfg.Creds([]string{"VAST_API_KEY"})` + `cfg.Decode(&own)`; `Config{MinReliability 0.95, BidMarkup
1.2, DirectOnly, Image, DiskGB 20, Filters map[string]any}`; `New(apiKey, cfg) *Driver`;
`DefaultImage = "vastai/pytorch:cuda-12.8.1-auto"` — tag confirmed on Docker Hub 2026-09-08
(pushed 2026-09-01; `-auto` picks the torch build for the host driver; 12.9.2/13.x `-auto` tags
also exist); `Capabilities{Spot, Balance, SelfStop}` with the self-stop line prefixed by
`[ -f /etc/environment ] && set -a && . /etc/environment; set +a;` because an ssh session does not
see the container env; volumes → `ErrUnsupported` with the machine-bound message; `Balance`
`GET /users/current/` → `balance` USD; `EnsureSSHKey` handles `{"ssh_keys":[…]}` and the bare
list (live account answers `[]`), posts `{"ssh_key": key}` and reads the id from the answer or a
re-list), `offers.go` (offer search, tokens, catalogue), `instances.go` (instance mapping, create,
stop/start/delete, ssh keys). Live probes 2026-09-08 (read-only): `POST /bundles/` works (200), and
so does `GET /bundles/?q=` — POST is used. **A search page is capped at 512 offers whatever `limit`
says**, and the cheapest 512 top out at ~$0.27/h (no RTX 4090, no H100), so `searchAll` pages by
price (`dph_total gt <last seen>`, max 8 pages, deduplicated by id because bid pages overlap):
today 3 on-demand + 2 bid pages = 1094 + 889 offers, 214 shapes. The crawl is cached in the Driver
for 30 s so `rig types` (InstanceTypes + Availability) searches once; never across commands.
Missing instance answers HTTP 200 `{"instances": null}` (not 404) — both map to `Gone`.
`GET /instances/{id}/` returns the object under `instances` (per the CLI source); a list shape is
tolerated. Onstart: `touch ~/.no_auto_tmux; [ -f ~/.rig/onstart.sh ] && bash ~/.rig/onstart.sh`.
Status: running→Running (Stopped when `intended_status` is stopped), loading/created/null→
Provisioning, exited→Stopped, offline/unknown→Error, absent→Gone, else pass-through. Endpoint:
`public_ipaddr:ports["22/tcp"][0].HostPort` else `ssh_host:ssh_port`, user root; `IP` falls back
to `ssh_host` so `waitRunning` sees a non-empty IP on proxy-only hosts. Tests
(`vast_test.go`, 20 tests, `httptest` fake that applies the filter DSL to the fixtures):
`testdata/offers_ondemand.json` + `offers_bid.json` (trimmed from the live crawl: 1xRTX-3060,
1xRTX-4090, 2xRTX-4090, 1xH100-SXM, 3 cheapest each), `instances.json` (hand-written from the CLI's
field list — the account has no instances), `user.json` (live shape, balance edited to 7.5),
`ssh_keys.json`. Seams: token grouping/prices/hardware/offer count, price paging, catalogue shared
within a command, availability by geolocation (on-demand vs bid), locations, label filter, status
map, direct vs proxy endpoint, Gone, stop/start/delete + "not found" text, offer-taken retry in
price order ending in `ErrNoCapacity` (with Vast's last message), location pin + config defaults,
bid maths (`min_bid × markup`), token case/spelling, `direct_only` + raw filters, volumes
unsupported, balance, ssh key list/register (both shapes), 429 retry/exhaustion, 401 text, factory
decode. Deviations: `gpu_ram`/`cpu_ram` are divided by 1024 not 1000 (24564 MB → 24 GB, not 25);
a `success:false` create whose text is not an offer-gone wording is *not* retried across offers —
credit/billing wordings abort at once, and unknown texts abort with the message so a config error
(bad image) is not reported as "no capacity" (offer-gone wording: `no_such_ask|not found|no
longer|unavailable|not available|already rented|been rented|not rentable|taken`, plus any 404 —
broaden when a real message is seen); case-insensitive tokens rely on GPU names seen in a search
this process made (`rig up` always searches via Availability first), otherwise dashes become
spaces with the case as typed. Follow-ups: `core.Pick` matches tokens with exact `slices.Contains`,
so `--type 1xrtx-4090` fails in Pick before the driver sees it (spec says matching is
case-insensitive; core-side); no `cuda_max_good` floor is applied by default, so an offer whose
driver cannot run the image is possible — set `filters: {cuda_max_good: {gte: 12.8}}`; the
hostname `label` is also what Vast shows in its console. Manual smoke not run (balance $0, no
billing method). `internal/cli/provider_test.go` `TestUnknownProviderNamesRegistered` expects
`have: testfake, verda` and now sees every registered driver — outside this issue's files; one
edit once all drivers land.

2026-09-08 (agent, code-review fixes): Four defects from the two-axis review, all in
`internal/provider/vast/` (tests first, red → green). **(1) Raw status no longer leaks into the
normalised enum.** `fromAPIStatus` returned `provider.Status(actual)` for an unrecognised word, so
such an instance matched no core/tui/cli/instcache comparison (spec: "Drivers map; core/tui/cli/
instcache compare constants") — invisible to `ResolveFresh`'s running check and to the TTL/status
logic. It now returns `provider.Error` (safe and visible) while `fromAPI` keeps Vast's word in
`Instance.RawStatus`, as it already did for the mapped words. Fixture: `instances.json` gained
`rig-odd` (`actual_status: "recreating"`, a word the driver does not map); `instances_found` 7 → 8.
`TestListInstancesMapsStatusAndEndpoint` now expects 7 rig-labelled instances (was 6) and asserts
`rig-odd` is `Error`/`"recreating"` — the only existing assertion changed.
**(2) Bid-only GPU shapes are priced honestly (story 17).** `catalogue.types()` folded the two
crawls together so a shape the crawl only reaches in the bid market got `PricePerHour` 0 (a `$0.00`
row a user could act on) and an offer count of 1 whatever the number of bids. `types()` now tracks,
per shape, whether the on-demand market carries it and how many offers stand behind the row's
quoted price: a shape in both markets is unchanged (cheapest `dph_total`, on-demand offer count);
a bid-only shape is priced by its cheapest bid-market `dph_total`, counts its bid offers, and its
`Description` reads `N offers (bid only)` — on-demand availability is what `rig up` without
`spot: true` needs, so the row says it cannot serve that. `SpotPrice` is still the cheapest
`min_bid` for both. `countOffers` (string increment) is replaced by `describeOffers(n, bidOnly)`.
New fixture `testdata/offers_bid_only.json` — two real bid offers for `2xRTX-5080` (ids 26184983
North Carolina and 49614257 Latvia, live read-only search 2026-09-08) — appended to the bid market
by the new `fakeVast.addOffers` helper, so the existing fixtures and their probe-derived expected
values stay untouched. `TestInstanceTypesBidOnlyShape` asserts `PricePerHour` 0.2817777777777778,
`SpotPrice` 0.28, `"2 offers (bid only)"`, the hardware, that the row sorts by its real price
(between `1xRTX-3060` and `1xRTX-4090`, not by the bid floor), and that `1xRTX-4090` (both markets)
is unchanged at 0.2962962962962963 / `"3 offers"`. Note: no shape is bid-only when queried directly
(every bid offer is the same machine slice) — bid-only rows come from the crawl's price reach
(8 × 512 offers), which is exactly what the fixture models.
**(3) The volumes error no longer cites `.scratch/`.** Now "Vast volumes are machine-bound, so rig
cannot attach one: copy data with rig sync/rig fetch, or use Vast's cloud sync" (the checkbox above
still quotes the original wording, including the dropped "see spec follow-up").
`TestVolumesUnsupported` additionally requires `rig sync` and forbids the word "spec".
**(4) `runtype "ssh_direc ssh_proxy"`** is upstream's own (abbreviated) spelling, confirmed against
Vast's CLI source; `instances.go` now carries a comment saying so, so no future reader "fixes" it
into a bug. Deliberately left alone (recorded above): volumes unsupported across all six methods,
aborting rather than walking every offer when a create fails for a non-offer-gone reason, the
512-offer page cap workaround and the 30 s in-process crawl cache, `gpu_ram`/`cpu_ram` ÷ 1024.
Follow-up for the TUI owner: `internal/tui/picker.go` clips `Description` to 14 columns, so
`"2 offers (bid only)"` renders as `"2 offers (bid…"`; `rig types` does not print `Description` at
all, so a bid-only row shows its price with `AVAILABLE` `-` unless `--spot` is passed.
