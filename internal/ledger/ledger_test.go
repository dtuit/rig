package ledger

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dtuit/rig/internal/provider"
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

// A ledger written before events carried a provider is Verda's history: its
// events read back as verda, so rig spend --since 720h still shows them under
// the right cloud.
func TestReadOldEventIsVerda(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	old := `{"event":"up","time":"2026-08-18T08:00:00Z","id":"i1","name":"rig-train","type":"1H100","location":"FIN-01","spot":false,"price_per_hour":1.79,"ttl":"12h"}` + "\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil || len(got) != 1 {
		t.Fatalf("Read = %v, %v; want the one old event", got, err)
	}
	if got[0].Provider != "verda" {
		t.Errorf("Provider = %q, want verda for an event without one", got[0].Provider)
	}
}

// Events carry the instance's provider so a RunPod pod's cost never lands in
// a Verda total; the summary line keeps it for grouping.
func TestEventsCarryProvider(t *testing.T) {
	in := &provider.Instance{ID: "p1", Name: "rig-pod", Type: "1xA100-SXM", Provider: "runpod", PricePerHour: 2,
		CreatedAt: ts("2026-08-18T08:00:00Z")}
	up := UpEvent(in, 4*time.Hour, ts("2026-08-18T08:00:00Z"))
	ext := ExtendEvent(in, ts("2026-08-18T14:00:00Z"), ts("2026-08-18T11:00:00Z"))
	down := DownEvent(in, ts("2026-08-18T10:00:00Z"))
	for _, e := range []Event{up, ext, down} {
		if e.Provider != "runpod" {
			t.Errorf("%s event Provider = %q, want runpod", e.Event, e.Provider)
		}
	}
	lines, _ := Summarize([]Event{up, down}, time.Time{}, ts("2026-08-19T00:00:00Z"))
	if len(lines) != 1 || lines[0].Provider != "runpod" {
		t.Errorf("lines = %+v, want one runpod line", lines)
	}
	lines, _ = Summarize([]Event{up}, time.Time{}, ts("2026-08-19T00:00:00Z"))
	if len(lines) != 1 || lines[0].Provider != "runpod" {
		t.Errorf("unclosed lines = %+v, want one runpod line", lines)
	}
}

func TestEventsForOneInstanceOldestFirst(t *testing.T) {
	base := time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC)
	events := []Event{
		{Event: "up", Time: base.Add(2 * time.Hour), Name: "rig-other", Provider: "verda"},
		{Event: "down", Time: base.Add(3 * time.Hour), Name: "rig-trainer", Provider: "verda", Cost: 12.5},
		{Event: "up", Time: base, Name: "rig-trainer", Provider: "verda", Type: "1H100.80S.32V"},
		{Event: "extend", Time: base.Add(time.Hour), Name: "rig-trainer", Provider: "verda"},
	}

	// the name is accepted with or without the hostname prefix, as -i is
	for _, key := range []string{"trainer", "rig-trainer"} {
		got := EventsFor(events, key)
		if len(got) != 3 {
			t.Fatalf("EventsFor(%q) = %d events, want 3: %+v", key, len(got), got)
		}
		if got[0].Event != "up" || got[1].Event != "extend" || got[2].Event != "down" {
			t.Errorf("EventsFor(%q) out of order: %s %s %s",
				key, got[0].Event, got[1].Event, got[2].Event)
		}
		for _, e := range got {
			if e.Name != "rig-trainer" {
				t.Errorf("picked up %s", e.Name)
			}
		}
	}
	if got := EventsFor(events, "nonesuch"); got != nil {
		t.Errorf("want nothing for an unknown instance, got %+v", got)
	}
	if got := EventsFor(events, ""); got != nil {
		t.Errorf("an empty name must not match every instance, got %d", len(got))
	}
}

// A relaunch under the same name is one story to its operator, so both
// runs' events come back together.
func TestEventsForSpansARelaunchOfTheSameName(t *testing.T) {
	base := time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC)
	events := []Event{
		{Event: "up", Time: base, ID: "a", Name: "rig-trainer"},
		{Event: "down", Time: base.Add(time.Hour), ID: "a", Name: "rig-trainer"},
		{Event: "up", Time: base.Add(2 * time.Hour), ID: "b", Name: "rig-trainer"},
	}
	if got := EventsFor(events, "trainer"); len(got) != 3 {
		t.Errorf("want all three, got %+v", got)
	}
}
