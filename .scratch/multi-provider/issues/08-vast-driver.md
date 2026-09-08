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
- [x] Manual smoke (after funding + billing method): as in 06, plus a `spot: true` run and a
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

2026-09-08 (agent, live smoke): **First live run against Vast** ($10 credit, never rented, no ssh
key registered). Isolated config (`RIG_CONFIG` in a scratch dir, `credentials_file`
`~/secrets/vast.env`, `ssh.public_key ~/.ssh/rig_ed25519.pub`, `defaults.ttl 30m`,
`direct_only: true`), binary built to the same scratch dir. Three instances rented and destroyed;
total spend **$0.0085** (credit 10.00 → 9.99147), ~35 min. **Four live bugs found and fixed, each
red → green against the httptest fake.**

**Sequence and results.** `rig types --available`: 8 s, 214 shapes grouped by geolocation, prices
sane (cheapest 1xQuadro-P2000 $0.03), no `$0.00` in `$/H`. `rig up --type 1xRTX-2070 --location
"South Korea, KR" --name smoke --ttl 30m`: **failed at first** (bug 1), then succeeded in **48 s**
(offer 49868123, $0.0589/h, id 50300289) — `EnsureSSHKey` registered the account's first key
(`POST /ssh/` → id 1336033) on the way. `rig status` / `--json`: endpoint `1.208.108.242:39013`
(direct, host-mapped port), provider `vast`, `raw_status` running. `rig ssh -- …`: no
`/run/systemd/system`, `~/.rig/ttl.pid` = 378 naming a live `bash /root/.rig/ttl.sh`, tmux 3.4 and
rsync 3.2.7 already in the image (`ensureTools` installed nothing), `~/.no_auto_tmux` and
`~/.rig/onstart.sh` in place, `env | grep -c CONTAINER_ID` = **0** (so an ssh session really does
not inherit the pair). `rig run --name hello --wait`: exit 0, `rig logs hello` printed the hostname
and `NVIDIA GeForce RTX 2070`, `rig ps` showed `exited 0`. `rig sync <dir>`: file arrived. `rig
extend 1h`: deadline moved. `rig tunnel --detach 8000` → `tunnel status` `connected` → `curl
localhost:8000` **HTTP 200** → `rig tunnel stop`. `rig adopt` mid-run reinstalled the footprint and
restarted the watchdog without duplicating it (pid 378 → 475).

**Self-stop: verified end to end.** With no rig-\* tmux session alive, `echo $(($(date +%s)-10)) >
~/.rig/deadline` at 18:42:51; the watchdog's next tick ran the stop line; the API showed
`intended_status: stopped` at **18:43:57** and `actual_status: exited` at **18:44:17** — the
container stopped itself with its own instance-scoped credentials, no account key involved
(ADR-0003 holds). `~/.rig/ttl.log` stayed empty (curl said nothing on success). This only worked
after bug 2 was fixed; the original line could never have worked.

**Bug 1 — a location pin matched no offer, so every `rig up` failed.** `Availability`/`Locations`
report `geolocation` as the API spells it ("South Korea, KR") and `CreateInstance` sent that back as
`geolocation eq`, but the offer filter only understands the **upper-case two-letter country code**:
`eq "South Korea, KR"` → 0 offers, `eq "kr"` → 0, `eq "KR"` → 1. Since `core.Pick` always pins a
location, `rig up` on Vast could never rent anything ("no capacity for … trying next" → `none of
1xRTX-2070 available on-demand`). Fixed in `offers.go`/`instances.go`: `countryCode()` narrows the
search to the country and `matchesLocation()` then matches the pin exactly client-side, so a pin
never rents a cheaper host elsewhere in the same country (a bare `US` still takes the country's
cheapest). `searchAll` gained a `base` clause argument and create now pages through it, because one
search page is capped at 512. Tests: the fake's `matches()` now models the country-code semantics
(that alone turned three existing tests red, reproducing the live failure), plus
`TestCreateInstanceLocationPinIsExactWithinCountry` over the three US 1xRTX-4090 fixtures
(`United States, US` → 50243756, `Delaware, US` → 45786457, `, US` → 40912235, `us` → cheapest).

**Bug 2 — the self-stop line read the wrong file and could not have stopped anything.** It sourced
`/etc/environment`, which on a Vast container holds only the **image's build-time env** — neither
`CONTAINER_ID` nor `CONTAINER_API_KEY` is in it — and cannot even be sourced: its values are
unquoted, so `. /etc/environment` died on `NVIDIA_REQUIRE_CUDA=cuda>=12.8 brand=unknown` with
`line 12: 471: No such file or directory` and left `$CONTAINER_ID` empty. The pair lives in the
environment of **PID 1** (`bash /.launch`, Vast's own launcher), and Vast also drops
`/root/.vast_api_key` (verified byte-identical to the injected `CONTAINER_API_KEY`, 64 chars — the
instance-scoped key, not the account's) and `/root/.vast_containerlabel` (`C.<id>`). `selfStop` is
now built by `renderSelfStop(procEnviron, containerKeyFile, containerLabelFile)`, reading
`/proc/1/environ` and falling back to those two files. The paths are arguments so
`TestSelfStopLineStopsThisContainer` **runs the real line** under bash against a fixture environ
(including a hostile `cuda>=12.8 brand=unknown` entry) with a fake `curl` on PATH, asserting the PUT
URL, the bearer token and `{"state":"stopped"}` — and the same for the fallback branch.
`TestCapabilities` now forbids `/etc/environment` (the one existing assertion changed).

**Bug 3 — a proxy-only host could never be rented (two live failures).** On an offer with
`direct_port_count: 0` the rental sat in `loading` for ever with `status_msg` `docker: invalid
hostPort: -1`: both `env {"-p 22:22": "1"}` **and** the `ssh_direc` half of the runtype ask docker
to map a host port the host does not have (dropping only the env still failed — measured). The
driver now sends the port request and `"ssh_direc ssh_proxy"` only when `direct_port_count > 0`, and
`"ssh_proxy"` alone otherwise (`runtypeFor`). New fixture `testdata/offers_proxy_only.json` (live
offer 50103181, RTX PRO 4000, Alberta CA, 0 ports) and
`TestCreateInstanceAsksForAPortOnlyWhereTheHostHasOne`. Verified live afterwards: `rig up --type
1xRTX-PRO-4000 --location "Alberta, CA"` came up in **42 s** (id 50302915, $0.18/h), `rig status`
endpoint `ssh8.vast.ai:22914` with `ip` still the public address, `rig run` returned `proxied from
466c5e1da2d6`, watchdog live, `rig down --yes` deleted it. Reaching a proxy-only host needed
`filters: {verified: {eq: false}, direct_port_count: {eq: 0}}` — **no verified offer today has
fewer than 9 direct ports**, so the proxy path is otherwise unreachable with the driver's default
`verified eq true`.

**Bug 4 — `Balance` reported $0.00 on a funded account.** Live `GET /users/current/` answers
`credit: 10.0, balance: 0` (a renter's money is `credit`; `balance` is a host's unpaid earnings), so
the dashboard tile would have read $0.00 with $10 available. `Balance` now returns `credit`
(deviation from this issue's checkbox, which says `balance`). `testdata/user.json` is the live shape
(credit 9.99419829187, balance 0) and `TestBalanceIsTheAccountCredit` expects the credit;
`TestRateLimitRetry`'s expected amount moved with the fixture.

**Spot.** `rig up --spot --type 1xRTX-3060 --location "Ohio, US"` came up in 4 min (id 50301384):
`is_bid: true`, bid placed at `min_bid 0.02666666666666667 × 1.2 = 0.032` exactly as the maths
says, instance `dph_total` 0.03756 (bid + storage), footprint installed, destroyed cleanly.

**Confirmed non-problems.** `disk_gb` default 20 is enough for the default image
(`vastai/pytorch:cuda-12.8.1-auto`, 12.4 GB compressed) — Vast's disk allocation does not have to
cover it; the instance reported `disk_space: 20.0` and worked. The default image ran on Turing
(RTX 2070) and Ampere, so no image override was needed; a Pascal card was avoided deliberately, and
note that an offer with `cuda_max_good < 12.8` will refuse this image (`NVIDIA_REQUIRE_CUDA
cuda>=12.8`) — the recorded `filters: {cuda_max_good: {gte: 12.8}}` escape hatch is the answer.

**Cleanup.** All three instances destroyed with `DELETE /instances/{id}/` (two through `rig down`,
one directly after a failed create); `GET /instances/` returns `{"instances": []}`. Credit
**10.00 → 9.99147** ($0.00853 spent). The registered ssh key (id 1336033) is **left in place** on
purpose — `EnsureSSHKey` reuses it and Vast copies account keys into new instances.

**Follow-ups (outside this issue's files).** (a) `rig down` with no `-i` refuses a self-stopped
instance — `ResolveFresh("")` only auto-selects `provider.Running`, so the normal end state of a
Vast/RunPod TTL expiry needs `rig down -i <name>`, while the stopped container keeps billing
storage; core-side. (b) `core.provision` logs only "no capacity for X in Y, trying next" and drops
the driver's reason, which is what made bug 1 hard to see; core-side. (c) `rig types` prints
`SPOT$/H 0.00` for a shape with no bid offers (`SpotPrice` 0) — cli-side. (d) `rig up`'s "instance
running at <ip>" prints the public address even when the endpoint is the proxy host; cosmetic.
