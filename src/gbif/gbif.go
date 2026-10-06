// Package gbif proxies GBIF's occurrence-density tiles for the dex map, so a
// visitor's browser never asks api.gbif.org itself, and keeps them in a disk
// cache (Home-Page docs/concepts/self-hosted-maps.md §8).
//
// GBIF answers with "Cache-Control: public, max-age=1200" and a dated weak
// ETag, so a shared cache is what it expects. A tile younger than max-age is
// served from disk; an older one is revalidated with If-None-Match; when GBIF
// is unreachable the stale tile is served rather than none. Empty tiles come
// back as 204 and are cached as such.
package gbif

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/basemap"
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
)

const (
	defaultBaseURL = "https://api.gbif.org/v2/map/occurrence/density"
	defaultMaxAge  = 1200 * time.Second
	maxZoom        = 14
)

// fixedQuery is the one parameter set the dex map uses; no query string from
// the client is ever passed upstream.
const fixedQuery = "srs=EPSG%3A3857&style=classic.poly&bin=hex&hexPerTile=110"

var taxonPattern = regexp.MustCompile(`^[0-9]{1,10}$`)

// Proxy is the tile proxy with its cache directory.
type Proxy struct {
	BaseURL  string
	Dir      string
	CapBytes int64
	Client   *http.Client
	Now      func() time.Time

	group singleflight.Group
	mu    sync.Mutex // serialises writes of one tile's files
}

// Default is the proxy main wires up.
var Default *Proxy

// Init builds Default from config.Data.Gbif.
func Init() {
	c := config.Data.Gbif
	p := &Proxy{
		BaseURL:  strings.TrimRight(c.BaseURL, "/"),
		Dir:      filepath.Join("data", "cache", "gbif"),
		CapBytes: int64(c.CacheMB) << 20,
		Client:   &http.Client{Timeout: 20 * time.Second},
		Now:      time.Now,
	}
	if p.BaseURL == "" {
		p.BaseURL = defaultBaseURL
	}
	if p.CapBytes <= 0 {
		p.CapBytes = 1024 << 20
	}
	Default = p
}

// RegisterRoutes wires GET /api/tiles/gbif/:taxonKey/:z/:x/:y.png.
func RegisterRoutes(api *gin.RouterGroup) {
	api.GET("/tiles/gbif/:taxon/:z/:x/:y", func(c *gin.Context) { Default.Handle(c) })
}

// meta is stored beside each cached tile.
type meta struct {
	Status    int       `json:"status"` // 200 or 204
	ETag      string    `json:"etag,omitempty"`
	MaxAge    int       `json:"maxAge"` // seconds
	FetchedAt time.Time `json:"fetchedAt"`
}

type tileResult struct {
	status int
	body   []byte
	m      meta
	stale  bool
}

// Handle serves one tile.
func (p *Proxy) Handle(c *gin.Context) {
	taxon := c.Param("taxon")
	z, x, y, ok := basemap.ParseZXY(c.Param("z"), c.Param("x"), c.Param("y"), "png", maxZoom)
	if !ok || !taxonPattern.MatchString(taxon) {
		c.String(http.StatusNotFound, "not found")
		return
	}
	rel := filepath.Join(taxon, strconv.Itoa(int(z)), strconv.Itoa(int(x)), strconv.Itoa(int(y)))
	v, err, _ := p.group.Do(rel, func() (any, error) { return p.get(taxon, z, x, y, rel) })
	if err != nil {
		log.Printf("gbif: %s: %v", rel, err)
		c.Header("Cache-Control", "no-store")
		c.String(http.StatusBadGateway, "occurrence tiles unavailable")
		return
	}
	r := v.(tileResult)
	age := r.m.MaxAge
	if r.stale {
		age = 60 // ask again soon; GBIF was down a moment ago
	}
	c.Header("Cache-Control", fmt.Sprintf("public, max-age=%d", age))
	c.Header("Content-Type", "image/png")
	if r.status == http.StatusNoContent {
		c.Status(http.StatusNoContent)
		return
	}
	c.Data(http.StatusOK, "image/png", r.body)
}

func (p *Proxy) paths(rel string) (string, string) {
	base := filepath.Join(p.Dir, rel)
	return base + ".png", base + ".json"
}

func (p *Proxy) get(taxon string, z uint8, x, y uint32, rel string) (tileResult, error) {
	pngPath, metaPath := p.paths(rel)
	cached, haveCache := p.readCache(pngPath, metaPath)
	now := p.Now()
	if haveCache && now.Sub(cached.m.FetchedAt) < time.Duration(cached.m.MaxAge)*time.Second {
		p.touch(pngPath, metaPath)
		return cached, nil
	}

	upstream := fmt.Sprintf("%s/%d/%d/%d@1x.png?%s&taxonKey=%s", p.BaseURL, z, x, y, fixedQuery, taxon)
	req, err := http.NewRequest(http.MethodGet, upstream, nil)
	if err != nil {
		return tileResult{}, err
	}
	req.Header.Set("User-Agent", config.UserAgent())
	if haveCache && cached.m.ETag != "" {
		req.Header.Set("If-None-Match", cached.m.ETag)
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		if haveCache {
			cached.stale = true
			return cached, nil
		}
		return tileResult{}, err
	}
	defer resp.Body.Close()

	maxAge := parseMaxAge(resp.Header.Get("Cache-Control"))
	switch resp.StatusCode {
	case http.StatusNotModified:
		if !haveCache {
			return tileResult{}, errors.New("304 without a cached tile")
		}
		cached.m.FetchedAt, cached.m.MaxAge = now, maxAge
		if e := resp.Header.Get("ETag"); e != "" {
			cached.m.ETag = e
		}
		p.writeMeta(metaPath, cached.m)
		p.touch(pngPath, metaPath)
		return cached, nil
	case http.StatusOK, http.StatusNoContent:
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			if haveCache {
				cached.stale = true
				return cached, nil
			}
			return tileResult{}, err
		}
		r := tileResult{status: resp.StatusCode, body: body, m: meta{
			Status: resp.StatusCode, ETag: resp.Header.Get("ETag"), MaxAge: maxAge, FetchedAt: now,
		}}
		if resp.StatusCode == http.StatusNoContent {
			r.body = nil
		}
		p.write(pngPath, metaPath, r)
		return r, nil
	default:
		if haveCache {
			cached.stale = true
			return cached, nil
		}
		return tileResult{}, fmt.Errorf("GBIF answered HTTP %d", resp.StatusCode)
	}
}

func parseMaxAge(cc string) int {
	for _, part := range strings.Split(cc, ",") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "max-age="); ok {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				return n
			}
		}
	}
	return int(defaultMaxAge / time.Second)
}

func (p *Proxy) readCache(pngPath, metaPath string) (tileResult, bool) {
	mb, err := os.ReadFile(metaPath)
	if err != nil {
		return tileResult{}, false
	}
	var m meta
	if json.Unmarshal(mb, &m) != nil {
		return tileResult{}, false
	}
	r := tileResult{status: m.Status, m: m}
	if m.Status == http.StatusOK {
		if r.body, err = os.ReadFile(pngPath); err != nil {
			return tileResult{}, false
		}
	}
	return r, true
}

func (p *Proxy) write(pngPath, metaPath string, r tileResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(pngPath), 0o755); err != nil {
		log.Printf("gbif: cache dir: %v", err)
		return
	}
	if r.status == http.StatusOK {
		if err := writeAtomic(pngPath, r.body); err != nil {
			log.Printf("gbif: cache write: %v", err)
			return
		}
	} else {
		_ = os.Remove(pngPath)
	}
	p.writeMetaLocked(metaPath, r.m)
}

func (p *Proxy) writeMeta(metaPath string, m meta) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writeMetaLocked(metaPath, m)
}

func (p *Proxy) writeMetaLocked(metaPath string, m meta) {
	b, _ := json.Marshal(m)
	if err := writeAtomic(metaPath, b); err != nil {
		log.Printf("gbif: cache meta write: %v", err)
	}
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// touch marks a tile as used: Evict removes the least recently used first.
func (p *Proxy) touch(paths ...string) {
	now := p.Now()
	for _, path := range paths {
		_ = os.Chtimes(path, now, now)
	}
}

// Evict trims the cache to CapBytes, least recently used tiles first.
func (p *Proxy) Evict() (removed int, err error) {
	type entry struct {
		base  string
		size  int64
		mtime time.Time
	}
	byBase := map[string]*entry{}
	var total int64
	err = filepath.WalkDir(p.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		base := strings.TrimSuffix(strings.TrimSuffix(path, ".png"), ".json")
		e := byBase[base]
		if e == nil {
			e = &entry{base: base}
			byBase[base] = e
		}
		e.size += info.Size()
		if info.ModTime().After(e.mtime) {
			e.mtime = info.ModTime()
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil || total <= p.CapBytes {
		return 0, err
	}
	entries := make([]*entry, 0, len(byBase))
	for _, e := range byBase {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mtime.Before(entries[j].mtime) })
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range entries {
		if total <= p.CapBytes {
			break
		}
		_ = os.Remove(e.base + ".png")
		_ = os.Remove(e.base + ".json")
		total -= e.size
		removed++
	}
	log.Printf("gbif: evicted %d cached tile(s)", removed)
	return removed, nil
}
