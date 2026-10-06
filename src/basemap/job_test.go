package basemap

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/aws/smithy-go"
	"github.com/gin-gonic/gin"
	"github.com/protomaps/go-pmtiles/pmtiles"
	"gocloud.dev/blob"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ---- a tiny but real PMTiles archive ----

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

// archive builds a valid PMTiles v3 file with one gzip "tile" at each given
// address. With withSamples it holds the tiles verifyServing reads.
func archive(t *testing.T, schema string, withSamples bool) []byte {
	t.Helper()
	addrs := [][3]uint32{{1, 0, 0}, {1, 1, 1}, {3, 4, 2}}
	if withSamples {
		addrs = append(addrs, sampleTiles()...)
	}
	type tile struct {
		id   uint64
		data []byte
	}
	var tiles []tile
	for _, a := range addrs {
		tiles = append(tiles, tile{pmtiles.ZxyToID(uint8(a[0]), a[1], a[2]),
			gz(t, fmt.Sprintf("tile %d/%d/%d %s", a[0], a[1], a[2], strings.Repeat("x", 40)))})
	}
	sort.Slice(tiles, func(i, j int) bool { return tiles[i].id < tiles[j].id })
	var data bytes.Buffer
	var entries []pmtiles.EntryV3
	for _, tl := range tiles {
		entries = append(entries, pmtiles.EntryV3{TileID: tl.id, Offset: uint64(data.Len()), Length: uint32(len(tl.data)), RunLength: 1})
		data.Write(tl.data)
	}
	root := pmtiles.SerializeEntries(entries, pmtiles.Gzip)
	meta, err := pmtiles.SerializeMetadata(map[string]interface{}{
		"version":                               schema,
		"planetiler:osm:osmosisreplicationtime": "2026-10-05T04:00:00Z",
		"attribution":                           "© OpenStreetMap",
	}, pmtiles.Gzip)
	if err != nil {
		t.Fatal(err)
	}
	h := pmtiles.HeaderV3{
		SpecVersion: 3,
		RootOffset:  pmtiles.HeaderV3LenBytes, RootLength: uint64(len(root)),
		MetadataOffset: pmtiles.HeaderV3LenBytes + uint64(len(root)), MetadataLength: uint64(len(meta)),
		TileDataOffset: pmtiles.HeaderV3LenBytes + uint64(len(root)) + uint64(len(meta)), TileDataLength: uint64(data.Len()),
		AddressedTilesCount: uint64(len(tiles)), TileEntriesCount: uint64(len(tiles)), TileContentsCount: uint64(len(tiles)),
		Clustered: true, InternalCompression: pmtiles.Gzip, TileCompression: pmtiles.Gzip, TileType: pmtiles.Mvt,
		MinZoom: 0, MaxZoom: 15, MinLonE7: -1800000000, MinLatE7: -850511287, MaxLonE7: 1800000000, MaxLatE7: 850511287,
	}
	h.LeafDirectoryOffset = h.TileDataOffset
	var out bytes.Buffer
	out.Write(pmtiles.SerializeHeader(h))
	out.Write(root)
	out.Write(meta)
	out.Write(data.Bytes())
	return out.Bytes()
}

// ---- a fake build host ----

type fakeSource struct {
	mu     sync.Mutex
	builds map[string][]byte
	etags  map[string]string
	// failAfter > 0 fails every range request after that many succeeded.
	failAfter int64
	ranges    atomic.Int64
	srv       *httptest.Server
}

func newFakeSource(t *testing.T) *fakeSource {
	f := &fakeSource{builds: map[string][]byte{}, etags: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".pmtiles")
		f.mu.Lock()
		b, ok := f.builds[name]
		etag := f.etags[name]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Range") != "" {
			n := f.ranges.Add(1)
			if f.failAfter > 0 && n > f.failAfter {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("ETag", etag)
		http.ServeContent(w, r, name+".pmtiles", time.Time{}, bytes.NewReader(b))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSource) put(version string, b []byte, etag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds[version], f.etags[version] = b, etag
}

// ---- harness ----

type harness struct {
	t   *testing.T
	dir string
	src *fakeSource
	now time.Time
	s   *service
	r   *gin.Engine
}

func setup(t *testing.T) *harness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.BasemapVersion{}, &models.BasemapPart{}); err != nil {
		t.Fatal(err)
	}
	database.Db = db

	h := &harness{t: t, dir: t.TempDir(), src: newFakeSource(t), now: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	quiet := log.New(io.Discard, "", 0)
	server, err := pmtiles.NewServerWithBucket(pmtiles.NewFileBucket(filepath.Join(h.dir, testPrefix)), "", quiet, 16, "https://companion.test/api/tiles/basemap")
	if err != nil {
		t.Fatal(err)
	}
	server.Start()
	h.s = &service{cfg: withDefaults(config.Basemap{Enabled: true}), bucketURL: "file://" + h.dir, prefix: testPrefix, server: server}
	h.s.job = &Job{
		Store: &DirStore{Root: h.dir}, Source: Source{BaseURL: h.src.srv.URL, Client: h.src.srv.Client()},
		Prefix: testPrefix, SchemaMajor: 4, PartSize: 100, Concurrency: 3, RetainDays: 7,
		Now: func() time.Time { return h.now }, Verify: h.s.verifyServing, OnChange: h.s.reload,
		Retries: 2, RetryPause: time.Millisecond,
	}
	svc = h.s
	t.Cleanup(func() { svc = nil })
	h.r = gin.New()
	RegisterRoutes(h.r.Group("/api"), func(c *gin.Context) { c.Next() })
	return h
}

func (h *harness) version(v string) models.BasemapVersion {
	h.t.Helper()
	var row models.BasemapVersion
	if database.Db.Where("version = ?", v).Limit(1).Find(&row).RowsAffected == 0 {
		h.t.Fatalf("no row for %s", v)
	}
	return row
}

func (h *harness) get(path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func (h *harness) objectPath(v string) string {
	return filepath.Join(h.dir, filepath.FromSlash(testPrefix), v+".pmtiles")
}

// testPrefix is the layout storage.Prefix gives this module by default.
const testPrefix = "home-page-companion/maps/basemap"

// ---- tests ----

func TestRunCopiesVerifiesAndServes(t *testing.T) {
	h := setup(t)
	src := archive(t, "4.15.2", true)
	h.src.put("20261005", src, `"etag-a"`)

	if err := h.s.job.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	v := h.version("20261005")
	if v.Status != models.BasemapActive || v.SchemaVersion != "4.15.2" || v.OSMTime != "2026-10-05T04:00:00Z" {
		t.Fatalf("row = %+v", v)
	}
	if v.PartsTotal != (len(src)+99)/100 {
		t.Errorf("parts = %d for %d bytes", v.PartsTotal, len(src))
	}
	got, err := os.ReadFile(h.objectPath("20261005"))
	if err != nil || !bytes.Equal(got, src) {
		t.Fatalf("copied object differs from the source (err %v)", err)
	}
	var leftover int64
	database.Db.Model(&models.BasemapPart{}).Count(&leftover)
	if leftover != 0 {
		t.Errorf("%d part rows left after completion", leftover)
	}

	w := h.get("/api/tiles/basemap.json")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=3600" {
		t.Fatalf("TileJSON: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	var tj struct {
		Tiles   []string `json:"tiles"`
		MaxZoom int      `json:"maxzoom"`
	}
	json.Unmarshal(w.Body.Bytes(), &tj)
	if len(tj.Tiles) != 1 || tj.Tiles[0] != "https://companion.test/api/tiles/basemap/20261005/{z}/{x}/{y}.mvt" || tj.MaxZoom != 15 {
		t.Fatalf("TileJSON body: %s", w.Body.String())
	}

	w = h.get("/api/tiles/basemap/20261005/1/1/1.mvt")
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" ||
		w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("tile: %d %v", w.Code, w.Header())
	}
	if w := h.get("/api/tiles/basemap/20261005/2/0/0.mvt"); w.Code != http.StatusNoContent {
		t.Errorf("missing tile: %d, want 204", w.Code)
	}
	for _, bad := range []string{
		"/api/tiles/basemap/20261004/1/1/1.mvt",  // not a stored version
		"/api/tiles/basemap/20261005/16/0/0.mvt", // beyond z15
		"/api/tiles/basemap/20261005/1/2/0.mvt",  // x outside the grid
		"/api/tiles/basemap/20261005/1/1/1.png",  // wrong extension
		"/api/tiles/basemap/..%2F..%2Fetc/1/1/1.mvt",
	} {
		if w := h.get(bad); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", bad, w.Code)
		}
	}

	// Nothing newer at the source: a second run is a no-op.
	n := h.src.ranges.Load()
	if err := h.s.job.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.src.ranges.Load() != n {
		t.Error("an up-to-date run fetched ranges")
	}
}

func TestRunResumesAfterAnInterruptedCopy(t *testing.T) {
	h := setup(t)
	src := archive(t, "4.15.2", true)
	h.src.put("20261005", src, `"etag-a"`)
	h.s.job.Concurrency = 1
	h.src.failAfter = 6 // header + metadata + 4 parts, then the host fails

	if err := h.s.job.Run(context.Background()); err == nil {
		t.Fatal("run should fail mid-copy")
	}
	v := h.version("20261005")
	var done int64
	database.Db.Model(&models.BasemapPart{}).Where("version_id = ?", v.ID).Count(&done)
	if v.Status != models.BasemapCopying || done == 0 || int(done) >= v.PartsTotal {
		t.Fatalf("after the failure: status %s, %d of %d parts", v.Status, done, v.PartsTotal)
	}

	h.src.failAfter = 0
	before := h.src.ranges.Load()
	if err := h.s.job.Run(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	// Only the missing parts are fetched again, plus the source header for
	// the verification.
	if fetched := h.src.ranges.Load() - before; fetched != int64(v.PartsTotal)-done+1 {
		t.Errorf("resume fetched %d ranges, want %d", fetched, int64(v.PartsTotal)-done+1)
	}
	got, _ := os.ReadFile(h.objectPath("20261005"))
	if !bytes.Equal(got, src) {
		t.Fatal("resumed object differs from the source")
	}
	if h.version("20261005").Status != models.BasemapActive {
		t.Fatal("resumed version not active")
	}
}

func TestRunRefusesAnotherSchemaMajor(t *testing.T) {
	h := setup(t)
	h.src.put("20261005", archive(t, "5.0.0", true), `"e"`)
	err := h.s.job.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "schema 5.0.0") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(h.dir, ".uploads")); len(entries) != 0 {
		t.Error("an upload was started for a refused build")
	}
	if st := h.s.job.Status(); st.LastErr == "" {
		t.Error("the refusal is not in the job status")
	}
}

func TestRunGivesUpWhenTheSourceChangesUnderACopy(t *testing.T) {
	h := setup(t)
	h.src.put("20261005", archive(t, "4.15.2", true), `"etag-a"`)
	h.s.job.Concurrency = 1
	h.src.failAfter = 5
	h.s.job.Run(context.Background())

	h.src.failAfter = 0
	h.src.put("20261005", archive(t, "4.15.2", true), `"etag-b"`) // re-published
	if err := h.s.job.Run(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	// The interrupted copy is failed and its upload aborted; the build was
	// then found afresh and copied from the start.
	if v := h.version("20261005"); v.Status != models.BasemapActive || v.SourceETag != `"etag-b"` {
		t.Fatalf("row = %+v", v)
	}
	if entries, _ := os.ReadDir(filepath.Join(h.dir, ".uploads")); len(entries) != 0 {
		t.Errorf("%d uploads left behind", len(entries))
	}
}

func TestVerificationFailureDeletesTheCopy(t *testing.T) {
	h := setup(t)
	h.src.put("20261005", archive(t, "4.15.2", false), `"e"`) // no sample tiles
	if err := h.s.job.Run(context.Background()); err == nil {
		t.Fatal("verification should fail")
	}
	if v := h.version("20261005"); v.Status != models.BasemapFailed {
		t.Fatalf("status %s", v.Status)
	}
	if _, err := os.Stat(h.objectPath("20261005")); !os.IsNotExist(err) {
		t.Error("the failed copy was not deleted")
	}
	if w := h.get("/api/tiles/basemap.json"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("TileJSON with nothing active: %d", w.Code)
	}
}

func TestNewVersionRetainsTheOldOneUntilCleanup(t *testing.T) {
	h := setup(t)
	h.src.put("20261005", archive(t, "4.15.2", true), `"a"`)
	if err := h.s.job.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.AddDate(0, 1, 0) // a month later
	h.src.put("20261105", archive(t, "4.16.0", true), `"b"`)
	if err := h.s.job.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := h.version("20261005")
	if old.Status != models.BasemapRetained || old.DeleteAfter == nil {
		t.Fatalf("old version: %+v", old)
	}
	if h.version("20261105").Status != models.BasemapActive {
		t.Fatal("new version not active")
	}
	// Browsers holding the old TileJSON still get tiles.
	if w := h.get("/api/tiles/basemap/20261005/1/1/1.mvt"); w.Code != 200 {
		t.Fatalf("retained version not served: %d", w.Code)
	}

	if d, _, _ := h.s.job.Cleanup(context.Background()); d != 0 {
		t.Fatal("cleanup deleted a version before its time")
	}
	h.now = h.now.AddDate(0, 0, 8)
	if d, _, err := h.s.job.Cleanup(context.Background()); err != nil || d != 1 {
		t.Fatalf("cleanup: %d, %v", d, err)
	}
	if _, err := os.Stat(h.objectPath("20261005")); !os.IsNotExist(err) {
		t.Error("expired version still in the bucket")
	}
	if w := h.get("/api/tiles/basemap/20261005/1/1/1.mvt"); w.Code != http.StatusNotFound {
		t.Errorf("deleted version still served: %d", w.Code)
	}
	if w := h.get("/api/tiles/basemap/20261105/1/1/1.mvt"); w.Code != 200 {
		t.Errorf("active version: %d", w.Code)
	}
}

func TestCleanupAbortsOnlyStaleUnownedUploads(t *testing.T) {
	h := setup(t)
	st := h.s.job.Store.(*DirStore)
	stale, _ := st.CreateUpload(context.Background(), testPrefix+"/20260901.pmtiles")
	fresh, _ := st.CreateUpload(context.Background(), testPrefix+"/20260902.pmtiles")
	foreign, _ := st.CreateUpload(context.Background(), "home-page-companion/other-module/thing.bin")
	old := time.Now().Add(-72 * time.Hour)
	os.Chtimes(st.uploadDir(stale), old, old)
	os.Chtimes(st.uploadDir(foreign), old, old)
	h.now = time.Now()

	_, aborted, err := h.s.job.Cleanup(context.Background())
	if err != nil || aborted != 1 {
		t.Fatalf("aborted %d, err %v", aborted, err)
	}
	for id, want := range map[string]bool{stale: false, fresh: true, foreign: true} {
		if _, err := os.Stat(st.uploadDir(id)); (err == nil) != want {
			t.Errorf("upload %s exists=%v, want %v", id, err == nil, want)
		}
	}
}

func TestFindTakesTheNewestBuildOfTheWeek(t *testing.T) {
	h := setup(t)
	h.src.put("20261001", []byte("x"), "")
	h.src.put("20261003", []byte("y"), "")
	h.src.put("20260920", []byte("z"), "") // older than a week
	b, err := h.s.job.Source.Find(context.Background(), h.now)
	if err != nil || b.Version != "20261003" {
		t.Fatalf("found %+v, %v", b, err)
	}
	h.src.builds = map[string][]byte{"20260920": []byte("z")}
	if _, err := h.s.job.Source.Find(context.Background(), h.now); err == nil {
		t.Error("a build older than a week was found")
	}
}

func TestParseZXY(t *testing.T) {
	cases := []struct {
		z, x, y string
		ok      bool
	}{
		{"0", "0", "0.mvt", true}, {"15", "32767", "32767.mvt", true},
		{"15", "32768", "0.mvt", false}, {"-1", "0", "0.mvt", false},
		{"3", "1", "1", false}, {"3", "a", "1.mvt", false}, {"99999999999", "0", "0.mvt", false},
	}
	for _, c := range cases {
		if _, _, _, ok := parseZXY(c.z, c.x, c.y, "mvt", 15); ok != c.ok {
			t.Errorf("%s/%s/%s: ok=%v", c.z, c.x, c.y, ok)
		}
	}
}

func TestServerBucketPutsThePrefixIntoFilePaths(t *testing.T) {
	if u, p := serverBucket("file:///srv/tiles", testPrefix); u != "file:///srv/tiles/"+testPrefix || p != "" {
		t.Errorf("file: %s %q", u, p)
	}
	s3 := "s3://lna-dev?endpoint=https://nbg1.your-objectstorage.com&region=nbg1"
	if u, p := serverBucket(s3, testPrefix); u != s3 || p != testPrefix {
		t.Errorf("s3: %s %q", u, p)
	}
}

// The library does not register gocloud's S3 driver; without the blank
// import in basemap.go the bucket fails to open at startup — which the file
// buckets of the other tests would never notice.
func TestS3DriverIsRegistered(t *testing.T) {
	if !blob.DefaultURLMux().ValidBucketScheme("s3") {
		t.Fatal(`no gocloud driver for "s3"`)
	}
}

// deniedStore answers AccessDenied for the parts of the uploads in deny —
// what S3 says when the requester did not initiate the upload, e.g. after the
// credentials were replaced between a copy and its resumption.
type deniedStore struct {
	Store
	mu      sync.Mutex
	deny    map[string]bool
	denyAll bool
	parts   int
	aborts  int
}

func (d *deniedStore) UploadPart(ctx context.Context, key, uploadID string, n int32, data []byte) (string, error) {
	d.mu.Lock()
	d.parts++
	denied := d.denyAll || d.deny[uploadID]
	d.mu.Unlock()
	if denied {
		return "", &smithy.GenericAPIError{Code: "AccessDenied", Message: "UnknownError"}
	}
	return d.Store.UploadPart(ctx, key, uploadID, n, data)
}

func (d *deniedStore) Abort(ctx context.Context, key, uploadID string) error {
	d.mu.Lock()
	d.aborts++
	denied := d.denyAll || d.deny[uploadID]
	d.mu.Unlock()
	if denied {
		return &smithy.GenericAPIError{Code: "AccessDenied", Message: "UnknownError"}
	}
	return d.Store.Abort(ctx, key, uploadID)
}

func TestARefusedResumeStartsTheCopyOver(t *testing.T) {
	h := setup(t)
	src := archive(t, "4.15.2", true)
	h.src.put("20261005", src, `"etag-a"`)
	h.s.job.Concurrency = 1
	h.src.failAfter = 5
	if err := h.s.job.Run(context.Background()); err == nil {
		t.Fatal("first run should be interrupted")
	}
	old := h.version("20261005")
	if old.Status != models.BasemapCopying {
		t.Fatalf("after the interruption: %s", old.Status)
	}

	// New credentials: the bucket refuses the old upload's parts — and its abort.
	h.src.failAfter = 0
	ds := &deniedStore{Store: h.s.job.Store, deny: map[string]bool{old.UploadID: true}}
	h.s.job.Store = ds
	if err := h.s.job.Run(context.Background()); err != nil {
		t.Fatalf("the run should recover by starting over: %v", err)
	}
	v := h.version("20261005")
	if v.Status != models.BasemapActive || v.UploadID == old.UploadID {
		t.Fatalf("after recovery: status %s, upload %s (old %s)", v.Status, v.UploadID, old.UploadID)
	}
	if got, _ := os.ReadFile(h.objectPath("20261005")); !bytes.Equal(got, src) {
		t.Fatal("the fresh copy differs from the source")
	}
	if ds.parts > v.PartsTotal+1 {
		t.Errorf("%d part uploads for %d parts: the refused one was retried", ds.parts, v.PartsTotal)
	}
}

func TestAnAccessDeniedFreshCopyFailsInsteadOfStickingInCopying(t *testing.T) {
	h := setup(t)
	h.src.put("20261005", archive(t, "4.15.2", true), `"e"`)
	ds := &deniedStore{Store: h.s.job.Store, denyAll: true}
	h.s.job.Store = ds
	err := h.s.job.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("err = %v", err)
	}
	if v := h.version("20261005"); v.Status != models.BasemapFailed {
		t.Fatalf("status %s: a denied copy must not stay resumable", v.Status)
	}
	if ds.parts != h.s.job.Concurrency && ds.parts > 4 {
		t.Errorf("%d part attempts: AccessDenied was retried", ds.parts)
	}
	// Fixed credentials: the next run starts fresh rather than resuming.
	ds.denyAll = false
	if err := h.s.job.Run(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if h.version("20261005").Status != models.BasemapActive {
		t.Fatal("not active after the credentials were fixed")
	}
}

func TestCleanupContinuesPastAnUploadItMayNotAbort(t *testing.T) {
	h := setup(t)
	st := h.s.job.Store.(*DirStore)
	foreign, _ := st.CreateUpload(context.Background(), testPrefix+"/20260901.pmtiles")
	stale, _ := st.CreateUpload(context.Background(), testPrefix+"/20260902.pmtiles")
	old := time.Now().Add(-72 * time.Hour)
	os.Chtimes(st.uploadDir(foreign), old, old)
	os.Chtimes(st.uploadDir(stale), old, old)
	h.now = time.Now()
	h.s.job.Store = &deniedStore{Store: st, deny: map[string]bool{foreign: true}}

	_, aborted, err := h.s.job.Cleanup(context.Background())
	if aborted != 1 || err == nil || !strings.Contains(err.Error(), foreign) {
		t.Fatalf("aborted %d, err %v", aborted, err)
	}
	if _, err := os.Stat(st.uploadDir(stale)); !os.IsNotExist(err) {
		t.Error("the abortable upload was left because another one failed")
	}
}
