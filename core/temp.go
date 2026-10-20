package core

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// tempPrefix is the core's short-lived area in the blob store.
const tempPrefix = "core/tmp"

// tempPath is where the gateway serves temp files.
const tempPath = "/api/core/tmp/"

// metaRow keeps small values of the core itself, such as the signing key of
// temp URLs.
type metaRow struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

func (metaRow) TableName() string { return "core_meta" }

// tempStore implements companion.TempStore on the blob store under
// core/tmp/, with URLs signed by the core. Without a blob module, files
// stay in memory until they expire.
type tempStore struct {
	c       *Core
	keyOnce sync.Once
	key     []byte

	mu  sync.Mutex
	mem map[string]memFile
}

type memFile struct {
	data        []byte
	contentType string
	expires     time.Time
}

func (t *tempStore) signingKey() []byte {
	t.keyOnce.Do(func() {
		if t.c.db != nil {
			var row metaRow
			if t.c.db.Where("key = ?", "temp_key").Limit(1).Find(&row).RowsAffected > 0 {
				t.key, _ = hex.DecodeString(row.Value)
			}
		}
		if len(t.key) == 0 {
			t.key = []byte(newEventID() + newEventID())
			if t.c.db != nil {
				t.c.db.Save(&metaRow{Key: "temp_key", Value: hex.EncodeToString(t.key)})
			}
		}
	})
	return t.key
}

func (t *tempStore) sign(name string, exp int64) string {
	m := hmac.New(sha256.New, t.signingKey())
	fmt.Fprintf(m, "%s|%d", name, exp)
	return hex.EncodeToString(m.Sum(nil))
}

// Put stores data and returns a signed URL valid for ttl.
func (t *tempStore) Put(ctx context.Context, data []byte, contentType string, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > 24*time.Hour {
		ttl = 24 * time.Hour
	}
	name := newEventID()
	exp := time.Now().Add(ttl)
	if t.c.blob != nil {
		b := t.c.bucket(tempPrefix)
		if err := b.Put(ctx, name, bytes.NewReader(data), int64(len(data)), contentType); err != nil {
			return "", err
		}
	} else {
		t.mu.Lock()
		if t.mem == nil {
			t.mem = map[string]memFile{}
		}
		t.mem[name] = memFile{data: data, contentType: contentType, expires: exp}
		t.mu.Unlock()
	}
	q := url.Values{"exp": {strconv.FormatInt(exp.Unix(), 10)}, "sig": {t.sign(name, exp.Unix())}}
	return strings.TrimSuffix(t.c.cfg().Core.PublicURL, "/") + tempPath + name + "?" + q.Encode(), nil
}

var errTempInvalid = errors.New("temp URL invalid or expired")

// open checks a temp URL's name and signature and returns the file.
func (t *tempStore) open(ctx context.Context, name, exp, sig string) ([]byte, string, error) {
	e, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > e || !hmac.Equal([]byte(sig), []byte(t.sign(name, e))) {
		return nil, "", errTempInvalid
	}
	if t.c.blob == nil {
		t.mu.Lock()
		f, ok := t.mem[name]
		t.mu.Unlock()
		if !ok {
			return nil, "", errTempInvalid
		}
		return f.data, f.contentType, nil
	}
	rc, info, err := t.c.bucket(tempPrefix).Get(ctx, name)
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	return b, info.ContentType, err
}

// serve answers GET /api/core/tmp/<name>?exp=…&sig=….
func (t *tempStore) serve(c *gin.Context) {
	b, ct, err := t.open(c.Request.Context(), c.Param("name"), c.Query("exp"), c.Query("sig"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	if ct == "" {
		ct = http.DetectContentType(b)
	}
	c.Data(http.StatusOK, ct, b)
}

// localFetch resolves the core's own temp URLs in-process, so a module
// handing an image to another never goes through the network.
func (c *Core) localFetch(ctx context.Context, raw string) ([]byte, string, bool, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", false, nil
	}
	name, ok := strings.CutPrefix(u.Path, tempPath)
	if !ok || strings.Contains(name, "/") {
		return nil, "", false, nil
	}
	if pub := c.cfg().Core.PublicURL; pub != "" && !strings.HasPrefix(raw, strings.TrimSuffix(pub, "/")) {
		return nil, "", false, nil
	}
	b, ct, err := c.temp.open(ctx, name, u.Query().Get("exp"), u.Query().Get("sig"))
	return b, ct, true, err
}

// cleanup deletes temp files older than a day.
func (t *tempStore) cleanup(ctx context.Context) error {
	cutoff := time.Now().Add(-24 * time.Hour)
	t.mu.Lock()
	for k, f := range t.mem {
		if time.Now().After(f.expires) {
			delete(t.mem, k)
		}
	}
	t.mu.Unlock()
	if t.c.blob == nil {
		return nil
	}
	b := t.c.bucket(tempPrefix)
	objs, err := b.List(ctx, "")
	if err != nil {
		return err
	}
	for _, o := range objs {
		if o.ModTime.Before(cutoff) {
			if err := b.Delete(ctx, o.Key); err != nil {
				return err
			}
		}
	}
	return nil
}
