package core

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/core/internal/legacyconf"
	"github.com/LNA-DEV/HomePageCompanion/lib/envfile"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// migratedKey marks a companion that a migration filled (core_meta).
const migratedKey = "legacy_migrated_at"

// MigrateOptions steer Migrate.
type MigrateOptions struct {
	// From is the old data directory: companion.db, config.yaml, .env,
	// microblog/ and trips/. It may be the new data directory: the new
	// state lives beside the old files (db/, blobs/, secret.key).
	From string
	// LegacyConfig is the old config.yaml (default <From>/config.yaml).
	LegacyConfig string
	// Check reports every step and changes nothing.
	Check bool
	// Force migrates into a companion that a migration already filled.
	Force bool
	Out   io.Writer
}

// Migrate moves a pre-module installation over in one go
// (docs/concepts/modular-architecture.md §5.11): the old config.yaml
// becomes companion.yaml and build.yaml, every module imports its part of
// companion.db and of the old files, and the refreshed tokens and the VAPID
// key move into the secret store. The old installation is archived first and
// never changed; an existing new state is backed up first.
func Migrate(ctx context.Context, opts Options, mo MigrateOptions) error {
	out := mo.Out
	if out == nil {
		out = os.Stdout
	}
	say := func(format string, args ...any) { fmt.Fprintf(out, format+"\n", args...) }
	if mo.From == "" {
		mo.From = "data"
	}
	if mo.LegacyConfig == "" {
		mo.LegacyConfig = filepath.Join(mo.From, "config.yaml")
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = "companion.yaml"
	}
	if opts.DataDir == "" {
		opts.DataDir = "data"
	}
	// The secrets the old config refers to, and TOKEN_ENCRYPTION_KEY for
	// the refreshed Meta tokens.
	envfile.Load(filepath.Join(mo.From, ".env"))
	envfile.Load(".env")

	raw, err := os.ReadFile(mo.LegacyConfig)
	if err != nil {
		return fmt.Errorf("the old config: %w", err)
	}
	old, err := legacyconf.Parse(raw)
	if err != nil {
		return err
	}
	conv, err := legacyconf.Convert(old, buildVersion())
	if err != nil {
		return err
	}
	_, statErr := os.Stat(opts.ConfigPath)
	haveConfig := statErr == nil
	buildPath := filepath.Join(filepath.Dir(opts.ConfigPath), "build.yaml")

	say("1. config: %s", mo.LegacyConfig)
	for _, n := range conv.Notes {
		say("   - %s", n)
	}
	if haveConfig {
		say("   %s exists and is used as it is (its instance names must be the ones below)", opts.ConfigPath)
	} else {
		say("   writes %s and %s", opts.ConfigPath, buildPath)
	}
	if mo.Check {
		say("\n--- %s\n%s--- %s\n%s---", opts.ConfigPath, conv.Companion, buildPath, conv.Build)
	}
	say("   building blocks: %s", strings.Join(conv.Modules, ", "))

	oldDB, closeDB, err := openLegacyDB(filepath.Join(mo.From, "companion.db"))
	if err != nil {
		return err
	}
	defer closeDB()
	if oldDB == nil {
		say("2. database: no %s, only the config and the files move", filepath.Join(mo.From, "companion.db"))
	} else {
		say("2. database: %s", filepath.Join(mo.From, "companion.db"))
	}

	if mo.Check {
		return migrateCheck(ctx, opts, mo, conv, oldDB, haveConfig, say)
	}

	// A companion a migration filled is not filled again by accident.
	if haveConfig && !mo.Force {
		at, err := migratedAt(ctx, opts)
		if err != nil {
			return err
		}
		if at != "" {
			return fmt.Errorf("this companion was migrated on %s already; --force imports again (restore a backup first, or rows clash)", at)
		}
	}

	// Backups first: the old installation, and the new state if there is one.
	backups := filepath.Join(opts.DataDir, "backups")
	if err := os.MkdirAll(backups, 0o755); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102-150405.000")
	legacyArchive := filepath.Join(backups, "legacy-"+stamp+".tar.gz")
	n, err := archiveLegacy(mo.From, mo.LegacyConfig, legacyArchive)
	if err != nil {
		return fmt.Errorf("archiving the old installation: %w", err)
	}
	say("3. backup: the old installation (%d file(s)) is in %s", n, legacyArchive)
	if haveConfig {
		name := filepath.Join(backups, "pre-migrate-"+stamp+".tar.gz")
		if err := backupTo(ctx, opts, name); err != nil {
			return fmt.Errorf("backing up the new state first: %w", err)
		}
		say("   the new state is in %s", name)
	} else {
		if err := os.WriteFile(opts.ConfigPath, conv.Companion, 0o600); err != nil {
			return err
		}
		if _, err := os.Stat(buildPath); errors.Is(err, fs.ErrNotExist) {
			if err := os.WriteFile(buildPath, conv.Build, 0o644); err != nil {
				return err
			}
		}
		say("   wrote %s and %s", opts.ConfigPath, buildPath)
	}

	c := New(opts)
	if err := c.Prepare(ctx); err != nil {
		return fmt.Errorf("the new config does not start (fix %s or .env and run again): %w", opts.ConfigPath, err)
	}
	defer c.Stop(ctx)
	say("4. modules:")
	if err := runImports(c, mo, conv, oldDB, say); err != nil {
		return fmt.Errorf("%w\nnothing is lost: the old installation is untouched; remove %s and the new state (data/db/, data/blobs/, secret.key) and run again", err, opts.ConfigPath)
	}
	if err := c.db.Save(&metaRow{Key: migratedKey, Value: time.Now().UTC().Format(time.RFC3339)}).Error; err != nil {
		return err
	}
	say("done: start the companion with %s; the old companion.db stays as it was", opts.ConfigPath)
	return nil
}

// migrateCheck runs the imports in check mode against a scratch copy of the
// new state, so they report what they would move and nothing is written
// where the companion lives.
func migrateCheck(ctx context.Context, opts Options, mo MigrateOptions, conv *legacyconf.Result, oldDB *gorm.DB, haveConfig bool, say func(string, ...any)) error {
	say("3. backup: the old installation would be archived under %s", filepath.Join(opts.DataDir, "backups"))
	scratch, err := os.MkdirTemp("", "companion-migrate-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	cfg := filepath.Join(scratch, "companion.yaml")
	data := conv.Companion
	if haveConfig {
		if data, err = os.ReadFile(opts.ConfigPath); err != nil {
			return err
		}
	}
	// The scratch state gets its own database; the blob store is the
	// configured one, which the imports only read in a check.
	if err := os.WriteFile(cfg, data, 0o600); err != nil {
		return err
	}
	c := New(Options{ConfigPath: cfg, DataDir: filepath.Join(scratch, "data"), Logs: io.Discard})
	if err := c.Prepare(ctx); err != nil {
		say("4. modules: the new config does not start yet: %v", err)
		say("   set the missing values in .env (or fix the config) and check again")
		return nil
	}
	defer c.Stop(ctx)
	say("4. modules (check, nothing is written):")
	return runImports(c, mo, conv, oldDB, say)
}

func runImports(c *Core, mo MigrateOptions, conv *legacyconf.Result, oldDB *gorm.DB, say func(string, ...any)) error {
	l := companion.NewLegacy(oldDB, mo.From, mo.Check, func(line string) { say("   %s", line) })
	l.Targets, l.Connections, l.Feeds = conv.Targets, conv.Connections, conv.Feeds
	c.mu.RLock()
	var blocks []*block
	for _, b := range c.blocks {
		if b.info.Import != nil {
			blocks = append(blocks, b)
		}
	}
	c.mu.RUnlock()
	sort.Slice(blocks, func(i, j int) bool {
		if blocks[i].info.Order != blocks[j].info.Order {
			return blocks[i].info.Order < blocks[j].info.Order
		}
		return blocks[i].info.ID < blocks[j].info.ID
	})
	for _, b := range blocks {
		if err := safely(func() error { return b.info.Import(b.mc, l) }); err != nil {
			return fmt.Errorf("%s: import: %w", b.info.ID, err)
		}
	}
	return nil
}

// openLegacyDB opens the old database read only; nil when there is none.
func openLegacyDB(path string) (*gorm.DB, func(), error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, func() {}, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	db, err := gorm.Open(sqlite.Open("file:"+abs+"?mode=ro&_busy_timeout=10000"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, nil, fmt.Errorf("the old database: %w", err)
	}
	return db, func() {
		if d, err := db.DB(); err == nil {
			d.Close()
		}
	}, nil
}

// archiveLegacy writes the old installation into one file: the database,
// the config, .env, and the microblog and trip images. The caches stay out.
func archiveLegacy(from, legacyConfig, dst string) (int, error) {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	n := 0
	add := func(src, name string) error {
		if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		n++
		return addFile(tw, src, name)
	}
	for _, name := range []string{"companion.db", ".env"} {
		if err := add(filepath.Join(from, name), name); err != nil {
			return n, err
		}
	}
	if err := add(legacyConfig, "config.yaml"); err != nil {
		return n, err
	}
	for _, dir := range []string{"microblog", "trips"} {
		entries, _ := os.ReadDir(filepath.Join(from, dir))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if err := add(filepath.Join(from, dir, e.Name()), dir+"/"+e.Name()); err != nil {
				return n, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return n, err
	}
	return n, gz.Close()
}

// migratedAt is when a migration filled the companion, "" if none did.
func migratedAt(ctx context.Context, opts Options) (string, error) {
	c := New(Options{ConfigPath: opts.ConfigPath, DataDir: opts.DataDir, Logs: io.Discard})
	if err := c.OpenStorage(ctx); err != nil {
		return "", err
	}
	defer c.Stop(ctx)
	var done metaRow
	if c.db == nil || c.db.Where("key = ?", migratedKey).Limit(1).Find(&done).RowsAffected == 0 {
		return "", nil
	}
	return done.Value, nil
}

// backupTo takes a full backup of the new state.
func backupTo(ctx context.Context, opts Options, name string) error {
	c := New(Options{ConfigPath: opts.ConfigPath, DataDir: opts.DataDir, Logs: io.Discard})
	if err := c.OpenStorage(ctx); err != nil {
		return err
	}
	defer c.Stop(ctx)
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = c.Backup(ctx, f, BackupOptions{IncludeEnv: true})
	return err
}

func migrateCmd(ctx context.Context, opts Options, args []string) error {
	fl := flag.NewFlagSet("migrate", flag.ContinueOnError)
	check := fl.Bool("check", false, "report every step, change nothing")
	from := fl.String("from", "data", "the old data directory")
	legacy := fl.String("legacy-config", "", "the old config.yaml (default <from>/config.yaml)")
	force := fl.Bool("force", false, "import again into a migrated companion")
	if err := fl.Parse(args); err != nil {
		return err
	}
	return Migrate(ctx, opts, MigrateOptions{From: *from, LegacyConfig: *legacy, Check: *check, Force: *force, Out: os.Stdout})
}
