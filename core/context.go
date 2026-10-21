package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"gorm.io/gorm"
)

// modCtx is a building block's companion.ModuleContext.
type modCtx struct {
	c    *Core
	info companion.Info
	log  *log.Logger

	dbOnce sync.Once
	db     *gorm.DB
	dbErr  error
}

func newModCtx(c *Core, info companion.Info) *modCtx {
	return &modCtx{c: c, info: info, log: log.New(c.logOut(), "module="+info.ID+" ", log.LstdFlags)}
}

func (m *modCtx) ModuleID() string  { return m.info.ID }
func (m *modCtx) Log() *log.Logger  { return m.log }
func (m *modCtx) PublicURL() string { return m.c.cfg().Core.PublicURL }
func (m *modCtx) DataDir() string   { return m.c.opts.DataDir }

func (m *modCtx) DB() *gorm.DB {
	if m.c.sql == nil {
		return nil
	}
	m.dbOnce.Do(func() {
		m.db, m.dbErr = m.c.sql.Open(m.info.ID)
		if m.dbErr != nil {
			m.log.Printf("database: %v", m.dbErr)
		}
	})
	return m.db
}

func (m *modCtx) Blobs() companion.Bucket {
	area := m.info.BlobArea
	if area == "" {
		area = m.info.ID
	}
	return m.c.bucket(area)
}

func (m *modCtx) Temp() companion.TempStore { return m.c.temp }

func (m *modCtx) Secrets() companion.Secrets { return m.c.secrets.scope(m.info.ID) }

func (m *modCtx) CacheDir() string {
	dir := filepath.Join(m.c.opts.DataDir, "cache", m.info.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		m.log.Printf("cache dir: %v", err)
	}
	return dir
}

func (m *modCtx) Emit(ctx context.Context, eventType string, data any) error {
	return m.c.events.emit(ctx, m.info.ID, eventType, data)
}

func (m *modCtx) HTTP() *http.Client { return m.c.httpClient }

func (m *modCtx) Instances() []companion.InstanceRef {
	m.c.mu.RLock()
	defer m.c.mu.RUnlock()
	var out []companion.InstanceRef
	for _, in := range m.c.instances {
		if in.block == m.info.ID && in.running {
			out = append(out, companion.InstanceRef{ID: in.id, Name: in.name, Instance: in.value})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

func (m *modCtx) Capability(instanceID string, cap companion.Capability) (any, error) {
	m.c.mu.RLock()
	defer m.c.mu.RUnlock()
	connected := false
	for _, in := range m.c.instances {
		if in.block != m.info.ID {
			continue
		}
		for _, ids := range in.slots {
			for _, id := range ids {
				if id == instanceID {
					connected = true
				}
			}
		}
	}
	if !connected {
		return nil, fmt.Errorf("%s is not connected to %s", instanceID, m.info.ID)
	}
	target, ok := m.c.instances[instanceID]
	if !ok || !target.running {
		return nil, fmt.Errorf("%s is not running", instanceID)
	}
	impl, err := target.provide(cap)
	if err != nil {
		return nil, err
	}
	return m.c.measured(cap, instanceID, impl), nil
}

func (m *modCtx) Metrics() companion.Metrics { return m.c.metrics.scope(m.info.ID) }

// instCtx is an instance's companion.Context.
type instCtx struct {
	*modCtx
	name string
}

func (i *instCtx) InstanceName() string { return i.name }
func (i *instCtx) InstanceID() string   { return companion.InstanceID(i.info.ID, i.name) }

// errBucket is the Bucket when no blob module is configured.
type errBucket struct{ prefix string }

var errNoBlobs = errors.New("no blob module configured")

func (e errBucket) Put(context.Context, string, io.Reader, int64, string) error { return errNoBlobs }
func (e errBucket) Get(context.Context, string) (io.ReadCloser, companion.ObjectInfo, error) {
	return nil, companion.ObjectInfo{}, errNoBlobs
}
func (e errBucket) ReadRange(context.Context, string, int64, int64) ([]byte, error) {
	return nil, errNoBlobs
}
func (e errBucket) Stat(context.Context, string) (companion.ObjectInfo, error) {
	return companion.ObjectInfo{}, errNoBlobs
}
func (e errBucket) List(context.Context, string) ([]companion.ObjectInfo, error) {
	return nil, errNoBlobs
}
func (e errBucket) Delete(context.Context, string) error { return errNoBlobs }
func (e errBucket) CreateUpload(context.Context, string, string) (string, error) {
	return "", errNoBlobs
}
func (e errBucket) UploadPart(context.Context, string, string, int32, []byte) (string, error) {
	return "", errNoBlobs
}
func (e errBucket) CompleteUpload(context.Context, string, string, []companion.CompletedPart) error {
	return errNoBlobs
}
func (e errBucket) AbortUpload(context.Context, string, string) error { return errNoBlobs }
func (e errBucket) ListUploads(context.Context, string) ([]companion.Upload, error) {
	return nil, errNoBlobs
}
func (e errBucket) Prefix() string { return e.prefix }

// userAgentTransport sets the companion's User-Agent: the usage policies of
// the tile builds, routing services and GBIF ask for a name and a contact.
type userAgentTransport struct {
	next  http.RoundTripper
	agent func() string
}

func (t userAgentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("User-Agent") == "" {
		r = r.Clone(r.Context())
		r.Header.Set("User-Agent", t.agent())
	}
	return t.next.RoundTrip(r)
}

func newHTTPClient(agent func() string) *http.Client {
	// No overall timeout: the basemap copies parts of up to hundreds of
	// MiB. Callers bound requests with their context.
	return &http.Client{Transport: userAgentTransport{next: http.DefaultTransport, agent: agent}}
}
