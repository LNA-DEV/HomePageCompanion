package companiontest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/storage"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The test storage modules.
const (
	SQLID  = "test.sql"
	BlobID = "test.blob"
)

var registerOnce sync.Once

func registerStorage() {
	registerOnce.Do(func() {
		if _, ok := companion.Lookup(SQLID); !ok {
			companion.Register(companion.Info{ID: SQLID, New: func() companion.Instance { return new(SQL) },
				Provides: []companion.Capability{storage.SQLV1}, Order: -100})
		}
		if _, ok := companion.Lookup(BlobID); !ok {
			companion.Register(companion.Info{ID: BlobID, New: func() companion.Instance { return NewMemBlob() },
				Provides: []companion.Capability{storage.BlobV1}, Order: -90})
		}
	})
}

// SQL is storage.sql/v1 on one SQLite file per area in the data directory.
type SQL struct {
	dir  string
	mu   sync.Mutex
	open map[string]*gorm.DB
}

func (s *SQL) Provision(ctx companion.Context) error {
	s.dir = filepath.Join(ctx.DataDir(), "db")
	s.open = map[string]*gorm.DB{}
	return os.MkdirAll(s.dir, 0o755)
}

func (s *SQL) Open(area string) (*gorm.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if db, ok := s.open[area]; ok {
		return db, nil
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(s.dir, area+".db")+"?_journal_mode=WAL&_busy_timeout=10000"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	s.open[area] = db
	return db, nil
}

func (s *SQL) Areas() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".db"); ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *SQL) Backup(ctx context.Context, area, dst string) error {
	db, err := s.Open(area)
	if err != nil {
		return err
	}
	return db.Exec("VACUUM INTO ?", dst).Error
}

func (s *SQL) Restore(ctx context.Context, area, src string) error {
	s.mu.Lock()
	if db, ok := s.open[area]; ok {
		if d, err := db.DB(); err == nil {
			d.Close()
		}
		delete(s.open, area)
	}
	s.mu.Unlock()
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	p := filepath.Join(s.dir, area+".db")
	os.Remove(p + "-wal")
	os.Remove(p + "-shm")
	return os.WriteFile(p, b, 0o600)
}

func (s *SQL) Drop(area string) error {
	return os.Remove(filepath.Join(s.dir, area+".db"))
}

func (s *SQL) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, db := range s.open {
		if d, err := db.DB(); err == nil {
			d.Close()
		}
		delete(s.open, k)
	}
	return nil
}

// MemBlob is storage.blob/v1 in memory.
type MemBlob struct {
	mu      sync.Mutex
	objects map[string]memObject
	uploads map[string]*memUpload
	n       int
}

type memObject struct {
	data []byte
	ct   string
	mod  time.Time
}

type memUpload struct {
	key       string
	parts     map[int32][]byte
	initiated time.Time
}

// NewMemBlob is an empty store.
func NewMemBlob() *MemBlob {
	return &MemBlob{objects: map[string]memObject{}, uploads: map[string]*memUpload{}}
}

func (m *MemBlob) Bucket(prefix string) companion.Bucket {
	return &memBucket{m: m, prefix: strings.Trim(prefix, "/")}
}

func (m *MemBlob) Durable() bool { return false }

// Keys lists every stored key; for assertions.
func (m *MemBlob) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type memBucket struct {
	m      *MemBlob
	prefix string
}

func (b *memBucket) Prefix() string { return b.prefix }

func (b *memBucket) full(k string) string {
	k = strings.TrimPrefix(k, "/")
	if b.prefix == "" {
		return k
	}
	return b.prefix + "/" + k
}

func (b *memBucket) rel(full string) string {
	if b.prefix == "" {
		return full
	}
	return strings.TrimPrefix(full, b.prefix+"/")
}

func (b *memBucket) Put(_ context.Context, key string, r io.Reader, _ int64, ct string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.m.mu.Lock()
	b.m.objects[b.full(key)] = memObject{data: data, ct: ct, mod: time.Now()}
	b.m.mu.Unlock()
	return nil
}

func (b *memBucket) Get(_ context.Context, key string) (io.ReadCloser, companion.ObjectInfo, error) {
	b.m.mu.Lock()
	o, ok := b.m.objects[b.full(key)]
	b.m.mu.Unlock()
	if !ok {
		return nil, companion.ObjectInfo{}, companion.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(o.data)), companion.ObjectInfo{Key: key, Size: int64(len(o.data)), ContentType: o.ct, ModTime: o.mod, ETag: fmt.Sprintf(`"%d"`, len(o.data))}, nil
}

func (b *memBucket) Stat(ctx context.Context, key string) (companion.ObjectInfo, error) {
	rc, info, err := b.Get(ctx, key)
	if err == nil {
		rc.Close()
	}
	return info, err
}

func (b *memBucket) ReadRange(_ context.Context, key string, off, n int64) ([]byte, error) {
	b.m.mu.Lock()
	o, ok := b.m.objects[b.full(key)]
	b.m.mu.Unlock()
	if !ok {
		return nil, companion.ErrNotFound
	}
	if off >= int64(len(o.data)) {
		return nil, nil
	}
	end := min(off+n, int64(len(o.data)))
	return append([]byte(nil), o.data[off:end]...), nil
}

func (b *memBucket) List(_ context.Context, prefix string) ([]companion.ObjectInfo, error) {
	b.m.mu.Lock()
	defer b.m.mu.Unlock()
	var out []companion.ObjectInfo
	base := b.full("")
	for k, o := range b.m.objects {
		if (b.prefix == "" || strings.HasPrefix(k, base)) && strings.HasPrefix(b.rel(k), prefix) {
			out = append(out, companion.ObjectInfo{Key: b.rel(k), Size: int64(len(o.data)), ContentType: o.ct, ModTime: o.mod})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (b *memBucket) Delete(_ context.Context, key string) error {
	b.m.mu.Lock()
	delete(b.m.objects, b.full(key))
	b.m.mu.Unlock()
	return nil
}

func (b *memBucket) CreateUpload(_ context.Context, key, _ string) (string, error) {
	b.m.mu.Lock()
	defer b.m.mu.Unlock()
	b.m.n++
	id := fmt.Sprintf("upload-%d", b.m.n)
	b.m.uploads[id] = &memUpload{key: b.full(key), parts: map[int32][]byte{}, initiated: time.Now()}
	return id, nil
}

func (b *memBucket) UploadPart(_ context.Context, _, id string, n int32, data []byte) (string, error) {
	b.m.mu.Lock()
	defer b.m.mu.Unlock()
	u, ok := b.m.uploads[id]
	if !ok {
		return "", fmt.Errorf("%w: %s", companion.ErrUploadGone, id)
	}
	u.parts[n] = append([]byte(nil), data...)
	return fmt.Sprintf(`"%d-%d"`, n, len(data)), nil
}

func (b *memBucket) CompleteUpload(_ context.Context, _, id string, parts []companion.CompletedPart) error {
	b.m.mu.Lock()
	defer b.m.mu.Unlock()
	u, ok := b.m.uploads[id]
	if !ok {
		return fmt.Errorf("%w: %s", companion.ErrUploadGone, id)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].Number < parts[j].Number })
	var all []byte
	for _, p := range parts {
		all = append(all, u.parts[p.Number]...)
	}
	b.m.objects[u.key] = memObject{data: all, mod: time.Now()}
	delete(b.m.uploads, id)
	return nil
}

func (b *memBucket) AbortUpload(_ context.Context, _, id string) error {
	b.m.mu.Lock()
	delete(b.m.uploads, id)
	b.m.mu.Unlock()
	return nil
}

func (b *memBucket) ListUploads(_ context.Context, prefix string) ([]companion.Upload, error) {
	b.m.mu.Lock()
	defer b.m.mu.Unlock()
	var out []companion.Upload
	for id, u := range b.m.uploads {
		if strings.HasPrefix(u.key, b.full(prefix)) {
			out = append(out, companion.Upload{Key: b.rel(u.key), UploadID: id, Initiated: u.initiated})
		}
	}
	return out, nil
}

var (
	_ storage.SQL  = (*SQL)(nil)
	_ storage.Blob = (*MemBlob)(nil)
)
