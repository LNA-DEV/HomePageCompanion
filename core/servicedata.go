package core

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Storage for services (docs/concepts/modular-architecture.md §5.10):
// services get no handle to the database or the blob store, the core
// stores for them, each in its own area.
//
//	/core/v1/data/<collection>/<id>   JSON documents, in the core's database
//	/core/v1/blobs/<key>              files, under services/<id>/ in the blob store
//	/core/v1/uploads                  resumable multipart uploads of large files
//	/core/v1/blob-urls                signed URLs to hand a file to someone else
//	/core/v1/temp                     short-lived files for other building blocks
//	/core/v1/fetch?url=               the core's own URLs (temp, signed blobs)
//
// Both stores are part of every backup, as the modules' are.

// serviceDocument is one document of a service.
type serviceDocument struct {
	ID         uint      `gorm:"primaryKey"`
	Service    string    `gorm:"uniqueIndex:idx_service_doc;size:128"`
	Collection string    `gorm:"uniqueIndex:idx_service_doc;size:128"`
	DocID      string    `gorm:"uniqueIndex:idx_service_doc;size:255"`
	Data       string    `gorm:"type:text"`
	UpdatedAt  time.Time `gorm:"index"`
}

func (serviceDocument) TableName() string { return "core_service_documents" }

const (
	maxDocument   = 1 << 20
	maxBufferBlob = wire.MaxBody
)

var (
	collectionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	documentID     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{0,254}$`)
	blobKey        = regexp.MustCompile(`^[A-Za-z0-9._-][A-Za-z0-9._/-]{0,511}$`)
)

func validKey(k string) bool {
	if !blobKey.MatchString(k) || strings.HasPrefix(k, "/") || strings.HasSuffix(k, "/") {
		return false
	}
	for _, part := range strings.Split(k, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// serviceArea is a service's area in the blob store.
func serviceArea(id string) string { return "services/" + id }

// serviceStorage answers the storage routes of /core/v1/; it reports
// whether the route was one of them.
func (m *serviceManager) serviceStorage(w http.ResponseWriter, r *http.Request, b *svcBlock, rest string) bool {
	switch {
	case rest == "data" || strings.HasPrefix(rest, "data/"):
		m.serviceData(w, r, b, strings.TrimPrefix(strings.TrimPrefix(rest, "data"), "/"))
	case rest == "blobs" || strings.HasPrefix(rest, "blobs/"):
		m.serviceBlobs(w, r, b, strings.TrimPrefix(strings.TrimPrefix(rest, "blobs"), "/"))
	case rest == "uploads" || strings.HasPrefix(rest, "uploads/"):
		m.serviceUploads(w, r, b, strings.TrimPrefix(strings.TrimPrefix(rest, "uploads"), "/"))
	case rest == "blob-urls" && r.Method == http.MethodPost:
		m.serviceBlobURL(w, r, b)
	case rest == "temp" && r.Method == http.MethodPost:
		m.serviceTemp(w, r)
	case rest == "fetch" && r.Method == http.MethodGet:
		m.serviceFetch(w, r)
	default:
		return false
	}
	return true
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ---- documents ----

type docView struct {
	ID        string          `json:"id"`
	Data      json.RawMessage `json:"data"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

func (m *serviceManager) serviceData(w http.ResponseWriter, r *http.Request, b *svcBlock, path string) {
	db := m.c.db
	if db == nil {
		apiError(w, http.StatusServiceUnavailable, wire.CodeUnavailable, "no database")
		return
	}
	collection, id, hasID := strings.Cut(path, "/")
	if !collectionName.MatchString(collection) || (hasID && !documentID.MatchString(id)) {
		apiError(w, http.StatusBadRequest, wire.CodeBadRequest, "collections and ids are letters, digits and . _ - (ids also : @)")
		return
	}
	q := db.Where("service = ? AND collection = ?", b.id, collection)
	if !hasID {
		if r.Method != http.MethodGet {
			apiError(w, http.StatusMethodNotAllowed, wire.CodeBadRequest, "GET a collection; PUT, GET or DELETE a document")
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 1000 {
			limit = 100
		}
		if p := r.URL.Query().Get("prefix"); p != "" {
			q = q.Where("doc_id LIKE ? ESCAPE '\\'", likePrefix(p))
		}
		if a := r.URL.Query().Get("after"); a != "" {
			q = q.Where("doc_id > ?", a)
		}
		var rows []serviceDocument
		if err := q.Order("doc_id").Limit(limit + 1).Find(&rows).Error; err != nil {
			apiError(w, http.StatusInternalServerError, wire.CodeInternal, err.Error())
			return
		}
		out := struct {
			Items []docView `json:"items"`
			Next  string    `json:"next,omitempty"`
		}{Items: []docView{}}
		for i, row := range rows {
			if i == limit {
				out.Next = rows[i-1].DocID
				break
			}
			out.Items = append(out.Items, docView{ID: row.DocID, Data: json.RawMessage(row.Data), UpdatedAt: row.UpdatedAt})
		}
		writeJSONStatus(w, http.StatusOK, out)
		return
	}
	switch r.Method {
	case http.MethodGet:
		var row serviceDocument
		if q.Where("doc_id = ?", id).Limit(1).Find(&row).RowsAffected == 0 {
			apiError(w, http.StatusNotFound, "not_found", "no document "+collection+"/"+id)
			return
		}
		writeJSONStatus(w, http.StatusOK, docView{ID: row.DocID, Data: json.RawMessage(row.Data), UpdatedAt: row.UpdatedAt})
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, maxDocument+1))
		if err != nil || len(body) > maxDocument {
			apiError(w, http.StatusRequestEntityTooLarge, wire.CodeBadRequest, "a document is at most 1 MiB")
			return
		}
		if !json.Valid(body) {
			apiError(w, http.StatusBadRequest, wire.CodeBadRequest, "a document is JSON")
			return
		}
		now := time.Now().UTC()
		err = db.Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&serviceDocument{}).Where("service = ? AND collection = ? AND doc_id = ?", b.id, collection, id).
				Updates(map[string]any{"data": string(body), "updated_at": now})
			if res.Error != nil || res.RowsAffected > 0 {
				return res.Error
			}
			return tx.Create(&serviceDocument{Service: b.id, Collection: collection, DocID: id, Data: string(body), UpdatedAt: now}).Error
		})
		if err != nil {
			apiError(w, http.StatusInternalServerError, wire.CodeInternal, err.Error())
			return
		}
		writeJSONStatus(w, http.StatusOK, docView{ID: id, Data: body, UpdatedAt: now})
	case http.MethodDelete:
		if err := q.Where("doc_id = ?", id).Delete(&serviceDocument{}).Error; err != nil {
			apiError(w, http.StatusInternalServerError, wire.CodeInternal, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		apiError(w, http.StatusMethodNotAllowed, wire.CodeBadRequest, "GET, PUT or DELETE")
	}
}

func likePrefix(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(p) + "%"
}

// ---- files ----

type objectView struct {
	Key         string    `json:"key"`
	Size        int64     `json:"size"`
	ContentType string    `json:"contentType,omitempty"`
	ModTime     time.Time `json:"modTime"`
	ETag        string    `json:"etag,omitempty"`
}

func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, companion.ErrNotFound):
		apiError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, errNoBlobs):
		apiError(w, http.StatusServiceUnavailable, wire.CodeUnavailable, err.Error())
	case errors.Is(err, companion.ErrUploadGone):
		apiError(w, http.StatusGone, "upload_gone", err.Error())
	default:
		apiError(w, http.StatusBadGateway, wire.CodeUnavailable, err.Error())
	}
}

func (m *serviceManager) serviceBlobs(w http.ResponseWriter, r *http.Request, b *svcBlock, key string) {
	bucket := m.c.bucket(serviceArea(b.id))
	ctx := r.Context()
	if key == "" {
		if r.Method != http.MethodGet {
			apiError(w, http.StatusMethodNotAllowed, wire.CodeBadRequest, "GET /core/v1/blobs?prefix= lists")
			return
		}
		objs, err := bucket.List(ctx, r.URL.Query().Get("prefix"))
		if err != nil {
			storeError(w, err)
			return
		}
		out := []objectView{}
		for _, o := range objs {
			out = append(out, objectView{Key: o.Key, Size: o.Size, ContentType: o.ContentType, ModTime: o.ModTime, ETag: o.ETag})
		}
		writeJSONStatus(w, http.StatusOK, out)
		return
	}
	if !validKey(key) {
		apiError(w, http.StatusBadRequest, wire.CodeBadRequest, "keys are letters, digits and . _ - in parts split by /")
		return
	}
	switch r.Method {
	case http.MethodPut:
		ct := r.Header.Get("Content-Type")
		var body io.Reader = r.Body
		size := r.ContentLength
		if size < 0 {
			// No length: buffered, so the store knows the size.
			data, err := io.ReadAll(io.LimitReader(r.Body, maxBufferBlob+1))
			if err != nil || len(data) > maxBufferBlob {
				apiError(w, http.StatusRequestEntityTooLarge, wire.CodeBadRequest, "send Content-Length with a file this large, or use /core/v1/uploads")
				return
			}
			body, size = bytes.NewReader(data), int64(len(data))
		}
		if err := bucket.Put(ctx, key, body, size, ct); err != nil {
			storeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet, http.MethodHead:
		info, err := bucket.Stat(ctx, key)
		if err != nil {
			storeError(w, err)
			return
		}
		if info.ContentType != "" {
			w.Header().Set("Content-Type", info.ContentType)
		}
		if info.ETag != "" {
			w.Header().Set("ETag", info.ETag)
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Last-Modified", info.ModTime.UTC().Format(http.TimeFormat))
		if start, length, ok := parseRange(r.Header.Get("Range"), info.Size); ok {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, info.Size))
			w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
			w.WriteHeader(http.StatusPartialContent)
			if r.Method == http.MethodGet {
				data, err := bucket.ReadRange(ctx, key, start, length)
				if err == nil {
					w.Write(data)
				}
			}
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
		if r.Method == http.MethodHead {
			return
		}
		rc, _, err := bucket.Get(ctx, key)
		if err != nil {
			storeError(w, err)
			return
		}
		defer rc.Close()
		io.Copy(w, rc)
	case http.MethodDelete:
		if err := bucket.Delete(ctx, key); err != nil && !errors.Is(err, companion.ErrNotFound) {
			storeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		apiError(w, http.StatusMethodNotAllowed, wire.CodeBadRequest, "GET, HEAD, PUT or DELETE")
	}
}

// parseRange reads one "bytes=a-b" range.
func parseRange(h string, size int64) (start, length int64, ok bool) {
	spec, found := strings.CutPrefix(h, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false
	}
	a, z, _ := strings.Cut(spec, "-")
	var err error
	if a == "" { // the last z bytes
		n, err := strconv.ParseInt(z, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		if n > size {
			n = size
		}
		return size - n, n, true
	}
	if start, err = strconv.ParseInt(a, 10, 64); err != nil || start >= size {
		return 0, 0, false
	}
	end := size - 1
	if z != "" {
		if end, err = strconv.ParseInt(z, 10, 64); err != nil || end < start {
			return 0, 0, false
		}
		if end >= size {
			end = size - 1
		}
	}
	return start, end - start + 1, true
}

// serviceUploads are multipart uploads for files too large for one
// request (the basemap's builds as a service).
func (m *serviceManager) serviceUploads(w http.ResponseWriter, r *http.Request, b *svcBlock, rest string) {
	bucket := m.c.bucket(serviceArea(b.id))
	ctx := r.Context()
	key := r.URL.Query().Get("key")
	parts := strings.Split(rest, "/")
	switch {
	case rest == "" && r.Method == http.MethodGet:
		ups, err := bucket.ListUploads(ctx, r.URL.Query().Get("prefix"))
		if err != nil {
			storeError(w, err)
			return
		}
		type view struct {
			Key       string    `json:"key"`
			UploadID  string    `json:"uploadId"`
			Initiated time.Time `json:"initiated"`
		}
		out := []view{}
		for _, u := range ups {
			out = append(out, view{u.Key, u.UploadID, u.Initiated})
		}
		writeJSONStatus(w, http.StatusOK, out)
	case rest == "" && r.Method == http.MethodPost:
		var body struct {
			Key         string `json:"key"`
			ContentType string `json:"contentType"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || !validKey(body.Key) {
			apiError(w, http.StatusBadRequest, wire.CodeBadRequest, `body: {"key": "…", "contentType": "…"}`)
			return
		}
		id, err := bucket.CreateUpload(ctx, body.Key, body.ContentType)
		if err != nil {
			storeError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, map[string]string{"uploadId": id})
	case len(parts) == 2 && parts[1] == "complete" && r.Method == http.MethodPost:
		var body struct {
			Key   string `json:"key"`
			Parts []struct {
				Number int32  `json:"number"`
				ETag   string `json:"etag"`
			} `json:"parts"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || !validKey(body.Key) {
			apiError(w, http.StatusBadRequest, wire.CodeBadRequest, `body: {"key": "…", "parts": [{"number", "etag"}]}`)
			return
		}
		var done []companion.CompletedPart
		for _, p := range body.Parts {
			done = append(done, companion.CompletedPart{Number: p.Number, ETag: p.ETag})
		}
		if err := bucket.CompleteUpload(ctx, body.Key, parts[0], done); err != nil {
			storeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 2 && r.Method == http.MethodPut:
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 1 || n > 10000 || !validKey(key) {
			apiError(w, http.StatusBadRequest, wire.CodeBadRequest, "PUT /core/v1/uploads/<id>/<part 1…10000>?key=<key>")
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, 5<<30))
		if err != nil {
			apiError(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())
			return
		}
		etag, err := bucket.UploadPart(ctx, key, parts[0], int32(n), data)
		if err != nil {
			storeError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, map[string]string{"etag": etag})
	case len(parts) == 1 && r.Method == http.MethodDelete:
		if !validKey(key) {
			apiError(w, http.StatusBadRequest, wire.CodeBadRequest, "DELETE /core/v1/uploads/<id>?key=<key>")
			return
		}
		if err := bucket.AbortUpload(ctx, key, parts[0]); err != nil {
			storeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		apiError(w, http.StatusNotFound, "not_found", "no such route: "+r.Method+" "+r.URL.Path)
	}
}

// ---- signed URLs, temp files, fetching the core's URLs ----

// blobPath is where the gateway serves signed blob URLs.
const blobPath = "/api/core/blob/"

func (c *Core) signBlob(full string, exp int64) string {
	h := hmac.New(sha256.New, c.temp.signingKey())
	fmt.Fprintf(h, "blob|%s|%d", full, exp)
	return hex.EncodeToString(h.Sum(nil))
}

// blobURL is a public URL of a file in the blob store, valid for ttl.
func (c *Core) blobURL(full string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	q := url.Values{"exp": {strconv.FormatInt(exp, 10)}, "sig": {c.signBlob(full, exp)}}
	return strings.TrimSuffix(c.cfg().Core.PublicURL, "/") + blobPath + full + "?" + q.Encode()
}

func (m *serviceManager) serviceBlobURL(w http.ResponseWriter, r *http.Request, b *svcBlock) {
	var body struct {
		Key string `json:"key"`
		TTL int    `json:"ttl"` // seconds, at most a week
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || !validKey(body.Key) {
		apiError(w, http.StatusBadRequest, wire.CodeBadRequest, `body: {"key": "…", "ttl": 3600}`)
		return
	}
	ttl := time.Duration(body.TTL) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	if ttl > 7*24*time.Hour {
		ttl = 7 * 24 * time.Hour
	}
	full := serviceArea(b.id) + "/" + body.Key
	if _, err := m.c.bucket("").Stat(r.Context(), full); err != nil {
		storeError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]string{"url": m.c.blobURL(full, ttl)})
}

// openSignedBlob checks a signed blob URL's signature.
func (c *Core) openSignedBlob(ctx context.Context, full, exp, sig string) (io.ReadCloser, companion.ObjectInfo, error) {
	e, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > e || !hmac.Equal([]byte(sig), []byte(c.signBlob(full, e))) || !validKey(full) {
		return nil, companion.ObjectInfo{}, errTempInvalid
	}
	return c.bucket("").Get(ctx, full)
}

// serveSignedBlob answers GET /api/core/blob/<key>?exp=…&sig=…, for anyone
// with the URL.
func (c *Core) serveSignedBlob(g *gin.Context) {
	rc, info, err := c.openSignedBlob(g.Request.Context(), strings.TrimPrefix(g.Param("key"), "/"), g.Query("exp"), g.Query("sig"))
	if err != nil {
		g.Status(http.StatusNotFound)
		return
	}
	defer rc.Close()
	ct := info.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	g.Header("Content-Length", strconv.FormatInt(info.Size, 10))
	g.Header("Content-Type", ct)
	g.Status(http.StatusOK)
	io.Copy(g.Writer, rc)
}

// serviceTemp stores a short-lived file: POST /core/v1/temp?ttl=<seconds>,
// the file as the body; the answer is its URL, valid at most a day.
func (m *serviceManager) serviceTemp(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, companion.MaxFetchBytes+1))
	if err != nil || len(data) > companion.MaxFetchBytes {
		apiError(w, http.StatusRequestEntityTooLarge, wire.CodeBadRequest, "a temp file is at most 64 MiB")
		return
	}
	ttl, _ := strconv.Atoi(r.URL.Query().Get("ttl"))
	u, err := m.c.temp.Put(r.Context(), data, r.Header.Get("Content-Type"), time.Duration(ttl)*time.Second)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]string{"url": u})
}

// serviceFetch hands a service a file behind one of the core's own URLs
// (an image to post, a temp or signed blob URL): the service cannot reach
// the companion's public address, and must not need egress for it. Any
// other URL goes through the egress proxy.
func (m *serviceManager) serviceFetch(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	data, ct, ok, err := m.c.localFetch(r.Context(), raw)
	if !ok {
		apiError(w, http.StatusForbidden, "not_local", "not a URL of the core; fetch it through the egress proxy")
		return
	}
	if err != nil {
		apiError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}
