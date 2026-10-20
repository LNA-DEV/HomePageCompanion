// Package dbsqlite is the db.sqlite module: storage.sql/v1 on SQLite, one
// file per building block (data/db/<id>.db), backed up with VACUUM INTO.
package dbsqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/LNA-DEV/HomePageCompanion/capability/storage"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func init() {
	companion.Register(companion.Info{
		ID:       "db.sqlite",
		Name:     "SQLite",
		New:      func() companion.Instance { return new(DB) },
		Provides: []companion.Capability{storage.SQLV1},
		Order:    -100,
	})
}

// DB is one SQLite directory.
type DB struct {
	// Dir holds the files; relative to the data directory ("db" when empty).
	Dir string `json:"dir"`
	// Verbose logs every SQL statement.
	Verbose bool `json:"verbose"`

	dir  string
	mu   sync.Mutex
	open map[string]*gorm.DB
}

var areaName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func (d *DB) Provision(ctx companion.Context) error {
	d.dir = d.Dir
	if d.dir == "" {
		d.dir = "db"
	}
	if !filepath.IsAbs(d.dir) {
		d.dir = filepath.Join(ctx.DataDir(), d.dir)
	}
	d.open = map[string]*gorm.DB{}
	return os.MkdirAll(d.dir, 0o755)
}

func (d *DB) path(area string) (string, error) {
	if !areaName.MatchString(area) {
		return "", fmt.Errorf("db.sqlite: invalid area %q", area)
	}
	return filepath.Join(d.dir, area+".db"), nil
}

// Open returns the area's handle. WAL and a busy timeout let the scheduler,
// the event worker and the HTTP handlers write at the same time.
func (d *DB) Open(area string) (*gorm.DB, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if db, ok := d.open[area]; ok {
		return db, nil
	}
	p, err := d.path(area)
	if err != nil {
		return nil, err
	}
	cfg := &gorm.Config{}
	if !d.Verbose {
		cfg.Logger = logger.Default.LogMode(logger.Warn)
	}
	db, err := gorm.Open(sqlite.Open(p+"?_journal_mode=WAL&_busy_timeout=10000"), cfg)
	if err != nil {
		return nil, err
	}
	d.open[area] = db
	return db, nil
}

func (d *DB) Areas() ([]string, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".db"); ok && !e.IsDir() && areaName.MatchString(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Backup writes a consistent copy with VACUUM INTO, which runs alongside
// readers and writers.
func (d *DB) Backup(ctx context.Context, area, dst string) error {
	db, err := d.Open(area)
	if err != nil {
		return err
	}
	_ = os.Remove(dst)
	return db.WithContext(ctx).Exec("VACUUM INTO ?", dst).Error
}

func (d *DB) Restore(ctx context.Context, area, src string) error {
	p, err := d.path(area)
	if err != nil {
		return err
	}
	d.closeArea(area)
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(p + suffix)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := p + ".restore"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (d *DB) Drop(area string) error {
	p, err := d.path(area)
	if err != nil {
		return err
	}
	d.closeArea(area)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(p + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (d *DB) closeArea(area string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if db, ok := d.open[area]; ok {
		if s, err := db.DB(); err == nil {
			s.Close()
		}
		delete(d.open, area)
	}
}

func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for area, db := range d.open {
		if s, err := db.DB(); err == nil {
			s.Close()
		}
		delete(d.open, area)
	}
	return nil
}

var _ storage.SQL = (*DB)(nil)
