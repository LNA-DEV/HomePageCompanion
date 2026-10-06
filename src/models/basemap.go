package models

import "time"

// Basemap version states. A version moves copying → verifying → active, then
// retained (still served, so browsers holding the old TileJSON keep working)
// → deleted. failed is terminal; its multipart upload is aborted.
const (
	BasemapCopying   = "copying"
	BasemapVerifying = "verifying"
	BasemapActive    = "active"
	BasemapRetained  = "retained"
	BasemapDeleted   = "deleted"
	BasemapFailed    = "failed"
)

// BasemapVersion is one copy of a Protomaps daily build in the bucket.
// Version is the build date (YYYYMMDD) and doubles as the tileset name in
// tile URLs, which is what makes those URLs immutable.
type BasemapVersion struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	Version       string     `gorm:"uniqueIndex" json:"version"`
	Key           string     `json:"key"`
	Size          int64      `json:"size"`
	SourceURL     string     `json:"sourceUrl"`
	SourceETag    string     `json:"sourceETag"`
	SchemaVersion string     `json:"schemaVersion"` // the build's metadata "version", e.g. 4.15.2
	OSMTime       string     `json:"osmTime"`       // planetiler:osm:osmosisreplicationtime
	Status        string     `gorm:"index" json:"status"`
	UploadID      string     `json:"-"`
	PartSize      int64      `json:"partSize"`
	PartsTotal    int        `json:"partsTotal"`
	Error         string     `json:"error,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	ActivatedAt   *time.Time `json:"activatedAt,omitempty"`
	DeleteAfter   *time.Time `json:"deleteAfter,omitempty"`
}

// BasemapPart records one finished part of a version's multipart upload, so a
// crash or a container restart resumes at the next missing part instead of
// starting the 139 GB copy over.
type BasemapPart struct {
	ID        uint  `gorm:"primaryKey"`
	VersionID uint  `gorm:"uniqueIndex:idx_basemap_part"`
	Number    int32 `gorm:"uniqueIndex:idx_basemap_part"` // 1-based, as S3 counts
	ETag      string
	Size      int64
}
