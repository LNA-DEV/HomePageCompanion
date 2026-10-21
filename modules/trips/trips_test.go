package trips_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/geo"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/modules/trips"
)

const routed = `
modules:
  fake.router: { instances: { main: {} } }
  trips:
    config: { routing: fake.router:main }
`

const unrouted = `
modules:
  trips: {}
`

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
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

func TestKeyIsStableAndSensitive(t *testing.T) {
	a := []geo.Point{{Lat: 48.137, Lng: 11.575}, {Lat: 47.8, Lng: 12.1}}
	if trips.Key("car", a) != trips.Key("car", []geo.Point{{Lat: 48.1370000001, Lng: 11.5750000002}, {Lat: 47.8, Lng: 12.1}}) {
		t.Error("float noise changed the key")
	}
	for name, other := range map[string]string{
		"mode":  trips.Key("train", a),
		"point": trips.Key("car", []geo.Point{a[0], {Lat: 47.81, Lng: 12.1}}),
		"order": trips.Key("car", []geo.Point{a[1], a[0]}),
		"extra": trips.Key("car", []geo.Point{a[0], {Lat: 48, Lng: 12}, a[1]}),
	} {
		if other == trips.Key("car", a) {
			t.Errorf("changing the %s kept the key", name)
		}
	}
	// The old companion's key for this leg: imported geometry must match.
	old := trips.Key("car", []geo.Point{{Lat: 48.137, Lng: 11.575}, {Lat: 53.55, Lng: 10.0}, {Lat: 55.605, Lng: 13.0}})
	if old != "0cc75712e58d178ba964af9a39b89030" {
		t.Errorf("key %s differs from the old companion's", old)
	}
}

func TestLegFor(t *testing.T) {
	prev := trips.TripStop{Lat: 48.1, Lng: 11.5}
	stop := trips.TripStop{Lat: 55.6, Lng: 13.0, TransportMode: "train",
		TransportWaypoints: []trips.Waypoint{{Lat: 0, Lng: 0}, {Lat: 53.5, Lng: 10.0}}}
	leg, ok := trips.LegFor(prev, stop)
	if !ok || leg.Mode != "train" || len(leg.Points) != 3 || leg.Points[1] != (geo.Point{Lat: 53.5, Lng: 10.0}) {
		t.Fatalf("leg = %+v", leg)
	}
	for _, mode := range []string{"flight", "", "  "} {
		stop.TransportMode = mode
		if _, ok := trips.LegFor(prev, stop); ok {
			t.Errorf("mode %q is routed", mode)
		}
	}
}

func TestAuthRequiredForWrites(t *testing.T) {
	h := companiontest.New(t, routed)
	if w := h.Public("POST", "/api/b/trips/trips", map[string]any{"title": "X"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", w.Code)
	}
	if w := h.Public("POST", "/api/b/trips/upload", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("upload without auth: %d", w.Code)
	}
}

func TestCRUDAndPublicShape(t *testing.T) {
	h := companiontest.New(t, routed)

	w := h.Admin("POST", "/api/b/trips/trips", map[string]any{"title": "Europe, mostly by train"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	created := decode[trips.AdminTripView](t, w)
	if created.Slug != "europe-mostly-by-train" {
		t.Fatalf("expected slugified title, got %q", created.Slug)
	}
	if again := decode[trips.AdminTripView](t, h.Admin("POST", "/api/b/trips/trips", map[string]any{"title": "Europe, mostly by train"})); again.Slug != "europe-mostly-by-train-2" {
		t.Fatalf("second slug %q", again.Slug)
	}

	// Two stops: the first without a leg, the second with a train leg, a
	// distance and two countries. daysTotal is the one manual stat.
	payload := map[string]any{
		"slug": created.Slug, "title": created.Title, "published": true, "daysTotal": 14,
		"stops": []map[string]any{
			{
				"name": "London", "startDate": "2026-06-03", "endDate": "2026-06-04",
				"lat": 51.5074, "lng": -0.1278,
				"status": "visited", "note": "Kicked things off.", "country": "United Kingdom",
				"photos": []map[string]any{{"url": "/api/trips/media/a.jpg", "caption": "Borough Market"}},
			},
			{
				"name": "Paris", "startDate": "2026-06-04", "endDate": "",
				"lat": 48.8566, "lng": 2.3522,
				"status": "current", "note": "Pastries.", "country": "France",
				"transportMode": "train", "transportLabel": "Eurostar", "transportDuration": "2h20",
				"distanceKm":         492,
				"transportCountries": []string{"France", "Italy"},
				"transportWaypoints": []map[string]any{{"lat": 50.9, "lng": 1.8}, {"lat": 49.5, "lng": 2.1}},
				"photos":             []map[string]any{{"url": "/api/trips/media/b.jpg", "caption": "Montmartre"}},
				"transportPhotos":    []map[string]any{{"url": "/api/trips/media/c.jpg", "caption": "Boarding"}},
			},
		},
	}
	path := "/api/b/trips/trips/1"
	if w := h.Admin("PUT", path, payload); w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}

	got := decode[trips.AdminTripView](t, h.Admin("GET", path, nil))
	if len(got.Stops) != 2 || got.Stops[0].StopKey != "london" {
		t.Fatalf("stops %+v", got.Stops)
	}
	if got.Stops[0].StartDate != "2026-06-03" || got.Stops[0].EndDate != "2026-06-04" {
		t.Fatalf("dates not round-tripped: %q..%q", got.Stops[0].StartDate, got.Stops[0].EndDate)
	}
	if len(got.Stops[1].TransportPhotos) != 1 || got.Stops[1].TransportPhotos[0].Caption != "Boarding" {
		t.Fatalf("transport photos not round-tripped: %+v", got.Stops[1].TransportPhotos)
	}
	if got.Stops[1].DistanceKm == nil || *got.Stops[1].DistanceKm != 492 {
		t.Fatalf("leg distance not round-tripped: %+v", got.Stops[1].DistanceKm)
	}
	if len(got.Stops[1].TransportCountries) != 2 || got.Stops[1].TransportCountries[1] != "Italy" {
		t.Fatalf("transport countries not round-tripped: %+v", got.Stops[1].TransportCountries)
	}
	if len(got.Stops[1].TransportWaypoints) != 2 || got.Stops[1].TransportWaypoints[1].Lat != 49.5 {
		t.Fatalf("transport waypoints not round-tripped: %+v", got.Stops[1].TransportWaypoints)
	}
	if got.Stops[0].TransportWaypoints == nil {
		t.Fatal("first stop waypoints should be [] not null")
	}

	var list []struct {
		Slug  string
		Stats struct{ Cities, Countries int }
	}
	json.Unmarshal(h.Public("GET", "/api/trips", nil).Body.Bytes(), &list)
	if len(list) != 1 || list[0].Slug != created.Slug || list[0].Stats.Cities != 2 || list[0].Stats.Countries != 3 {
		t.Fatalf("public list wrong: %+v", list)
	}

	w = h.Public("GET", "/api/trips/"+created.Slug, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("public get: %d", w.Code)
	}
	var pub struct {
		Stats struct {
			DaysElapsed, DaysTotal, DistanceKm *int
			Cities, Countries                  int
		}
		Stops []struct {
			Dates       string
			Photos      []struct{ Src, Cap string }
			TransportIn *struct {
				Mode       string
				DistanceKm *int
				Countries  []string
				Waypoints  []trips.Waypoint
				Photos     []struct{ Src, Cap string }
			}
		}
	}
	json.Unmarshal(w.Body.Bytes(), &pub)
	// Distance is the per-leg sum; countries the union UK, France, Italy;
	// daysElapsed is capped at the manual total (the dates are past).
	s := pub.Stats
	if s.Cities != 2 || s.Countries != 3 || s.DistanceKm == nil || *s.DistanceKm != 492 ||
		s.DaysTotal == nil || *s.DaysTotal != 14 || s.DaysElapsed == nil || *s.DaysElapsed != 14 {
		t.Fatalf("stats %+v", s)
	}
	if pub.Stops[0].TransportIn != nil {
		t.Fatalf("first stop should have null transportIn, got %+v", pub.Stops[0].TransportIn)
	}
	in := pub.Stops[1].TransportIn
	if in == nil || in.Mode != "train" || in.DistanceKm == nil || *in.DistanceKm != 492 || len(in.Countries) != 2 ||
		len(in.Waypoints) != 2 || in.Waypoints[0].Lng != 1.8 || in.Photos[0].Src != "/api/trips/media/c.jpg" {
		t.Fatalf("transportIn %+v", in)
	}
	if pub.Stops[0].Photos[0].Cap != "Borough Market" || pub.Stops[0].Dates != "Jun 3–4" || pub.Stops[1].Dates != "Jun 4–" {
		t.Fatalf("stops %+v", pub.Stops)
	}

	// Another trip may not take the slug.
	if w := h.Admin("PUT", "/api/b/trips/trips/2", map[string]any{"slug": created.Slug, "title": "x"}); w.Code != http.StatusConflict {
		t.Fatalf("slug clash: %d", w.Code)
	}

	// Unpublished trips are not public.
	payload["published"] = false
	h.Admin("PUT", path, payload)
	if w := h.Public("GET", "/api/trips/"+created.Slug, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unpublished trip: %d", w.Code)
	}

	if w := h.Admin("DELETE", path, nil); w.Code != http.StatusOK {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := h.Admin("GET", path, nil); w.Code != http.StatusNotFound {
		t.Fatalf("after delete: %d", w.Code)
	}
}

func upload(t *testing.T, h *companiontest.Harness, name string, data []byte) string {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	req := httptest.NewRequest("POST", "/api/b/trips/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "ApiKey "+companiontest.AdminKey)
	w := httptest.NewRecorder()
	h.Core.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	var up struct {
		URL  string `json:"url"`
		Size int    `json:"size"`
	}
	json.Unmarshal(w.Body.Bytes(), &up)
	if up.Size == 0 {
		t.Fatalf("upload response %+v", up)
	}
	return up.URL
}

func TestUploadKeepsUndecodableFilesAndGuardsTheMediaRoute(t *testing.T) {
	h := companiontest.New(t, unrouted)
	url := upload(t, h, "pic.png", []byte("not-a-real-png-but-bytes"))
	if filepath.Ext(url) != ".png" {
		t.Fatalf("url %q", url)
	}
	if w := h.Public("GET", url, nil); w.Code != http.StatusOK || w.Body.String() != "not-a-real-png-but-bytes" {
		t.Fatalf("media: %d %q", w.Code, w.Body)
	}
	for _, p := range []string{"/api/trips/media/..%2f..%2fconfig.yaml", "/api/trips/media/missing.jpg"} {
		if w := h.Public("GET", p, nil); w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
}

// A decodable image is re-encoded as JPEG (which strips EXIF/IPTC/XMP, GPS
// included) with the long edge capped at 4096px: by the media slot when
// connected, else in-process.
func TestUploadTransformsToJPEGAndCapsDimensions(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 5000, 100))
	for x := range 5000 {
		src.Set(x, 0, color.RGBA{R: uint8(x % 256), A: 255})
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, src); err != nil {
		t.Fatal(err)
	}
	h := companiontest.New(t, unrouted)
	url := upload(t, h, "trip.png", pngBuf.Bytes())
	if filepath.Ext(url) != ".jpg" {
		t.Fatalf("expected a .jpg url, got %q", url)
	}
	rc, _, err := h.Module("trips").Blobs().Get(context.Background(), filepath.Base(url))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(rc)
	rc.Close()
	img, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil || format != "jpeg" || img.Bounds().Dx() != 4096 {
		t.Fatalf("stored %s, err %v", format, err)
	}
}

// With a media slot the upload goes through it (the fake passes the bytes
// through and reports success).
func TestUploadUsesTheMediaSlot(t *testing.T) {
	h := companiontest.New(t, "modules:\n  fake.media: {}\n  trips: { config: { media: fake.media } }\n")
	if url := upload(t, h, "doc.heic", []byte("undecodable")); filepath.Ext(url) != ".jpg" {
		t.Fatalf("the media slot was not used: %q", url)
	}
}

func routesTrip(t *testing.T, h *companiontest.Harness) trips.AdminTripView {
	t.Helper()
	h.Admin("POST", "/api/b/trips/trips", map[string]any{"title": "Routes"})
	w := h.Admin("PUT", "/api/b/trips/trips/1", map[string]any{
		"slug": "routes", "title": "Routes", "published": true,
		"stops": []map[string]any{
			{"name": "Munich", "lat": 48.137, "lng": 11.575},
			{"name": "Malmö", "lat": 55.605, "lng": 13.0, "transportMode": "car",
				"transportWaypoints": []map[string]any{{"lat": 53.55, "lng": 10.0}}},
			{"name": "Kalmar", "lat": 56.66, "lng": 16.36, "transportMode": "train"},
			{"name": "Gotland", "lat": 57.64, "lng": 18.29, "transportMode": "flight"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	return decode[trips.AdminTripView](t, w)
}

type publicGeometry struct {
	Stops []struct {
		TransportIn *struct {
			Geometry [][2]float64 `json:"geometry"`
		} `json:"transportIn"`
	} `json:"stops"`
}

// The editor sees each leg's route status by key; the public payload
// carries only "ok" tracks, never one for a flight.
func TestRouteGeometryReachesThePublicPayload(t *testing.T) {
	h := companiontest.New(t, unrouted)
	adm := routesTrip(t, h)
	if adm.Stops[0].Route != nil || adm.Stops[3].Route != nil {
		t.Fatalf("first stop / flight carry a route: %+v / %+v", adm.Stops[0].Route, adm.Stops[3].Route)
	}
	car, train := adm.Stops[1].Route, adm.Stops[2].Route
	if car == nil || train == nil || car.Status != "pending" || car.Key == train.Key {
		t.Fatalf("routes: %+v %+v", car, train)
	}
	if car.Key != "0cc75712e58d178ba964af9a39b89030" {
		t.Fatalf("car key %s differs from the old companion's", car.Key)
	}

	db := h.Module("trips").DB()
	track := [][2]float64{{48.137, 11.575}, {53.55, 10.0}, {55.605, 13.0}}
	db.Create(&trips.RouteGeometry{Key: car.Key, Mode: "car", Status: "ok", Source: "osrm", Points: track})
	db.Create(&trips.RouteGeometry{Key: train.Key, Mode: "train", Status: "failed", Error: "no rail"})

	adm = decode[trips.AdminTripView](t, h.Admin("GET", "/api/b/trips/trips/1", nil))
	if adm.Stops[1].Route.Status != "ok" || adm.Stops[2].Route.Status != "failed" || adm.Stops[2].Route.Error != "no rail" {
		t.Fatalf("statuses: %+v %+v", adm.Stops[1].Route, adm.Stops[2].Route)
	}

	raw := decode[publicGeometry](t, h.Public("GET", "/api/trips/routes", nil))
	if g := raw.Stops[1].TransportIn.Geometry; len(g) != 3 || g[1] != track[1] {
		t.Fatalf("car geometry: %v", g)
	}
	if raw.Stops[2].TransportIn.Geometry != nil || raw.Stops[3].TransportIn.Geometry != nil {
		t.Fatal("a failed leg or a flight carries geometry")
	}

	// Saving the trip again keeps the keys, so the track survives the stops
	// being re-created.
	resave, _ := json.Marshal(adm)
	var payload map[string]any
	json.Unmarshal(resave, &payload)
	h.Admin("PUT", "/api/b/trips/trips/1", payload)
	raw = decode[publicGeometry](t, h.Public("GET", "/api/trips/routes", nil))
	if len(raw.Stops[1].TransportIn.Geometry) != 3 {
		t.Fatal("geometry lost after re-saving the trip")
	}

	// Without a router, backfill says so.
	if w := h.Admin("POST", "/api/b/trips/routes/backfill", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("backfill without router: %d", w.Code)
	}
}

func TestWorkerRoutesSavedLegsAndRetriesFailures(t *testing.T) {
	h := companiontest.New(t, routed)
	router := companiontest.Instance[*companiontest.FakeRouter](h, "fake.router:main")
	tr := companiontest.Instance[*trips.Trips](h, "trips:default")
	router.Fail(errors.New("osrm down"))

	adm := routesTrip(t, h)
	car := adm.Stops[1].Route.Key
	waitFor(t, func() bool { return tr.Pending() == 0 })
	if router.CallCount() != 2 {
		t.Fatalf("router asked %d times, want 2 (the flight is drawn straight)", router.CallCount())
	}
	adm = decode[trips.AdminTripView](t, h.Admin("GET", "/api/b/trips/trips/1", nil))
	if r := adm.Stops[1].Route; r.Status != "failed" || r.Error != "osrm down" {
		t.Fatalf("failed leg: %+v", r)
	}

	// The backfill retries the failed legs.
	router.Fail(nil)
	w := h.Admin("POST", "/api/b/trips/routes/backfill", nil)
	if w.Code != http.StatusAccepted || decode[map[string]int](t, w)["queued"] != 2 {
		t.Fatalf("backfill: %d %s", w.Code, w.Body)
	}
	waitFor(t, func() bool { return tr.Pending() == 0 })
	raw := decode[publicGeometry](t, h.Public("GET", "/api/trips/routes", nil))
	if len(raw.Stops[1].TransportIn.Geometry) != 3 || len(raw.Stops[2].TransportIn.Geometry) != 2 {
		t.Fatalf("geometry after retry: %+v", raw)
	}

	// Computed legs are not queued again, unless forced or recomputed.
	calls := router.CallCount()
	if n := decode[map[string]int](t, h.Admin("POST", "/api/b/trips/routes/backfill", nil))["queued"]; n != 0 {
		t.Errorf("re-queued %d computed legs", n)
	}
	if w := h.Admin("POST", "/api/b/trips/routes/"+car+"/recompute", nil); w.Code != http.StatusAccepted {
		t.Fatalf("recompute: %d", w.Code)
	}
	if w := h.Admin("POST", "/api/b/trips/routes/nope/recompute", nil); w.Code != http.StatusNotFound {
		t.Fatalf("recompute unknown: %d", w.Code)
	}
	waitFor(t, func() bool { return tr.Pending() == 0 })
	if router.CallCount() != calls+1 {
		t.Errorf("recompute asked %d times", router.CallCount()-calls)
	}
	st := decode[map[string]any](t, h.Admin("GET", "/api/b/trips/routes", nil))
	if st["ok"].(float64) != 2 || st["failed"].(float64) != 0 || st["router"] != true {
		t.Fatalf("status %v", st)
	}
}

// Legs saved while the companion was down (or before routing existed) are
// queued at start.
func TestStartQueuesMissingLegs(t *testing.T) {
	h := companiontest.New(t, unrouted)
	routesTrip(t, h)
	if err := h.Rewrite(routed); err != nil {
		t.Fatal(err)
	}
	router := companiontest.Instance[*companiontest.FakeRouter](h, "fake.router:main")
	tr := companiontest.Instance[*trips.Trips](h, "trips:default")
	waitFor(t, func() bool { return tr.Pending() == 0 && router.CallCount() == 2 })
}

func TestImportsTheOldTrips(t *testing.T) {
	h := companiontest.New(t, unrouted)
	old := companiontest.LegacyDB(t)
	if err := old.DB.AutoMigrate(&trips.Trip{}, &trips.TripStop{}, &trips.TripPhoto{}, &trips.RouteGeometry{}); err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`INSERT INTO trips (id, slug, title, published, days_total, created_at, updated_at) VALUES (1, 'routes', 'Routes', 1, 9, '2026-06-01', '2026-06-02')`,
		`INSERT INTO trip_stops (id, trip_id, position, stop_key, name, lat, lng, status, transport_mode, transport_countries, transport_waypoints) VALUES
			(10, 1, 0, 'munich', 'Munich', 48.137, 11.575, 'visited', '', 'null', 'null'),
			(11, 1, 1, 'malmo', 'Malmö', 55.605, 13.0, 'current', 'car', '["Denmark"]', '[{"lat":53.55,"lng":10}]')`,
		`INSERT INTO trip_photos (id, stop_id, kind, position, url, caption) VALUES (5, 11, 'stop', 0, '/api/trips/media/abc.jpg', 'Harbour')`,
		`INSERT INTO route_geometries (key, mode, status, source, points, fetched_at) VALUES ('0cc75712e58d178ba964af9a39b89030', 'car', 'ok', 'osrm', '[[48.137,11.575],[53.55,10],[55.605,13]]', '2026-06-02')`,
	}
	for _, s := range stmts {
		if err := old.DB.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(filepath.Join(old.DataDir, "trips"), 0o755)
	os.WriteFile(filepath.Join(old.DataDir, "trips", "abc.jpg"), []byte("old image"), 0o644)

	info, _ := companion.Lookup("trips")
	if err := info.Import(h.Module("trips"), old); err != nil {
		t.Fatal(err)
	}
	var pub struct {
		Stats struct{ Countries int }
		Stops []struct {
			Photos      []struct{ Src string }
			TransportIn *struct {
				Countries []string
				Geometry  [][2]float64
			}
		}
	}
	json.Unmarshal(h.Public("GET", "/api/trips/routes", nil).Body.Bytes(), &pub)
	if len(pub.Stops) != 2 || pub.Stats.Countries != 1 || pub.Stops[1].TransportIn == nil ||
		len(pub.Stops[1].TransportIn.Geometry) != 3 || pub.Stops[1].Photos[0].Src != "/api/trips/media/abc.jpg" {
		t.Fatalf("imported trip %+v", pub)
	}
	if m := h.Public("GET", "/api/trips/media/abc.jpg", nil); m.Body.String() != "old image" {
		t.Fatalf("media %d", m.Code)
	}
	// New trips get ids after the imported ones.
	if v := decode[trips.AdminTripView](t, h.Admin("POST", "/api/b/trips/trips", map[string]any{"title": "Next"})); v.ID != 2 {
		t.Fatalf("new trip id %d", v.ID)
	}
}
