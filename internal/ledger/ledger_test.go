package ledger

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestAppendReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	events := []Event{
		{Event: "up", Time: ts("2026-08-18T08:00:00Z"), ID: "i1", Name: "rig-train", PricePerHour: 1.79, TTL: "12h"},
		{Event: "down", Time: ts("2026-08-18T14:06:00Z"), ID: "i1", Name: "rig-train", PricePerHour: 1.79, LifetimeSecs: 21960, Cost: 10.92},
	}
	for _, e := range events {
		if err := Append(path, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Event != "up" || got[1].Cost != 10.92 {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

func TestReadMissingFileIsEmpty(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want empty, nil", got, err)
	}
}

func TestSummarizeClosedInstance(t *testing.T) {
	events := []Event{
		{Event: "up", Time: ts("2026-08-18T08:00:00Z"), ID: "i1", Name: "rig-train", Type: "1H100", PricePerHour: 1.79},
		{Event: "down", Time: ts("2026-08-18T14:06:00Z"), ID: "i1", Name: "rig-train", Type: "1H100", PricePerHour: 1.79, LifetimeSecs: 21960, Cost: 10.92},
	}
	lines, total := Summarize(events, time.Time{}, ts("2026-08-19T00:00:00Z"))
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	l := lines[0]
	if !l.Closed || l.Estimated || l.Cost != 10.92 {
		t.Errorf("line = %+v, want closed, exact, cost 10.92", l)
	}
	if total != 10.92 {
		t.Errorf("total = %v", total)
	}
}

func TestSummarizeUnclosedEstimatesThroughTTL(t *testing.T) {
	// TTL-torn instance: up at 08:00 with 4h TTL, never downed, now is much later.
	events := []Event{
		{Event: "up", Time: ts("2026-08-18T08:00:00Z"), ID: "i2", Name: "rig-sweep", PricePerHour: 2.0, TTL: "4h"},
	}
	lines, total := Summarize(events, time.Time{}, ts("2026-08-19T00:00:00Z"))
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	l := lines[0]
	if l.Closed || !l.Estimated {
		t.Errorf("line = %+v, want unclosed estimate", l)
	}
	// capped at the TTL: 4h × $2 = $8, not 16h × $2
	if math.Abs(l.Cost-8.0) > 0.001 || math.Abs(total-8.0) > 0.001 {
		t.Errorf("cost = %v, want 8.0 (capped at TTL)", l.Cost)
	}
}

func TestSummarizeExtendPushesTheEstimateCap(t *testing.T) {
	// up with 4h TTL, extended to a deadline 10h after launch, never downed
	events := []Event{
		{Event: "up", Time: ts("2026-08-18T08:00:00Z"), ID: "i4", Name: "rig-ext", PricePerHour: 2.0, TTL: "4h"},
		{Event: "extend", Time: ts("2026-08-18T11:00:00Z"), ID: "i4", Deadline: ts("2026-08-18T18:00:00Z")},
	}
	lines, _ := Summarize(events, time.Time{}, ts("2026-08-19T08:00:00Z"))
	if math.Abs(lines[0].Cost-20.0) > 0.001 {
		t.Errorf("cost = %v, want 20.0 (10h × $2, capped at the extended deadline)", lines[0].Cost)
	}
}

func TestSummarizeStillRunningEstimatesToNow(t *testing.T) {
	events := []Event{
		{Event: "up", Time: ts("2026-08-18T08:00:00Z"), ID: "i3", Name: "rig-live", PricePerHour: 2.0, TTL: "12h"},
	}
	lines, _ := Summarize(events, time.Time{}, ts("2026-08-18T10:00:00Z"))
	if math.Abs(lines[0].Cost-4.0) > 0.001 {
		t.Errorf("cost = %v, want 4.0 (2h so far)", lines[0].Cost)
	}
}

func TestSummarizeSinceFilters(t *testing.T) {
	events := []Event{
		{Event: "up", Time: ts("2026-08-01T08:00:00Z"), ID: "old", Name: "rig-old", PricePerHour: 1.0},
		{Event: "down", Time: ts("2026-08-01T10:00:00Z"), ID: "old", Name: "rig-old", PricePerHour: 1.0, Cost: 2.0},
		{Event: "up", Time: ts("2026-08-18T08:00:00Z"), ID: "new", Name: "rig-new", PricePerHour: 1.0},
		{Event: "down", Time: ts("2026-08-18T10:00:00Z"), ID: "new", Name: "rig-new", PricePerHour: 1.0, Cost: 2.0},
	}
	lines, total := Summarize(events, ts("2026-08-17T00:00:00Z"), ts("2026-08-19T00:00:00Z"))
	if len(lines) != 1 || lines[0].Name != "rig-new" {
		t.Fatalf("lines = %+v, want only rig-new", lines)
	}
	if total != 2.0 {
		t.Errorf("total = %v", total)
	}
}
