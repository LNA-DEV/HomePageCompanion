package main

import (
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
	router.POST("/upload/:platform", uploadNext)

	router.Run(":8080")
}

func uploadNext(c *gin.Context) {
	platform := c.Param("platform")
	autouploader.Publish(platform)
}
