package routing

import (
	"net/http"

	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes wires the admin endpoints. There is no public one: the
// geometry reaches the website inside the trip payload.
func RegisterRoutes(api *gin.RouterGroup, authMiddleware gin.HandlerFunc) {
	adm := api.Group("/admin/routes")
	adm.Use(authMiddleware)
	{
		adm.GET("", status)
		adm.POST("/backfill", backfill)
		adm.POST("/:key/recompute", recompute)
	}
}

func status(c *gin.Context) {
	var ok, failed int64
	database.Db.Model(&models.RouteGeometry{}).Where("status = ?", StatusOK).Count(&ok)
	database.Db.Model(&models.RouteGeometry{}).Where("status = ?", StatusFailed).Count(&failed)
	c.JSON(http.StatusOK, gin.H{"ok": ok, "failed": failed, "pending": Default.Pending()})
}

// backfill queues every leg without geometry and every failed one;
// ?force=1 recomputes all of them.
func backfill(c *gin.Context) {
	n := EnqueueAll(c.Query("force") == "1")
	c.JSON(http.StatusAccepted, gin.H{"queued": n})
}

func recompute(c *gin.Context) {
	if !Recompute(c.Param("key")) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no current leg has that key"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "queued"})
}
