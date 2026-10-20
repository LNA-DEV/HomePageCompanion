// Package storage defines the storage capabilities: storage.sql/v1 (the one
// database module, Go only) and storage.blob/v1 (the one blob module).
// Building blocks never use these directly; the core hands each one its own
// area through companion.ModuleContext.
package storage

import (
	"context"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"gorm.io/gorm"
)

// SQL is the database module. Every building block, the core included, has
// its own area (a file per module with db.sqlite, a schema with Postgres).
type SQL interface {
	// Open returns the area's handle, creating the area if needed.
	Open(area string) (*gorm.DB, error)
	// Areas lists the existing areas.
	Areas() ([]string, error)
	// Backup writes a consistent copy of the area to the file dst.
	Backup(ctx context.Context, area, dst string) error
	// Restore replaces the area with a copy Backup wrote. The area must
	// not be open.
	Restore(ctx context.Context, area, src string) error
	// Drop removes the area and its data.
	Drop(area string) error
	// Close closes every open handle.
	Close() error
}

// Blob is the blob module.
type Blob interface {
	// Bucket returns the view of the store under prefix ("" = the whole
	// configured prefix).
	Bucket(prefix string) companion.Bucket
	// Durable reports whether the store keeps its own history (a
	// versioned S3 bucket), so a backup may leave the files out.
	Durable() bool
}

var (
	SQLV1  = companion.NewCapability[SQL]("storage.sql", 1)
	BlobV1 = companion.NewCapability[Blob]("storage.blob", 1)
)

// ErrNotFound is returned for a missing object.
var ErrNotFound = companion.ErrNotFound
