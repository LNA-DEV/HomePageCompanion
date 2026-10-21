package routing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

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
}

func TestRailQueryTimeIsATomorrowMorning(t *testing.T) {
	if got := railQueryTime(time.Date(2026, 10, 6, 23, 30, 0, 0, time.UTC)); got != "2026-10-07T07:00:00Z" {
		t.Errorf("got %s", got)
	}
}

func TestServiceKeepsTheInterval(t *testing.T) {
	f := newFakeServices(t)
	s := &Service{router: f.router(), MinInterval: companionDuration(40 * time.Millisecond)}
	start := time.Now()
	for i := 0; i < 2; i++ {
		if _, err := s.Route(t.Context(), "car", []geoPoint{{Lat: 48, Lng: 11}, {Lat: 49, Lng: 12}}); err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Error("the second request did not wait")
	}
	if _, err := s.Route(t.Context(), "flight", []geoPoint{{Lat: 48, Lng: 11}, {Lat: 49, Lng: 12}}); err == nil {
		t.Error("a flight is routed")
	}
}
