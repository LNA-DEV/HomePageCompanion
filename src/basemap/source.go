package basemap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/protomaps/go-pmtiles/pmtiles"
)

const defaultSource = "https://build.protomaps.com"

// Source is where the daily builds live: "<BaseURL>/<YYYYMMDD>.pmtiles".
type Source struct {
	BaseURL string
	Client  *http.Client
}

// Build is one daily build found at the source.
type Build struct {
	Version string // YYYYMMDD
	URL     string
	Size    int64
	ETag    string
}

// Inspection is what the job reads before it copies a single byte.
type Inspection struct {
	Header      pmtiles.HeaderV3
	HeaderBytes []byte
	Metadata    map[string]any
}

func (s Source) url(version string) string {
	return strings.TrimRight(s.BaseURL, "/") + "/" + version + ".pmtiles"
}

// Find returns the newest build of the last week, newest first from today:
// Protomaps keeps "all builds for the past week", and publishes no index.
func (s Source) Find(ctx context.Context, now time.Time) (Build, error) {
	for i := 0; i < 7; i++ {
		version := now.UTC().AddDate(0, 0, -i).Format("20060102")
		b, err := s.Head(ctx, version)
		if err == nil {
			return b, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Build{}, err
		}
	}
	return Build{}, errors.New("no build in the last 7 days")
}

// Head looks up one build.
func (s Source) Head(ctx context.Context, version string) (Build, error) {
	u := s.url(version)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return Build{}, err
	}
	req.Header.Set("User-Agent", config.UserAgent())
	resp, err := s.Client.Do(req)
	if err != nil {
		return Build{}, err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Build{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return Build{}, fmt.Errorf("HEAD %s: HTTP %d", u, resp.StatusCode)
	}
	if resp.ContentLength <= 0 {
		return Build{}, fmt.Errorf("HEAD %s: no Content-Length", u)
	}
	return Build{Version: version, URL: u, Size: resp.ContentLength, ETag: resp.Header.Get("ETag")}, nil
}

// ReadRange fetches [offset, offset+length) of a build. With an ETag it asks
// If-Match, so a build that changed under a running copy fails the part
// instead of mixing two files.
func (s Source) ReadRange(ctx context.Context, b Build, offset, length int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.UserAgent())
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	if b.ETag != "" {
		req.Header.Set("If-Match", b.ETag)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusPreconditionFailed:
		return nil, errSourceChanged
	case http.StatusNotFound:
		return nil, errSourceGone
	default:
		return nil, fmt.Errorf("GET %s range %d+%d: HTTP %d", b.URL, offset, length, resp.StatusCode)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if want := fmt.Sprintf("bytes %d-%d/", offset, offset+length-1); !strings.HasPrefix(cr, want) {
			return nil, fmt.Errorf("GET %s: Content-Range %q, wanted %q…", b.URL, cr, want)
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, length+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != length {
		return nil, fmt.Errorf("GET %s range %d+%d: got %d bytes", b.URL, offset, length, len(data))
	}
	return data, nil
}

var (
	errSourceChanged = errors.New("the build changed at the source during the copy")
	errSourceGone    = errors.New("the build is no longer at the source")
)

// Inspect reads the header and the metadata of a build.
func (s Source) Inspect(ctx context.Context, b Build) (Inspection, error) {
	hb, err := s.ReadRange(ctx, b, 0, pmtiles.HeaderV3LenBytes)
	if err != nil {
		return Inspection{}, err
	}
	return inspectBytes(hb, func(off, n int64) ([]byte, error) { return s.ReadRange(ctx, b, off, n) })
}

func inspectBytes(hb []byte, read func(off, n int64) ([]byte, error)) (Inspection, error) {
	if len(hb) < pmtiles.HeaderV3LenBytes || string(hb[:7]) != "PMTiles" {
		return Inspection{}, errors.New("not a PMTiles archive")
	}
	h, err := pmtiles.DeserializeHeader(hb)
	if err != nil {
		return Inspection{}, err
	}
	mb, err := read(int64(h.MetadataOffset), int64(h.MetadataLength))
	if err != nil {
		return Inspection{}, err
	}
	meta, err := pmtiles.DeserializeMetadata(bytes.NewReader(mb), h.InternalCompression)
	if err != nil {
		return Inspection{}, fmt.Errorf("metadata: %w", err)
	}
	return Inspection{Header: h, HeaderBytes: hb, Metadata: meta}, nil
}

// Check holds a build to what the site's style needs: PMTiles v3, vector
// tiles, zoom 15, and the configured schema major. A build that fails it is
// refused before anything is written to the bucket.
func (in Inspection) Check(schemaMajor int) error {
	if in.Header.SpecVersion != 3 {
		return fmt.Errorf("PMTiles spec v%d, want v3", in.Header.SpecVersion)
	}
	if in.Header.TileType != pmtiles.Mvt {
		return fmt.Errorf("tile type %d, want MVT", in.Header.TileType)
	}
	if in.Header.MaxZoom != 15 {
		return fmt.Errorf("max zoom %d, want 15", in.Header.MaxZoom)
	}
	v := in.SchemaVersion()
	if v == "" {
		return errors.New(`metadata has no "version"`)
	}
	major, err := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	if err != nil {
		return fmt.Errorf("metadata version %q: %v", v, err)
	}
	if major != schemaMajor {
		return fmt.Errorf("schema %s, the site's style is written for %d.x", v, schemaMajor)
	}
	return nil
}

// SchemaVersion is the metadata's "version", e.g. "4.15.2".
func (in Inspection) SchemaVersion() string {
	v, _ := in.Metadata["version"].(string)
	return v
}

// OSMTime is the OSM replication time the build was made from.
func (in Inspection) OSMTime() string {
	v, _ := in.Metadata["planetiler:osm:osmosisreplicationtime"].(string)
	return v
}
