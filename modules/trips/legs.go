package trips

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/capability/geo"
)

const (
	StatusOK     = "ok"
	StatusFailed = "failed"
)

// Leg is one routable transport leg: what Key hashes, and what the router
// is asked.
type Leg struct {
	Key    string
	Mode   string
	Points []geo.Point
}

// Routed reports whether a mode gets a computed track at all. Flights are
// drawn straight, and a stop without a transport mode has no leg.
func Routed(mode string) bool {
	return mode != "" && mode != "flight"
}

// Key identifies a leg by everything its route depends on. Coordinates are
// rounded to 1e-6° (~0.1 m), so float noise from a JSON round trip does not
// produce a new key. The format is the old companion's, so imported
// geometry keeps matching.
func Key(mode string, points []geo.Point) string {
	var b strings.Builder
	b.WriteString(mode)
	for _, p := range points {
		fmt.Fprintf(&b, "|%.6f,%.6f", p.Lat, p.Lng)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:16])
}

// LegFor builds the leg into stop from the previous stop, through the
// via-points in order: A → via-points → B. Via-points at 0,0 are skipped.
func LegFor(prev, stop TripStop) (Leg, bool) {
	mode := strings.TrimSpace(stop.TransportMode)
	if !Routed(mode) {
		return Leg{}, false
	}
	pts := []geo.Point{{Lat: prev.Lat, Lng: prev.Lng}}
	for _, w := range stop.TransportWaypoints {
		if w.Lat == 0 && w.Lng == 0 {
			continue
		}
		pts = append(pts, geo.Point{Lat: w.Lat, Lng: w.Lng})
	}
	pts = append(pts, geo.Point{Lat: stop.Lat, Lng: stop.Lng})
	return Leg{Key: Key(mode, pts), Mode: mode, Points: pts}, true
}

// LegsOf returns the routable legs of an ordered stop list, keyed by the
// destination stop's position.
func LegsOf(stops []TripStop) map[int]Leg {
	legs := map[int]Leg{}
	for i := 1; i < len(stops); i++ {
		if leg, ok := LegFor(stops[i-1], stops[i]); ok {
			legs[i] = leg
		}
	}
	return legs
}

// legSlice orders the legs by stop position.
func legSlice(m map[int]Leg) []Leg {
	idx := make([]int, 0, len(m))
	for i := range m {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	out := make([]Leg, 0, len(m))
	for _, i := range idx {
		out = append(out, m[i])
	}
	return out
}

func legKeys(legs map[int]Leg) []string {
	keys := make([]string, 0, len(legs))
	for _, l := range legs {
		keys = append(keys, l.Key)
	}
	return keys
}
