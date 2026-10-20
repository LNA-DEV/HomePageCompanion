package main

import (
	"testing"
	"time"

	"github.com/robfig/cron"
)

// Every fixed schedule parses, and runs as often as intended. The inventory
// once ran every minute instead of every hour (autoposting-ui.md §3 #2).
func TestFixedSchedules(t *testing.T) {
	want := map[string]time.Duration{
		"config reload":      5 * time.Minute,
		"inventory":          time.Hour,
		"interactions":       time.Minute,
		"basemap cleanup":    24 * time.Hour,
		"gbif eviction":      24 * time.Hour,
		"meta token refresh": 24 * time.Hour,
	}
	start := time.Date(2026, 10, 20, 12, 0, 30, 0, time.UTC)
	for _, s := range fixedSchedules {
		sched, err := cron.Parse(s.spec)
		if err != nil {
			t.Errorf("%s: %q does not parse: %v", s.name, s.spec, err)
			continue
		}
		first := sched.Next(start)
		gap := sched.Next(first).Sub(first)
		if gap != want[s.name] {
			t.Errorf("%s: %q runs every %v, want %v", s.name, s.spec, gap, want[s.name])
		}
		delete(want, s.name)
	}
	for name := range want {
		t.Errorf("%s: no schedule", name)
	}
}
