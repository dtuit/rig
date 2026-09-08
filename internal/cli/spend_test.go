package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// ts parses an RFC3339 time; the ledger tests' precedent.
func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
