// Package routing computes the drawn track of every trip leg once, on the
// server, and stores it. The website used to ask OSRM's and Transitous'
// public demo servers from every visitor's browser on every view; now the
// public trip payload carries transportIn.geometry and the browser routes
// nothing (Home-Page docs/concepts/self-hosted-maps.md §6).
//
// A leg is identified by Key(mode, points): the same leg typed again — or a
// trip saved again, which re-creates every stop — finds its stored geometry.
package routing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
)

const (
	StatusOK     = "ok"
	StatusFailed = "failed"
)

// Point is a WGS84 coordinate.
type Point struct {
	Lat float64
	Lng float64
}

// Leg is one routable transport leg: what Key hashes, and what the worker
// needs to compute it.
type Leg struct {
	Key    string
	Mode   string
	Points []Point
}

// Routed reports whether a mode gets a computed track at all. Flights are
// drawn straight, and a stop without a transport mode has no leg payload to
// carry geometry in.
func Routed(mode string) bool {
	return mode != "" && mode != "flight"
}

// Key identifies a leg by everything its route depends on. Coordinates are
// rounded to 1e-6° (~0.1 m), so float noise from a JSON round trip does not
// produce a new key.
func Key(mode string, points []Point) string {
	var b strings.Builder
	b.WriteString(mode)
	for _, p := range points {
		fmt.Fprintf(&b, "|%.6f,%.6f", p.Lat, p.Lng)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:16])
}

// LegFor builds the leg *into* stop from the previous stop, threading the
// manual via-points in order — the same path travel.js used to route:
// A → via-points → B. Via-points at 0,0 are skipped, as the browser did.
func LegFor(prev, stop models.TripStop) (Leg, bool) {
	mode := strings.TrimSpace(stop.TransportMode)
	if !Routed(mode) {
		return Leg{}, false
	}
	pts := []Point{{prev.Lat, prev.Lng}}
	for _, w := range stop.TransportWaypoints {
		if w.Lat == 0 && w.Lng == 0 {
			continue
		}
		pts = append(pts, Point{w.Lat, w.Lng})
	}
	pts = append(pts, Point{stop.Lat, stop.Lng})
	return Leg{Key: Key(mode, pts), Mode: mode, Points: pts}, true
}

// LegsOf returns the routable legs of an ordered stop list, keyed by the
// destination stop's position.
func LegsOf(stops []models.TripStop) map[int]Leg {
	legs := map[int]Leg{}
	for i := 1; i < len(stops); i++ {
		if leg, ok := LegFor(stops[i-1], stops[i]); ok {
			legs[i] = leg
		}
	}
	return legs
}

// Lookup returns the stored "ok" geometries for the given keys.
func Lookup(keys []string) map[string][][2]float64 {
	out := map[string][][2]float64{}
	if len(keys) == 0 {
		return out
	}
	var rows []models.RouteGeometry
	database.Db.Where("key IN ? AND status = ?", keys, StatusOK).Find(&rows)
	for _, r := range rows {
		out[r.Key] = r.Points
	}
	return out
}

// Statuses returns the stored row (any status) for each key that has one.
func Statuses(keys []string) map[string]models.RouteGeometry {
	out := map[string]models.RouteGeometry{}
	if len(keys) == 0 {
		return out
	}
	var rows []models.RouteGeometry
	database.Db.Select("key", "mode", "status", "source", "error", "fetched_at").
		Where("key IN ?", keys).Find(&rows)
	for _, r := range rows {
		out[r.Key] = r
	}
	return out
}

// ---- the worker ----

// Worker computes queued legs one at a time and never sends more than one
// request per MinInterval to the routing services — the OSRM demo server's
// limit is one per second.
type Worker struct {
	Router      *Router
	MinInterval time.Duration

	mu      sync.Mutex
	queued  map[string]bool
	queue   chan job
	started bool

	lastMu      sync.Mutex
	lastRequest time.Time
}

type job struct {
	leg   Leg
	force bool
}

// Default is the process-wide worker started from main.
var Default = &Worker{MinInterval: time.Second}

// Start launches the worker goroutine. Safe to call once.
func (w *Worker) Start() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return
	}
	if w.Router == nil {
		w.Router = NewRouter(config.Data.Routing)
	}
	w.queued = map[string]bool{}
	w.queue = make(chan job, 4096)
	w.started = true
	go w.run()
}

// Enqueue schedules legs whose geometry is missing (or, with force, every
// given leg). Legs already waiting are not queued twice.
func (w *Worker) Enqueue(legs []Leg, force bool) int {
	if len(legs) == 0 {
		return 0
	}
	keys := make([]string, 0, len(legs))
	for _, l := range legs {
		keys = append(keys, l.Key)
	}
	have := Statuses(keys)

	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return 0
	}
	n := 0
	for _, l := range legs {
		if w.queued[l.Key] {
			continue
		}
		if row, ok := have[l.Key]; ok && row.Status == StatusOK && !force {
			continue
		}
		select {
		case w.queue <- job{leg: l, force: force}:
			w.queued[l.Key] = true
			n++
		default:
			log.Printf("routing: queue full, dropping leg %s", l.Key)
		}
	}
	return n
}

// Pending is the number of legs waiting or in progress.
func (w *Worker) Pending() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.queued)
}

func (w *Worker) run() {
	for j := range w.queue {
		w.compute(j)
		w.mu.Lock()
		delete(w.queued, j.leg.Key)
		w.mu.Unlock()
	}
}

func (w *Worker) compute(j job) {
	if !j.force {
		var existing models.RouteGeometry
		if database.Db.Where("key = ? AND status = ?", j.leg.Key, StatusOK).Limit(1).Find(&existing).RowsAffected > 0 {
			return
		}
	}
	pts, source, err := w.Router.Route(j.leg.Mode, j.leg.Points, w.wait)
	row := models.RouteGeometry{Key: j.leg.Key, Mode: j.leg.Mode, FetchedAt: time.Now().UTC()}
	if err != nil {
		row.Status, row.Error = StatusFailed, err.Error()
		log.Printf("routing: leg %s (%s) failed: %v", j.leg.Key, j.leg.Mode, err)
	} else {
		row.Status, row.Source, row.Points = StatusOK, source, pts
	}
	if err := database.Db.Save(&row).Error; err != nil {
		log.Printf("routing: saving leg %s: %v", j.leg.Key, err)
	}
}

// wait blocks until MinInterval has passed since the previous request.
func (w *Worker) wait() {
	w.lastMu.Lock()
	defer w.lastMu.Unlock()
	if d := time.Until(w.lastRequest.Add(w.MinInterval)); d > 0 {
		time.Sleep(d)
	}
	w.lastRequest = time.Now()
}

// ---- trip-level helpers ----

// EnqueueTrip schedules every routable leg of one trip that has no geometry.
func EnqueueTrip(tripID uint) int {
	var stops []models.TripStop
	database.Db.Where("trip_id = ?", tripID).Order("position ASC").Find(&stops)
	return Default.Enqueue(legSlice(LegsOf(stops)), false)
}

// EnqueueAll schedules the legs of every trip: the missing ones, and the
// failed ones again. With force, every leg is recomputed.
func EnqueueAll(force bool) int {
	var trips []models.Trip
	database.Db.Select("id").Find(&trips)
	var legs []Leg
	for _, t := range trips {
		var stops []models.TripStop
		database.Db.Where("trip_id = ?", t.ID).Order("position ASC").Find(&stops)
		legs = append(legs, legSlice(LegsOf(stops))...)
	}
	return Default.Enqueue(legs, force)
}

// Recompute forces one leg, found by key among the current trips.
func Recompute(key string) bool {
	var trips []models.Trip
	database.Db.Select("id").Find(&trips)
	for _, t := range trips {
		var stops []models.TripStop
		database.Db.Where("trip_id = ?", t.ID).Order("position ASC").Find(&stops)
		for _, leg := range LegsOf(stops) {
			if leg.Key == key {
				return Default.Enqueue([]Leg{leg}, true) > 0 || Default.isQueued(key)
			}
		}
	}
	return false
}

func (w *Worker) isQueued(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.queued[key]
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
