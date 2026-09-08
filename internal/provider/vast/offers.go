package vast

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dtuit/rig/internal/provider"
)

// offer is one rentable machine slice as `POST /bundles/` returns it. Only
// the fields the driver reads are decoded. Sizes are in MB; the reliability
// filter key is `reliability` but the response field is `reliability2`.
type offer struct {
	ID              int64   `json:"id"`
	MachineID       int64   `json:"machine_id"`
	GPUName         string  `json:"gpu_name"`
	NumGPUs         int     `json:"num_gpus"`
	GPURAM          float64 `json:"gpu_ram"`
	CPURAM          float64 `json:"cpu_ram"`
	CPUCores        float64 `json:"cpu_cores_effective"`
	DPHTotal        float64 `json:"dph_total"`
	MinBid          float64 `json:"min_bid"`
	Geolocation     string  `json:"geolocation"`
	Reliability     float64 `json:"reliability2"`
	DirectPortCount int     `json:"direct_port_count"`
}

// catalogue is one crawl of the marketplace: every on-demand and every
// biddable offer that passes the config's filters, cheapest first.
type catalogue struct {
	onDemand []offer
	bid      []offer
	at       time.Time
}

// token is the type name rig shows for a GPU shape: `<n>x<gpu_name>` with
// spaces turned into dashes (`1xRTX-4090`, `2xH100-SXM`).
func token(n int, gpuName string) string {
	return fmt.Sprintf("%dx%s", n, strings.ReplaceAll(strings.TrimSpace(gpuName), " ", "-"))
}

// parseToken turns a type token back into a GPU count and the GPU name as
// Vast spells it, case-insensitively: a name seen in a search this process
// made wins, otherwise the dashes become spaces again.
func (d *Driver) parseToken(tok string) (int, string, error) {
	bad := func() error {
		return fmt.Errorf("instance type %q is not <count>x<gpu> (e.g. 1xRTX-4090); rig types lists Vast's", tok)
	}
	i := strings.IndexAny(tok, "xX")
	if i <= 0 || i == len(tok)-1 {
		return 0, "", bad()
	}
	n, err := strconv.Atoi(tok[:i])
	if err != nil || n < 1 {
		return 0, "", bad()
	}
	rest := tok[i+1:]
	d.mu.Lock()
	name, ok := d.names[strings.ToLower(rest)]
	d.mu.Unlock()
	if !ok {
		name = strings.ReplaceAll(rest, "-", " ")
	}
	return n, name, nil
}

// remember records the spelling of every GPU name seen so tokens typed by
// the user resolve to what the API expects.
func (d *Driver) remember(offers []offer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, o := range offers {
		key := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(o.GPUName), " ", "-"))
		d.names[key] = o.GPUName
	}
}

// countryCode is what an offer query's geolocation clause takes: the
// host's upper-case two-letter country code. Vast spells the location out
// in responses ("South Korea, KR") but filters on the code alone — measured
// live 2026-09-08, where `geolocation eq "South Korea, KR"` and a
// lower-case "kr" both matched nothing — so a location pin is narrowed
// server-side by code and matched exactly by matchesLocation afterwards.
// A pin the code cannot be read from yields "" (no clause).
func countryCode(geo string) string {
	geo = strings.TrimSpace(geo)
	if i := strings.LastIndex(geo, ","); i >= 0 {
		geo = strings.TrimSpace(geo[i+1:])
	}
	if len(geo) != 2 {
		return ""
	}
	return strings.ToUpper(geo)
}

// matchesLocation reports whether an offer's geolocation satisfies a
// location pin. A bare country code ("US") takes any host in that country;
// anything else must equal the geolocation as Vast spells it, which is how
// rig types and rig locations print it.
func matchesLocation(geo, pin string) bool {
	pin = strings.TrimSpace(pin)
	if pin == "" {
		return true
	}
	if !strings.Contains(pin, ",") && len(pin) == 2 {
		return strings.EqualFold(countryCode(geo), pin)
	}
	return strings.EqualFold(strings.TrimSpace(geo), pin)
}

// searchType is Vast's offer market: on-demand rentals or bids.
type searchType string

const (
	onDemand searchType = "on-demand"
	bid      searchType = "bid"
)

// query builds the offer search body: the config's baseline (rentable,
// verified, not external, not rented, reliable enough, direct ports when
// asked, the user's raw filters), then the caller's clauses on top.
func (d *Driver) query(typ searchType, clauses map[string]any) map[string]any {
	q := map[string]any{
		"rentable":    map[string]any{"eq": true},
		"verified":    map[string]any{"eq": true},
		"external":    map[string]any{"eq": false},
		"rented":      map[string]any{"eq": false},
		"reliability": map[string]any{"gte": d.cfg.MinReliability},
	}
	if d.cfg.DirectOnly {
		q["direct_port_count"] = map[string]any{"gt": 0}
	}
	for k, v := range d.cfg.Filters {
		q[k] = v
	}
	for k, v := range clauses {
		q[k] = v
	}
	q["type"] = string(typ)
	q["order"] = [][]string{{"dph_total", "asc"}}
	q["limit"] = d.pageSize
	return q
}

// search runs one offer search page.
func (d *Driver) search(ctx context.Context, q map[string]any) ([]offer, error) {
	var res struct {
		Offers []offer `json:"offers"`
	}
	if err := d.c.do(ctx, "POST", "/bundles/", nil, q, &res); err != nil {
		return nil, fmt.Errorf("searching offers: %w", err)
	}
	d.remember(res.Offers)
	return res.Offers, nil
}

// maxPages bounds the catalogue crawl.
const maxPages = 8

// searchAll crawls every offer of one market type matching base by paging
// on price (`dph_total gt <last seen>`): a page is capped at pageSize
// whatever limit is asked for, so one search never sees the whole market.
// Bid pages can overlap, so offers are deduplicated by id.
func (d *Driver) searchAll(ctx context.Context, typ searchType, base map[string]any) ([]offer, error) {
	var (
		out  []offer
		seen = map[int64]bool{}
		last = math.Inf(-1)
	)
	for page := 0; page < maxPages; page++ {
		clauses := map[string]any{}
		for k, v := range base {
			clauses[k] = v
		}
		if page > 0 {
			cursor := map[string]any{"gt": last}
			if user, ok := d.cfg.Filters["dph_total"].(map[string]any); ok {
				for k, v := range user {
					cursor[k] = v
				}
				cursor["gt"] = last
			}
			clauses["dph_total"] = cursor
		}
		offers, err := d.search(ctx, d.query(typ, clauses))
		if err != nil {
			return nil, err
		}
		fresh := 0
		for _, o := range offers {
			if !seen[o.ID] {
				seen[o.ID] = true
				out = append(out, o)
				fresh++
			}
			if o.DPHTotal > last {
				last = o.DPHTotal
			}
		}
		if len(offers) < d.pageSize || fresh == 0 {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DPHTotal < out[j].DPHTotal })
	return out, nil
}

// catalogue returns the process's current crawl, refreshing it after
// catalogueTTL.
func (d *Driver) catalogue(ctx context.Context) (*catalogue, error) {
	d.mu.Lock()
	cat := d.cat
	d.mu.Unlock()
	if cat != nil && time.Since(cat.at) < catalogueTTL {
		return cat, nil
	}
	od, err := d.searchAll(ctx, onDemand, nil)
	if err != nil {
		return nil, err
	}
	bids, err := d.searchAll(ctx, bid, nil)
	if err != nil {
		return nil, err
	}
	cat = &catalogue{onDemand: od, bid: bids, at: time.Now()}
	d.mu.Lock()
	d.cat = cat
	d.mu.Unlock()
	return cat, nil
}

// types folds the crawl into one InstanceType per (num_gpus, gpu_name):
// the cheapest on-demand offer's price and hardware, the cheapest min_bid as
// SpotPrice, the offer count as Description. A shape the crawl only reaches
// in the bid market gets a row too, priced by the cheapest bid-market
// dph_total and marked "(bid only)" in Description, because `rig up` without
// `spot: true` cannot rent it. Cheapest first.
func (c *catalogue) types() []provider.InstanceType {
	type key struct {
		n    int
		name string
	}
	rows := map[key]*provider.InstanceType{}
	order := []key{}
	counts := map[key]int{}       // offers behind the row's quoted price
	hasOnDemand := map[key]bool{} // shapes the on-demand market carries
	newRow := func(o offer) *provider.InstanceType {
		return &provider.InstanceType{
			Type:         token(o.NumGPUs, o.GPUName),
			GPU:          o.GPUName,
			GPUCount:     o.NumGPUs,
			GPUMemoryGB:  int(math.Round(o.GPURAM / 1024)),
			MemoryGB:     int(math.Round(o.CPURAM / 1024)),
			CPUCount:     int(math.Round(o.CPUCores)),
			PricePerHour: o.DPHTotal,
		}
	}
	for _, o := range c.onDemand {
		k := key{o.NumGPUs, o.GPUName}
		row, ok := rows[k]
		if !ok {
			row = newRow(o)
			rows[k] = row
			order = append(order, k)
		}
		if o.DPHTotal < row.PricePerHour {
			row.PricePerHour = o.DPHTotal
		}
		hasOnDemand[k] = true
		counts[k]++
	}
	for _, o := range c.bid {
		k := key{o.NumGPUs, o.GPUName}
		row, ok := rows[k]
		if !ok {
			row = newRow(o) // priced by the bid market until an on-demand offer says otherwise
			rows[k] = row
			order = append(order, k)
		}
		if !hasOnDemand[k] {
			if o.DPHTotal < row.PricePerHour {
				row.PricePerHour = o.DPHTotal
			}
			counts[k]++
		}
		if row.SpotPrice == 0 || o.MinBid < row.SpotPrice {
			row.SpotPrice = o.MinBid
		}
	}
	for k, row := range rows {
		row.Description = describeOffers(counts[k], !hasOnDemand[k])
	}
	out := make([]provider.InstanceType, 0, len(order))
	for _, k := range order {
		out = append(out, *rows[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := priceOf(out[i]), priceOf(out[j])
		if pi != pj {
			return pi < pj
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// priceOf orders catalogue rows: on-demand price, else the bid floor.
func priceOf(t provider.InstanceType) float64 {
	if t.PricePerHour > 0 {
		return t.PricePerHour
	}
	return t.SpotPrice
}

// describeOffers renders a row's offer count, saying so when the only
// offers are bids: an on-demand rental of that shape is not available.
func describeOffers(n int, bidOnly bool) string {
	desc := fmt.Sprintf("%d offers", n)
	if n == 1 {
		desc = "1 offer"
	}
	if bidOnly {
		desc += " (bid only)"
	}
	return desc
}
