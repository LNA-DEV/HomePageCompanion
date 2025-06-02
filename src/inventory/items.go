package inventory

import (
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"gorm.io/gorm"
)

// ListFeedItems returns all items, optionally filtered by FeedID.
func ListFeedItems(feedID *uint) ([]models.FeedItem, error) {
	var items []models.FeedItem
	query := database.Db.Preload("Authors").Preload("Categories")

	if feedID != nil {
		query = query.Where("feed_id = ?", *feedID)
	}

	if err := query.Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// GetFeedItemByID returns a specific item by ID.
func GetFeedItemByID(id uint) (*models.FeedItem, error) {
	var item models.FeedItem
	if err := database.Db.
		Preload("Authors").
		Preload("Categories").
		First(&item, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &item, nil
}
