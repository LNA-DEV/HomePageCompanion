package companion

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"gorm.io/gorm"
)

// ModuleContext is what the core gives a building block once: access to its
// own areas in the database, the blob store and the secret store, and to the
// events. Everything is scoped to the building block; nothing reaches another
// one's data.
type ModuleContext interface {
	ModuleID() string

	// Log writes to the shared app log with "module=<id>" in front.
	Log() *log.Logger

	// DB is the building block's own area in the database module. Each
	// module migrates its own tables there (GORM AutoMigrate). It is nil
	// until the database module started, which is before any other module.
	DB() *gorm.DB

	// Blobs is the building block's area in the blob module, keys relative
	// to "<prefix>/<area>/".
	Blobs() Bucket

	// Temp is the core's short-lived area (deleted after a day), for files
	// handed to other building blocks, such as images to publish.
	Temp() TempStore

	// Secrets is the building block's part of the secret store: values
	// created at runtime (refreshed tokens, keys), encrypted at rest.
	Secrets() Secrets

	// CacheDir is a directory on local disk for pure caches. The core
	// enforces CacheLimit by deleting least recently used files.
	CacheDir() string

	// Emit publishes an event. It is stored in the outbox first and
	// delivered at least once to every subscriber.
	Emit(ctx context.Context, eventType string, data any) error

	// HTTP is the client for outgoing requests, with the companion's user
	// agent.
	HTTP() *http.Client

	// Instances returns the building block's current instances.
	Instances() []InstanceRef

	// Capability returns another instance's implementation of cap, for an
	// instance connected to one of this building block's slots.
	Capability(instanceID string, cap Capability) (any, error)

	// Metrics records the building block's own values.
	Metrics() Metrics

	// DataDir is the companion's data directory, for the storage modules.
	DataDir() string

	// PublicURL is the companion's public base URL ("https://companion.example.com"),
	// "" when not configured.
	PublicURL() string
}

// Context is what an instance gets in Provision: its module's context plus
// its own name.
type Context interface {
	ModuleContext
	InstanceName() string
	InstanceID() string
}

// InstanceRef is a running instance.
type InstanceRef struct {
	ID       string // "<block>:<name>"
	Name     string
	Instance Instance
}

// Bucket is a view of the blob store under a fixed prefix. Keys use "/".
type Bucket interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	ReadRange(ctx context.Context, key string, offset, length int64) ([]byte, error)
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
	Delete(ctx context.Context, key string) error

	// Resumable multipart upload, for large copies (the basemap).
	CreateUpload(ctx context.Context, key, contentType string) (string, error)
	UploadPart(ctx context.Context, key, uploadID string, number int32, data []byte) (string, error)
	CompleteUpload(ctx context.Context, key, uploadID string, parts []CompletedPart) error
	AbortUpload(ctx context.Context, key, uploadID string) error
	ListUploads(ctx context.Context, prefix string) ([]Upload, error)

	// Prefix is the bucket's full key prefix in the store, for logs.
	Prefix() string
}

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key         string
	Size        int64
	ContentType string
	ModTime     time.Time
	ETag        string
}

// CompletedPart is one finished part of a multipart upload.
type CompletedPart struct {
	Number int32
	ETag   string
}

// Upload is an unfinished multipart upload.
type Upload struct {
	Key       string
	UploadID  string
	Initiated time.Time
}

// TempStore holds short-lived files and hands out URLs to them that other
// building blocks, services included, can fetch.
type TempStore interface {
	// Put stores data and returns a URL valid for ttl.
	Put(ctx context.Context, data []byte, contentType string, ttl time.Duration) (string, error)
}

// Secrets is a building block's part of the secret store.
type Secrets interface {
	Get(name string) (value string, ok bool, err error)
	Put(name, value string) error
	Delete(name string) error
}

// Metrics records values for /metrics, labelled with the building block.
type Metrics interface {
	Add(name string, delta float64, labels ...string)
	Set(name string, value float64, labels ...string)
}

// ErrNotFound is returned by a Bucket for a missing object.
var ErrNotFound = errors.New("not found")

// ErrUploadGone is returned by a Bucket when a multipart upload takes no
// more parts: it was aborted or completed, or another set of credentials
// started it (S3 then answers AccessDenied). Retrying does not help; start
// a new upload.
var ErrUploadGone = errors.New("upload no longer usable")

// ErrDenied is returned by a Bucket when the store refuses the credentials
// or the bucket does not exist. Retrying does not help.
var ErrDenied = errors.New("the store refused the credentials or the bucket")
