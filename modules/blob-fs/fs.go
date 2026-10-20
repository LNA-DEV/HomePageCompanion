// Package blobfs is the blob.fs module: storage.blob/v1 on a local
// directory (data/blobs/ by default). Every building block's area is a
// folder, "<prefix>/<id>/".
package blobfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/storage"
	"github.com/LNA-DEV/HomePageCompanion/companion"
)

func init() {
	companion.Register(companion.Info{
		ID:       "blob.fs",
		Name:     "Files on disk",
		New:      func() companion.Instance { return new(Store) },
		Provides: []companion.Capability{storage.BlobV1},
		Order:    -90,
	})
}

// DefaultPrefix is the companion's folder when prefix is empty.
const DefaultPrefix = "home-page-companion"

// Store is a directory.
type Store struct {
	// Dir is the root, relative to the data directory ("blobs" when empty).
	Dir string `json:"dir"`
	// Prefix is the companion's folder under Dir.
	Prefix string `json:"prefix"`

	root string
}

func (s *Store) Provision(ctx companion.Context) error {
	s.root = s.Dir
	if s.root == "" {
		s.root = "blobs"
	}
	if !filepath.IsAbs(s.root) {
		s.root = filepath.Join(ctx.DataDir(), s.root)
	}
	if s.Prefix == "" {
		s.Prefix = DefaultPrefix
	}
	return os.MkdirAll(s.root, 0o755)
}

// Bucket returns the view under "<prefix>/<area>".
func (s *Store) Bucket(area string) companion.Bucket {
	return &Dir{Root: s.root, prefix: strings.Trim(path.Join(s.Prefix, area), "/")}
}

// Durable: a directory keeps no history, so backups include the files.
func (s *Store) Durable() bool { return false }

var _ storage.Blob = (*Store)(nil)

// Dir keeps objects as files under Root/<prefix> and an upload's parts under
// Root/.uploads/<id>/.
type Dir struct {
	Root   string
	prefix string
}

// NewDir is a bucket on a directory, without the module; for tests.
func NewDir(root, prefix string) *Dir { return &Dir{Root: root, prefix: strings.Trim(prefix, "/")} }

func (d *Dir) Prefix() string { return d.prefix }

func (d *Dir) key(k string) (string, error) {
	k = strings.TrimPrefix(k, "/")
	clean := path.Clean("/" + k)
	if clean != "/"+k || k == "" {
		return "", fmt.Errorf("blob.fs: invalid key %q", k)
	}
	return path.Join(d.prefix, k), nil
}

func (d *Dir) path(full string) string { return filepath.Join(d.Root, filepath.FromSlash(full)) }

func (d *Dir) uploadDir(id string) string { return filepath.Join(d.Root, ".uploads", id) }

func (d *Dir) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	full, err := d.key(key)
	if err != nil {
		return err
	}
	dst := d.path(full)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".put-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func (d *Dir) info(full, key string, fi fs.FileInfo) companion.ObjectInfo {
	ct := mime.TypeByExtension(path.Ext(key))
	return companion.ObjectInfo{Key: key, Size: fi.Size(), ContentType: ct, ModTime: fi.ModTime(),
		ETag: `"` + strconv.FormatInt(fi.Size(), 36) + "-" + strconv.FormatInt(fi.ModTime().UnixNano(), 36) + `"`}
}

func (d *Dir) Get(_ context.Context, key string) (io.ReadCloser, companion.ObjectInfo, error) {
	full, err := d.key(key)
	if err != nil {
		return nil, companion.ObjectInfo{}, err
	}
	f, err := os.Open(d.path(full))
	if errors.Is(err, os.ErrNotExist) {
		return nil, companion.ObjectInfo{}, companion.ErrNotFound
	}
	if err != nil {
		return nil, companion.ObjectInfo{}, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, companion.ObjectInfo{}, err
	}
	return f, d.info(full, key, fi), nil
}

func (d *Dir) Stat(_ context.Context, key string) (companion.ObjectInfo, error) {
	full, err := d.key(key)
	if err != nil {
		return companion.ObjectInfo{}, err
	}
	fi, err := os.Stat(d.path(full))
	if errors.Is(err, os.ErrNotExist) {
		return companion.ObjectInfo{}, companion.ErrNotFound
	}
	if err != nil {
		return companion.ObjectInfo{}, err
	}
	return d.info(full, key, fi), nil
}

func (d *Dir) ReadRange(_ context.Context, key string, offset, length int64) ([]byte, error) {
	full, err := d.key(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(d.path(full))
	if errors.Is(err, os.ErrNotExist) {
		return nil, companion.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, length)
	n, err := f.ReadAt(b, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return b[:n], nil
}

// List returns every object under prefix (relative to the bucket), sorted.
func (d *Dir) List(_ context.Context, prefix string) ([]companion.ObjectInfo, error) {
	base := d.path(d.prefix)
	var out []companion.ObjectInfo
	err := filepath.WalkDir(base, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if e.IsDir() || strings.HasPrefix(e.Name(), ".put-") || strings.HasSuffix(e.Name(), ".part") {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, ".uploads/") || !strings.HasPrefix(key, prefix) {
			return nil
		}
		fi, err := e.Info()
		if err != nil {
			return nil
		}
		out = append(out, d.info(path.Join(d.prefix, key), key, fi))
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, err
}

func (d *Dir) Delete(_ context.Context, key string) error {
	full, err := d.key(key)
	if err != nil {
		return err
	}
	err = os.Remove(d.path(full))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (d *Dir) CreateUpload(_ context.Context, key, _ string) (string, error) {
	full, err := d.key(key)
	if err != nil {
		return "", err
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := os.MkdirAll(d.uploadDir(id), 0o755); err != nil {
		return "", err
	}
	return id, os.WriteFile(filepath.Join(d.uploadDir(id), "key"), []byte(full), 0o644)
}

func (d *Dir) UploadPart(_ context.Context, _, uploadID string, number int32, data []byte) (string, error) {
	dir := d.uploadDir(uploadID)
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("no such upload %s", uploadID)
	}
	name := filepath.Join(dir, fmt.Sprintf("%05d", number))
	if err := os.WriteFile(name, data, 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("\"%d-%d\"", number, len(data)), nil
}

func (d *Dir) CompleteUpload(_ context.Context, key, uploadID string, parts []companion.CompletedPart) error {
	full, err := d.key(key)
	if err != nil {
		return err
	}
	dir := d.uploadDir(uploadID)
	dst := d.path(full)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	sorted := append([]companion.CompletedPart(nil), parts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Number < sorted[j].Number })
	for _, p := range sorted {
		b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%05d", p.Number)))
		if err != nil {
			f.Close()
			return err
		}
		if _, err := f.Write(b); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (d *Dir) AbortUpload(_ context.Context, _, uploadID string) error {
	return os.RemoveAll(d.uploadDir(uploadID))
}

// ListUploads returns the unfinished uploads whose key starts with prefix
// (relative to the bucket).
func (d *Dir) ListUploads(_ context.Context, prefix string) ([]companion.Upload, error) {
	entries, err := os.ReadDir(filepath.Join(d.Root, ".uploads"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	want := path.Join(d.prefix, prefix)
	if prefix == "" {
		want = d.prefix + "/"
	}
	var out []companion.Upload
	for _, e := range entries {
		key, err := os.ReadFile(filepath.Join(d.uploadDir(e.Name()), "key"))
		if err != nil || !strings.HasPrefix(string(key), want) {
			continue
		}
		info, _ := e.Info()
		var initiated time.Time
		if info != nil {
			initiated = info.ModTime()
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(string(key), d.prefix), "/")
		out = append(out, companion.Upload{Key: rel, UploadID: e.Name(), Initiated: initiated})
	}
	return out, nil
}
