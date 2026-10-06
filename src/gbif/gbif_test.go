package gbif

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type upstream struct {
	srv      *httptest.Server
	calls    atomic.Int64
	down     atomic.Bool
	mu       sync.Mutex
	lastURL  string
	lastINM  string
	etag     string
	notModOK bool
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{etag: `W/"2026-10-03T05:00Z--gzip"`, notModOK: true}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.calls.Add(1)
		u.mu.Lock()
		u.lastURL, u.lastINM = r.URL.String(), r.Header.Get("If-None-Match")
		u.mu.Unlock()
		if u.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=1200")
		w.Header().Set("ETag", u.etag)
		if u.notModOK && r.Header.Get("If-None-Match") == u.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if strings.Contains(r.URL.Path, "/9/") { // an empty area
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("PNG:" + r.URL.Path))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func setup(t *testing.T) (*Proxy, *upstream, *gin.Engine, *time.Time) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	u := newUpstream(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	p := &Proxy{BaseURL: u.srv.URL + "/density", Dir: t.TempDir(), CapBytes: 1 << 20,
		Client: u.srv.Client(), Now: func() time.Time { return now }}
	Default = p
	r := gin.New()
	RegisterRoutes(r.Group("/api"))
	return p, u, r, &now
}

func get(r *gin.Engine, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestFetchesWithTheFixedParametersOnly(t *testing.T) {
	_, u, r, _ := setup(t)
	w := get(r, "/api/tiles/gbif/5219243/3/4/2.png?style=evil&taxonKey=1")
	if w.Code != 200 || w.Body.String() != "PNG:/density/3/4/2@1x.png" {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
	want := "/density/3/4/2@1x.png?srs=EPSG%3A3857&style=classic.poly&bin=hex&hexPerTile=110&taxonKey=5219243"
	if u.lastURL != want {
		t.Errorf("upstream URL\n got %s\nwant %s", u.lastURL, want)
	}
	if w.Header().Get("Cache-Control") != "public, max-age=1200" {
		t.Errorf("Cache-Control %q", w.Header().Get("Cache-Control"))
	}
}

func TestServesFromCacheThenRevalidates(t *testing.T) {
	_, u, r, now := setup(t)
	get(r, "/api/tiles/gbif/1/3/4/2.png")
	get(r, "/api/tiles/gbif/1/3/4/2.png")
	if u.calls.Load() != 1 {
		t.Fatalf("a fresh tile went upstream: %d calls", u.calls.Load())
	}
	*now = now.Add(21 * time.Minute)
	w := get(r, "/api/tiles/gbif/1/3/4/2.png")
	if u.calls.Load() != 2 || u.lastINM != u.etag {
		t.Fatalf("stale tile not revalidated: %d calls, If-None-Match %q", u.calls.Load(), u.lastINM)
	}
	if w.Code != 200 || w.Body.String() != "PNG:/density/3/4/2@1x.png" {
		t.Fatalf("after 304: %d %q", w.Code, w.Body.String())
	}
	get(r, "/api/tiles/gbif/1/3/4/2.png")
	if u.calls.Load() != 2 {
		t.Error("the 304 did not refresh the tile's age")
	}
}

func TestServesStaleWhenGBIFIsDown(t *testing.T) {
	_, u, r, now := setup(t)
	get(r, "/api/tiles/gbif/1/3/4/2.png")
	u.down.Store(true)
	*now = now.Add(time.Hour)
	w := get(r, "/api/tiles/gbif/1/3/4/2.png")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("stale fallback: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	if w := get(r, "/api/tiles/gbif/1/3/5/2.png"); w.Code != http.StatusBadGateway {
		t.Errorf("uncached tile while down: %d, want 502", w.Code)
	}
}

func TestEmptyTilesAreCachedAs204(t *testing.T) {
	_, u, r, _ := setup(t)
	for i := 0; i < 2; i++ {
		if w := get(r, "/api/tiles/gbif/1/9/1/1.png"); w.Code != http.StatusNoContent {
			t.Fatalf("empty tile: %d", w.Code)
		}
	}
	if u.calls.Load() != 1 {
		t.Errorf("a cached empty tile went upstream again: %d calls", u.calls.Load())
	}
}

func TestRejectsBadAddresses(t *testing.T) {
	_, u, r, _ := setup(t)
	for _, bad := range []string{
		"/api/tiles/gbif/abc/1/0/0.png",
		"/api/tiles/gbif/12345678901/1/0/0.png",
		"/api/tiles/gbif/1/15/0/0.png",
		"/api/tiles/gbif/1/1/2/0.png",
		"/api/tiles/gbif/1/1/0/0.jpg",
		"/api/tiles/gbif/..%2F..%2Fx/1/0/0.png",
	} {
		if w := get(r, bad); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	if u.calls.Load() != 0 {
		t.Error("a rejected address reached GBIF")
	}
}

func TestConcurrentMissesShareOneFetch(t *testing.T) {
	_, u, r, _ := setup(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); get(r, "/api/tiles/gbif/7/5/1/1.png") }()
	}
	wg.Wait()
	if c := u.calls.Load(); c > 3 {
		t.Errorf("%d upstream calls for one tile", c)
	}
}

func TestEvictRemovesLeastRecentlyUsedFirst(t *testing.T) {
	p, _, r, _ := setup(t)
	for _, path := range []string{"1/3/0/0", "1/3/0/1", "1/3/0/2"} {
		get(r, "/api/tiles/gbif/"+path+".png")
	}
	base := func(rel string) string { return filepath.Join(p.Dir, filepath.FromSlash(rel)) }
	// Make 3/0/0 the oldest, 3/0/2 the newest.
	for i, rel := range []string{"1/3/0/0", "1/3/0/1", "1/3/0/2"} {
		ts := time.Now().Add(time.Duration(i-3) * time.Hour)
		os.Chtimes(base(rel)+".png", ts, ts)
		os.Chtimes(base(rel)+".json", ts, ts)
	}
	var size int64
	filepath.Walk(p.Dir, func(_ string, fi os.FileInfo, _ error) error {
		if fi != nil && !fi.IsDir() {
			size += fi.Size()
		}
		return nil
	})
	p.CapBytes = size - 1 // one tile too many
	if n, err := p.Evict(); err != nil || n != 1 {
		t.Fatalf("evicted %d, %v", n, err)
	}
	if _, err := os.Stat(base("1/3/0/0") + ".png"); !os.IsNotExist(err) {
		t.Error("the oldest tile survived")
	}
	if _, err := os.Stat(base("1/3/0/2") + ".png"); err != nil {
		t.Error("the newest tile was evicted")
	}
}
