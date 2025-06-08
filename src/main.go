package main

import (
	"log"
	"net/http"

	"github.com/LNA-DEV/HomePageCompanion/autouploader"
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/inventory"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/LNA-DEV/HomePageCompanion/webmention"
	"github.com/LNA-DEV/HomePageCompanion/webpush"
	"github.com/gin-gonic/gin"
	"github.com/robfig/cron"
)

func main() {
	log.Print("Started companion")

	// Config
	config.LoadConfig()

	// Database
	database.LoadDatabase()
	database.MigrateModels([]interface{}{models.Webmention{}, models.AutoUploadItem{}, models.VAPIDKey{}, models.NotificationSubscription{}, models.Feed{}, models.FeedItem{}, models.Author{}, models.Category{}})

	// Inventory
	inventory.PopulateDatabase()

	// Webpush
	webpush.LoadVAPIDKeys()

	// Cron setup
	c := cron.New()

	for _, connection := range config.Data.Connections {
		if connection.Cron != nil {
			c.AddFunc(*connection.Cron, func() { autouploader.Publish(connection) })
		}
	}

	c.AddFunc("0 */5 * * * *", func() { config.LoadConfig() })
	c.AddFunc("0 * */1 * * *", func() { inventory.PopulateDatabase() })
	c.Start()

	// Router config
	router := gin.Default()

	router.POST("/api/webmention", webmention.HandleWebmention)
	router.POST("/api/upload/:connectionName", validateAPIKey(), uploadNext)
	router.GET("/api/webpush/vapidkey", getVapidPublicKey)
	router.POST("api/webpush/subscribe", webpush.SubscribeHandler())
	router.POST("api/webpush/broadcast", validateAPIKey(), broadcast)
	router.GET("/health", health)

	router.Run(":8080")
}

func broadcast(c *gin.Context) {
	webpush.BroadcastNotification("test")
}

func getVapidPublicKey(c *gin.Context) {
	jsonData := []byte(webpush.VapidKey.PublicKey)
	c.Data(http.StatusOK, "application/text", jsonData)
}

func uploadNext(c *gin.Context) {
	connectionName := c.Param("connectionName")

	var connection config.Connection

	for _, item := range config.Data.Connections {
		if item.Name == connectionName {
			connection = item
			break
		}
	}

	autouploader.Publish(connection)
}

func health(c *gin.Context) {
	jsonData := []byte(`{"msg":"this worked"}`)
	c.Data(http.StatusOK, "application/json", jsonData)
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
