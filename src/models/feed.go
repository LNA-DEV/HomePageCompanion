package models

import (
	"time"

	"gorm.io/gorm"
)

type Feed struct {
	gorm.Model
	Title       string
	Description string
	Link        string
	FeedURL     string
	Language    string
	Copyright   string
	Generator   string
	ItemTypes   string
	Items       []FeedItem `gorm:"foreignKey:FeedID"`
	Authors     []Author   `gorm:"foreignKey:FeedID"`
}

type FeedItem struct {
	gorm.Model
	FeedID      uint
	Title       string
	Description string
	Link        string
	ItemType    string
	ImageUrl    string
	Categories  []Category `gorm:"many2many:feed_item_categories"`
	Published   time.Time
	GUID        string `gorm:"uniqueIndex"`
	Authors     []Author `gorm:"foreignKey:FeedItemID"`
}

type Author struct {
	gorm.Model
	Name       string
	Email      string
	FeedID     *uint
	FeedItemID *uint
}

type Category struct {
	gorm.Model
	Name       string        `gorm:"uniqueIndex"`
	FeedItems  []FeedItem    `gorm:"many2many:feed_item_categories"`
}
