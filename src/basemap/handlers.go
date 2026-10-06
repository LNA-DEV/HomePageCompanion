package basemap

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes wires the public tile endpoints and the admin ones.
//
//	GET  /api/tiles/basemap.json                        TileJSON of the active version
//	GET  /api/tiles/basemap/:version/:z/:x/:y.mvt      one tile (active or retained version)
//	GET  /api/admin/basemap                             status
//	POST /api/admin/basemap/update                      start an update now
//	POST /api/admin/basemap/cleanup                     delete expired versions, stale uploads
func RegisterRoutes(api *gin.RouterGroup, authMiddleware gin.HandlerFunc) {
	api.GET("/tiles/basemap.json", tileJSON)
	api.GET("/tiles/basemap/:version/:z/:x/:y", tile)

	adm := api.Group("/admin/basemap")
	adm.Use(authMiddleware)
	{
		adm.GET("", adminStatus)
		adm.POST("/update", adminUpdate)
		adm.POST("/cleanup", adminCleanup)
	}
}

var versionPattern = regexp.MustCompile(`^\d{8}$`)

func tileJSON(c *gin.Context) {
	if svc == nil {
		unavailable(c, "basemap disabled")
		return
	}
	version := svc.activeVersion()
	if version == "" {
		unavailable(c, "no basemap version is active yet")
		return
	}
	status, headers, body := svc.server.Get(c.Request.Context(), "/"+version+".json")
	if status != http.StatusOK {
		log.Printf("basemap: TileJSON for %s: HTTP %d %s", version, status, body)
		unavailable(c, "basemap unavailable")
		return
	}
	// An hour: a switch to a new version reaches browsers within that, and
	// the old version is retained for days, so nobody is left without tiles.
	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("ETag", headers["ETag"])
	c.Data(http.StatusOK, "application/json", body)
}

func tile(c *gin.Context) {
	if svc == nil {
		unavailable(c, "basemap disabled")
		return
	}
	version := c.Param("version")
	z, x, y, ok := parseZXY(c.Param("z"), c.Param("x"), c.Param("y"), "mvt", 15)
	if !ok || !versionPattern.MatchString(version) || !svc.canServe(version) {
		c.Header("Cache-Control", "public, max-age=300")
		c.String(http.StatusNotFound, "not found")
		return
	}
	status, headers, body := svc.server.Get(c.Request.Context(), fmt.Sprintf("/%s/%d/%d/%d.mvt", version, z, x, y))
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
		log.Printf("basemap: tile %s/%d/%d/%d: HTTP %d %s", version, z, x, y, status, body)
		c.Header("Cache-Control", "no-store")
		c.String(http.StatusBadGateway, "basemap unavailable")
	}
}

// parseZXY validates a tile address: integers, z ≤ maxZoom, x and y inside
// the zoom level's grid, and the expected extension on y.
func parseZXY(zs, xs, ys, ext string, maxZoom int) (uint8, uint32, uint32, bool) {
	ys, found := strings.CutSuffix(ys, "."+ext)
	if !found {
		return 0, 0, 0, false
	}
	z, err1 := strconv.Atoi(zs)
	x, err2 := strconv.ParseUint(xs, 10, 32)
	y, err3 := strconv.ParseUint(ys, 10, 32)
	if err1 != nil || err2 != nil || err3 != nil || z < 0 || z > maxZoom {
		return 0, 0, 0, false
	}
	n := uint64(1) << uint(z)
	if x >= n || y >= n {
		return 0, 0, 0, false
	}
	return uint8(z), uint32(x), uint32(y), true
}

// ParseZXY is parseZXY for other tile proxies in this module.
func ParseZXY(zs, xs, ys, ext string, maxZoom int) (uint8, uint32, uint32, bool) {
	return parseZXY(zs, xs, ys, ext, maxZoom)
}

func unavailable(c *gin.Context, msg string) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": msg})
}

// ---- admin ----

func adminStatus(c *gin.Context) {
	if svc == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false})
		return
	}
	var versions []models.BasemapVersion
	database.Db.Order("id DESC").Limit(12).Find(&versions)
	c.JSON(http.StatusOK, gin.H{
		"enabled":    true,
		"bucket":     redactBucket(svc.bucketURL),
		"prefix":     svc.prefix,
		"source":     svc.cfg.Source,
		"schedule":   svc.cfg.Schedule,
		"publicUrl":  svc.cfg.PublicURL,
		"active":     svc.activeVersion(),
		"job":        svc.job.Status(),
		"versions":   versions,
		"retainDays": svc.cfg.RetainDays,
	})
}

func adminUpdate(c *gin.Context) {
	if svc == nil {
		unavailable(c, "basemap disabled")
		return
	}
	if svc.job.Status().Running {
		c.JSON(http.StatusConflict, gin.H{"error": ErrRunning.Error()})
		return
	}
	StartUpdate()
	c.JSON(http.StatusAccepted, gin.H{"status": "update started"})
}

func adminCleanup(c *gin.Context) {
	if svc == nil {
		unavailable(c, "basemap disabled")
		return
	}
	deleted, aborted, err := svc.job.Cleanup(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "deleted": deleted, "aborted": aborted})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted, "aborted": aborted})
}

// ---- entry points for main ----

// StartUpdate runs an update in the background unless one is running.
func StartUpdate() {
	if svc == nil {
		return
	}
	go func() {
		if err := svc.job.Run(context.Background()); err != nil && err != ErrRunning {
			log.Printf("basemap: %v", err)
		}
	}()
}

// ResumeInterrupted restarts a copy that a crash or restart cut short.
func ResumeInterrupted() {
	if svc == nil {
		return
	}
	var n int64
	database.Db.Model(&models.BasemapVersion{}).
		Where("status IN ?", []string{models.BasemapCopying, models.BasemapVerifying}).Count(&n)
	if n > 0 {
		log.Print("basemap: resuming an interrupted copy")
		StartUpdate()
	}
}

// Cleanup runs the cleanup from cron.
func Cleanup() {
	if svc == nil {
		return
	}
	if _, _, err := svc.job.Cleanup(context.Background()); err != nil {
		log.Printf("basemap: cleanup: %v", err)
	}
}

// Schedule is the configured cron spec ("" when disabled or manual-only).
func Schedule() string {
	if svc == nil {
		return ""
	}
	return svc.cfg.Schedule
}
