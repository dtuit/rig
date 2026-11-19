// Package ledger keeps a local append-only record of instance lifecycle
// events for spend reporting. Advisory only: the provider's billing is
// authoritative, and instances torn down by the TTL watchdog get no down
// event, so their cost is an estimate (issue 05).
package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/provider"
)

// DefaultProvider is the provider an event recorded before events carried one
// belongs to: every ledger written by a single-provider rig was Verda's.
const DefaultProvider = "verda"

// Event is one lifecycle record. Provider names the cloud the instance was
// on; Read fills it in for older records (see DefaultProvider), so callers
// never see it empty.
type Event struct {
	Event        string    `json:"event"` // "up" | "extend" | "down"
	Time         time.Time `json:"time"`
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Provider     string    `json:"provider,omitempty"`
	Type         string    `json:"type"`
	Location     string    `json:"location"`
	Spot         bool      `json:"spot"`
	PricePerHour float64   `json:"price_per_hour"`
	TTL          string    `json:"ttl,omitempty"`              // up events
	Deadline     time.Time `json:"deadline,omitempty"`         // extend events: new self-destruct time
	LifetimeSecs int64     `json:"lifetime_seconds,omitempty"` // down events
	Cost         float64   `json:"cost,omitempty"`             // down events
}

// Path returns the ledger location, beside the config file.
func Path() string {
	return filepath.Join(filepath.Dir(config.DefaultPath()), "ledger.jsonl")
}

// UpEvent records an instance launch.
func UpEvent(in *provider.Instance, ttl time.Duration, now time.Time) Event {
	return Event{
		Event: "up", Time: now, ID: in.ID, Name: in.Name, Provider: in.Provider, Type: in.Type,
		Location: in.Location, Spot: in.Spot, PricePerHour: in.PricePerHour,
		TTL: ttl.String(),
	}
}

// ExtendEvent records a pushed-out deadline, so an unclosed instance's
// estimate reaches the extended deadline instead of stopping at the up TTL.
func ExtendEvent(in *provider.Instance, deadline, now time.Time) Event {
	return Event{
		Event: "extend", Time: now, ID: in.ID, Name: in.Name, Provider: in.Provider, Type: in.Type,
		Location: in.Location, Spot: in.Spot, PricePerHour: in.PricePerHour,
		Deadline: deadline,
	}
}

// DownEvent records a deliberate teardown with the exact lifetime cost.
func DownEvent(in *provider.Instance, now time.Time) Event {
	lifetime := now.Sub(in.CreatedAt)
	return Event{
		Event: "down", Time: now, ID: in.ID, Name: in.Name, Provider: in.Provider, Type: in.Type,
		Location: in.Location, Spot: in.Spot, PricePerHour: in.PricePerHour,
		LifetimeSecs: int64(lifetime.Seconds()),
		Cost:         lifetime.Hours() * in.PricePerHour,
	}
}

func Append(path string, e Event) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, string(b))
	return err
}

// Read returns all events, each with its Provider filled (DefaultProvider for
// records written before the field existed); a missing ledger is empty, not
// an error.
func Read(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var events []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue // a corrupt line loses one record, not the ledger
		}
		if e.Provider == "" {
			e.Provider = DefaultProvider
		}
		events = append(events, e)
	}
	return events, sc.Err()
}

// Line is one instance's spend within a summary window.
type Line struct {
	Name      string    `json:"name"`
	Provider  string    `json:"provider,omitempty"`
	Type      string    `json:"type"`
	StartedAt time.Time `json:"started_at"`
	Hours     float64   `json:"hours"`
	Cost      float64   `json:"cost"`
	Closed    bool      `json:"closed"`    // a down event was recorded
	Estimated bool      `json:"estimated"` // no down event: cost estimated through the TTL
}

// EventsFor is one instance's lifecycle from the ledger, oldest first:
// every up, extend and down recorded under that name. It answers the
// questions a spend summary cannot — when was this extended, and how many
// times — and it costs no provider call, which is why the dashboard can
// show it beside an instance without asking the cloud anything.
//
// The name may be given with or without the rig- hostname prefix, as every
// other command accepts it. Matching on the name rather than the ID is
// deliberate: a volume-backed instance relaunched under the same name is
// the same piece of work to its operator, and its history should read as
// one story.
func EventsFor(events []Event, name string) []Event {
	if name == "" {
		return nil
	}
	// ledger writes the hostname; -i takes either spelling
	full := name
	if !strings.HasPrefix(name, hostPrefix) {
		full = hostPrefix + name
	}
	var out []Event
	for _, e := range events {
		if e.Name == name || e.Name == full {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

// hostPrefix is core.HostPrefix, spelled again here because core imports
// this package and not the other way round.
const hostPrefix = "rig-"

// Summarize pairs up/down events by instance ID. A closed instance reports
// its recorded cost; an unclosed one (still running, or TTL-torn without a
// down event) is estimated from launch to min(now, launch+TTL) and marked.
// since (zero = everything) filters on when the spend ended.
func Summarize(events []Event, since, now time.Time) ([]Line, float64) {
	ups := map[string]Event{}
	closed := map[string]bool{}
	extended := map[string]time.Time{}
	var lines []Line
	for _, e := range events {
		switch e.Event {
		case "up":
			ups[e.ID] = e
		case "extend":
			if e.Deadline.After(extended[e.ID]) {
				extended[e.ID] = e.Deadline
			}
		case "down":
			closed[e.ID] = true
			if e.Time.Before(since) {
				continue
			}
			start := e.Time.Add(-time.Duration(e.LifetimeSecs) * time.Second)
			if up, ok := ups[e.ID]; ok {
				start = up.Time
			}
			lines = append(lines, Line{
				Name: e.Name, Provider: e.Provider, Type: e.Type, StartedAt: start,
				Hours: float64(e.LifetimeSecs) / 3600, Cost: e.Cost, Closed: true,
			})
		}
	}
	for id, up := range ups {
		if closed[id] {
			continue
		}
		end := now
		if ttl, err := time.ParseDuration(up.TTL); err == nil {
			deadline := up.Time.Add(ttl)
			if ext := extended[id]; ext.After(deadline) {
				deadline = ext
			}
			if deadline.Before(now) {
				end = deadline
			}
		}
		if end.Before(since) {
			continue
		}
		hours := end.Sub(up.Time).Hours()
		lines = append(lines, Line{
			Name: up.Name, Provider: up.Provider, Type: up.Type, StartedAt: up.Time,
			Hours: hours, Cost: hours * up.PricePerHour, Estimated: true,
		})
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].StartedAt.Before(lines[j].StartedAt) })
	var total float64
	for _, l := range lines {
		total += l.Cost
	}
	return lines, total
}
