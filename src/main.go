package main

import (
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/autouploader"
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/interactions"
	"github.com/LNA-DEV/HomePageCompanion/inventory"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/LNA-DEV/HomePageCompanion/webmention"
	"github.com/LNA-DEV/HomePageCompanion/webpush"
	"github.com/gin-contrib/cors"
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

	// Build regex pattern dynamically
	subdomainRegex := regexp.MustCompile(`^https?://([a-z0-9-]+\.)*` + regexp.QuoteMeta(config.Data.Security.Domain) + `(:[0-9]+)?$`)
	localhostRegex := regexp.MustCompile(`^https?://localhost(:[0-9]+)?$`)

	config := cors.Config{
		AllowOrigins: []string{}, // use AllowOriginFunc instead
		AllowOriginFunc: func(origin string) bool {
			return subdomainRegex.MatchString(origin) || localhostRegex.MatchString(origin)
		},
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Authorization", "Content-Type"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}

	router.Use(cors.New(config))

	router.POST("/api/webmention", webmention.HandleWebmention)
	router.POST("/api/upload/:connectionName", validateAPIKey(), uploadNext)
	router.GET("/api/webpush/vapidkey", getVapidPublicKey)
	router.POST("/api/webpush/subscribe", webpush.SubscribeHandler())
	router.POST("/api/webpush/broadcast", validateAPIKey(), broadcast)
	router.GET("/api/interactions/post/:target_name/:item_name", interactions.HandleInteraction)
	router.GET("/health", health)

	router.Run(":8080")
}

func broadcast(c *gin.Context) {
	var notif models.Notification
	if err := c.ShouldBindJSON(&notif); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON"})
		return
	}

	if notif.Title == "" || notif.Body == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Title and Body are required"})
		return
	}

	webpush.BroadcastNotification(notif)
	c.JSON(http.StatusOK, gin.H{"status": "Broadcast sent"})
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
