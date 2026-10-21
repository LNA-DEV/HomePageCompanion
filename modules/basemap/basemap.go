// Package basemap is the basemap module: it serves the site's vector
// basemap and keeps it current.
//
// A Protomaps planet build (one PMTiles file, ~139 GB, zoom 0–15) is copied
// monthly from the daily builds into the module's blob area and served from
// there as z/x/y tiles through go-pmtiles. The browser only ever talks to
// the companion; the bucket stays private. Design: Home-Page
// docs/concepts/self-hosted-maps.md §2–§5.
package basemap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/protomaps/go-pmtiles/pmtiles"
)

const (
	defaultSource      = "https://build.protomaps.com"
	defaultPartSizeMiB = 64
	defaultConcurrency = 4
	defaultCacheSizeMB = 64
	defaultRetainDays  = 7
	tilesPath          = "/api/tiles/basemap"
)

func init() {
	companion.Register(companion.Info{
		ID:   "basemap",
		Name: "Basemap",
		New:  func() companion.Instance { return new(Basemap) },
		// The old companion kept its versions under maps/basemap/; the area
		// stays, so a migrated installation serves them without a copy.
		BlobArea: "maps/basemap",
		Start:    migrate,
		Routes:   routes,
		Aliases:  []string{tilesPath, tilesPath + ".json"},
		Import:   importLegacy,
		UI: &companion.UI{
			Nav:   []companion.NavEntry{{Label: "Basemap", Icon: "map-pinned", Page: "", Group: "Website", Order: 50}},
			Pages: []companion.Page{{Path: "", Element: "basemap-page", Title: "Basemap"}},
		},
	})
}

func migrate(mc companion.ModuleContext) error {
	if mc.DB() == nil {
		return errors.New("basemap: no database")
	}
	return mc.DB().AutoMigrate(&Version{}, &Part{})
}

// Basemap is the module's one instance.
type Basemap struct {
	// Source is the base URL of the daily builds, "<Source>/<YYYYMMDD>.pmtiles".
	Source string `json:"source"`
	// SchemaMajor is the tile schema the site's style is written for. A
	// build whose metadata "version" has another major is refused before
	// copying (4 when 0).
	SchemaMajor int `json:"schemaMajor"`
	// Schedule is a cron spec with seconds. Empty: manual runs only.
	Schedule string `json:"schedule"`
	// RetainDays keeps the previous version this long after a switch, so a
	// browser holding the old TileJSON keeps getting tiles.
	RetainDays int `json:"retainDays"`
	// PublicURL is the tile base as browsers see it; TileJSON's tiles URL is
	// "<PublicURL>/<version>/{z}/{x}/{y}.mvt". Default: the companion's
	// public URL + /api/tiles/basemap.
	PublicURL string `json:"publicUrl"`
	// PartSizeMiB and Concurrency shape the multipart copy (64 and 4 when 0).
	PartSizeMiB int `json:"partSizeMiB"`
	Concurrency int `json:"concurrency"`
	// CacheSizeMB is go-pmtiles' directory cache (64 when 0).
	CacheSizeMB int `json:"cacheSizeMB"`

	ctx    companion.Context
	server *pmtiles.Server
	job    *Job

	mu       sync.RWMutex
	active   string
	servable map[string]bool

	runMu  sync.Mutex
	runCtx context.Context
	cancel context.CancelFunc
	runs   sync.WaitGroup
}

func (b *Basemap) Provision(ctx companion.Context) error {
	b.ctx = ctx
	if b.Source == "" {
		b.Source = defaultSource
	}
	if b.SchemaMajor == 0 {
		b.SchemaMajor = 4
	}
	if b.RetainDays <= 0 {
		b.RetainDays = defaultRetainDays
	}
	if b.PartSizeMiB <= 0 {
		b.PartSizeMiB = defaultPartSizeMiB
	}
	if b.Concurrency <= 0 {
		b.Concurrency = defaultConcurrency
	}
	if b.CacheSizeMB <= 0 {
		b.CacheSizeMB = defaultCacheSizeMB
	}
	if b.PublicURL == "" && ctx.PublicURL() != "" {
		b.PublicURL = strings.TrimRight(ctx.PublicURL(), "/") + tilesPath
	}
	b.PublicURL = strings.TrimRight(b.PublicURL, "/")
	if err := migrate(ctx); err != nil {
		return err
	}
	b.server = tileServer(ctx, b.PublicURL, b.CacheSizeMB)
	b.job = &Job{
		Bucket:      ctx.Blobs,
		DB:          ctx.DB,
		Log:         ctx.Log(),
		Source:      Source{BaseURL: b.Source, Client: &http.Client{Transport: ctx.HTTP().Transport, Timeout: 10 * time.Minute}},
		SchemaMajor: b.SchemaMajor,
		PartSize:    int64(b.PartSizeMiB) << 20,
		Concurrency: b.Concurrency,
		RetainDays:  b.RetainDays,
		Now:         time.Now,
		Verify:      b.verifyServing,
		OnChange:    b.reload,
	}
	b.reload()
	return nil
}

func (b *Basemap) Validate() error {
	if b.PublicURL == "" {
		return errors.New("basemap: publicUrl is needed (or core.publicUrl): TileJSON names the tiles with it")
	}
	return nil
}

// Start resumes a copy that a crash or restart cut short.
func (b *Basemap) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	b.runMu.Lock()
	b.runCtx, b.cancel = ctx, cancel
	b.runMu.Unlock()
	var n int64
	b.ctx.DB().Model(&Version{}).Where("status IN ?", []string{StatusCopying, StatusVerifying}).Count(&n)
	if n > 0 {
		b.ctx.Log().Print("resuming an interrupted copy")
		b.startUpdate(ctx)
	}
	return nil
}

// Stop interrupts a running copy; its parts are kept, and the next start
// resumes it.
func (b *Basemap) Stop(ctx context.Context) error {
	b.runMu.Lock()
	if b.cancel != nil {
		b.cancel()
	}
	b.runMu.Unlock()
	done := make(chan struct{})
	go func() { b.runs.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startUpdate runs an update in the background unless one is running.
func (b *Basemap) startUpdate(ctx context.Context) {
	b.runs.Add(1)
	go func() {
		defer b.runs.Done()
		if err := b.job.Run(ctx); err != nil && !errors.Is(err, ErrRunning) {
			b.ctx.Log().Print(err)
		}
	}()
}

// update starts an update from the admin page; it ends with the instance.
func (b *Basemap) update() {
	b.runMu.Lock()
	ctx := b.runCtx
	b.runMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	b.startUpdate(ctx)
}

func (b *Basemap) Jobs() []companion.Job {
	return []companion.Job{
		// A 139 GB copy takes hours; an interrupted one resumes.
		{Name: "update", Schedule: b.Schedule, Timeout: 72 * time.Hour, Run: func(ctx context.Context) error {
			if err := b.job.Run(ctx); err != nil && !errors.Is(err, ErrRunning) {
				return err
			}
			return nil
		}},
		{Name: "cleanup", Schedule: "0 30 4 * * *", Timeout: time.Hour, Run: func(ctx context.Context) error {
			_, _, err := b.job.Cleanup(ctx)
			return err
		}},
	}
}

// ---- the tile server ----

// tileServer reads through the module's blob area. go-pmtiles' server has
// no Stop: a config change that replaces the instance leaves the old one's
// cache goroutine idle.
func tileServer(mc companion.ModuleContext, publicURL string, cacheMB int) *pmtiles.Server {
	logger := log.New(mc.Log().Writer(), mc.Log().Prefix()+"pmtiles: ", 0)
	s, _ := pmtiles.NewServerWithBucket(blobBucket{mc}, "", logger, cacheMB, publicURL)
	s.Start()
	return s
}

// blobBucket is go-pmtiles' Bucket on the module's blob area. Versions are
// immutable (a new build gets a new name), so there is no ETag to check.
type blobBucket struct{ mc companion.ModuleContext }

func (blobBucket) Close() error { return nil }

func (b blobBucket) NewRangeReader(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	r, _, _, err := b.NewRangeReaderEtag(ctx, key, offset, length, "")
	return r, err
}

func (b blobBucket) NewRangeReaderEtag(ctx context.Context, key string, offset, length int64, _ string) (io.ReadCloser, string, int, error) {
	data, err := b.mc.Blobs().ReadRange(ctx, key, offset, length)
	if errors.Is(err, companion.ErrNotFound) {
		return nil, "", http.StatusNotFound, err
	}
	if err != nil {
		return nil, "", http.StatusInternalServerError, err
	}
	return io.NopCloser(bytes.NewReader(data)), "", http.StatusPartialContent, nil
}

// reload re-reads which versions may be served: the active one and the
// retained ones.
func (b *Basemap) reload() {
	var rows []Version
	b.ctx.DB().Where("status IN ?", []string{StatusActive, StatusRetained}).Find(&rows)
	servable := map[string]bool{}
	active := ""
	for _, r := range rows {
		servable[r.Version] = true
		if r.Status == StatusActive {
			active = r.Version
		}
	}
	b.mu.Lock()
	b.servable, b.active = servable, active
	b.mu.Unlock()
}

func (b *Basemap) activeVersion() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.active
}

func (b *Basemap) canServe(version string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.servable[version]
}

// sampleTiles are fetched through the serving path before a version goes
// live: the whole world, and Munich at z8 and z15, tiles that exist in every
// planet build, so an empty answer means a broken copy.
func sampleTiles() [][3]uint32 {
	tiles := [][3]uint32{{0, 0, 0}}
	for _, z := range []uint8{8, 15} {
		x, y := lonLatToTile(11.5755, 48.1374, z)
		tiles = append(tiles, [3]uint32{uint32(z), x, y})
	}
	return tiles
}

func lonLatToTile(lon, lat float64, z uint8) (uint32, uint32) {
	n := math.Exp2(float64(z))
	x := (lon + 180) / 360 * n
	r := lat * math.Pi / 180
	y := (1 - math.Log(math.Tan(r)+1/math.Cos(r))/math.Pi) / 2 * n
	return uint32(x), uint32(y)
}

// verifyServing reads a new version through exactly the code that will
// serve it: its TileJSON, and the sample tiles, each a non-empty gzip
// stream.
func (b *Basemap) verifyServing(ctx context.Context, version string) error {
	if status, _, body := b.server.Get(ctx, "/"+version+".json"); status != http.StatusOK {
		return fmt.Errorf("TileJSON: HTTP %d %s", status, body)
	}
	for _, t := range sampleTiles() {
		status, _, body := b.server.Get(ctx, fmt.Sprintf("/%s/%d/%d/%d.mvt", version, t[0], t[1], t[2]))
		if status != http.StatusOK {
			return fmt.Errorf("tile %d/%d/%d: HTTP %d", t[0], t[1], t[2], status)
		}
		if len(body) < 3 || body[0] != 0x1f || body[1] != 0x8b {
			return fmt.Errorf("tile %d/%d/%d: not a gzip stream (%d bytes)", t[0], t[1], t[2], len(body))
		}
	}
	return nil
}
