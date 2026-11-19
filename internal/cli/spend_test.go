package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/ledger"
)

// seedLedger points the ledger at a temp directory and writes events to it.
func seedLedger(t *testing.T, events ...ledger.Event) {
	t.Helper()
	t.Setenv("RIG_CONFIG", filepath.Join(t.TempDir(), "config.yml"))
	for _, e := range events {
		if err := ledger.Append(ledger.Path(), e); err != nil {
			t.Fatal(err)
		}
	}
}

var (
	verdaUp   = ledger.Event{Event: "up", Time: ts("2026-08-18T08:00:00Z"), ID: "i1", Name: "rig-train", Type: "1H100", PricePerHour: 1.79, TTL: "12h"}
	verdaDown = ledger.Event{Event: "down", Time: ts("2026-08-18T14:06:00Z"), ID: "i1", Name: "rig-train", Type: "1H100", PricePerHour: 1.79, LifetimeSecs: 21960, Cost: 10.92}
	podUp     = ledger.Event{Event: "up", Time: ts("2026-08-18T09:00:00Z"), ID: "p1", Name: "rig-pod", Provider: "runpod", Type: "1xA100-SXM", PricePerHour: 2, TTL: "4h"}
	podDown   = ledger.Event{Event: "down", Time: ts("2026-08-18T11:00:00Z"), ID: "p1", Name: "rig-pod", Provider: "runpod", Type: "1xA100-SXM", PricePerHour: 2, LifetimeSecs: 7200, Cost: 4}
)

// A ledger from one cloud — every ledger written so far, with no provider on
// its events — renders exactly as it always has: no provider column, no
// subtotals.
func TestSpendSingleProviderUnchanged(t *testing.T) {
	seedLedger(t, verdaUp, verdaDown)
	out := runRig(t, &fakeProvider{}, "spend")
	if !strings.HasPrefix(out, "NAME       TYPE   STARTED") {
		t.Errorf("header changed:\n%s", out)
	}
	if !strings.HasSuffix(out, "\ntotal: $10.92\n") {
		t.Errorf("total line changed:\n%s", out)
	}
	if strings.Contains(out, "PROVIDER") || strings.Contains(out, "verda") {
		t.Errorf("single-provider spend must not mention providers:\n%s", out)
	}
}

// Once two clouds appear in the window, each row says which one it ran on
// and each cloud gets its own subtotal ahead of the grand total, so a RunPod
// pod's cost is never read as Verda's.
func TestSpendGroupsPerProvider(t *testing.T) {
	seedLedger(t, verdaUp, verdaDown, podUp, podDown)
	out := runRig(t, &fakeProvider{}, "spend")
	if !strings.HasPrefix(out, "NAME       PROVIDER  TYPE        STARTED") {
		t.Errorf("header lacks the provider column:\n%s", out)
	}
	for _, want := range []string{"rig-train  verda   ", "rig-pod    runpod  "} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "\nrunpod: $4.00\nverda: $10.92\ntotal: $14.92\n") {
		t.Errorf("subtotals missing or misordered:\n%s", out)
	}

	seedLedger(t, verdaUp, verdaDown, podUp, podDown)
	out = runRig(t, &fakeProvider{}, "spend", "--json")
	if !strings.Contains(out, `"provider":"runpod"`) || !strings.Contains(out, `"provider":"verda"`) {
		t.Errorf("spend --json lines lack provider:\n%s", out)
	}
}

// The PROVIDER column and the per-cloud subtotals answer the same question
// `rig status`' column does — is more than one cloud in play — so a user
// with two provider blocks and one cloud's history sees the provider named
// in both tables, not in one.
func TestSpendNamesProvidersWhenConfigKnowsSeveral(t *testing.T) {
	seedLedger(t, verdaUp, verdaDown)
	two := &config.Config{Providers: map[string]config.ProviderConfig{"verda": {}, "runpod": {}}}
	out := runRigCfg(t, two, &fakeProvider{}, "spend")
	if !strings.HasPrefix(out, "NAME       PROVIDER  TYPE") {
		t.Errorf("header lacks the provider column:\n%s", out)
	}
	if !strings.Contains(out, "rig-train  verda ") {
		t.Errorf("row lacks the provider:\n%s", out)
	}
	if !strings.HasSuffix(out, "\nverda: $10.92\ntotal: $10.92\n") {
		t.Errorf("subtotal missing:\n%s", out)
	}
}

// ts parses an RFC3339 time; the ledger tests' precedent.
func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// `rig spend -i <name>` asks a different question of the same ledger: what
// happened to this instance and when, rather than what the estate cost.
func TestSpendForOneInstanceIsATimeline(t *testing.T) {
	extend := ledger.Event{Event: "extend", Time: ts("2026-08-18T11:30:00Z"), ID: "i1",
		Name: "rig-train", Type: "1H100", PricePerHour: 1.79, Deadline: ts("2026-08-19T02:00:00Z")}
	seedLedger(t, verdaUp, extend, verdaDown, podUp, podDown)
	out := runRig(t, &fakeProvider{}, "spend", "-i", "train")

	if !strings.HasPrefix(out, "WHEN") {
		t.Errorf("not a timeline:\n%s", out)
	}
	for _, want := range []string{"up", "extend", "down", "1H100 in", "ttl 12h", "cost $10.92"} {
		if !strings.Contains(out, want) {
			t.Errorf("timeline missing %q:\n%s", want, out)
		}
	}
	// the other cloud's instance is not this instance's history
	if strings.Contains(out, "rig-pod") || strings.Contains(out, "1xA100-SXM") {
		t.Errorf("another instance leaked in:\n%s", out)
	}
	// and it is a timeline, not a summary
	if strings.Contains(out, "total: $") {
		t.Errorf("printed the estate summary as well:\n%s", out)
	}
}

// An instance with no down event is still open; the watchdog leaves none, so
// the timeline has to say so rather than trailing off.
func TestSpendForOneInstanceSaysWhenItIsStillOpen(t *testing.T) {
	seedLedger(t, verdaUp)
	out := runRig(t, &fakeProvider{}, "spend", "-i", "rig-train")
	if !strings.Contains(out, "still open since") {
		t.Errorf("an unclosed instance should say so:\n%s", out)
	}
}

func TestSpendForAnUnknownInstanceSaysSo(t *testing.T) {
	seedLedger(t, verdaUp, verdaDown)
	out := runRig(t, &fakeProvider{}, "spend", "-i", "nonesuch")
	if !strings.Contains(out, "no ledger events for nonesuch") {
		t.Errorf("want a plain answer, got:\n%s", out)
	}
}
