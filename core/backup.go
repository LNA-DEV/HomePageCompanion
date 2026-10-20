package core

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// BackupOptions select what goes into a backup.
type BackupOptions struct {
	IncludeEnv   bool // .env (secrets typed in by the operator)
	IncludeBlobs bool // files of a blob store that keeps its own history (S3)
	Module       string
}

// BackupManifest is manifest.json at the top of a backup.
type BackupManifest struct {
	Version  string    `json:"version"`
	Created  time.Time `json:"created"`
	Areas    []string  `json:"areas"`
	Blobs    bool      `json:"blobs"`
	Services []string  `json:"services,omitempty"`
	Config   []string  `json:"config"`
}

// configFiles are the config a backup carries, next to companion.yaml.
func (c *Core) configFiles(includeEnv bool) []string {
	dir := filepath.Dir(c.opts.ConfigPath)
	files := []string{c.opts.ConfigPath, filepath.Join(dir, "build.yaml"), filepath.Join(dir, "companion.lock")}
	if includeEnv {
		files = append(files, filepath.Join(dir, ".env"), filepath.Join(c.opts.DataDir, ".env"))
	}
	var out []string
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			out = append(out, f)
		}
	}
	return out
}

// Backup writes the whole state to w as a tar.gz: one file per database
// area, the blob files (unless the store keeps its own history), the service
// volumes and the config. Nothing in a building block has to help.
func (c *Core) Backup(ctx context.Context, w io.Writer, o BackupOptions) (*BackupManifest, error) {
	if c.sql == nil {
		return nil, errors.New("backup: no database module")
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	m := &BackupManifest{Version: c.version, Created: time.Now().UTC()}

	tmp, err := os.MkdirTemp("", "companion-backup-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	areas, err := c.sql.Areas()
	if err != nil {
		return nil, err
	}
	sort.Strings(areas)
	for _, a := range areas {
		if o.Module != "" && a != o.Module {
			continue
		}
		dst := filepath.Join(tmp, a+".db")
		if err := c.sql.Backup(ctx, a, dst); err != nil {
			return nil, fmt.Errorf("backup %s: %w", a, err)
		}
		if err := addFile(tw, dst, "db/"+a+".db"); err != nil {
			return nil, err
		}
		m.Areas = append(m.Areas, a)
	}

	if c.blob != nil && (!c.blob.Durable() || o.IncludeBlobs) {
		prefix := ""
		if o.Module != "" {
			prefix = c.blobArea(o.Module)
		}
		b := c.bucket(prefix)
		objs, err := b.List(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("backup blobs: %w", err)
		}
		for _, obj := range objs {
			if strings.HasPrefix(path.Join(prefix, obj.Key), tempPrefix+"/") || strings.HasPrefix(path.Join(prefix, obj.Key), "core/backups/") {
				continue
			}
			rc, info, err := b.Get(ctx, obj.Key)
			if err != nil {
				return nil, err
			}
			hdr := &tar.Header{Name: "blobs/" + path.Join(prefix, obj.Key), Mode: 0o644, Size: info.Size, ModTime: info.ModTime}
			if err := tw.WriteHeader(hdr); err != nil {
				rc.Close()
				return nil, err
			}
			_, err = io.Copy(tw, rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
		}
		m.Blobs = true
	}

	if o.Module == "" {
		svcs, err := c.services.backupVolumes(ctx, tw)
		if err != nil {
			return nil, err
		}
		m.Services = svcs
		for _, f := range c.configFiles(o.IncludeEnv) {
			name := "config/" + filepath.Base(f)
			if strings.HasPrefix(filepath.Clean(f), filepath.Clean(c.opts.DataDir)) {
				name = "config/data/" + filepath.Base(f)
			}
			if err := addFile(tw, f, name); err != nil {
				return nil, err
			}
			m.Config = append(m.Config, name)
		}
	}

	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o644, Size: int64(len(mb)), ModTime: m.Created}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(mb); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return m, gz.Close()
}

func addFile(tw *tar.Writer, src, name string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: fi.Size(), ModTime: fi.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// RestoreOptions select what a restore brings back.
type RestoreOptions struct {
	Module string // only this building block's database area and blobs
	Config bool   // also companion.yaml, build.yaml, companion.lock (and .env if present)
}

// Restore brings a backup back. The core must not be serving: run it from
// "companion restore" with the companion stopped.
func (c *Core) Restore(ctx context.Context, r io.Reader, o RestoreOptions) error {
	if c.sql == nil {
		return errors.New("restore: no database module")
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	tmp, err := os.MkdirTemp("", "companion-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if strings.HasPrefix(name, "..") || strings.HasPrefix(name, "/") {
			return fmt.Errorf("restore: unsafe path %q", h.Name)
		}
		switch {
		case strings.HasPrefix(name, "db/"):
			area := strings.TrimSuffix(strings.TrimPrefix(name, "db/"), ".db")
			if o.Module != "" && area != o.Module {
				continue
			}
			dst := filepath.Join(tmp, area+".db")
			if err := writeFrom(tr, dst); err != nil {
				return err
			}
			if area == "core" && c.db != nil {
				if sqlDB, err := c.db.DB(); err == nil {
					sqlDB.Close()
				}
			}
			if err := c.sql.Restore(ctx, area, dst); err != nil {
				return fmt.Errorf("restore %s: %w", area, err)
			}
			c.log.Printf("restore: database area %s", area)
		case strings.HasPrefix(name, "blobs/"):
			key := strings.TrimPrefix(name, "blobs/")
			if o.Module != "" && !strings.HasPrefix(key, c.blobArea(o.Module)+"/") {
				continue
			}
			if c.blob == nil {
				return errors.New("restore: the backup has blob files but no blob module is configured")
			}
			if err := c.bucket("").Put(ctx, key, tr, h.Size, ""); err != nil {
				return fmt.Errorf("restore blob %s: %w", key, err)
			}
		case strings.HasPrefix(name, "services/"):
			if o.Module == "" {
				if err := c.services.restoreVolume(ctx, name, tr); err != nil {
					return err
				}
			}
		case strings.HasPrefix(name, "config/") && o.Config && o.Module == "":
			rel := strings.TrimPrefix(name, "config/")
			dst := filepath.Join(filepath.Dir(c.opts.ConfigPath), rel)
			if d, ok := strings.CutPrefix(rel, "data/"); ok {
				dst = filepath.Join(c.opts.DataDir, d)
			}
			if err := writeFrom(tr, dst); err != nil {
				return err
			}
			c.log.Printf("restore: %s", dst)
		}
	}
	return nil
}

func writeFrom(r io.Reader, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// blobArea is the key prefix of a building block's blob area.
func (c *Core) blobArea(id string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if b, ok := c.blocks[id]; ok && b.info.BlobArea != "" {
		return b.info.BlobArea
	}
	return id
}

const backupDir = "core/backups"

// scheduledBackup writes a backup to core/backups/ in the blob store (or
// data/backups/ without one) and keeps the newest core.backups.keep.
func (c *Core) scheduledBackup(ctx context.Context) error {
	name, err := c.storeBackup(ctx)
	if err != nil {
		return err
	}
	c.log.Printf("backup: wrote %s", name)
	keep := c.cfg().Core.Backups.Keep
	if keep <= 0 {
		keep = 7
	}
	names, err := c.backupNames(ctx)
	if err != nil {
		return err
	}
	for i := keep; i < len(names); i++ {
		if err := c.deleteBackup(ctx, names[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c *Core) storeBackup(ctx context.Context) (string, error) {
	name := "backup-" + time.Now().UTC().Format("20060102-150405") + ".tar.gz"
	f, err := os.CreateTemp("", "companion-backup-*.tar.gz")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := c.Backup(ctx, f, BackupOptions{}); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if c.blob == nil {
		dir := filepath.Join(c.opts.DataDir, "backups")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		return name, copyFile(f.Name(), filepath.Join(dir, name))
	}
	in, err := os.Open(f.Name())
	if err != nil {
		return "", err
	}
	defer in.Close()
	fi, _ := in.Stat()
	return name, c.bucket(backupDir).Put(ctx, name, in, fi.Size(), "application/gzip")
}

func (c *Core) backupNames(ctx context.Context) ([]string, error) {
	var names []string
	if c.blob == nil {
		entries, err := os.ReadDir(filepath.Join(c.opts.DataDir, "backups"))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, e := range entries {
			names = append(names, e.Name())
		}
	} else {
		objs, err := c.bucket(backupDir).List(ctx, "")
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			names = append(names, o.Key)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names, nil
}

func (c *Core) deleteBackup(ctx context.Context, name string) error {
	if c.blob == nil {
		return os.Remove(filepath.Join(c.opts.DataDir, "backups", name))
	}
	return c.bucket(backupDir).Delete(ctx, name)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeFrom(in, dst)
}

func (c *Core) listBackups(g *gin.Context) {
	names, err := c.backupNames(g.Request.Context())
	if err != nil {
		g.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if names == nil {
		names = []string{}
	}
	g.JSON(http.StatusOK, names)
}

func (c *Core) createBackup(g *gin.Context) {
	name, err := c.storeBackup(g.Request.Context())
	if err != nil {
		g.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	g.JSON(http.StatusCreated, gin.H{"name": name})
}

func (c *Core) downloadBackup(g *gin.Context) {
	name := filepath.Base(g.Param("name"))
	g.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	if c.blob == nil {
		g.File(filepath.Join(c.opts.DataDir, "backups", name))
		return
	}
	rc, info, err := c.bucket(backupDir).Get(g.Request.Context(), name)
	if err != nil {
		g.Status(http.StatusNotFound)
		return
	}
	defer rc.Close()
	g.DataFromReader(http.StatusOK, info.Size, "application/gzip", rc, nil)
}
