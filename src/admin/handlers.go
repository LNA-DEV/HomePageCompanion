package admin

import (
	"net/http"
	"strconv"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/inventory"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/gin-gonic/gin"
)

// DashboardStats represents aggregated statistics for the dashboard
type DashboardStats struct {
	FeedCount         int64            `json:"feedCount"`
	FeedItemCount     int64            `json:"feedItemCount"`
	PublicationCount  int64            `json:"publicationCount"`
	InteractionCount  int64            `json:"interactionCount"`
	TotalLikes        int64            `json:"totalLikes"`
	SubscriberCount   int64            `json:"subscriberCount"`
	WebmentionCount   int64            `json:"webmentionCount"`
	NativeLikeCount   int64            `json:"nativeLikeCount"`
	ConnectionCount   int              `json:"connectionCount"`
	PlatformBreakdown map[string]int64 `json:"platformBreakdown"`
}

// FeedWithCount represents a feed with its item count
type FeedWithCount struct {
	models.Feed
	ItemCount int64 `json:"itemCount"`
}

// ConnectionInfo represents a connection with sanitized info (no secrets)
type ConnectionInfo struct {
	Name       string  `json:"name"`
	SourceName string  `json:"sourceName"`
	TargetName string  `json:"targetName"`
	Caption    string  `json:"caption"`
	Cron       *string `json:"cron"`
	Platform   string  `json:"platform"`
}

// RegisterRoutes registers all admin API routes
func RegisterRoutes(router *gin.RouterGroup, authMiddleware gin.HandlerFunc) {
	admin := router.Group("/admin")
	admin.Use(authMiddleware)
	{
		admin.GET("/auth/verify", VerifyAuth)
		admin.GET("/stats", GetStats)
		admin.GET("/feeds", GetFeeds)
		admin.GET("/feeds/:id", GetFeed)
		admin.GET("/feeds/:id/items", GetFeedItems)
		admin.GET("/publications", GetPublications)
		admin.DELETE("/publications/:id", DeletePublication)
		admin.GET("/interactions", GetInteractions)
		admin.GET("/interactions/summary", GetInteractionsSummary)
		admin.GET("/subscribers", GetSubscribers)
		admin.DELETE("/subscribers/:id", DeleteSubscriber)
		admin.GET("/webmentions", GetWebmentions)
		admin.GET("/connections", GetConnections)
	}
}

// VerifyAuth verifies that the API key is valid
func VerifyAuth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"valid": true})
}

// GetStats returns dashboard statistics
func GetStats(c *gin.Context) {
	var stats DashboardStats

	database.Db.Model(&models.Feed{}).Count(&stats.FeedCount)
	database.Db.Model(&models.FeedItem{}).Count(&stats.FeedItemCount)
	database.Db.Model(&models.AutoUploadItem{}).Count(&stats.PublicationCount)
	database.Db.Model(&models.Interaction{}).Count(&stats.InteractionCount)
	database.Db.Model(&models.NotificationSubscription{}).Count(&stats.SubscriberCount)
	database.Db.Model(&models.Webmention{}).Count(&stats.WebmentionCount)
	database.Db.Model(&models.NativeLike{}).Count(&stats.NativeLikeCount)

	// Sum of all likes from interactions
	database.Db.Model(&models.Interaction{}).Select("COALESCE(SUM(like_count), 0)").Scan(&stats.TotalLikes)

	stats.ConnectionCount = len(config.Data.Connections)

	// Platform breakdown for publications
	stats.PlatformBreakdown = make(map[string]int64)
	var platformCounts []struct {
		Platform string
		Count    int64
	}
	database.Db.Model(&models.AutoUploadItem{}).
		Select("platform, count(*) as count").
		Group("platform").
		Scan(&platformCounts)
	for _, pc := range platformCounts {
		stats.PlatformBreakdown[pc.Platform] = pc.Count
	}

	c.JSON(http.StatusOK, stats)
}

// GetFeeds returns all feeds with item counts
func GetFeeds(c *gin.Context) {
	feeds, err := inventory.ListFeeds(false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch feeds"})
		return
	}

	// Get item counts for each feed
	var result []FeedWithCount
	for _, feed := range feeds {
		var count int64
		database.Db.Model(&models.FeedItem{}).Where("feed_id = ?", feed.ID).Count(&count)
		result = append(result, FeedWithCount{
			Feed:      feed,
			ItemCount: count,
		})
	}

	c.JSON(http.StatusOK, result)
}

// GetFeed returns a single feed by ID
func GetFeed(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid feed ID"})
		return
	}

	feed, err := inventory.GetFeedByID(uint(id), false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch feed"})
		return
	}
	if feed == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Feed not found"})
		return
	}

	c.JSON(http.StatusOK, feed)
}

// GetFeedItems returns paginated items for a feed
func GetFeedItems(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid feed ID"})
		return
	}

	// Pagination
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	var items []models.FeedItem
	var total int64

	database.Db.Model(&models.FeedItem{}).Where("feed_id = ?", id).Count(&total)
	database.Db.Where("feed_id = ?", id).
		Preload("Categories").
		Preload("Authors").
		Order("published DESC").
		Offset(offset).
		Limit(limit).
		Find(&items)

	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

// GetPublications returns all auto-uploaded items
func GetPublications(c *gin.Context) {
	var items []models.AutoUploadItem

	// Optional filtering
	platform := c.Query("platform")
	query := database.Db.Model(&models.AutoUploadItem{})
	if platform != "" {
		query = query.Where("platform = ?", platform)
	}

	query.Order("created_at DESC").Find(&items)
	c.JSON(http.StatusOK, items)
}

// DeletePublication deletes a publication record
func DeletePublication(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID"})
		return
	}

	result := database.Db.Delete(&models.AutoUploadItem{}, id)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete publication"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Publication not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Publication deleted"})
}

// GetInteractions returns all interactions
func GetInteractions(c *gin.Context) {
	var interactions []models.Interaction

	// Optional filtering
	platform := c.Query("platform")
	itemName := c.Query("itemName")

	query := database.Db.Model(&models.Interaction{})
	if platform != "" {
		query = query.Where("platform = ?", platform)
	}
	if itemName != "" {
		query = query.Where("item_name = ?", itemName)
	}

	query.Order("updated_at DESC").Find(&interactions)
	c.JSON(http.StatusOK, interactions)
}

// InteractionSummary represents aggregated interaction stats
type InteractionSummary struct {
	TotalLikes        int64            `json:"totalLikes"`
	TotalNativeLikes  int64            `json:"totalNativeLikes"`
	PlatformBreakdown map[string]int64 `json:"platformBreakdown"`
	TopItems          []ItemLikes      `json:"topItems"`
}

// ItemLikes represents likes for a specific item
type ItemLikes struct {
	ItemName   string `json:"itemName"`
	TotalLikes int64  `json:"totalLikes"`
}

// GetInteractionsSummary returns aggregated interaction statistics
func GetInteractionsSummary(c *gin.Context) {
	var summary InteractionSummary

	// Total likes from all platforms
	database.Db.Model(&models.Interaction{}).
		Select("COALESCE(SUM(like_count), 0)").
		Scan(&summary.TotalLikes)

	// Total native likes
	database.Db.Model(&models.NativeLike{}).Count(&summary.TotalNativeLikes)

	// Breakdown by platform
	summary.PlatformBreakdown = make(map[string]int64)
	var platformSums []struct {
		Platform string
		Total    int64
	}
	database.Db.Model(&models.Interaction{}).
		Select("platform, COALESCE(SUM(like_count), 0) as total").
		Group("platform").
		Scan(&platformSums)
	for _, ps := range platformSums {
		summary.PlatformBreakdown[ps.Platform] = ps.Total
	}

	// Top 10 items by total likes
	database.Db.Model(&models.Interaction{}).
		Select("item_name, COALESCE(SUM(like_count), 0) as total_likes").
		Group("item_name").
		Order("total_likes DESC").
		Limit(10).
		Scan(&summary.TopItems)

	c.JSON(http.StatusOK, summary)
}

// GetSubscribers returns all push notification subscribers
func GetSubscribers(c *gin.Context) {
	var subscribers []models.NotificationSubscription
	database.Db.Order("created_at DESC").Find(&subscribers)

	// Sanitize - don't expose auth keys
	type SafeSubscriber struct {
		ID        uint   `json:"id"`
		Endpoint  string `json:"endpoint"`
		CreatedAt string `json:"createdAt"`
	}

	var result []SafeSubscriber
	for _, sub := range subscribers {
		result = append(result, SafeSubscriber{
			ID:        sub.ID,
			Endpoint:  sub.Endpoint,
			CreatedAt: sub.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	c.JSON(http.StatusOK, result)
}

// DeleteSubscriber removes a push notification subscriber
func DeleteSubscriber(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID"})
		return
	}

	result := database.Db.Delete(&models.NotificationSubscription{}, id)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete subscriber"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Subscriber not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Subscriber deleted"})
}

// GetWebmentions returns all webmentions
func GetWebmentions(c *gin.Context) {
	var webmentions []models.Webmention
	database.Db.Order("created_at DESC").Find(&webmentions)
	c.JSON(http.StatusOK, webmentions)
}

// GetConnections returns all configured connections (sanitized)
func GetConnections(c *gin.Context) {
	// Build a map of target name to platform
	targetPlatforms := make(map[string]string)
	for _, target := range config.Data.Targets {
		targetPlatforms[target.Name] = target.Platform
	}

	var connections []ConnectionInfo
	for _, conn := range config.Data.Connections {
		connections = append(connections, ConnectionInfo{
			Name:       conn.Name,
			SourceName: conn.SourceName,
			TargetName: conn.TargetName,
			Caption:    conn.Caption,
			Cron:       conn.Cron,
			Platform:   targetPlatforms[conn.TargetName],
		})
	}

	c.JSON(http.StatusOK, connections)
}
