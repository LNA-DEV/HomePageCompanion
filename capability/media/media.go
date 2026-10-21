// Package media defines media.image/v1: resizing images and reading their
// metadata, for building blocks in any language.
package media

import (
	"context"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// Image processes images.
type Image interface {
	// Resize returns the image fitted into the limits: the long edge
	// shortened, re-encoded as JPEG until it fits MaxBytes. An image that
	// already fits comes back unchanged.
	Resize(ctx context.Context, data []byte, limits Limits) ([]byte, error)
	// ReadExif returns what the image's EXIF, IPTC and XMP say.
	ReadExif(ctx context.Context, data []byte) (Metadata, error)
}

var ImageV1 = companion.NewCapability[Image]("media.image", 1)

// Limits for Resize; 0 means no limit.
type Limits struct {
	MaxBytes      int  `json:"maxBytes,omitempty"`
	MaxLongEdge   int  `json:"maxLongEdge,omitempty"`
	ForceReencode bool `json:"forceReencode,omitempty"`
}

// Metadata is what an image says about itself.
type Metadata struct {
	Make            string   `json:"make,omitempty"`
	Model           string   `json:"model,omitempty"`
	Lens            string   `json:"lens,omitempty"`
	FocalLength     float64  `json:"focalLength,omitempty"`
	FNumber         float64  `json:"fNumber,omitempty"`
	ExposureSeconds float64  `json:"exposureSeconds,omitempty"`
	ISO             int      `json:"iso,omitempty"`
	Artist          string   `json:"artist,omitempty"`
	Copyright       string   `json:"copyright,omitempty"`
	Keywords        []string `json:"keywords,omitempty"`
}
