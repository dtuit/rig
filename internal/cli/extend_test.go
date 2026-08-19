package cli

import (
	"testing"
	"time"
)

func TestParseExtendArgs(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		by        string
		byChanged bool
		wantKey   string
		wantDur   time.Duration
		wantErr   bool
	}{
		{"name and duration positional", []string{"grid422", "8h"}, "6h", false, "grid422", 8 * time.Hour, false},
		{"duration only positional", []string{"8h"}, "6h", false, "", 8 * time.Hour, false},
		{"name only falls back to --by", []string{"grid422"}, "6h", false, "grid422", 6 * time.Hour, false},
		{"no args uses --by default", nil, "6h", false, "", 6 * time.Hour, false},
		{"explicit --by with name", []string{"grid422"}, "3h", true, "grid422", 3 * time.Hour, false},
		{"duration twice is an error", []string{"grid422", "8h"}, "3h", true, "", 0, true},
		{"garbage duration", []string{"grid422", "8hh"}, "6h", false, "", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key, d, err := parseExtendArgs(c.args, c.by, c.byChanged)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got key=%q d=%v", key, d)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if key != c.wantKey || d != c.wantDur {
				t.Errorf("got (%q, %v), want (%q, %v)", key, d, c.wantKey, c.wantDur)
			}
		})
	}
}
