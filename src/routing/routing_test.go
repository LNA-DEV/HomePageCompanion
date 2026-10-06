package routing

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Trip{}, &models.TripStop{}, &models.RouteGeometry{}); err != nil {
		t.Fatal(err)
	}
	database.Db = db
}

func TestKeyIsStableAndSensitive(t *testing.T) {
	a := []Point{{48.137, 11.575}, {47.8, 12.1}}
	if Key("car", a) != Key("car", []Point{{48.1370000001, 11.5750000002}, {47.8, 12.1}}) {
		t.Error("float noise changed the key")
	}
	for name, other := range map[string]string{
		"mode":  Key("train", a),
		"point": Key("car", []Point{{48.137, 11.575}, {47.81, 12.1}}),
		"order": Key("car", []Point{a[1], a[0]}),
		"extra": Key("car", []Point{a[0], {48, 12}, a[1]}),
	} {
		if other == Key("car", a) {
			t.Errorf("changing the %s kept the key", name)
		}
	}
}

func TestLegFor(t *testing.T) {
	prev := models.TripStop{Lat: 48.1, Lng: 11.5}
	stop := models.TripStop{Lat: 55.6, Lng: 13.0, TransportMode: "train",
		TransportWaypoints: []models.Waypoint{{Lat: 0, Lng: 0}, {Lat: 53.5, Lng: 10.0}}}
	leg, ok := LegFor(prev, stop)
	if !ok || leg.Mode != "train" || len(leg.Points) != 3 || leg.Points[1] != (Point{53.5, 10.0}) {
		t.Fatalf("leg = %+v", leg)
	}
	for _, mode := range []string{"flight", "", "  "} {
		stop.TransportMode = mode
		if _, ok := LegFor(prev, stop); ok {
			t.Errorf("mode %q is routed", mode)
		}
	}
}

func TestDecodePolyline(t *testing.T) {
	// Google's documented example, precision 5.
	got := DecodePolyline("_p~iF~ps|U_ulLnnqC_mqNvxq`@", 5)
	want := [][2]float64{{38.5, -120.2}, {40.7, -120.95}, {43.252, -126.453}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if fmt.Sprintf("%.5f", got[i][0]) != fmt.Sprintf("%.5f", want[i][0]) ||
			fmt.Sprintf("%.5f", got[i][1]) != fmt.Sprintf("%.5f", want[i][1]) {
			t.Errorf("point %d: %v, want %v", i, got[i], want[i])
		}
	}
	if len(DecodePolyline("_p~iF~ps|U_ul", 5)) != 1 {
		t.Error("a truncated polyline should decode its complete points only")
	}
}

func TestSimplifyKeepsEndsAndCorners(t *testing.T) {
	var line [][2]float64
	for i := 0; i <= 100; i++ {
		line = append(line, [2]float64{0, float64(i) * 0.001}) // straight
	}
	line = append(line, [2]float64{0.05, 0.1}) // a corner
	s := Simplify(line, 0.00005)
	if len(s) != 3 || s[0] != line[0] || s[len(s)-1] != line[len(line)-1] {
		t.Fatalf("simplified to %v", s)
	}
}

// fakeServices answers like OSRM and Transitous and counts the requests.
type fakeServices struct {
	osrm, transitous atomic.Int64
	railFails        bool
	srv              *httptest.Server
	userAgents       []string
}

func newFakeServices(t *testing.T) *fakeServices {
	f := &fakeServices{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.userAgents = append(f.userAgents, r.UserAgent())
		switch {
		case strings.HasPrefix(r.URL.Path, "/osrm/driving/"):
			f.osrm.Add(1)
			var coords [][2]float64
			for _, pair := range strings.Split(strings.TrimPrefix(r.URL.Path, "/osrm/driving/"), ";") {
				var lng, lat float64
				fmt.Sscanf(pair, "%f,%f", &lng, &lat)
				coords = append(coords, [2]float64{lng, lat}, [2]float64{lng + 0.01, lat + 0.02})
			}
			json.NewEncoder(w).Encode(map[string]any{"code": "Ok", "routes": []any{
				map[string]any{"geometry": map[string]any{"coordinates": coords}},
			}})
		case r.URL.Path == "/plan":
			f.transitous.Add(1)
			if f.railFails {
				json.NewEncoder(w).Encode(map[string]any{"itineraries": []any{}})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"itineraries": []any{map[string]any{"legs": []any{
				map[string]any{"legGeometry": map[string]any{"points": "_p~iF~ps|U_ulLnnqC", "precision": 5}},
			}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServices) router() *Router {
	return &Router{OSRMURL: f.srv.URL + "/osrm", TransitousURL: f.srv.URL + "/plan", Client: f.srv.Client(),
		Now: func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }}
}

func TestRouteByMode(t *testing.T) {
	f := newFakeServices(t)
	r := f.router()
	pts := []Point{{48, 11}, {49, 12}, {50, 13}}

	if _, src, err := r.Route("car", pts, nil); err != nil || src != "osrm" || f.osrm.Load() != 1 {
		t.Fatalf("car: %s %v, osrm calls %d", src, err, f.osrm.Load())
	}
	if _, src, err := r.Route("train", pts, nil); err != nil || src != "transitous" || f.transitous.Load() != 2 {
		t.Fatalf("train: %s %v, transitous calls %d (one per segment)", src, err, f.transitous.Load())
	}
	f.railFails = true
	if _, src, err := r.Route("train", pts, nil); err != nil || src != "osrm" {
		t.Fatalf("train without rail should fall back to the road: %s %v", src, err)
	}
	for _, ua := range f.userAgents {
		if !strings.HasPrefix(ua, "HomePageCompanion/") {
			t.Errorf("User-Agent %q", ua)
		}
	}
}

func TestRailQueryTimeIsATomorrowMorning(t *testing.T) {
	if got := railQueryTime(time.Date(2026, 10, 6, 23, 30, 0, 0, time.UTC)); got != "2026-10-07T07:00:00Z" {
		t.Errorf("got %s", got)
	}
}

func TestWorkerComputesStoresAndRateLimits(t *testing.T) {
	setupDB(t)
	f := newFakeServices(t)
	w := &Worker{Router: f.router(), MinInterval: 30 * time.Millisecond}
	w.Start()

	trip := models.Trip{Slug: "t"}
	database.Db.Create(&trip)
	stops := []models.TripStop{
		{TripID: trip.ID, Position: 0, Lat: 48, Lng: 11},
		{TripID: trip.ID, Position: 1, Lat: 49, Lng: 12, TransportMode: "car"},
		{TripID: trip.ID, Position: 2, Lat: 50, Lng: 13, TransportMode: "flight"},
		{TripID: trip.ID, Position: 3, Lat: 51, Lng: 14, TransportMode: "train"},
	}
	database.Db.Create(&stops)
	legs := legSlice(LegsOf(stops))
	if len(legs) != 2 {
		t.Fatalf("%d routable legs, want 2 (the flight is drawn straight)", len(legs))
	}

	start := time.Now()
	if n := w.Enqueue(legs, false); n != 2 {
		t.Fatalf("queued %d", n)
	}
	waitFor(t, func() bool { return w.Pending() == 0 })
	if time.Since(start) < 30*time.Millisecond {
		t.Error("the second request did not wait for the interval")
	}
	got := Lookup([]string{legs[0].Key, legs[1].Key})
	if len(got) != 2 || len(got[legs[0].Key]) < 2 {
		t.Fatalf("stored geometry: %v", got)
	}
	for _, p := range got[legs[0].Key] {
		if math.Abs(p[0]*1e5-math.Round(p[0]*1e5)) > 1e-6 || math.Abs(p[1]*1e5-math.Round(p[1]*1e5)) > 1e-6 {
			t.Errorf("point %v not rounded to 5 decimals", p)
			break
		}
	}

	// Already computed: nothing is queued again, unless forced.
	calls := f.osrm.Load() + f.transitous.Load()
	if n := w.Enqueue(legs, false); n != 0 {
		t.Errorf("re-queued %d computed legs", n)
	}
	if n := w.Enqueue(legs[:1], true); n != 1 {
		t.Errorf("forced: queued %d", n)
	}
	waitFor(t, func() bool { return w.Pending() == 0 })
	if f.osrm.Load()+f.transitous.Load() != calls+1 {
		t.Error("forced recompute did not ask again")
	}
}

func TestWorkerRecordsFailuresAndBackfillRetriesThem(t *testing.T) {
	setupDB(t)
	f := newFakeServices(t)
	broken := &Router{OSRMURL: f.srv.URL + "/nope", TransitousURL: f.srv.URL + "/nope", Client: f.srv.Client(), Now: time.Now}
	w := &Worker{Router: broken, MinInterval: time.Millisecond}
	w.Start()
	leg, _ := LegFor(models.TripStop{Lat: 1, Lng: 1}, models.TripStop{Lat: 2, Lng: 2, TransportMode: "car"})
	w.Enqueue([]Leg{leg}, false)
	waitFor(t, func() bool { return w.Pending() == 0 })
	if st := Statuses([]string{leg.Key})[leg.Key]; st.Status != StatusFailed || st.Error == "" {
		t.Fatalf("status %+v", st)
	}
	if len(Lookup([]string{leg.Key})) != 0 {
		t.Error("a failed leg has public geometry")
	}
	w.Router = f.router()
	if n := w.Enqueue([]Leg{leg}, false); n != 1 {
		t.Fatal("a failed leg is not retried")
	}
	waitFor(t, func() bool { return w.Pending() == 0 })
	if Statuses([]string{leg.Key})[leg.Key].Status != StatusOK {
		t.Error("retry did not succeed")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
