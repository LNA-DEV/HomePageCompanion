package core

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/storage"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/core/internal/conf"
)

// bucket is the blob store under area ("" = the whole companion prefix).
func (c *Core) bucket(area string) companion.Bucket {
	if c.blob == nil {
		return errBucket{prefix: area}
	}
	return c.blob.Bucket(area)
}

// OpenStorage starts only the storage instances, for the commands that work
// on the data while the companion does not serve (backup, restore, migrate,
// secrets rotate).
func (c *Core) OpenStorage(ctx context.Context) error {
	if err := os.MkdirAll(c.opts.DataDir, 0o755); err != nil {
		return err
	}
	f, raw, err := conf.Load(c.opts.ConfigPath, c.envFiles()...)
	if err != nil {
		return err
	}
	return c.openStorageFrom(f, raw)
}

func (c *Core) openStorageFrom(f *conf.File, raw []byte) error {
	lock, _, err := c.services.readLock()
	if err != nil {
		return err
	}
	ps, err := c.plan(f, lock)
	if err != nil {
		return err
	}
	c.conf.Store(f)
	c.confRaw = raw
	next := map[string]*instance{}
	for _, p := range ps {
		if !provides(p.info, storage.SQLV1) && !provides(p.info, storage.BlobV1) {
			continue
		}
		in, err := c.create(p, next)
		if err != nil {
			return err
		}
		next[p.id] = in
		if err := c.afterStorage(in); err != nil {
			return err
		}
		c.mu.Lock()
		c.instances[p.id] = in
		c.order = append(c.order, p.id)
		if _, ok := c.blocks[p.block]; !ok {
			c.blocks[p.block] = &block{info: p.info, mc: in.ctx.modCtx}
		}
		c.mu.Unlock()
		if err := c.startInstance(in); err != nil {
			return err
		}
	}
	return nil
}

// enforceCacheLimit keeps data/cache/ under core.cacheMB by deleting the
// least recently used files first (the access time is the file's mtime,
// which the caches touch on every hit).
func (c *Core) enforceCacheLimit(context.Context) error {
	limit := int64(c.cfg().Core.CacheMB)
	if limit <= 0 {
		limit = 1024
	}
	limit <<= 20
	root := filepath.Join(c.opts.DataDir, "cache")
	type file struct {
		path string
		size int64
		mod  time.Time
	}
	var files []file
	var total int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, file{p, fi.Size(), fi.ModTime()})
		total += fi.Size()
		return nil
	})
	if err != nil {
		return err
	}
	if total <= limit {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= limit*9/10 {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
	return nil
}
