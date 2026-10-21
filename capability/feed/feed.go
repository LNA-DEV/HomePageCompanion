// Package feed defines feed.source/v1: a source of posts, photos and
// articles, such as an RSS feed.
package feed

import (
	"context"
	"errors"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// Source lists the items of one feed.
type Source interface {
	ListItems(ctx context.Context) ([]Item, error)
	GetItem(ctx context.Context, id string) (Item, error)
}

var SourceV1 = companion.NewCapability[Source]("feed.source", 1)

// ErrNotFound is returned by GetItem for an unknown item.
var ErrNotFound = errors.New("feed: item not found")

// Item kinds.
const (
	KindPhoto   = "photo"
	KindArticle = "article"
)

// EventItemDiscovered is emitted once per new item.
const EventItemDiscovered = "feed.item.discovered/v1"

// ItemDiscovered is the payload of EventItemDiscovered.
type ItemDiscovered struct {
	Source string `json:"source"` // instance, "rss:blog"
	Item   Item   `json:"item"`
}

// Item is one post of a feed.
type Item struct {
	ID          string    `json:"id"` // the feed's GUID
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary,omitempty"`     // plain-text excerpt
	Description string    `json:"description,omitempty"` // the feed's HTML
	Link        string    `json:"link"`
	Categories  []string  `json:"categories,omitempty"`
	Published   time.Time `json:"published"`
	// HasDate is false when the feed gave no date; Published is then the
	// time the item was first seen.
	HasDate bool   `json:"hasDate"`
	Image   *Image `json:"image,omitempty"`

	People    []Person `json:"people,omitempty"`
	Copyright string   `json:"copyright,omitempty"` // the feed's copyright line
	FeedTitle string   `json:"feedTitle,omitempty"`
}

// Image is an item's image. URL is the one to post (the feed's
// media:content); OriginalURL is the full file with its metadata intact
// (the enclosure), when the feed has one.
type Image struct {
	URL         string `json:"url"`
	OriginalURL string `json:"originalUrl,omitempty"`
	Alt         string `json:"alt,omitempty"`
}

// MetadataURL is the URL to read EXIF from: the original, else the image.
func (i *Image) MetadataURL() string {
	if i == nil {
		return ""
	}
	if i.OriginalURL != "" {
		return i.OriginalURL
	}
	return i.URL
}

// Person is someone shown in a photo (lna:model). See
// docs/concepts/autouploader-model-tags.md for the feed contract.
type Person struct {
	Slug     string    `json:"slug"`
	Name     string    `json:"name"`
	Autotag  bool      `json:"autotag"` // agreed to be mentioned
	Profiles []Profile `json:"profiles,omitempty"`
}

// Profile is one profile of a Person; Platform is a lowercase key such as
// "instagram" or "mastodon".
type Profile struct {
	Platform string `json:"platform"`
	URL      string `json:"url"`
}
