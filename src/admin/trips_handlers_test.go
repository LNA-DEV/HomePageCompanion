package admin

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const testTripKey = "ApiKey test-key"

// setupTripTest wires a gin engine backed by a fresh temp-file SQLite DB and a
// simple header-based auth middleware, and chdirs into a scratch dir so the
// upload handler writes data/trips/ there rather than into the repo.
func setupTripTest(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "test.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.Trip{}, &models.TripStop{}, &models.TripPhoto{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.Db = db

	auth := func(c *gin.Context) {
		if c.GetHeader("Authorization") != testTripKey {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "nope"})
			return
		}
		c.Next()
	}

	r := gin.New()
	api := r.Group("/api")
	RegisterTripRoutes(api, auth)
	return r
}

func do(t *testing.T, r *gin.Engine, method, path, auth string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
}

func TestTripAuthRequiredForWrites(t *testing.T) {
	r := setupTripTest(t)
	w := do(t, r, http.MethodPost, "/api/admin/trips", "", map[string]any{"title": "X"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", w.Code)
	}
}

func TestTripCRUDAndPublicShape(t *testing.T) {
	r := setupTripTest(t)

	// Create
	w := do(t, r, http.MethodPost, "/api/admin/trips", testTripKey, map[string]any{
		"title": "Europe, mostly by train",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d (%s)", w.Code, w.Body.String())
	}
	var created adminTripView
	decode(t, w, &created)
	if created.Slug != "europe-mostly-by-train" {
		t.Fatalf("expected slugified title, got %q", created.Slug)
	}

	// Update with two stops; the first has no transport leg, the second a train
	// leg with a distance and multiple countries. daysTotal is the one manual
	// stat; the rest (daysElapsed, cities, countries, distance) are derived.
	total := 14
	payload := map[string]any{
		"slug":      created.Slug,
		"title":     created.Title,
		"published": true,
		"daysTotal": total,
		"stops": []map[string]any{
			{
				"name": "London", "startDate": "2026-06-03", "endDate": "2026-06-04",
				"lat": 51.5074, "lng": -0.1278,
				"status": "visited", "note": "Kicked things off.", "country": "United Kingdom",
				"transportMode": "",
				"photos":        []map[string]any{{"url": "/api/trips/media/a.jpg", "caption": "Borough Market"}},
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
	w = do(t, r, http.MethodPut, "/api/admin/trips/1", testTripKey, payload)
	if w.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	// Admin GET should round-trip the two stops with split photo galleries.
	w = do(t, r, http.MethodGet, "/api/admin/trips/1", testTripKey, nil)
	var got adminTripView
	decode(t, w, &got)
	if len(got.Stops) != 2 {
		t.Fatalf("expected 2 stops, got %d", len(got.Stops))
	}
	if got.Stops[0].StopKey != "london" {
		t.Fatalf("expected derived stopKey 'london', got %q", got.Stops[0].StopKey)
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
		t.Fatalf("first stop waypoints should be [] not null: %+v", got.Stops[0].TransportWaypoints)
	}

	// Public list returns the published trip.
	w = do(t, r, http.MethodGet, "/api/trips", "", nil)
	var list []publicTripListItem
	decode(t, w, &list)
	if len(list) != 1 || list[0].Slug != created.Slug || list[0].Stats.Cities != 2 {
		t.Fatalf("public list wrong: %+v", list)
	}
	if list[0].Stats.Countries != 3 {
		t.Fatalf("public list countries should be 3 (UK, France, Italy), got %d", list[0].Stats.Countries)
	}

	// Public get-by-slug yields the HTML-friendly shape.
	w = do(t, r, http.MethodGet, "/api/trips/"+created.Slug, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("public get: expected 200, got %d", w.Code)
	}
	var pub publicTrip
	decode(t, w, &pub)
	// Distance is the per-leg sum (only Paris has a leg: 492). Countries is the
	// distinct union UK+France+Italy = 3. daysTotal is the manual 14; daysElapsed
	// is derived and (the trip dates being in the past) capped at the total.
	if pub.Stats.Cities != 2 {
		t.Fatalf("expected 2 cities, got %d", pub.Stats.Cities)
	}
	if pub.Stats.Countries != 3 {
		t.Fatalf("expected 3 countries, got %d", pub.Stats.Countries)
	}
	if pub.Stats.DistanceKm == nil || *pub.Stats.DistanceKm != 492 {
		t.Fatalf("expected distance 492, got %v", pub.Stats.DistanceKm)
	}
	if pub.Stats.DaysTotal == nil || *pub.Stats.DaysTotal != 14 {
		t.Fatalf("expected manual daysTotal 14, got %v", pub.Stats.DaysTotal)
	}
	if pub.Stats.DaysElapsed == nil || *pub.Stats.DaysElapsed != 14 {
		t.Fatalf("expected derived daysElapsed capped at 14, got %v", pub.Stats.DaysElapsed)
	}
	if pub.Stops[1].TransportIn == nil || pub.Stops[1].TransportIn.DistanceKm == nil ||
		*pub.Stops[1].TransportIn.DistanceKm != 492 || len(pub.Stops[1].TransportIn.Countries) != 2 {
		t.Fatalf("public transport leg distance/countries wrong: %+v", pub.Stops[1].TransportIn)
	}
	if len(pub.Stops[1].TransportIn.Waypoints) != 2 || pub.Stops[1].TransportIn.Waypoints[0].Lng != 1.8 {
		t.Fatalf("public transport leg waypoints wrong: %+v", pub.Stops[1].TransportIn.Waypoints)
	}
	if pub.Stops[0].TransportIn != nil {
		t.Fatalf("first stop should have null transportIn, got %+v", pub.Stops[0].TransportIn)
	}
	if pub.Stops[1].TransportIn == nil || pub.Stops[1].TransportIn.Mode != "train" {
		t.Fatalf("second stop transportIn wrong: %+v", pub.Stops[1].TransportIn)
	}
	if pub.Stops[1].TransportIn.Photos[0].Src != "/api/trips/media/c.jpg" {
		t.Fatalf("transport photo src wrong: %+v", pub.Stops[1].TransportIn.Photos)
	}
	if pub.Stops[0].Photos[0].Cap != "Borough Market" {
		t.Fatalf("stop photo caption wrong: %+v", pub.Stops[0].Photos)
	}
	// Dates are formatted server-side from the stored start/end.
	if pub.Stops[0].Dates != "Jun 3–4" {
		t.Fatalf("expected same-month range 'Jun 3–4', got %q", pub.Stops[0].Dates)
	}
	if pub.Stops[1].Dates != "Jun 4–" {
		t.Fatalf("expected open-ended range 'Jun 4–', got %q", pub.Stops[1].Dates)
	}

	// Unpublish hides it from the public endpoints.
	payload["published"] = false
	w = do(t, r, http.MethodPut, "/api/admin/trips/1", testTripKey, payload)
	if w.Code != http.StatusOK {
		t.Fatalf("unpublish update failed: %d", w.Code)
	}
	w = do(t, r, http.MethodGet, "/api/trips/"+created.Slug, "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unpublished trip should be 404 publicly, got %d", w.Code)
	}

	// Delete.
	w = do(t, r, http.MethodDelete, "/api/admin/trips/1", testTripKey, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d", w.Code)
	}
	w = do(t, r, http.MethodGet, "/api/admin/trips/1", testTripKey, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("after delete expected 404, got %d", w.Code)
	}
}

func TestTripUploadAndMediaTraversalGuard(t *testing.T) {
	r := setupTripTest(t)

	// Multipart upload returns a media URL and persists the file.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "pic.png")
	fw.Write([]byte("not-a-real-png-but-bytes"))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/trips/upload", &buf)
	req.Header.Set("Authorization", testTripKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("upload: expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	var up struct {
		URL  string `json:"url"`
		Size int    `json:"size"`
	}
	decode(t, w, &up)
	if up.Size == 0 || filepath.Ext(up.URL) != ".png" {
		t.Fatalf("upload response wrong: %+v", up)
	}

	// Path-traversal attempt on the media route is rejected.
	w = do(t, r, http.MethodGet, "/api/trips/media/..%2f..%2fconfig.yaml", "", nil)
	if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
		t.Fatalf("traversal should be blocked, got %d", w.Code)
	}
}

// TestTripUploadTransformsToJPEGAndCapsDimensions verifies a decodable image is
// re-encoded to JPEG (which strips EXIF/IPTC/XMP metadata, incl. GPS) and that
// the long edge is capped at 4096px.
func TestTripUploadTransformsToJPEGAndCapsDimensions(t *testing.T) {
	r := setupTripTest(t)

	// A real PNG wider than the 4096px cap, exercising both the
	// metadata-stripping re-encode and the resize.
	src := image.NewRGBA(image.Rect(0, 0, 5000, 100))
	for x := range 5000 {
		src.Set(x, 0, color.RGBA{R: uint8(x % 256), A: 255})
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, src); err != nil {
		t.Fatalf("encode source png: %v", err)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "trip.png")
	fw.Write(pngBuf.Bytes())
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/trips/upload", &body)
	req.Header.Set("Authorization", testTripKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("upload: expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	var up struct {
		URL  string `json:"url"`
		Size int    `json:"size"`
	}
	decode(t, w, &up)

	// A PNG input must come back as a re-encoded .jpg.
	if filepath.Ext(up.URL) != ".jpg" {
		t.Fatalf("expected transformed .jpg url, got %q", up.URL)
	}

	// The stored file decodes as JPEG and is capped at 4096px on the long edge.
	raw, err := os.ReadFile(filepath.Join(tripMediaDir, filepath.Base(up.URL)))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode stored file: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("stored file format = %q, want jpeg", format)
	}
	if got := img.Bounds().Dx(); got != 4096 {
		t.Fatalf("long edge not capped: width = %d, want 4096", got)
	}
}
