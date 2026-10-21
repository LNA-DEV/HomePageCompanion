package basemap

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/protomaps/go-pmtiles/pmtiles"
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
	failAfter atomic.Int64
	ranges    atomic.Int64
	srv       *httptest.Server
	agents    sync.Map
}

func newFakeSource(t *testing.T) *fakeSource {
	f := &fakeSource{builds: map[string][]byte{}, etags: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.agents.Store(r.UserAgent(), true)
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
			if fa := f.failAfter.Load(); fa > 0 && n > fa {
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
	h   *companiontest.Harness
	src *fakeSource
	b   *Basemap
	mu  sync.Mutex
	now time.Time
}

func config(src string, extra string) string {
	return fmt.Sprintf("modules:\n  basemap:\n    config: { source: %q, publicUrl: \"https://companion.test/api/tiles/basemap\"%s }\n", src, extra)
}

func setup(t *testing.T) *harness {
	t.Helper()
	src := newFakeSource(t)
	h := &harness{t: t, src: src, now: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	h.h = companiontest.New(t, config(src.srv.URL, ""))
	h.attach()
	return h
}

// attach takes the running instance and shrinks its copy to test size.
func (h *harness) attach() {
	h.b = companiontest.Instance[*Basemap](h.h, "basemap:default")
	j := h.b.job
	j.PartSize, j.Concurrency, j.Retries, j.RetryPause = 100, 3, 2, time.Millisecond
	j.Now = h.clock
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

func (h *harness) version(v string) Version {
	h.t.Helper()
	var row Version
	if h.b.ctx.DB().Where("version = ?", v).Limit(1).Find(&row).RowsAffected == 0 {
		h.t.Fatalf("no row for %s", v)
	}
	return row
}

func (h *harness) get(path string) *httptest.ResponseRecorder { return h.h.Public("GET", path, nil) }

func (h *harness) object(v string) ([]byte, bool) {
	rc, _, err := h.b.ctx.Blobs().Get(context.Background(), v+".pmtiles")
	if err != nil {
		return nil, false
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	return data, true
}

func (h *harness) uploads() []companion.Upload {
	ups, _ := h.b.ctx.Blobs().ListUploads(context.Background(), "")
	return ups
}

func (h *harness) run() error { return h.b.job.Run(context.Background()) }

// ---- tests ----

func TestRunCopiesVerifiesAndServes(t *testing.T) {
	h := setup(t)
	src := archive(t, "4.15.2", true)
	h.src.put("20261005", src, `"etag-a"`)

	if err := h.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	v := h.version("20261005")
	if v.Status != StatusActive || v.SchemaVersion != "4.15.2" || v.OSMTime != "2026-10-05T04:00:00Z" || v.Key != "20261005.pmtiles" {
		t.Fatalf("row = %+v", v)
	}
	if v.PartsTotal != (len(src)+99)/100 {
		t.Errorf("parts = %d for %d bytes", v.PartsTotal, len(src))
	}
	if got, ok := h.object("20261005"); !ok || !bytes.Equal(got, src) {
		t.Fatal("copied object differs from the source")
	}
	var leftover int64
	h.b.ctx.DB().Model(&Part{}).Count(&leftover)
	if leftover != 0 {
		t.Errorf("%d part rows left after completion", leftover)
	}
	// The build host sees the companion's User-Agent.
	h.src.agents.Range(func(k, _ any) bool {
		if !strings.HasPrefix(k.(string), "HomePageCompanion/") {
			t.Errorf("User-Agent %q", k)
		}
		return true
	})

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
	if err := h.run(); err != nil {
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
	h.b.job.Concurrency = 1
	h.src.failAfter.Store(6) // header + metadata + 4 parts, then the host fails

	if err := h.run(); err == nil {
		t.Fatal("run should fail mid-copy")
	}
	v := h.version("20261005")
	var done int64
	h.b.ctx.DB().Model(&Part{}).Where("version_id = ?", v.ID).Count(&done)
	if v.Status != StatusCopying || done == 0 || int(done) >= v.PartsTotal {
		t.Fatalf("after the failure: status %s, %d of %d parts", v.Status, done, v.PartsTotal)
	}

	h.src.failAfter.Store(0)
	before := h.src.ranges.Load()
	if err := h.run(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	// Only the missing parts are fetched again, plus the source header for
	// the verification.
	if fetched := h.src.ranges.Load() - before; fetched != int64(v.PartsTotal)-done+1 {
		t.Errorf("resume fetched %d ranges, want %d", fetched, int64(v.PartsTotal)-done+1)
	}
	if got, _ := h.object("20261005"); !bytes.Equal(got, src) {
		t.Fatal("resumed object differs from the source")
	}
	if h.version("20261005").Status != StatusActive {
		t.Fatal("resumed version not active")
	}
}

// A restart (here: a config change replacing the instance) resumes an
// interrupted copy on its own.
func TestStartResumesAnInterruptedCopy(t *testing.T) {
	h := setup(t)
	src := archive(t, "4.15.2", true)
	h.src.put("20261005", src, `"etag-a"`)
	h.b.job.Concurrency = 1
	h.src.failAfter.Store(6)
	h.run()
	if h.version("20261005").Status != StatusCopying {
		t.Fatal("not interrupted")
	}
	h.src.failAfter.Store(0)

	// The new instance copies with the default 64 MiB parts; the recorded
	// part size of the version decides, so the copy continues.
	if err := h.h.Rewrite(config(h.src.srv.URL, ", retainDays: 3")); err != nil {
		t.Fatal(err)
	}
	b := companiontest.Instance[*Basemap](h.h, "basemap:default")
	deadline := time.Now().Add(5 * time.Second)
	for b.activeVersion() != "20261005" {
		if time.Now().After(deadline) {
			t.Fatalf("not resumed: %+v", b.job.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, _ := h.object("20261005"); !bytes.Equal(got, src) {
		t.Fatal("resumed object differs from the source")
	}
}

func TestRunRefusesAnotherSchemaMajor(t *testing.T) {
	h := setup(t)
	h.src.put("20261005", archive(t, "5.0.0", true), `"e"`)
	err := h.run()
	if err == nil || !strings.Contains(err.Error(), "schema 5.0.0") {
		t.Fatalf("err = %v", err)
	}
	if len(h.uploads()) != 0 {
		t.Error("an upload was started for a refused build")
	}
	if st := h.b.job.Status(); st.LastErr == "" {
		t.Error("the refusal is not in the job status")
	}
}

func TestRunGivesUpWhenTheSourceChangesUnderACopy(t *testing.T) {
	h := setup(t)
	h.src.put("20261005", archive(t, "4.15.2", true), `"etag-a"`)
	h.b.job.Concurrency = 1
	h.src.failAfter.Store(5)
	h.run()

	h.src.failAfter.Store(0)
	h.src.put("20261005", archive(t, "4.15.2", true), `"etag-b"`) // re-published
	if err := h.run(); err != nil {
		t.Fatalf("second run: %v", err)
	}
	// The interrupted copy is failed and its upload aborted; the build was
	// then found afresh and copied from the start.
	if v := h.version("20261005"); v.Status != StatusActive || v.SourceETag != `"etag-b"` {
		t.Fatalf("row = %+v", v)
	}
	if ups := h.uploads(); len(ups) != 0 {
		t.Errorf("%d uploads left behind", len(ups))
	}
}

func TestVerificationFailureDeletesTheCopy(t *testing.T) {
	h := setup(t)
	h.src.put("20261005", archive(t, "4.15.2", false), `"e"`) // no sample tiles
	if err := h.run(); err == nil {
		t.Fatal("verification should fail")
	}
	if v := h.version("20261005"); v.Status != StatusFailed {
		t.Fatalf("status %s", v.Status)
	}
	if _, ok := h.object("20261005"); ok {
		t.Error("the failed copy was not deleted")
	}
	if w := h.get("/api/tiles/basemap.json"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("TileJSON with nothing active: %d", w.Code)
	}
}

func TestNewVersionRetainsTheOldOneUntilCleanup(t *testing.T) {
	h := setup(t)
	h.src.put("20261005", archive(t, "4.15.2", true), `"a"`)
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.advance(31 * 24 * time.Hour) // a month later
	h.src.put("20261105", archive(t, "4.16.0", true), `"b"`)
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	old := h.version("20261005")
	if old.Status != StatusRetained || old.DeleteAfter == nil {
		t.Fatalf("old version: %+v", old)
	}
	if h.version("20261105").Status != StatusActive {
		t.Fatal("new version not active")
	}
	// Browsers holding the old TileJSON still get tiles.
	if w := h.get("/api/tiles/basemap/20261005/1/1/1.mvt"); w.Code != 200 {
		t.Fatalf("retained version not served: %d", w.Code)
	}

	if d, _, _ := h.b.job.Cleanup(context.Background()); d != 0 {
		t.Fatal("cleanup deleted a version before its time")
	}
	h.advance(8 * 24 * time.Hour)
	w := h.h.Admin("POST", "/api/b/basemap/cleanup", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"deleted":1`) {
		t.Fatalf("cleanup: %d %s", w.Code, w.Body)
	}
	if _, ok := h.object("20261005"); ok {
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
	ctx := context.Background()
	mem := companiontest.Instance[*companiontest.MemBlob](h.h, companiontest.BlobID+":default")
	bucket := h.b.ctx.Blobs()
	stale, _ := bucket.CreateUpload(ctx, "20260901.pmtiles", "")
	fresh, _ := bucket.CreateUpload(ctx, "20260902.pmtiles", "")
	other := mem.Bucket("other-module")
	foreign, _ := other.CreateUpload(ctx, "thing.bin", "")
	mem.Backdate(stale, 72*time.Hour)
	mem.Backdate(foreign, 72*time.Hour)
	h.b.job.Now = time.Now

	_, aborted, err := h.b.job.Cleanup(ctx)
	if err != nil || aborted != 1 {
		t.Fatalf("aborted %d, err %v", aborted, err)
	}
	left := map[string]bool{}
	for _, u := range h.uploads() {
		left[u.UploadID] = true
	}
	if ups, _ := other.ListUploads(ctx, ""); len(ups) == 1 {
		left[ups[0].UploadID] = true
	}
	for id, want := range map[string]bool{stale: false, fresh: true, foreign: true} {
		if left[id] != want {
			t.Errorf("upload %s exists=%v, want %v", id, left[id], want)
		}
	}
}

func TestFindTakesTheNewestBuildOfTheWeek(t *testing.T) {
	h := setup(t)
	h.src.put("20261001", []byte("x"), "")
	h.src.put("20261003", []byte("y"), "")
	h.src.put("20260920", []byte("z"), "") // older than a week
	b, err := h.b.job.Source.Find(context.Background(), h.clock())
	if err != nil || b.Version != "20261003" {
		t.Fatalf("found %+v, %v", b, err)
	}
	h.src.mu.Lock()
	h.src.builds = map[string][]byte{"20260920": []byte("z")}
	h.src.mu.Unlock()
	if _, err := h.b.job.Source.Find(context.Background(), h.clock()); err == nil {
		t.Error("a build older than a week was found")
	}
}

// deniedBucket answers AccessDenied for the parts of the uploads in deny,
// what S3 says when the requester did not initiate the upload, e.g. after
// the credentials were replaced between a copy and its resumption. The blob
// module marks it ErrUploadGone.
type deniedBucket struct {
	companion.Bucket
	mu      sync.Mutex
	deny    map[string]bool
	denyAll bool
	parts   int
	aborts  int
}

func accessDenied() error {
	return fmt.Errorf("%w: api error AccessDenied: UnknownError", companion.ErrUploadGone)
}

func (d *deniedBucket) UploadPart(ctx context.Context, key, uploadID string, n int32, data []byte) (string, error) {
	d.mu.Lock()
	d.parts++
	denied := d.denyAll || d.deny[uploadID]
	d.mu.Unlock()
	if denied {
		return "", accessDenied()
	}
	return d.Bucket.UploadPart(ctx, key, uploadID, n, data)
}

func (d *deniedBucket) AbortUpload(ctx context.Context, key, uploadID string) error {
	d.mu.Lock()
	d.aborts++
	denied := d.denyAll || d.deny[uploadID]
	d.mu.Unlock()
	if denied {
		return accessDenied()
	}
	return d.Bucket.AbortUpload(ctx, key, uploadID)
}

func (d *deniedBucket) set(all bool) {
	d.mu.Lock()
	d.denyAll = all
	d.mu.Unlock()
}

func (h *harness) deny(ds *deniedBucket) {
	ds.Bucket = h.b.ctx.Blobs()
	h.b.job.Bucket = func() companion.Bucket { return ds }
}

func TestARefusedResumeStartsTheCopyOver(t *testing.T) {
	h := setup(t)
	src := archive(t, "4.15.2", true)
	h.src.put("20261005", src, `"etag-a"`)
	h.b.job.Concurrency = 1
	h.src.failAfter.Store(5)
	if err := h.run(); err == nil {
		t.Fatal("first run should be interrupted")
	}
	old := h.version("20261005")
	if old.Status != StatusCopying {
		t.Fatalf("after the interruption: %s", old.Status)
	}

	// New credentials: the bucket refuses the old upload's parts, and its abort.
	h.src.failAfter.Store(0)
	ds := &deniedBucket{deny: map[string]bool{old.UploadID: true}}
	h.deny(ds)
	if err := h.run(); err != nil {
		t.Fatalf("the run should recover by starting over: %v", err)
	}
	v := h.version("20261005")
	if v.Status != StatusActive || v.UploadID == old.UploadID {
		t.Fatalf("after recovery: status %s, upload %s (old %s)", v.Status, v.UploadID, old.UploadID)
	}
	if got, _ := h.object("20261005"); !bytes.Equal(got, src) {
		t.Fatal("the fresh copy differs from the source")
	}
	if ds.parts > v.PartsTotal+1 {
		t.Errorf("%d part uploads for %d parts: the refused one was retried", ds.parts, v.PartsTotal)
	}
}

func TestAnAccessDeniedFreshCopyFailsInsteadOfStickingInCopying(t *testing.T) {
	h := setup(t)
	h.src.put("20261005", archive(t, "4.15.2", true), `"e"`)
	ds := &deniedBucket{denyAll: true}
	h.deny(ds)
	err := h.run()
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("err = %v", err)
	}
	if v := h.version("20261005"); v.Status != StatusFailed {
		t.Fatalf("status %s: a denied copy must not stay resumable", v.Status)
	}
	if ds.parts > h.b.job.Concurrency+1 {
		t.Errorf("%d part attempts: AccessDenied was retried", ds.parts)
	}
	// Fixed credentials: the next run starts fresh rather than resuming.
	ds.set(false)
	if err := h.run(); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if h.version("20261005").Status != StatusActive {
		t.Fatal("not active after the credentials were fixed")
	}
}

func TestCleanupContinuesPastAnUploadItMayNotAbort(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	mem := companiontest.Instance[*companiontest.MemBlob](h.h, companiontest.BlobID+":default")
	bucket := h.b.ctx.Blobs()
	foreign, _ := bucket.CreateUpload(ctx, "20260901.pmtiles", "")
	stale, _ := bucket.CreateUpload(ctx, "20260902.pmtiles", "")
	mem.Backdate(foreign, 72*time.Hour)
	mem.Backdate(stale, 72*time.Hour)
	h.b.job.Now = time.Now
	h.deny(&deniedBucket{deny: map[string]bool{foreign: true}})

	_, aborted, err := h.b.job.Cleanup(ctx)
	if aborted != 1 || err == nil || !strings.Contains(err.Error(), foreign) {
		t.Fatalf("aborted %d, err %v", aborted, err)
	}
	for _, u := range h.uploads() {
		if u.UploadID == stale {
			t.Error("the abortable upload was left because another one failed")
		}
	}
}

func TestAdminUpdateAndStatus(t *testing.T) {
	h := setup(t)
	h.src.put("20261005", archive(t, "4.15.2", true), `"a"`)
	if w := h.h.Admin("POST", "/api/b/basemap/update", nil); w.Code != http.StatusAccepted {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.b.activeVersion() == "" {
		if time.Now().After(deadline) {
			t.Fatalf("update did not finish: %+v", h.b.job.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	var st struct {
		Enabled  bool
		Active   string
		Versions []Version
		Job      Status
	}
	w := h.h.Admin("GET", "/api/b/basemap", nil)
	json.Unmarshal(w.Body.Bytes(), &st)
	if !st.Enabled || st.Active != "20261005" || len(st.Versions) != 1 || st.Job.LastRun == nil {
		t.Fatalf("status %s", w.Body)
	}
	if h.h.Public("GET", "/api/b/basemap", nil).Code != http.StatusUnauthorized {
		t.Fatal("status without admin")
	}
}

func TestPublicURLDefaultsToTheCompanions(t *testing.T) {
	h := companiontest.New(t, "modules:\n  basemap: {}\n")
	b := companiontest.Instance[*Basemap](h, "basemap:default")
	if b.PublicURL != "http://companion.test/api/tiles/basemap" {
		t.Fatalf("publicUrl %q", b.PublicURL)
	}
}

func TestImportsTheOldVersions(t *testing.T) {
	h := setup(t)
	src := archive(t, "4.15.2", true)
	// The objects are already in the area: it is the old maps/basemap/.
	if err := h.b.ctx.Blobs().Put(context.Background(), "20261005.pmtiles", bytes.NewReader(src), int64(len(src)), ""); err != nil {
		t.Fatal(err)
	}
	old := companiontest.LegacyDB(t)
	old.DB.AutoMigrate(&Version{}, &Part{})
	old.DB.Exec(`INSERT INTO basemap_versions (id, version, key, size, status, schema_version, upload_id, part_size, parts_total, created_at, updated_at)
		VALUES (3, '20261005', 'home-page-companion/maps/basemap/20261005.pmtiles', ?, 'active', '4.15.2', '', 100, 1, '2026-10-05', '2026-10-05'),
		       (4, '20261105', 'home-page-companion/maps/basemap/20261105.pmtiles', 999, 'copying', '4.16.0', 'upload-x', 100, 10, '2026-11-05', '2026-11-05')`, len(src))
	old.DB.Exec(`INSERT INTO basemap_parts (version_id, number, e_tag, size) VALUES (4, 1, '"1-100"', 100), (4, 2, '"2-100"', 100)`)

	info, _ := companion.Lookup("basemap")
	if err := info.Import(h.h.Module("basemap"), old); err != nil {
		t.Fatal(err)
	}
	if v := h.version("20261105"); v.Key != "20261105.pmtiles" || v.UploadID != "upload-x" || v.Status != StatusCopying {
		t.Fatalf("interrupted copy %+v", v)
	}
	var parts int64
	h.b.ctx.DB().Model(&Part{}).Where("version_id = ?", 4).Count(&parts)
	if parts != 2 {
		t.Fatalf("%d parts", parts)
	}
	h.b.reload()
	if w := h.get("/api/tiles/basemap/20261005/1/1/1.mvt"); w.Code != 200 {
		t.Fatalf("imported version not served: %d", w.Code)
	}
}
