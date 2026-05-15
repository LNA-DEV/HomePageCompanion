package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/inventory"
	"github.com/LNA-DEV/HomePageCompanion/logger"
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
	Name         string  `json:"name"`
	SourceName   string  `json:"sourceName"`
	TargetName   string  `json:"targetName"`
	Caption      string  `json:"caption"`
	Cron         *string `json:"cron"`
	Platform     string  `json:"platform"`
	SourceFeedID *uint   `json:"sourceFeedId,omitempty"`
	TargetUrl    string  `json:"targetUrl,omitempty"`
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
		admin.GET("/feed-items/lookup", GetFeedItemsLookup)
		admin.POST("/feed-items/lookup", GetFeedItemsLookup)
		admin.GET("/publications", GetPublications)
		admin.DELETE("/publications/:id", DeletePublication)
		admin.GET("/interactions", GetInteractions)
		admin.GET("/interactions/summary", GetInteractionsSummary)
		admin.GET("/subscribers", GetSubscribers)
		admin.DELETE("/subscribers/:id", DeleteSubscriber)
		admin.GET("/webmentions", GetWebmentions)
		admin.GET("/connections", GetConnections)
		admin.GET("/logs", GetLogs)
		admin.GET("/upload-attempts", GetUploadAttempts)
		admin.GET("/targets/health", GetTargetHealth)
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

// RecentFailure captures the latest failed upload per platform for a feed item.
type RecentFailure struct {
	Platform   string    `json:"platform"`
	TargetName string    `json:"targetName"`
	ErrorCode  string    `json:"errorCode"`
	HTTPStatus int       `json:"httpStatus,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// FeedItemWithEngagement enriches a FeedItem with publications, interactions, and counts.
type FeedItemWithEngagement struct {
	models.FeedItem
	Publications    []models.AutoUploadItem `json:"publications"`
	Interactions    []models.Interaction    `json:"interactions"`
	NativeLikeCount int64                   `json:"nativeLikeCount"`
	WebmentionCount int64                   `json:"webmentionCount"`
	RecentFailures  []RecentFailure         `json:"recentFailures"`
}

// GetFeedItems returns paginated items for a feed, each enriched with the
// publications, interactions, native-like count, and webmention count it produced.
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

	enriched := enrichFeedItems(items)

	c.JSON(http.StatusOK, gin.H{
		"items": enriched,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

// enrichFeedItems joins publications, interactions, native-like counts, and
// webmention counts onto a slice of feed items using bulk queries.
func enrichFeedItems(items []models.FeedItem) []FeedItemWithEngagement {
	result := make([]FeedItemWithEngagement, len(items))
	if len(items) == 0 {
		return result
	}

	guids := make([]string, 0, len(items))
	links := make([]string, 0, len(items))
	for _, it := range items {
		if it.GUID != "" {
			guids = append(guids, it.GUID)
		}
		if it.Link != "" {
			links = append(links, it.Link)
		}
	}

	pubsByGUID := make(map[string][]models.AutoUploadItem)
	intsByGUID := make(map[string][]models.Interaction)
	nativeByGUID := make(map[string]int64)
	webmentionByLink := make(map[string]int64)
	failuresByGUID := make(map[string][]RecentFailure)

	if len(guids) > 0 {
		var pubs []models.AutoUploadItem
		database.Db.Where("item_id IN ?", guids).Find(&pubs)
		for _, p := range pubs {
			pubsByGUID[p.ItemID] = append(pubsByGUID[p.ItemID], p)
		}

		var ints []models.Interaction
		database.Db.Where("item_id IN ?", guids).Find(&ints)
		for _, i := range ints {
			intsByGUID[i.ItemID] = append(intsByGUID[i.ItemID], i)
		}

		var nativeRows []struct {
			ItemID string
			Count  int64
		}
		database.Db.Model(&models.NativeLike{}).
			Select("item_id, COUNT(*) as count").
			Where("item_id IN ?", guids).
			Group("item_id").
			Scan(&nativeRows)
		for _, r := range nativeRows {
			nativeByGUID[r.ItemID] = r.Count
		}

		// Most recent failed attempt per (item, platform) within the last 7 days.
		failureCutoff := time.Now().Add(-7 * 24 * time.Hour)
		var failures []models.UploadAttempt
		database.Db.Where("item_id IN ? AND success = ? AND created_at >= ?", guids, false, failureCutoff).
			Order("created_at DESC").
			Find(&failures)
		seen := make(map[string]bool) // key: itemID + "|" + platform
		for _, f := range failures {
			key := f.ItemID + "|" + f.Platform
			if seen[key] {
				continue
			}
			seen[key] = true
			failuresByGUID[f.ItemID] = append(failuresByGUID[f.ItemID], RecentFailure{
				Platform:   f.Platform,
				TargetName: f.TargetName,
				ErrorCode:  f.ErrorCode,
				HTTPStatus: f.HTTPStatus,
				CreatedAt:  f.CreatedAt,
			})
		}
	}

	if len(links) > 0 {
		var wmRows []struct {
			Target string
			Count  int64
		}
		database.Db.Model(&models.Webmention{}).
			Select("target, COUNT(*) as count").
			Where("target IN ?", links).
			Group("target").
			Scan(&wmRows)
		for _, r := range wmRows {
			webmentionByLink[r.Target] = r.Count
		}
	}

	for i, it := range items {
		pubs := pubsByGUID[it.GUID]
		if pubs == nil {
			pubs = []models.AutoUploadItem{}
		}
		ints := intsByGUID[it.GUID]
		if ints == nil {
			ints = []models.Interaction{}
		}
		fails := failuresByGUID[it.GUID]
		if fails == nil {
			fails = []RecentFailure{}
		}
		result[i] = FeedItemWithEngagement{
			FeedItem:        it,
			Publications:    pubs,
			Interactions:    ints,
			NativeLikeCount: nativeByGUID[it.GUID],
			WebmentionCount: webmentionByLink[it.Link],
			RecentFailures:  fails,
		}
	}

	return result
}

// MiniFeedItem is a compact projection used by the lookup endpoint.
type MiniFeedItem struct {
	ID       uint   `json:"id"`
	FeedID   uint   `json:"feedId"`
	Title    string `json:"title"`
	ImageUrl string `json:"imageUrl"`
	Link     string `json:"link"`
}

// FeedItemsLookupRequest is the optional JSON body for the lookup endpoint.
// Either guids or links may be supplied. Query-string fallback is also supported.
type FeedItemsLookupRequest struct {
	Guids []string `json:"guids"`
	Links []string `json:"links"`
}

// GetFeedItemsLookup returns a map of GUID -> MiniFeedItem for the given GUIDs,
// or Link -> MiniFeedItem if the `links` field/param is used instead.
// Used by the admin UI to enrich publications/interactions/webmentions tables.
// Accepts a JSON POST body, or query-string fallback for small lookups.
func GetFeedItemsLookup(c *gin.Context) {
	result := make(map[string]MiniFeedItem)

	splitCSV := func(s string) []string {
		out := []string{}
		for _, v := range strings.Split(s, ",") {
			v = strings.TrimSpace(v)
			if v != "" {
				out = append(out, v)
			}
		}
		return out
	}

	clean := func(in []string) []string {
		out := make([]string, 0, len(in))
		for _, v := range in {
			v = strings.TrimSpace(v)
			if v != "" {
				out = append(out, v)
			}
		}
		return out
	}

	var guids, links []string

	if c.Request.Method == http.MethodPost {
		var body FeedItemsLookupRequest
		if err := c.ShouldBindJSON(&body); err == nil {
			guids = clean(body.Guids)
			links = clean(body.Links)
		}
	}

	if len(guids) == 0 && len(links) == 0 {
		if q := c.Query("links"); q != "" {
			links = splitCSV(q)
		} else if q := c.Query("guids"); q != "" {
			guids = splitCSV(q)
		}
	}

	if len(links) > 0 {
		var items []models.FeedItem
		database.Db.Select("id, feed_id, title, image_url, link, guid").
			Where("link IN ?", links).
			Find(&items)
		for _, it := range items {
			result[it.Link] = MiniFeedItem{
				ID:       it.ID,
				FeedID:   it.FeedID,
				Title:    it.Title,
				ImageUrl: it.ImageUrl,
				Link:     it.Link,
			}
		}
		c.JSON(http.StatusOK, result)
		return
	}

	if len(guids) == 0 {
		c.JSON(http.StatusOK, result)
		return
	}

	var items []models.FeedItem
	database.Db.Select("id, feed_id, title, image_url, link, guid").
		Where("guid IN ?", guids).
		Find(&items)

	for _, it := range items {
		result[it.GUID] = MiniFeedItem{
			ID:       it.ID,
			FeedID:   it.FeedID,
			Title:    it.Title,
			ImageUrl: it.ImageUrl,
			Link:     it.Link,
		}
	}

	c.JSON(http.StatusOK, result)
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
	itemID := c.Query("itemId")

	query := database.Db.Model(&models.Interaction{})
	if platform != "" {
		query = query.Where("platform = ?", platform)
	}
	if itemID != "" {
		query = query.Where("item_id = ?", itemID)
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
	ItemID     string `json:"itemId"`
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
		Select("item_id, COALESCE(SUM(like_count), 0) as total_likes").
		Group("item_id").
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
	// Index targets by name for quick lookup of platform + profile/instance URL
	targetByName := make(map[string]config.Target)
	for _, target := range config.Data.Targets {
		targetByName[target.Name] = target
	}

	connections := make([]ConnectionInfo, 0, len(config.Data.Connections))
	for _, conn := range config.Data.Connections {
		target := targetByName[conn.TargetName]

		var sourceFeedID *uint
		var feed models.Feed
		if err := database.Db.Select("id").Where("feed_name = ?", conn.SourceName).First(&feed).Error; err == nil {
			id := feed.ID
			sourceFeedID = &id
		}

		var targetUrl string
		switch target.Platform {
		case "pixelfed", "mastodon":
			targetUrl = target.InstanceUrl
		case "bluesky":
			if target.Username != "" {
				targetUrl = "https://bsky.app/profile/" + target.Username
			}
		}

		connections = append(connections, ConnectionInfo{
			Name:         conn.Name,
			SourceName:   conn.SourceName,
			TargetName:   conn.TargetName,
			Caption:      conn.Caption,
			Cron:         conn.Cron,
			Platform:     target.Platform,
			SourceFeedID: sourceFeedID,
			TargetUrl:    targetUrl,
		})
	}

	c.JSON(http.StatusOK, connections)
}

// ---------------------------------------------------------------------------
// Logs

// LogsResponse describes the payload returned by GetLogs.
type LogsResponse struct {
	Files []string `json:"files"`
	File  string   `json:"file"`
	Lines []string `json:"lines"`
}

// GetLogs returns the tail of a daily log file under data/logs/.
// Query params: file=<name>, tail=<int>, search=<substring>.
// Defaults: most recent file, tail=200 (capped at 1000).
func GetLogs(c *gin.Context) {
	files, err := logger.ListFiles()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list log files"})
		return
	}

	file := c.Query("file")
	if file == "" && len(files) > 0 {
		file = files[0]
	}
	if file == "" {
		c.JSON(http.StatusOK, LogsResponse{Files: files, File: "", Lines: []string{}})
		return
	}

	tail, _ := strconv.Atoi(c.DefaultQuery("tail", "200"))
	if tail < 1 {
		tail = 200
	}
	if tail > 1000 {
		tail = 1000
	}

	lines, err := logger.TailLines(file, tail, c.Query("search"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, LogsResponse{Files: files, File: file, Lines: lines})
}

// ---------------------------------------------------------------------------
// Upload attempts

// GetUploadAttempts returns paginated upload-attempt rows, newest first.
// Filters: status=success|failed, platform, itemId, targetName.
func GetUploadAttempts(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset := (page - 1) * limit

	query := database.Db.Model(&models.UploadAttempt{})
	switch c.Query("status") {
	case "failed":
		query = query.Where("success = ?", false)
	case "success":
		query = query.Where("success = ?", true)
	}
	if p := c.Query("platform"); p != "" {
		query = query.Where("platform = ?", p)
	}
	if t := c.Query("targetName"); t != "" {
		query = query.Where("target_name = ?", t)
	}
	if id := c.Query("itemId"); id != "" {
		query = query.Where("item_id = ?", id)
	}

	var total int64
	query.Count(&total)

	var rows []models.UploadAttempt
	query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&rows)

	c.JSON(http.StatusOK, gin.H{
		"items": rows,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

// ---------------------------------------------------------------------------
// Target health

// TargetHealth summarises the current health of a configured target by
// inspecting recent upload attempts.
type TargetHealth struct {
	Name            string     `json:"name"`
	Platform        string     `json:"platform"`
	Status          string     `json:"status"` // healthy | degraded | down | unknown
	LastAttemptAt   *time.Time `json:"lastAttemptAt,omitempty"`
	LastSuccessAt   *time.Time `json:"lastSuccessAt,omitempty"`
	LastFailureAt   *time.Time `json:"lastFailureAt,omitempty"`
	LastError       string     `json:"lastError,omitempty"`
	LastErrorCode   string     `json:"lastErrorCode,omitempty"`
	LastHTTPStatus  int        `json:"lastHttpStatus,omitempty"`
	RecentFailures  int64      `json:"recentFailures"`
	RecentSuccesses int64      `json:"recentSuccesses"`
}

// GetTargetHealth returns one TargetHealth row per configured target.
func GetTargetHealth(c *gin.Context) {
	cutoff := time.Now().Add(-24 * time.Hour)
	healths := make([]TargetHealth, 0, len(config.Data.Targets))

	for _, t := range config.Data.Targets {
		h := TargetHealth{Name: t.Name, Platform: t.Platform, Status: "unknown"}

		var lastAttempt models.UploadAttempt
		if err := database.Db.Where("target_name = ?", t.Name).
			Order("created_at DESC").First(&lastAttempt).Error; err == nil {
			ts := lastAttempt.CreatedAt
			h.LastAttemptAt = &ts
			if lastAttempt.Success {
				h.LastSuccessAt = &ts
			} else {
				h.LastFailureAt = &ts
				h.LastError = lastAttempt.ErrorMessage
				h.LastErrorCode = lastAttempt.ErrorCode
				h.LastHTTPStatus = lastAttempt.HTTPStatus
			}
		}

		var lastSuccess models.UploadAttempt
		if h.LastSuccessAt == nil {
			if err := database.Db.Where("target_name = ? AND success = ?", t.Name, true).
				Order("created_at DESC").First(&lastSuccess).Error; err == nil {
				ts := lastSuccess.CreatedAt
				h.LastSuccessAt = &ts
			}
		}
		var lastFailure models.UploadAttempt
		if h.LastFailureAt == nil {
			if err := database.Db.Where("target_name = ? AND success = ?", t.Name, false).
				Order("created_at DESC").First(&lastFailure).Error; err == nil {
				ts := lastFailure.CreatedAt
				h.LastFailureAt = &ts
				if h.LastError == "" {
					h.LastError = lastFailure.ErrorMessage
					h.LastErrorCode = lastFailure.ErrorCode
					h.LastHTTPStatus = lastFailure.HTTPStatus
				}
			}
		}

		database.Db.Model(&models.UploadAttempt{}).
			Where("target_name = ? AND success = ? AND created_at >= ?", t.Name, false, cutoff).
			Count(&h.RecentFailures)
		database.Db.Model(&models.UploadAttempt{}).
			Where("target_name = ? AND success = ? AND created_at >= ?", t.Name, true, cutoff).
			Count(&h.RecentSuccesses)

		switch {
		case h.LastAttemptAt == nil:
			h.Status = "unknown"
		case h.LastFailureAt != nil && h.LastSuccessAt == nil:
			h.Status = "down"
		case h.LastFailureAt != nil && h.LastSuccessAt != nil && h.LastFailureAt.After(*h.LastSuccessAt):
			h.Status = "down"
		case h.RecentFailures > 0:
			h.Status = "degraded"
		default:
			h.Status = "healthy"
		}

		healths = append(healths, h)
	}

	c.JSON(http.StatusOK, healths)
}

// ---------------------------------------------------------------------------
// Client log ingest

const (
	maxClientLogEntries  = 200
	maxClientLogMessage  = 4096
	maxClientLogBodySize = 1 << 20 // 1 MiB
)

type clientLogEntry struct {
	Level   string `json:"level"`
	Source  string `json:"source"`
	URL     string `json:"url,omitempty"`
	Time    string `json:"time"`
	Message string `json:"message"`
}

type clientLogPayload struct {
	ClientID string           `json:"clientId"`
	Entries  []clientLogEntry `json:"entries"`
}

// IngestClientLogs accepts a batch of browser-side log entries and appends
// them as text lines to data/logs/clients/<clientId>/YYYY-MM-DD.log.
func IngestClientLogs(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxClientLogBodySize)

	var payload clientLogPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
		return
	}
	if err := logger.ValidateClientID(payload.ClientID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid clientId"})
		return
	}
	if len(payload.Entries) == 0 {
		c.Status(http.StatusNoContent)
		return
	}
	if len(payload.Entries) > maxClientLogEntries {
		payload.Entries = payload.Entries[:maxClientLogEntries]
	}

	writer, err := logger.ClientWriter(payload.ClientID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log writer unavailable"})
		return
	}

	var buf strings.Builder
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, e := range payload.Entries {
		level := strings.ToLower(strings.TrimSpace(e.Level))
		if level == "" {
			level = "info"
		}
		source := strings.TrimSpace(e.Source)
		if source == "" {
			source = "client"
		}
		msg := e.Message
		if len(msg) > maxClientLogMessage {
			msg = msg[:maxClientLogMessage] + "…"
		}
		// Collapse newlines so each entry stays on a single line.
		msg = strings.ReplaceAll(msg, "\r\n", "\n")
		msg = strings.ReplaceAll(msg, "\n", " ⏎ ")

		buf.WriteString(now)
		buf.WriteString(" [")
		buf.WriteString(level)
		buf.WriteString("] [")
		buf.WriteString(source)
		buf.WriteString("]")
		if e.URL != "" {
			buf.WriteString(" ")
			buf.WriteString(e.URL)
		}
		if e.Time != "" {
			buf.WriteString(" client=")
			buf.WriteString(e.Time)
		}
		buf.WriteString(" ")
		buf.WriteString(msg)
		buf.WriteString("\n")
	}

	if _, err := writer.Write([]byte(buf.String())); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write failed"})
		return
	}
	c.Status(http.StatusNoContent)
}
