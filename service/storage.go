package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
)

// The blob store and the temp files of a service are the core's
// (/core/v1/blobs, /core/v1/uploads, /core/v1/temp): a service holds no
// store credentials (docs/concepts/modular-architecture.md §5.10).

func (m *modCtx) Blobs() companion.Bucket   { return coreBucket{m} }
func (m *modCtx) Temp() companion.TempStore { return coreTemp{m} }

// do sends one request to the core's API for services.
func (m *modCtx) do(ctx context.Context, method, path string, body io.Reader, size int64, header map[string]string) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, method, m.coreURL()+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil && size >= 0 {
		r.ContentLength = size
	}
	for k, v := range header {
		r.Header.Set(k, v)
	}
	m.authorize(r)
	resp, err := m.core.Do(r)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		e := wire.ReadError(resp)
		// By the status too: a HEAD answer has no body.
		switch {
		case e.Code == "not_found" || resp.StatusCode == http.StatusNotFound:
			return nil, fmt.Errorf("%s %s: %w", method, path, companion.ErrNotFound)
		case e.Code == "upload_gone" || resp.StatusCode == http.StatusGone:
			return nil, fmt.Errorf("%s %s: %w", method, path, companion.ErrUploadGone)
		}
		return nil, e
	}
	return resp, nil
}

func blobPath(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/core/v1/blobs/" + strings.Join(parts, "/")
}

type coreBucket struct{ m *modCtx }

func (b coreBucket) Prefix() string { return "services/" + b.m.s.info.ID + "/" }

func (b coreBucket) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	h := map[string]string{}
	if contentType != "" {
		h["Content-Type"] = contentType
	}
	resp, err := b.m.do(ctx, http.MethodPut, blobPath(key), r, size, h)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func infoFrom(key string, h http.Header) companion.ObjectInfo {
	info := companion.ObjectInfo{Key: key, ContentType: h.Get("Content-Type"), ETag: h.Get("ETag")}
	if cr := h.Get("Content-Range"); cr != "" {
		if i := strings.LastIndexByte(cr, '/'); i >= 0 {
			info.Size, _ = strconv.ParseInt(cr[i+1:], 10, 64)
		}
	} else {
		info.Size, _ = strconv.ParseInt(h.Get("Content-Length"), 10, 64)
	}
	info.ModTime, _ = http.ParseTime(h.Get("Last-Modified"))
	return info
}

func (b coreBucket) Get(ctx context.Context, key string) (io.ReadCloser, companion.ObjectInfo, error) {
	resp, err := b.m.do(ctx, http.MethodGet, blobPath(key), nil, -1, nil)
	if err != nil {
		return nil, companion.ObjectInfo{}, err
	}
	return resp.Body, infoFrom(key, resp.Header), nil
}

func (b coreBucket) ReadRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	resp, err := b.m.do(ctx, http.MethodGet, blobPath(key), nil, -1, map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (b coreBucket) Stat(ctx context.Context, key string) (companion.ObjectInfo, error) {
	resp, err := b.m.do(ctx, http.MethodHead, blobPath(key), nil, -1, nil)
	if err != nil {
		return companion.ObjectInfo{}, err
	}
	resp.Body.Close()
	return infoFrom(key, resp.Header), nil
}

func (b coreBucket) List(ctx context.Context, prefix string) ([]companion.ObjectInfo, error) {
	resp, err := b.m.do(ctx, http.MethodGet, "/core/v1/blobs?prefix="+url.QueryEscape(prefix), nil, -1, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var objs []struct {
		Key         string    `json:"key"`
		Size        int64     `json:"size"`
		ContentType string    `json:"contentType"`
		ModTime     time.Time `json:"modTime"`
		ETag        string    `json:"etag"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&objs); err != nil {
		return nil, err
	}
	out := make([]companion.ObjectInfo, len(objs))
	for i, o := range objs {
		out[i] = companion.ObjectInfo{Key: o.Key, Size: o.Size, ContentType: o.ContentType, ModTime: o.ModTime, ETag: o.ETag}
	}
	return out, nil
}

func (b coreBucket) Delete(ctx context.Context, key string) error {
	resp, err := b.m.do(ctx, http.MethodDelete, blobPath(key), nil, -1, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (b coreBucket) jsonCall(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	size := int64(-1)
	if in != nil {
		raw, _ := json.Marshal(in)
		body, size = bytes.NewReader(raw), int64(len(raw))
	}
	resp, err := b.m.do(ctx, method, path, body, size, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (b coreBucket) CreateUpload(ctx context.Context, key, contentType string) (string, error) {
	var out struct {
		UploadID string `json:"uploadId"`
	}
	err := b.jsonCall(ctx, http.MethodPost, "/core/v1/uploads", map[string]string{"key": key, "contentType": contentType}, &out)
	return out.UploadID, err
}

func (b coreBucket) UploadPart(ctx context.Context, key, uploadID string, number int32, data []byte) (string, error) {
	resp, err := b.m.do(ctx, http.MethodPut, fmt.Sprintf("/core/v1/uploads/%s/%d?key=%s", url.PathEscape(uploadID), number, url.QueryEscape(key)), bytes.NewReader(data), int64(len(data)), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		ETag string `json:"etag"`
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out.ETag, err
}

func (b coreBucket) CompleteUpload(ctx context.Context, key, uploadID string, parts []companion.CompletedPart) error {
	type part struct {
		Number int32  `json:"number"`
		ETag   string `json:"etag"`
	}
	ps := make([]part, len(parts))
	for i, p := range parts {
		ps[i] = part{p.Number, p.ETag}
	}
	return b.jsonCall(ctx, http.MethodPost, "/core/v1/uploads/"+url.PathEscape(uploadID)+"/complete", map[string]any{"key": key, "parts": ps}, nil)
}

func (b coreBucket) AbortUpload(ctx context.Context, key, uploadID string) error {
	return b.jsonCall(ctx, http.MethodDelete, "/core/v1/uploads/"+url.PathEscape(uploadID)+"?key="+url.QueryEscape(key), nil, nil)
}

func (b coreBucket) ListUploads(ctx context.Context, prefix string) ([]companion.Upload, error) {
	var ups []struct {
		Key       string    `json:"key"`
		UploadID  string    `json:"uploadId"`
		Initiated time.Time `json:"initiated"`
	}
	if err := b.jsonCall(ctx, http.MethodGet, "/core/v1/uploads?prefix="+url.QueryEscape(prefix), nil, &ups); err != nil {
		return nil, err
	}
	out := make([]companion.Upload, len(ups))
	for i, u := range ups {
		out[i] = companion.Upload{Key: u.Key, UploadID: u.UploadID, Initiated: u.Initiated}
	}
	return out, nil
}

type coreTemp struct{ m *modCtx }

func (t coreTemp) Put(ctx context.Context, data []byte, contentType string, ttl time.Duration) (string, error) {
	h := map[string]string{}
	if contentType != "" {
		h["Content-Type"] = contentType
	}
	resp, err := t.m.do(ctx, http.MethodPost, "/core/v1/temp?ttl="+strconv.Itoa(int(ttl.Seconds())), bytes.NewReader(data), int64(len(data)), h)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		URL string `json:"url"`
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out.URL, err
}

// fetchLocal resolves the core's own URLs (temp files, signed blob URLs)
// through /core/v1/fetch: a service cannot reach the companion's public
// address, and needs no egress for it. companion.Fetch asks it first.
func (m *modCtx) fetchLocal(ctx context.Context, raw string) ([]byte, string, bool, error) {
	pub := strings.TrimSuffix(m.PublicURL(), "/")
	if pub == "" || !(strings.HasPrefix(raw, pub+"/api/core/tmp/") || strings.HasPrefix(raw, pub+"/api/core/blob/")) {
		return nil, "", false, nil
	}
	resp, err := m.do(ctx, http.MethodGet, "/core/v1/fetch?url="+url.QueryEscape(raw), nil, -1, nil)
	if err != nil {
		return nil, "", true, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, companion.MaxFetchBytes+1))
	if err == nil && len(b) > companion.MaxFetchBytes {
		err = errors.New("fetch: larger than the limit")
	}
	return b, resp.Header.Get("Content-Type"), true, err
}
