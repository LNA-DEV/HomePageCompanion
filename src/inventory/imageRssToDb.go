package inventory

import (
	"fmt"
	"log"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/mmcdole/gofeed"
	"gorm.io/gorm"
)

// imageRssToDatabase mirrors one image feed into the database. A feed that
// cannot be fetched or stored returns an error and leaves the stored items as
// they are; the next run tries again.
func imageRssToDatabase(feedURL string, feedName string) error {
	parser := gofeed.NewParser()
	parsedFeed, err := parser.ParseURL(feedURL)
	if err != nil {
		return fmt.Errorf("fetch feed %s: %w", feedName, err)
	}

	var authorsFeed []models.Author
	for _, author := range parsedFeed.Authors {
		authorsFeed = append(authorsFeed, models.Author{
			Name:  author.Name,
			Email: author.Email,
		})
	}

	feed := models.Feed{
		FeedName:    feedName,
		Title:       parsedFeed.Title,
		Description: parsedFeed.Description,
		Link:        parsedFeed.Link,
		FeedURL:     feedURL,
		ItemTypes:   "image",
		Language:    parsedFeed.Language,
		Authors:     authorsFeed,
		Copyright:   parsedFeed.Copyright,
		Generator:   parsedFeed.Generator,
	}

	// Update or create feed
	var existingFeed models.Feed
	err = database.Db.Where("feed_url = ?", feedURL).First(&existingFeed).Error
	if err == gorm.ErrRecordNotFound {
		if err := database.Db.Create(&feed).Error; err != nil {
			return fmt.Errorf("save feed %s: %w", feedName, err)
		}
	} else if err == nil {
		feed.ID = existingFeed.ID
		database.Db.Model(&existingFeed).Updates(feed)
	} else {
		return fmt.Errorf("query feed %s: %w", feedName, err)
	}

	// Track seen GUIDs
	seenGUIDs := make(map[string]bool)

	for _, item := range parsedFeed.Items {
		if item.GUID == "" {
			log.Printf("Guid already exists. This should not have happened.")
			continue // Skip items without unique ID
		}
		seenGUIDs[item.GUID] = true

		published := time.Now()
		if item.PublishedParsed != nil {
			published = *item.PublishedParsed
		}

		var authors []models.Author
		for _, author := range item.Authors {
			authors = append(authors, models.Author{
				Name:  author.Name,
				Email: author.Email,
			})
		}

		var categories []models.Category
		for _, cat := range item.Categories {
			var category models.Category
			if err := database.Db.FirstOrCreate(&category, models.Category{Name: cat}).Error; err == nil {
				categories = append(categories, category)
			} else {
				log.Printf("Error saving category '%s': %v", cat, err)
			}
		}

		// Lookup even soft-deleted items
		var existingItem models.FeedItem
		result := database.Db.Unscoped().Where("guid = ?", item.GUID).First(&existingItem)

		feedItem := models.FeedItem{
			FeedID:      feed.ID,
			Title:       item.Title,
			Description: item.Description,
			Link:        item.Link,
			Published:   published,
			GUID:        item.GUID,
			Authors:     authors,
			Categories:  categories,
			ImageUrl:    imageURL(item),
			ItemType:    "image",
		}

		if result.Error == gorm.ErrRecordNotFound {
			// Create new item
			if err := database.Db.Create(&feedItem).Error; err != nil {
				log.Printf("Error saving new item '%s': %v", item.Title, err)
			}
		} else if result.Error == nil {
			// If soft-deleted, undelete it
			if existingItem.DeletedAt.Valid {
				database.Db.Model(&existingItem).Update("DeletedAt", nil)
			}

			// Update item fields if changed
			database.Db.Model(&existingItem).Updates(feedItem)
		} else {
			log.Printf("Error checking item '%s': %v", item.Title, result.Error)
		}
	}

	// Soft-delete missing items
	var allItems []models.FeedItem
	database.Db.Where("feed_id = ?", feed.ID).Find(&allItems)

	for _, item := range allItems {
		if !seenGUIDs[item.GUID] {
			if err := database.Db.Delete(&item).Error; err != nil {
				log.Printf("Error soft-deleting item '%s': %v", item.Title, err)
			}
		}
	}
	return nil
}

// imageURL is the item's image, or "" for an item without one.
func imageURL(item *gofeed.Item) string {
	if item.Image == nil {
		return ""
	}
	return item.Image.URL
}
