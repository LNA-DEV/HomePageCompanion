package inventory

import (
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"gorm.io/gorm"
)

// ListFeeds returns all feeds, optionally including their items.
func ListFeeds(includeItems bool) ([]models.Feed, error) {
	var feeds []models.Feed
	query := database.Db.Model(&models.Feed{})

	if includeItems {
		query = query.Preload("Items").Preload("Authors")
	} else {
		query = query.Preload("Authors")
	}

	if err := query.Find(&feeds).Error; err != nil {
		return nil, err
	}
	return feeds, nil
}

// GetFeedByID returns a feed by ID, with optional items.
func GetFeedByID(id uint, includeItems bool) (*models.Feed, error) {
	var feed models.Feed
	query := database.Db.Model(&models.Feed{})

	if includeItems {
		query = query.Preload("Items.Categories").Preload("Items.Authors")
	}

	if err := query.Preload("Authors").First(&feed, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &feed, nil
}
