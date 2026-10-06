// Package basemap serves the site's vector basemap and keeps it current.
//
// A Protomaps planet build (one PMTiles file, ~139 GB, zoom 0–15) is copied
// monthly from the daily builds into an S3 bucket and served from there as
// z/x/y tiles through go-pmtiles. The browser only ever talks to this
// companion; the bucket stays private. Design: Home-Page
// docs/concepts/self-hosted-maps.md §2–§5.
package basemap

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/LNA-DEV/HomePageCompanion/storage"
	"github.com/protomaps/go-pmtiles/pmtiles"
	// go-pmtiles opens cloud buckets through gocloud, whose drivers register
	// themselves on import; its CLI imports them, the library does not.
	_ "gocloud.dev/blob/s3blob"
)

const (
	defaultPartSizeMiB = 64
	defaultConcurrency = 4
	defaultCacheSizeMB = 64
	defaultRetainDays  = 7
	// module is this package's folder under the companion's storage prefix:
	// home-page-companion/maps/basemap/<YYYYMMDD>.pmtiles.
	module = "maps/basemap"
)

// service is the process-wide state: the tile server, the job and which
// versions may be served.
type service struct {
	cfg       config.Basemap
	bucketURL string
	prefix    string
	server    *pmtiles.Server
	job       *Job

	mu       sync.RWMutex
	active   string
	servable map[string]bool
}

var svc *service

// Enabled reports whether Init found a bucket to work with.
func Enabled() bool { return svc != nil }

// Init opens the bucket and the tile server from config.Data.Basemap and
// config.Data.Storage. Unless the basemap is enabled and a storage bucket is
// configured, the feature stays off and every route answers 503.
func Init(ctx context.Context) error {
	cfg := withDefaults(config.Data.Basemap)
	if !cfg.Enabled {
		log.Print("basemap: not enabled")
		return nil
	}
	rawBucket := storage.BucketURL()
	if rawBucket == "" {
		return fmt.Errorf("basemap: enabled, but storage.bucketUrl is not set")
	}
	keyPrefix := storage.Prefix(module)
	bucketURL, prefix := serverBucket(rawBucket, keyPrefix)
	bucket, err := pmtiles.OpenBucket(ctx, bucketURL, prefix)
	if err != nil {
		return fmt.Errorf("basemap: open bucket: %w", err)
	}
	logger := log.New(log.Writer(), "basemap/pmtiles: ", log.LstdFlags)
	server, err := pmtiles.NewServerWithBucket(bucket, "", logger, cfg.CacheSizeMB, strings.TrimRight(cfg.PublicURL, "/"))
	if err != nil {
		return fmt.Errorf("basemap: tile server: %w", err)
	}
	server.Start()

	store, err := OpenStore(ctx, rawBucket)
	if err != nil {
		return fmt.Errorf("basemap: store: %w", err)
	}
	s := &service{cfg: cfg, bucketURL: rawBucket, prefix: keyPrefix, server: server}
	s.job = &Job{
		Store:       store,
		Source:      Source{BaseURL: cfg.Source, Client: &http.Client{Timeout: 10 * time.Minute}},
		Prefix:      keyPrefix,
		SchemaMajor: cfg.SchemaMajor,
		PartSize:    int64(cfg.PartSizeMiB) << 20,
		Concurrency: cfg.Concurrency,
		RetainDays:  cfg.RetainDays,
		Now:         time.Now,
		Verify:      s.verifyServing,
		OnChange:    s.reload,
	}
	svc = s
	s.reload()
	log.Printf("basemap: enabled, bucket %s, prefix %q, active version %q", redactBucket(rawBucket), keyPrefix, s.activeVersion())
	return nil
}

func withDefaults(c config.Basemap) config.Basemap {
	if c.Source == "" {
		c.Source = defaultSource
	}
	if c.SchemaMajor == 0 {
		c.SchemaMajor = 4
	}
	if c.RetainDays <= 0 {
		c.RetainDays = defaultRetainDays
	}
	if c.PartSizeMiB <= 0 {
		c.PartSizeMiB = defaultPartSizeMiB
	}
	if c.Concurrency <= 0 {
		c.Concurrency = defaultConcurrency
	}
	if c.CacheSizeMB <= 0 {
		c.CacheSizeMB = defaultCacheSizeMB
	}
	return c
}

// serverBucket adapts the bucket URL for go-pmtiles: it applies a prefix to
// cloud buckets but ignores it for file:// ones, so there it goes into the
// path instead.
func serverBucket(bucketURL, prefix string) (string, string) {
	if strings.HasPrefix(bucketURL, "file://") {
		return "file://" + path.Join(strings.TrimPrefix(bucketURL, "file://"), prefix), ""
	}
	return bucketURL, prefix
}

// redactBucket keeps credentials out of the log should anyone put them into
// the URL.
func redactBucket(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparsable)"
	}
	u.User = nil
	return u.String()
}

// reload re-reads which versions may be served: the active one and the
// retained ones.
func (s *service) reload() {
	var rows []models.BasemapVersion
	database.Db.Where("status IN ?", []string{models.BasemapActive, models.BasemapRetained}).Find(&rows)
	servable := map[string]bool{}
	active := ""
	for _, r := range rows {
		servable[r.Version] = true
		if r.Status == models.BasemapActive {
			active = r.Version
		}
	}
	s.mu.Lock()
	s.servable, s.active = servable, active
	s.mu.Unlock()
}

func (s *service) activeVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

func (s *service) canServe(version string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.servable[version]
}

// sampleTiles are fetched through the serving path before a version goes
// live: the whole world, and Munich at z8 and z15 — tiles that exist in every
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

// verifyServing reads a new version through exactly the code that will serve
// it: its TileJSON, and the sample tiles, each a non-empty gzip stream.
func (s *service) verifyServing(ctx context.Context, version string) error {
	if status, _, body := s.server.Get(ctx, "/"+version+".json"); status != http.StatusOK {
		return fmt.Errorf("TileJSON: HTTP %d %s", status, body)
	}
	for _, t := range sampleTiles() {
		status, _, body := s.server.Get(ctx, fmt.Sprintf("/%s/%d/%d/%d.mvt", version, t[0], t[1], t[2]))
		if status != http.StatusOK {
			return fmt.Errorf("tile %d/%d/%d: HTTP %d", t[0], t[1], t[2], status)
		}
		if len(body) < 3 || body[0] != 0x1f || body[1] != 0x8b {
			return fmt.Errorf("tile %d/%d/%d: not a gzip stream (%d bytes)", t[0], t[1], t[2], len(body))
		}
	}
	return nil
}
