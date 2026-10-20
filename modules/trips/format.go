package trips

import (
	"strings"
	"time"
)

// formatStopDates turns the stored YYYY-MM-DD start/end into the range the
// website renders:
//   - "Jun 3–4"        same month, two days
//   - "Jun 30 – Jul 2" spans months
//   - "Jun 11–"        open-ended (no end date yet, e.g. the current stop)
//   - "Jun 3"          single day, or end == start
//
// Unparseable input falls back to the raw start.
func formatStopDates(start, end string) string {
	start = strings.TrimSpace(start)
	end = strings.TrimSpace(end)
	if start == "" {
		return ""
	}
	s, err := time.Parse("2006-01-02", start)
	if err != nil {
		return start
	}
	startStr := s.Format("Jan 2")
	if end == "" {
		return startStr + "–"
	}
	e, err := time.Parse("2006-01-02", end)
	if err != nil {
		return startStr
	}
	if s.Equal(e) {
		return startStr
	}
	if s.Year() == e.Year() && s.Month() == e.Month() {
		return startStr + "–" + e.Format("2")
	}
	return startStr + " – " + e.Format("Jan 2")
}

// computeStats derives the header stats from the stops. daysTotal is the one
// manual stat; cities is the stop count; countries the distinct union of the
// stops' and legs' countries; distanceKm the sum of the legs (nil if none
// entered); daysElapsed counts from the earliest start to today, floored at
// 0 and capped at daysTotal when that is set.
func computeStats(stops []TripStop, daysTotal *int, now time.Time) publicStats {
	stats := publicStats{Cities: len(stops), DaysTotal: daysTotal}

	countries := map[string]bool{}
	distance := 0
	hasDistance := false
	var firstStart time.Time
	hasStart := false

	for _, s := range stops {
		if c := strings.TrimSpace(s.Country); c != "" {
			countries[c] = true
		}
		for _, c := range s.TransportCountries {
			if c = strings.TrimSpace(c); c != "" {
				countries[c] = true
			}
		}
		if s.DistanceKm != nil {
			distance += *s.DistanceKm
			hasDistance = true
		}
		if d, err := time.Parse("2006-01-02", strings.TrimSpace(s.StartDate)); err == nil {
			if !hasStart || d.Before(firstStart) {
				firstStart = d
			}
			hasStart = true
		}
	}

	stats.Countries = len(countries)
	if hasDistance {
		stats.DistanceKm = &distance
	}
	if hasStart {
		now = now.UTC()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		elapsed := int(today.Sub(firstStart).Hours()/24) + 1
		if elapsed < 0 {
			elapsed = 0
		}
		if daysTotal != nil && elapsed > *daysTotal {
			elapsed = *daysTotal
		}
		stats.DaysElapsed = &elapsed
	}
	return stats
}

// tripDateRange formats the trip's span, first start to last end (the last
// start when open-ended): "Jun 3–4, 2026" or "Jun 3 – Jul 2, 2026"; "" when
// no dates are set.
func tripDateRange(stops []TripStop) string {
	if len(stops) == 0 {
		return ""
	}
	s, err := time.Parse("2006-01-02", strings.TrimSpace(stops[0].StartDate))
	if err != nil {
		return ""
	}
	last := stops[len(stops)-1]
	endStr := strings.TrimSpace(last.EndDate)
	if endStr == "" {
		endStr = strings.TrimSpace(last.StartDate)
	}
	e, err := time.Parse("2006-01-02", endStr)
	if err != nil || e.Before(s) || s.Equal(e) {
		return s.Format("Jan 2, 2006")
	}
	if s.Year() == e.Year() {
		if s.Month() == e.Month() {
			return s.Format("Jan 2") + "–" + e.Format("2, 2006")
		}
		return s.Format("Jan 2") + " – " + e.Format("Jan 2, 2006")
	}
	return s.Format("Jan 2, 2006") + " – " + e.Format("Jan 2, 2006")
}

// cleanCountries trims and drops blanks; never nil, so JSON says [] rather
// than null.
func cleanCountries(in []string) []string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// cleanWaypoints drops (0,0), which means unset; never nil.
func cleanWaypoints(in []Waypoint) []Waypoint {
	out := make([]Waypoint, 0, len(in))
	for _, w := range in {
		if w.Lat == 0 && w.Lng == 0 {
			continue
		}
		out = append(out, w)
	}
	return out
}
