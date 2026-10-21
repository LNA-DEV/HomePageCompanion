package basemap

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/lib/tiles"
	"github.com/gin-gonic/gin"
)

func instance(mc companion.ModuleContext) *Basemap {
	for _, in := range mc.Instances() {
		if b, ok := in.Instance.(*Basemap); ok {
			return b
		}
	}
	return nil
}

// routes:
//
//	GET  /api/tiles/basemap.json                    TileJSON of the active version
//	GET  /api/tiles/basemap/:version/:z/:x/:y.mvt  one tile (active or retained version)
//	GET  /api/b/basemap/                            status
//	POST /api/b/basemap/update                      start an update now
//	POST /api/b/basemap/cleanup                     delete expired versions, stale uploads
func routes(mc companion.ModuleContext, r *companion.Router) {
	with := func(h func(*gin.Context, *Basemap)) gin.HandlerFunc {
		return func(c *gin.Context) {
			b := instance(mc)
			if b == nil {
				unavailable(c, "basemap not configured")
				return
			}
			h(c, b)
		}
	}
	r.Alias(tilesPath+".json").GET("", with(tileJSON))
	r.Alias(tilesPath).GET("/:version/:z/:x/:y", with(tile))

	r.Admin.GET("", func(c *gin.Context) {
		b := instance(mc)
		if b == nil {
			c.JSON(http.StatusOK, gin.H{"enabled": false})
			return
		}
		var versions []Version
		mc.DB().Order("id DESC").Limit(12).Find(&versions)
		c.JSON(http.StatusOK, gin.H{
			"enabled":    true,
			"store":      mc.Blobs().Prefix(),
			"source":     b.Source,
			"schedule":   b.Schedule,
			"publicUrl":  b.PublicURL,
			"active":     b.activeVersion(),
			"job":        b.job.Status(),
			"versions":   versions,
			"retainDays": b.RetainDays,
		})
	})
	r.Admin.POST("/update", with(func(c *gin.Context, b *Basemap) {
		if b.job.Status().Running {
			c.JSON(http.StatusConflict, gin.H{"error": ErrRunning.Error()})
			return
		}
		b.update()
		c.JSON(http.StatusAccepted, gin.H{"status": "update started"})
	}))
	r.Admin.POST("/cleanup", with(func(c *gin.Context, b *Basemap) {
		deleted, aborted, err := b.job.Cleanup(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "deleted": deleted, "aborted": aborted})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": deleted, "aborted": aborted})
	}))
}

var versionPattern = regexp.MustCompile(`^\d{8}$`)

func tileJSON(c *gin.Context, b *Basemap) {
	version := b.activeVersion()
	if version == "" {
		unavailable(c, "no basemap version is active yet")
		return
	}
	status, headers, body := b.server.Get(c.Request.Context(), "/"+version+".json")
	if status != http.StatusOK {
		b.ctx.Log().Printf("TileJSON for %s: HTTP %d %s", version, status, body)
		unavailable(c, "basemap unavailable")
		return
	}
	// An hour: a switch to a new version reaches browsers within that, and
	// the old version is retained for days, so nobody is left without tiles.
	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("ETag", headers["ETag"])
	c.Data(http.StatusOK, "application/json", body)
}

func tile(c *gin.Context, b *Basemap) {
	version := c.Param("version")
	z, x, y, ok := tiles.ParseZXY(c.Param("z"), c.Param("x"), c.Param("y"), "mvt", 15)
	if !ok || !versionPattern.MatchString(version) || !b.canServe(version) {
		c.Header("Cache-Control", "public, max-age=300")
		c.String(http.StatusNotFound, "not found")
		return
	}
	status, headers, body := b.server.Get(c.Request.Context(), fmt.Sprintf("/%s/%d/%d/%d.mvt", version, z, x, y))
	switch status {
	case http.StatusOK:
		for _, h := range []string{"Content-Type", "Content-Encoding", "ETag"} {
			if v := headers[h]; v != "" {
				c.Header(h, v)
			}
		}
		// The version is in the URL, so a tile under it never changes.
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.Data(http.StatusOK, headers["Content-Type"], body)
	case http.StatusNoContent:
		// No tile there (open sea at high zoom): just as immutable.
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.Status(http.StatusNoContent)
	case http.StatusNotFound:
		c.Header("Cache-Control", "public, max-age=300")
		c.String(http.StatusNotFound, "not found")
	default:
		b.ctx.Log().Printf("tile %s/%d/%d/%d: HTTP %d %s", version, z, x, y, status, body)
		c.Header("Cache-Control", "no-store")
		c.String(http.StatusBadGateway, "basemap unavailable")
	}
}

func unavailable(c *gin.Context, msg string) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": msg})
}
