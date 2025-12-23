package backfill

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	blueskyapi "github.com/LNA-DEV/HomePageCompanion/blue_sky_api"
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/corona10/goimagehash"
	"github.com/mmcdole/gofeed"
)

const hashDistanceThreshold = 10

type RSSImageData struct {
	ItemName string
	ImageURL string
	Hash     *goimagehash.ImageHash
}

func RunBackfill() {
	log.Println("Starting backfill process...")

	for _, connection := range config.Data.Connections {
		var target config.Target
		for _, t := range config.Data.Targets {
			if t.Name == connection.TargetName {
				target = t
				break
			}
		}

		var source config.Datasource
		for _, s := range config.Data.Datasources.Rss {
			if s.Name == connection.SourceName {
				source = s
				break
			}
		}

		log.Printf("Processing platform: %s", target.Platform)

		switch target.Platform {
		case "pixelfed":
			backfillPixelfed(target, source)
		case "bluesky":
			backfillBluesky(target, source)
		case "instagram":
			backfillInstagram(target, source)
		}
	}

	log.Println("Backfill process completed.")
}

func getItemsNeedingBackfill(platform string) ([]models.AutoUploadItem, error) {
	var items []models.AutoUploadItem
	query := database.Db.Where("platform = ?", platform)

	// Each platform needs different fields
	switch platform {
	case "bluesky":
		// Bluesky needs post_url and version_id
		query = query.Where("post_url IS NULL OR version_id IS NULL")
	case "instagram":
		// Instagram only needs post_id
		query = query.Where("post_id IS NULL")
	case "pixelfed":
		// Pixelfed needs post_url and post_id
		query = query.Where("post_url IS NULL OR post_id IS NULL")
	default:
		// Fallback to checking all fields
		query = query.Where("post_url IS NULL OR post_id IS NULL OR version_id IS NULL")
	}

	err := query.Find(&items).Error
	return items, err
}

func loadRSSImageHashes(feedURL string) ([]RSSImageData, error) {
	parser := gofeed.NewParser()
	feed, err := parser.ParseURL(feedURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse RSS feed: %w", err)
	}

	var imageData []RSSImageData
	for _, item := range feed.Items {
		if item.Image == nil || item.Image.URL == "" {
			continue
		}

		hash, err := computeHashFromURL(item.Image.URL)
		if err != nil {
			log.Printf("Warning: could not compute hash for %s: %v", item.Title, err)
			continue
		}

		imageData = append(imageData, RSSImageData{
			ItemName: item.Title,
			ImageURL: item.Image.URL,
			Hash:     hash,
		})
	}

	return imageData, nil
}

func computeHashFromURL(imageURL string) (*goimagehash.ImageHash, error) {
	const maxRetries = 3

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err := http.Get(imageURL)
		if err != nil {
			lastErr = err
			log.Printf("Attempt %d/%d failed for %s: %v", attempt, maxRetries, imageURL, err)
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			lastErr = fmt.Errorf("failed to download image: status %d", resp.StatusCode)
			if resp.StatusCode >= 500 {
				log.Printf("Attempt %d/%d failed for %s: status %d", attempt, maxRetries, imageURL, resp.StatusCode)
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return nil, lastErr
		}

		img, _, err := image.Decode(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to decode image: %w", err)
		}

		return goimagehash.PerceptionHash(img)
	}

	return nil, fmt.Errorf("after %d retries: %w", maxRetries, lastErr)
}

func computeHashFromBytes(data []byte) (*goimagehash.ImageHash, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	return goimagehash.PerceptionHash(img)
}

func findMatchingRSSItem(platformHash *goimagehash.ImageHash, rssImages []RSSImageData) *RSSImageData {
	for i, rssImg := range rssImages {
		distance, err := platformHash.Distance(rssImg.Hash)
		if err != nil {
			continue
		}
		if distance <= hashDistanceThreshold {
			return &rssImages[i]
		}
	}
	return nil
}

func updateAutoUploadItem(itemName, platform string, postURL, versionID, postID *string) error {
	return database.Db.
		Model(&models.AutoUploadItem{}).
		Where("item_name = ? AND platform = ?", itemName, platform).
		Updates(map[string]interface{}{
			"post_url":   postURL,
			"version_id": versionID,
			"post_id":    postID,
		}).Error
}

// Pixelfed backfill

type PixelfedStatus struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	MediaAttach []struct {
		URL string `json:"url"`
	} `json:"media_attachments"`
}

func backfillPixelfed(target config.Target, source config.Datasource) {
	items, err := getItemsNeedingBackfill("pixelfed")
	if err != nil {
		log.Printf("Error getting items for pixelfed: %v", err)
		return
	}

	if len(items) == 0 {
		log.Println("No pixelfed items need backfill")
		return
	}

	log.Printf("Found %d pixelfed items needing backfill", len(items))

	rssImages, err := loadRSSImageHashes(source.FeedURL)
	if err != nil {
		log.Printf("Error loading RSS hashes: %v", err)
		return
	}

	// Filter RSS images to only those in our items list
	itemNames := make(map[string]bool)
	for _, item := range items {
		itemNames[item.ItemName] = true
	}

	var relevantRSSImages []RSSImageData
	for _, rss := range rssImages {
		if itemNames[rss.ItemName] {
			relevantRSSImages = append(relevantRSSImages, rss)
		}
	}

	if len(relevantRSSImages) == 0 {
		log.Println("No matching RSS images found for items needing backfill")
		return
	}

	// Fetch account ID
	accountID, err := getPixelfedAccountID(target)
	if err != nil {
		log.Printf("Error getting Pixelfed account ID: %v", err)
		return
	}

	// Fetch all statuses
	statuses, err := fetchAllPixelfedStatuses(target, accountID)
	if err != nil {
		log.Printf("Error fetching Pixelfed statuses: %v", err)
		return
	}

	log.Printf("Fetched %d statuses from Pixelfed", len(statuses))

	// Match each status to RSS items
	for _, status := range statuses {
		if len(status.MediaAttach) == 0 {
			continue
		}

		mediaURL := status.MediaAttach[0].URL
		platformHash, err := computeHashFromURL(mediaURL)
		if err != nil {
			log.Printf("Could not hash Pixelfed image %s: %v", status.ID, err)
			continue
		}

		match := findMatchingRSSItem(platformHash, relevantRSSImages)
		if match != nil {
			log.Printf("Matched Pixelfed post %s to RSS item %s", status.ID, match.ItemName)
			err = updateAutoUploadItem(match.ItemName, "pixelfed", &status.URL, nil, &status.ID)
			if err != nil {
				log.Printf("Error updating item: %v", err)
			}
		}
	}
}

func getPixelfedAccountID(target config.Target) (string, error) {
	req, _ := http.NewRequest("GET", target.InstanceUrl+"/api/v1/accounts/verify_credentials", nil)
	req.Header.Set("Authorization", "Bearer "+target.PAT)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var account struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&account); err != nil {
		return "", err
	}

	return account.ID, nil
}

func fetchAllPixelfedStatuses(target config.Target, accountID string) ([]PixelfedStatus, error) {
	var allStatuses []PixelfedStatus
	baseURL := fmt.Sprintf("%s/api/v1/accounts/%s/statuses", target.InstanceUrl, accountID)
	maxID := ""

	for {
		requestURL := baseURL + "?limit=40"
		if maxID != "" {
			requestURL += "&max_id=" + maxID
		}

		req, _ := http.NewRequest("GET", requestURL, nil)
		req.Header.Set("Authorization", "Bearer "+target.PAT)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return allStatuses, err
		}

		var statuses []PixelfedStatus
		if err := json.NewDecoder(resp.Body).Decode(&statuses); err != nil {
			resp.Body.Close()
			return allStatuses, err
		}
		resp.Body.Close()

		if len(statuses) == 0 {
			break
		}

		allStatuses = append(allStatuses, statuses...)

		// Use the last status ID as max_id for next page
		maxID = statuses[len(statuses)-1].ID

		log.Printf("Fetched %d Pixelfed statuses so far...", len(allStatuses))
	}

	return allStatuses, nil
}

// Bluesky backfill

type BlueskyFeedResponse struct {
	Feed   []BlueskyFeedItem `json:"feed"`
	Cursor string            `json:"cursor"`
}

type BlueskyFeedItem struct {
	Post struct {
		URI    string `json:"uri"`
		CID    string `json:"cid"`
		Record struct {
			Embed struct {
				Images []struct {
					Image struct {
						Ref struct {
							Link string `json:"$link"`
						} `json:"ref"`
					} `json:"image"`
				} `json:"images"`
			} `json:"embed"`
		} `json:"record"`
		Embed struct {
			Images []struct {
				Fullsize string `json:"fullsize"`
			} `json:"images"`
		} `json:"embed"`
	} `json:"post"`
}

func backfillBluesky(target config.Target, source config.Datasource) {
	items, err := getItemsNeedingBackfill("bluesky")
	if err != nil {
		log.Printf("Error getting items for bluesky: %v", err)
		return
	}

	if len(items) == 0 {
		log.Println("No bluesky items need backfill")
		return
	}

	log.Printf("Found %d bluesky items needing backfill", len(items))

	rssImages, err := loadRSSImageHashes(source.FeedURL)
	if err != nil {
		log.Printf("Error loading RSS hashes: %v", err)
		return
	}

	// Filter RSS images to only those in our items list
	itemNames := make(map[string]bool)
	for _, item := range items {
		itemNames[item.ItemName] = true
	}

	var relevantRSSImages []RSSImageData
	for _, rss := range rssImages {
		if itemNames[rss.ItemName] {
			relevantRSSImages = append(relevantRSSImages, rss)
		}
	}

	if len(relevantRSSImages) == 0 {
		log.Println("No matching RSS images found for items needing backfill")
		return
	}

	// Login to Bluesky
	session, err := blueskyapi.BlueskyLogin(target.Username, target.PAT)
	if err != nil {
		log.Printf("Error logging into Bluesky: %v", err)
		return
	}

	// Fetch all posts
	posts, err := fetchAllBlueskyPosts(session)
	if err != nil {
		log.Printf("Error fetching Bluesky posts: %v", err)
		return
	}

	log.Printf("Fetched %d posts from Bluesky", len(posts))

	// Match each post to RSS items
	for _, feedItem := range posts {
		post := feedItem.Post
		if post.Embed.Images == nil || len(post.Embed.Images) == 0 {
			continue
		}

		imageURL := post.Embed.Images[0].Fullsize
		if imageURL == "" {
			continue
		}

		platformHash, err := computeHashFromURL(imageURL)
		if err != nil {
			log.Printf("Could not hash Bluesky image %s: %v", post.URI, err)
			continue
		}

		match := findMatchingRSSItem(platformHash, relevantRSSImages)
		if match != nil {
			log.Printf("Matched Bluesky post %s to RSS item %s", post.URI, match.ItemName)
			err = updateAutoUploadItem(match.ItemName, "bluesky", &post.URI, &post.CID, nil)
			if err != nil {
				log.Printf("Error updating item: %v", err)
			}
		}
	}
}

func fetchAllBlueskyPosts(session *blueskyapi.BlueskySession) ([]BlueskyFeedItem, error) {
	var allPosts []BlueskyFeedItem
	cursor := ""

	for {
		feedURL := fmt.Sprintf("https://bsky.social/xrpc/app.bsky.feed.getAuthorFeed?actor=%s&limit=50", url.QueryEscape(session.Did))
		if cursor != "" {
			feedURL += "&cursor=" + url.QueryEscape(cursor)
		}

		req, _ := http.NewRequest("GET", feedURL, nil)
		req.Header.Set("Authorization", "Bearer "+session.AccessJwt)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return allPosts, err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var feedResp BlueskyFeedResponse
		if err := json.Unmarshal(body, &feedResp); err != nil {
			return allPosts, err
		}

		if len(feedResp.Feed) == 0 {
			break
		}

		allPosts = append(allPosts, feedResp.Feed...)

		if feedResp.Cursor == "" {
			break
		}
		cursor = feedResp.Cursor
	}

	return allPosts, nil
}

// Instagram backfill

type InstagramMediaResponse struct {
	Data []struct {
		ID       string `json:"id"`
		MediaURL string `json:"media_url"`
	} `json:"data"`
	Paging struct {
		Next string `json:"next"`
	} `json:"paging"`
}

func backfillInstagram(target config.Target, source config.Datasource) {
	items, err := getItemsNeedingBackfill("instagram")
	if err != nil {
		log.Printf("Error getting items for instagram: %v", err)
		return
	}

	if len(items) == 0 {
		log.Println("No instagram items need backfill")
		return
	}

	log.Printf("Found %d instagram items needing backfill", len(items))

	rssImages, err := loadRSSImageHashes(source.FeedURL)
	if err != nil {
		log.Printf("Error loading RSS hashes: %v", err)
		return
	}

	// Filter RSS images to only those in our items list
	itemNames := make(map[string]bool)
	for _, item := range items {
		itemNames[item.ItemName] = true
	}

	var relevantRSSImages []RSSImageData
	for _, rss := range rssImages {
		if itemNames[rss.ItemName] {
			relevantRSSImages = append(relevantRSSImages, rss)
		}
	}

	if len(relevantRSSImages) == 0 {
		log.Println("No matching RSS images found for items needing backfill")
		return
	}

	// Fetch all media
	media, err := fetchAllInstagramMedia(target)
	if err != nil {
		log.Printf("Error fetching Instagram media: %v", err)
		return
	}

	log.Printf("Fetched %d media items from Instagram", len(media))

	// Match each media to RSS items
	for _, m := range media {
		if m.MediaURL == "" {
			continue
		}

		platformHash, err := computeHashFromURL(m.MediaURL)
		if err != nil {
			log.Printf("Could not hash Instagram image %s: %v", m.ID, err)
			continue
		}

		match := findMatchingRSSItem(platformHash, relevantRSSImages)
		if match != nil {
			log.Printf("Matched Instagram post %s to RSS item %s", m.ID, match.ItemName)
			err = updateAutoUploadItem(match.ItemName, "instagram", nil, nil, &m.ID)
			if err != nil {
				log.Printf("Error updating item: %v", err)
			}
		}
	}
}

type InstagramMedia struct {
	ID       string
	MediaURL string
}

func fetchAllInstagramMedia(target config.Target) ([]InstagramMedia, error) {
	var allMedia []InstagramMedia
	nextURL := fmt.Sprintf("https://graph.instagram.com/v22.0/%s/media?fields=id,media_url&access_token=%s",
		target.AccountId, url.QueryEscape(target.AccessToken))

	for nextURL != "" {
		resp, err := http.Get(nextURL)
		if err != nil {
			return allMedia, err
		}

		var mediaResp InstagramMediaResponse
		if err := json.NewDecoder(resp.Body).Decode(&mediaResp); err != nil {
			resp.Body.Close()
			return allMedia, err
		}
		resp.Body.Close()

		for _, m := range mediaResp.Data {
			allMedia = append(allMedia, InstagramMedia{
				ID:       m.ID,
				MediaURL: m.MediaURL,
			})
		}

		nextURL = mediaResp.Paging.Next
	}

	return allMedia, nil
}
