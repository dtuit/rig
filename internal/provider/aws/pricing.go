package aws

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	pricingtypes "github.com/aws/aws-sdk-go-v2/service/pricing/types"
)

// pricingRegion hosts the Price List API (it exists in only a few regions);
// the regionCode filter still selects the configured region's prices.
const pricingRegion = "us-east-1"

const priceCacheTTL = 24 * time.Hour

// priceCache is ~/.cache/rig/aws-pricing-<region>.json: the Linux, shared-
// tenancy on-demand hourly price of every instance type in the region.
// A type the price list did not carry is stored at 0 so it is not asked
// for again until the cache expires.
type priceCache struct {
	FetchedAt time.Time          `json:"fetched_at"`
	Prices    map[string]float64 `json:"prices"`
}

func (d *Driver) cachePath() string {
	return filepath.Join(d.cacheDir, "aws-pricing-"+d.region+".json")
}

// onDemandPrices answers the on-demand hourly price of each named type from
// the 24h cache, refreshing the whole region's list when the cache is stale
// or lacks a type. Prices decorate a listing, so a Price List failure leaves
// them at 0 rather than failing `rig types` — but it is warned about once
// per process, because a 0 price also lands in the spend ledger.
func (d *Driver) onDemandPrices(ctx context.Context, names []string) map[string]float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	pc := d.prices
	if pc == nil {
		pc = d.readPriceCache()
	}
	stale := pc == nil || d.now().Sub(pc.FetchedAt) >= priceCacheTTL
	if !stale {
		for _, n := range names {
			if _, ok := pc.Prices[n]; !ok {
				stale = true
				break
			}
		}
	}
	if stale && !d.pricingDown {
		got, err := d.fetchPrices(ctx)
		if err != nil {
			d.pricingDown = true
			d.logf("warning: AWS pricing is unavailable (%v); prices show as 0 for the rest of this command and recorded spend for anything launched now will be wrong", err)
		} else {
			for _, n := range names {
				if _, ok := got[n]; !ok {
					got[n] = 0
				}
			}
			pc = &priceCache{FetchedAt: d.now(), Prices: got}
			d.writePriceCache(pc)
		}
	}
	d.prices = pc
	out := make(map[string]float64, len(names))
	if pc != nil {
		for _, n := range names {
			out[n] = pc.Prices[n]
		}
	}
	return out
}

// fetchPrices queries the Price List for every Linux, shared-tenancy,
// no-pre-installed-software, used-capacity EC2 product in the region and
// keeps the RunInstances hourly price per instance type.
func (d *Driver) fetchPrices(ctx context.Context) (map[string]float64, error) {
	term := func(field, value string) pricingtypes.Filter {
		return pricingtypes.Filter{Type: pricingtypes.FilterTypeTermMatch, Field: &field, Value: &value}
	}
	pag := pricing.NewGetProductsPaginator(d.pricing, &pricing.GetProductsInput{
		ServiceCode:   sdk.String("AmazonEC2"),
		FormatVersion: sdk.String("aws_v1"),
		MaxResults:    sdk.Int32(100),
		Filters: []pricingtypes.Filter{
			term("regionCode", d.region),
			term("operatingSystem", "Linux"),
			term("tenancy", "Shared"),
			term("preInstalledSw", "NA"),
			term("capacitystatus", "Used"),
		},
	})
	out := map[string]float64{}
	for pag.HasMorePages() {
		page, err := pag.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, doc := range page.PriceList {
			if typ, usd, ok := parsePrice(doc); ok {
				out[typ] = usd
			}
		}
	}
	return out, nil
}

// parsePrice reads one price-list document: the instance type and its
// hourly USD on-demand price, for RunInstances operations only (the SQL
// and other variants carry suffixed operation codes).
func parsePrice(doc string) (typ string, usd float64, ok bool) {
	var item struct {
		Product struct {
			Attributes map[string]string `json:"attributes"`
		} `json:"product"`
		Terms struct {
			OnDemand map[string]struct {
				PriceDimensions map[string]struct {
					Unit         string            `json:"unit"`
					PricePerUnit map[string]string `json:"pricePerUnit"`
				} `json:"priceDimensions"`
			} `json:"OnDemand"`
		} `json:"terms"`
	}
	if json.Unmarshal([]byte(doc), &item) != nil {
		return "", 0, false
	}
	typ = item.Product.Attributes["instanceType"]
	if typ == "" || !strings.HasPrefix(item.Product.Attributes["operation"], "RunInstances") {
		return "", 0, false
	}
	for _, term := range item.Terms.OnDemand {
		for _, dim := range term.PriceDimensions {
			if dim.Unit != "Hrs" {
				continue
			}
			if v, err := strconv.ParseFloat(dim.PricePerUnit["USD"], 64); err == nil && v > 0 {
				return typ, v, true
			}
		}
	}
	return "", 0, false
}

func (d *Driver) readPriceCache() *priceCache {
	if d.cacheDir == "" {
		return nil
	}
	b, err := os.ReadFile(d.cachePath())
	if err != nil {
		return nil
	}
	var pc priceCache
	if json.Unmarshal(b, &pc) != nil || pc.Prices == nil {
		return nil
	}
	return &pc
}

// writePriceCache is best effort: an unwritable cache only costs a refetch.
func (d *Driver) writePriceCache(pc *priceCache) {
	if d.cacheDir == "" {
		return
	}
	b, err := json.Marshal(pc)
	if err != nil {
		return
	}
	if err := os.MkdirAll(d.cacheDir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(d.cachePath(), b, 0o644)
}
