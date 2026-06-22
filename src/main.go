package main

import (
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/admin"
	"github.com/LNA-DEV/HomePageCompanion/autouploader"
	"github.com/LNA-DEV/HomePageCompanion/backfill"
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/interactions"
	"github.com/LNA-DEV/HomePageCompanion/inventory"
	"github.com/LNA-DEV/HomePageCompanion/logger"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/LNA-DEV/HomePageCompanion/webmention"
	"github.com/LNA-DEV/HomePageCompanion/webpush"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/robfig/cron"
)

func main() {
	// Tee log output to dedicated daily files (app-*.log, access-*.log) as
	// well as the original stdout/stderr. Safe to fail silently — we always
	// keep the originals.
	if err := logger.Init(filepath.Join("data", "logs")); err != nil {
		log.Printf("logger init failed: %v", err)
	}

	// Gin's HTTP access log goes to its own access-*.log file (plus the
	// original console FDs for docker logs). It does NOT feed into the app
	// log stream.
	gin.DefaultWriter = logger.Access()
	gin.DefaultErrorWriter = logger.AccessError()

	log.Print("Started companion")

	// Config
	config.LoadConfig()

	// Database
	database.LoadDatabase()
	// Data migrations (must run before schema AutoMigrate so column renames are applied first)
	database.RunMigrations()

	database.MigrateModels([]interface{}{models.Webmention{}, models.AutoUploadItem{}, models.VAPIDKey{}, models.NotificationSubscription{}, models.Feed{}, models.FeedItem{}, models.Author{}, models.Category{}, models.Interaction{}, models.NativeLike{}, models.UploadAttempt{}, models.MicroblogPost{}, models.MicroblogPublication{}, models.MicroblogComment{}, models.Trip{}, models.TripStop{}, models.TripPhoto{}})

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
	c.AddFunc("0 * * * * *", func() { interactions.RunTick() })
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

	// API routes
	api := router.Group("/api")
	{
		api.POST("/webmention", webmention.HandleWebmention)
		api.POST("/upload/:connectionName", validateAPIKey(), uploadNext)
		api.GET("/webpush/vapidkey", getVapidPublicKey)
		api.POST("/webpush/subscribe", webpush.SubscribeHandler())
		api.POST("/webpush/broadcast", validateAPIKey(), broadcast)
		api.GET("/interactions/post/:target_name/:item_id", interactions.HandleInteraction)
		api.POST("/interactions/native/:item_id/like", interactions.HandleNativeLike)
		api.DELETE("/interactions/native/:item_id/like", interactions.HandleNativeUnlike)
		api.GET("/interactions/native/:item_id/status", interactions.HandleNativeLikeStatus)
		api.POST("/interactions/fetch", validateAPIKey(), triggerInteractionsFetch)
		api.POST("/backfill", validateAPIKey(), triggerBackfill)
		// Client log ingest: also accepts ?token=<apiKey> so the browser can
		// flush its buffer via navigator.sendBeacon (which cannot set headers).
		api.POST("/admin/client-logs", validateAPIKeyOrToken(), admin.IngestClientLogs)
	}

	// Admin API routes
	admin.RegisterRoutes(api, validateAPIKey())

	// Microblog routes (both admin and public sub-trees)
	admin.RegisterMicroblogRoutes(api, validateAPIKey())

	// Trip routes (both admin and public sub-trees)
	admin.RegisterTripRoutes(api, validateAPIKey())

	// Health check
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
	found := false

	for _, item := range config.Data.Connections {
		if item.Name == connectionName {
			connection = item
			found = true
			break
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown connection: " + connectionName})
		return
	}

	go autouploader.Publish(connection)
	c.JSON(http.StatusAccepted, gin.H{"status": "publish scheduled", "connection": connectionName})
}

func health(c *gin.Context) {
	jsonData := []byte(`{"msg":"this worked"}`)
	c.Data(http.StatusOK, "application/json", jsonData)
}

func triggerInteractionsFetch(c *gin.Context) {
	go interactions.FetchAllThrottled()
	c.JSON(http.StatusAccepted, gin.H{"status": "Interactions fetch scheduled"})
}

func triggerBackfill(c *gin.Context) {
	go backfill.RunBackfill()
	c.JSON(http.StatusOK, gin.H{"status": "Backfill started"})
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

// validateAPIKeyOrToken accepts either the Authorization: ApiKey <key> header
// (used by normal fetch requests) or a ?token=<key> query param (so the browser
// can flush logs via navigator.sendBeacon, which cannot set custom headers).
func validateAPIKeyOrToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		expectedAuth := "ApiKey " + config.Data.Security.ApiKey
		if c.Request.Header.Get("Authorization") == expectedAuth {
			c.Next()
			return
		}
		if c.Query("token") == config.Data.Security.ApiKey && config.Data.Security.ApiKey != "" {
			c.Next()
			return
		}
		c.JSON(http.StatusUnauthorized, gin.H{"status": http.StatusUnauthorized, "message": "Authentication failed"})
		c.Abort()
	}
}
