package interactions

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/gin-gonic/gin"
)

var ErrRateLimited = errors.New("rate limited")

// HandleInteraction retrieves interactions from the database for a given item
func HandleInteraction(c *gin.Context) {
	itemName := c.Param("item_name")
	targetName := c.Param("target_name")

	var interactions []models.Interaction
	var err error

	if targetName == "all" {
		err = database.Db.Where("item_name = ?", itemName).Find(&interactions).Error
	} else {
		err = database.Db.Where("item_name = ? AND target_name = ?", itemName, targetName).Find(&interactions).Error
	}

	if err != nil {
		c.Data(http.StatusInternalServerError, "application/text", []byte(err.Error()))
		return
	}

	likesList := []LikesResponse{}
	for _, interaction := range interactions {
		likesList = append(likesList, LikesResponse{
			Platform: interaction.Platform,
			Likes:    interaction.LikeCount,
		})
	}

	jsonData, jsonErr := json.Marshal(likesList)
	if jsonErr != nil {
		c.Data(http.StatusInternalServerError, "application/text", []byte(jsonErr.Error()))
		return
	}

	c.Data(http.StatusOK, "application/json", jsonData)
}

// FetchAndStoreInteractions fetches interactions from all platforms and stores them in the database
func FetchAndStoreInteractions() {
	log.Println("Starting interactions fetch...")

	// Get all unique item names from AutoUploadItem
	var items []models.AutoUploadItem
	if err := database.Db.Find(&items).Error; err != nil {
		log.Printf("Error fetching auto upload items: %v", err)
		return
	}

	// Group items by item name to process each item once per platform
	itemsByName := make(map[string][]models.AutoUploadItem)
	for _, item := range items {
		itemsByName[item.ItemName] = append(itemsByName[item.ItemName], item)
	}

	// Track rate-limited platforms to skip them
	rateLimitedPlatforms := make(map[string]bool)

	for itemName, platformItems := range itemsByName {
		for _, item := range platformItems {
			// Skip if this platform is rate limited
			if rateLimitedPlatforms[item.Platform] {
				continue
			}

			// Find the target for this platform
			var target config.Target
			for _, t := range config.Data.Targets {
				if t.Platform == item.Platform {
					target = t
					break
				}
			}

			if target.Name == "" {
				continue
			}

			var likeCount int
			var fetchErr error

			switch item.Platform {
			case "bluesky":
				result, e := handleBlueskyLikes(item, target.Name)
				if e != nil {
					fetchErr = e
				} else {
					likeCount = len(result.Likes)
				}

			case "pixelfed":
				result, e := handlePixelfedLikes(item, target.Name)
				if e != nil {
					fetchErr = e
				} else {
					likeCount = len(result.Accounts)
				}

			case "instagram":
				result, e := handleInstagramLikes(item, target.Name)
				if e != nil {
					fetchErr = e
				} else {
					likeCount = result.LikeCount
				}

			default:
				continue
			}

			if fetchErr != nil {
				if errors.Is(fetchErr, ErrRateLimited) {
					log.Printf("Rate limited on %s, skipping remaining requests for this platform", item.Platform)
					rateLimitedPlatforms[item.Platform] = true
				} else {
					log.Printf("Error fetching %s likes for %s: %v", item.Platform, itemName, fetchErr)
				}
				continue
			}

			// Upsert interaction
			var interaction models.Interaction
			result := database.Db.Where("item_name = ? AND platform = ? AND target_name = ?", itemName, item.Platform, target.Name).First(&interaction)

			if result.Error != nil {
				// Create new
				interaction = models.Interaction{
					ItemName:   itemName,
					Platform:   item.Platform,
					TargetName: target.Name,
					LikeCount:  likeCount,
				}
				if err := database.Db.Create(&interaction).Error; err != nil {
					log.Printf("Error creating interaction for %s on %s: %v", itemName, item.Platform, err)
				}
			} else {
				// Update existing
				interaction.LikeCount = likeCount
				if err := database.Db.Save(&interaction).Error; err != nil {
					log.Printf("Error updating interaction for %s on %s: %v", itemName, item.Platform, err)
				}
			}

			log.Printf("Stored interaction for %s on %s: %d likes", itemName, item.Platform, likeCount)
		}
	}

	log.Println("Finished interactions fetch")
}

type LikesResponse struct {
	Platform string `json:"platform"`
	Likes    int    `json:"likes"`
}
