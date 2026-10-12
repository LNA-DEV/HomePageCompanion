package metatoken

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RegisterRoutes wires the admin endpoints. Neither ever returns a token.
func RegisterRoutes(api *gin.RouterGroup, authMiddleware gin.HandlerFunc) {
	adm := api.Group("/admin/tokens")
	adm.Use(authMiddleware)
	{
		adm.GET("", list)
		adm.POST("/:target/refresh", refreshNow)
	}
}

func list(c *gin.Context) {
	c.JSON(http.StatusOK, Statuses())
}

// refreshNow refreshes one target right away and answers with its status. A
// refusal from Meta is not an HTTP error here: it shows up in lastError.
func refreshNow(c *gin.Context) {
	if sealer == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "token refresh disabled: " + disabledReason})
		return
	}
	target := c.Param("target")
	if !RefreshNow(target) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no stored Instagram/Threads token for target " + target})
		return
	}
	s, _ := StatusOf(target)
	c.JSON(http.StatusOK, s)
}
