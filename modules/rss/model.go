package rss

import (
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
)

// Feed is the last state of one feed instance.
type Feed struct {
	Instance    string `gorm:"primaryKey"`
	URL         string
	Title       string
	Description string
	Link        string
	Language    string
	Copyright   string
	Generator   string
	FetchedAt   *time.Time
	LastError   string
	UpdatedAt   time.Time
}

func (Feed) TableName() string { return "rss_feeds" }

// Item is one mirrored feed item. An item that left the feed keeps its row
// with RemovedAt set: its publications and likes still point at it.
type Item struct {
	ID          uint   `gorm:"primaryKey"`
	Instance    string `gorm:"uniqueIndex:idx_rss_item;index"`
	GUID        string `gorm:"uniqueIndex:idx_rss_item;index"`
	Kind        string `gorm:"index"`
	Title       string
	Summary     string
	Description string `gorm:"type:text"`
	Link        string `gorm:"index"`
	ImageURL    string
	OriginalURL string
	Alt         string
	Categories  []string      `gorm:"serializer:json"`
	People      []feed.Person `gorm:"serializer:json"`
	Published   time.Time     `gorm:"index"`
	HasDate     bool
	FirstSeen   time.Time
	RemovedAt   *time.Time `gorm:"index"`
	UpdatedAt   time.Time
}

func (Item) TableName() string { return "rss_items" }

// toFeedItem is the capability's view of a row.
func (it Item) toFeedItem(f *Feed) feed.Item {
	out := feed.Item{
		ID: it.GUID, Kind: it.Kind, Title: it.Title, Summary: it.Summary, Description: it.Description,
		Link: it.Link, Categories: it.Categories, Published: it.Published, HasDate: it.HasDate, People: it.People,
	}
	if it.ImageURL != "" {
		out.Image = &feed.Image{URL: it.ImageURL, OriginalURL: it.OriginalURL, Alt: it.Alt}
	}
	if f != nil {
		out.Copyright, out.FeedTitle = f.Copyright, f.Title
	}
	return out
}
