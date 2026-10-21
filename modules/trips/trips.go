// Package trips is the trips module: trips with their stops and photos, and
// the computed track of every leg. The website reads them under the old
// /api/trips/ URLs; the track reaches it inside the trip payload
// (transportIn.geometry), so browsers route nothing.
//
// Legs are routed through the routing slot (geo.routing/v1), one at a time.
// Without a router the trips work as before, drawn straight.
package trips

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/geo"
	"github.com/LNA-DEV/HomePageCompanion/capability/media"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"gorm.io/gorm"
)

func init() {
	companion.Register(companion.Info{
		ID:   "trips",
		Name: "Trips",
		New:  func() companion.Instance { return new(Trips) },
		Slots: []companion.Slot{
			{Name: "routing", Needs: geo.RoutingV1, Optional: true},
			{Name: "media", Needs: media.ImageV1, Optional: true},
		},
		Migrate: migrate,
		Routes:  routes,
		Aliases: []string{"/api/trips"},
		Import:  importLegacy,
		UI: &companion.UI{
			Nav:   []companion.NavEntry{{Label: "Trips", Icon: "map", Page: "", Group: "Content", Order: 35}},
			Pages: []companion.Page{{Path: "", Element: "trips-page", Title: "Trips"}},
		},
	})
}

func migrate(mc companion.ModuleContext) error {
	if mc.DB() == nil {
		return errors.New("trips: no database")
	}
	return mc.DB().AutoMigrate(&Trip{}, &TripStop{}, &TripPhoto{}, &RouteGeometry{})
}

// Trips is the module's one instance.
type Trips struct {
	Routing geo.Router  `companion:"slot=routing"`
	Media   media.Image `companion:"slot=media"`

	ctx companion.Context

	mu      sync.Mutex
	queued  map[string]bool
	queue   chan job
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
}

type job struct {
	leg   Leg
	force bool
}

func (t *Trips) Provision(ctx companion.Context) error {
	t.ctx = ctx
	return migrate(ctx)
}

func (t *Trips) db() *gorm.DB { return t.ctx.DB() }

// Start launches the routing worker and queues the legs saved without
// geometry and the failed ones.
func (t *Trips) Start() error {
	if t.Routing == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.queued = map[string]bool{}
	t.queue = make(chan job, 4096)
	t.cancel = cancel
	t.done = make(chan struct{})
	t.started = true
	t.mu.Unlock()
	go t.run(ctx)
	t.EnqueueAll(false)
	return nil
}

func (t *Trips) Stop(ctx context.Context) error {
	t.mu.Lock()
	if !t.started {
		t.mu.Unlock()
		return nil
	}
	t.started = false
	t.cancel()
	done := t.done
	t.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Jobs: a nightly backfill retries the failed legs.
func (t *Trips) Jobs() []companion.Job {
	return []companion.Job{{Name: "routes", Schedule: "0 17 4 * * *", Timeout: time.Minute, Run: func(context.Context) error {
		t.EnqueueAll(false)
		return nil
	}}}
}

// Lookup returns the stored "ok" geometries for the given keys.
func Lookup(db *gorm.DB, keys []string) map[string][][2]float64 {
	out := map[string][][2]float64{}
	if len(keys) == 0 {
		return out
	}
	var rows []RouteGeometry
	db.Where("key IN ? AND status = ?", keys, StatusOK).Find(&rows)
	for _, r := range rows {
		out[r.Key] = r.Points
	}
	return out
}

// Statuses returns the stored row (any status) for each key that has one.
func Statuses(db *gorm.DB, keys []string) map[string]RouteGeometry {
	out := map[string]RouteGeometry{}
	if len(keys) == 0 {
		return out
	}
	var rows []RouteGeometry
	db.Select("key", "mode", "status", "source", "error", "fetched_at").Where("key IN ?", keys).Find(&rows)
	for _, r := range rows {
		out[r.Key] = r
	}
	return out
}

// Enqueue schedules legs whose geometry is missing (or, with force, every
// given leg). Legs already waiting are not queued twice. Without a router
// nothing is queued.
func (t *Trips) Enqueue(legs []Leg, force bool) int {
	if len(legs) == 0 {
		return 0
	}
	keys := make([]string, 0, len(legs))
	for _, l := range legs {
		keys = append(keys, l.Key)
	}
	have := Statuses(t.db(), keys)

	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		return 0
	}
	n := 0
	for _, l := range legs {
		if t.queued[l.Key] {
			continue
		}
		if row, ok := have[l.Key]; ok && row.Status == StatusOK && !force {
			continue
		}
		select {
		case t.queue <- job{leg: l, force: force}:
			t.queued[l.Key] = true
			n++
		default:
			t.ctx.Log().Printf("routing queue full, dropping leg %s", l.Key)
		}
	}
	return n
}

// Pending is the number of legs waiting or in progress.
func (t *Trips) Pending() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.queued)
}

func (t *Trips) isQueued(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.queued[key]
}

func (t *Trips) run(ctx context.Context) {
	defer close(t.done)
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-t.queue:
			t.compute(ctx, j)
			t.mu.Lock()
			delete(t.queued, j.leg.Key)
			t.mu.Unlock()
		}
	}
}

func (t *Trips) compute(ctx context.Context, j job) {
	if !j.force {
		var existing RouteGeometry
		if t.db().Where("key = ? AND status = ?", j.leg.Key, StatusOK).Limit(1).Find(&existing).RowsAffected > 0 {
			return
		}
	}
	route, err := t.Routing.Route(ctx, j.leg.Mode, j.leg.Points)
	if ctx.Err() != nil {
		return // stopping; the next start queues the leg again
	}
	row := RouteGeometry{Key: j.leg.Key, Mode: j.leg.Mode, FetchedAt: time.Now().UTC()}
	if err != nil {
		row.Status, row.Error = StatusFailed, err.Error()
		t.ctx.Log().Printf("routing: leg %s (%s) failed: %v", j.leg.Key, j.leg.Mode, err)
	} else {
		row.Status, row.Source, row.Points = StatusOK, route.Source, route.Points
	}
	if err := t.db().Save(&row).Error; err != nil {
		t.ctx.Log().Printf("routing: saving leg %s: %v", j.leg.Key, err)
	}
}

func (t *Trips) stopsOf(tripID uint) []TripStop {
	var stops []TripStop
	t.db().Where("trip_id = ?", tripID).Order("position ASC").Find(&stops)
	return stops
}

// EnqueueTrip schedules every routable leg of one trip without geometry.
func (t *Trips) EnqueueTrip(tripID uint) int {
	return t.Enqueue(legSlice(LegsOf(t.stopsOf(tripID))), false)
}

// EnqueueAll schedules the legs of every trip: the missing ones, and the
// failed ones again. With force, every leg is recomputed.
func (t *Trips) EnqueueAll(force bool) int {
	var trips []Trip
	t.db().Select("id").Find(&trips)
	var legs []Leg
	for _, tr := range trips {
		legs = append(legs, legSlice(LegsOf(t.stopsOf(tr.ID)))...)
	}
	return t.Enqueue(legs, force)
}

// Recompute forces one leg, found by key among the current trips.
func (t *Trips) Recompute(key string) bool {
	var trips []Trip
	t.db().Select("id").Find(&trips)
	for _, tr := range trips {
		for _, leg := range LegsOf(t.stopsOf(tr.ID)) {
			if leg.Key == key {
				return t.Enqueue([]Leg{leg}, true) > 0 || t.isQueued(key)
			}
		}
	}
	return false
}
