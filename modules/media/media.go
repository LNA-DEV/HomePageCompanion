// Package media is the media module: media.image/v1 (resize, readExif) on
// the companion's image code, so services in any language can use it. Go
// modules use the libraries directly.
package media

import (
	"context"
	"io"
	"net/http"
	"strconv"

	"github.com/LNA-DEV/HomePageCompanion/capability/media"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/lib/imagemeta"
	"github.com/LNA-DEV/HomePageCompanion/lib/imageresize"
	"github.com/gin-gonic/gin"
)

func init() {
	companion.Register(companion.Info{
		ID:       "media",
		Name:     "Images",
		New:      func() companion.Instance { return new(Images) },
		Provides: []companion.Capability{media.ImageV1},
		Routes:   routes,
	})
}

// Images resizes and reads images.
type Images struct{}

func (Images) Resize(_ context.Context, data []byte, lim media.Limits) ([]byte, error) {
	return imageresize.PrepareForTarget(data, imageresize.Limits{MaxBytes: lim.MaxBytes, MaxLongEdge: lim.MaxLongEdge, ForceReencode: lim.ForceReencode})
}

func (Images) ReadExif(_ context.Context, data []byte) (media.Metadata, error) {
	m, err := imagemeta.Extract(data)
	if m == nil {
		return media.Metadata{}, err
	}
	return media.Metadata{Make: m.Make, Model: m.Model, Lens: m.Lens, FocalLength: m.FocalLengthMM, FNumber: m.FNumber,
		ExposureSeconds: m.ExposureSeconds, ISO: m.ISO, Artist: m.Artist, Copyright: m.Copyright, Keywords: m.Keywords}, err
}

var _ media.Image = Images{}

const maxBody = 64 << 20

// routes expose the capability over HTTP for services: POST the image bytes
// to /api/b/media/resize?maxBytes=&maxLongEdge= or /api/b/media/exif.
func routes(mc companion.ModuleContext, r *companion.Router) {
	var img Images
	r.Admin.POST("/resize", func(c *gin.Context) {
		data, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBody))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		lim := media.Limits{MaxBytes: atoi(c.Query("maxBytes")), MaxLongEdge: atoi(c.Query("maxLongEdge")), ForceReencode: c.Query("reencode") == "true"}
		out, err := img.Resize(c.Request.Context(), data, lim)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
			return
		}
		c.Data(http.StatusOK, http.DetectContentType(out), out)
	})
	r.Admin.POST("/exif", func(c *gin.Context) {
		data, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBody))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		m, err := img.ReadExif(c.Request.Context(), data)
		if err != nil && m.Make == "" && len(m.Keywords) == 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, m)
	})
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
