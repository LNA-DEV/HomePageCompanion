package main

import (
	"net/http"

	"github.com/LNA-DEV/HomePageCompanion/src/autouploader"
	"github.com/LNA-DEV/HomePageCompanion/src/config"
	"github.com/LNA-DEV/HomePageCompanion/src/database"
	"github.com/LNA-DEV/HomePageCompanion/src/webmention"
	"github.com/gin-gonic/gin"
)

func main() {
	// Config
	config.LoadConfig()

	// Database
	database.LoadDatabase()
	database.MigrateModels([]interface{}{webmention.Webmention{}, autouploader.AutoUploadItem{}})

	// Router config
	router := gin.Default()

	router.POST("/webmention", webmention.HandleWebmention)
	router.POST("/upload/:platform", validateAPIKey(), uploadNext)

	router.Run(":8080")
}

func uploadNext(c *gin.Context) {
	platform := c.Param("platform")
	autouploader.Publish(platform)
}

func validateAPIKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		authentication := c.Request.Header.Get("Authorization")
		expectedAuth := "ApiKey " + config.Data.Security.ApiKey

		if authentication != expectedAuth {
			c.JSON(http.StatusUnauthorized, gin.H{"status": http.StatusUnauthorized, "message": "Authentication failed"})
			c.Abort()
			return
		}

		c.Next()
	}
}
