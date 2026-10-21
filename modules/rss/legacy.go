package rss

import (
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// importLegacy copies the old inventory (feeds, feed_items, their
// categories) into the module's tables. Soft-deleted items become removed
// items, so their publications keep a title and an image.
func importLegacy(mc companion.ModuleContext, l *companion.Legacy) error {
	if !l.HasTable("feed_items") {
		l.Reportf("rss: no feed_items table, nothing to move")
		return nil
	}
	type oldFeed struct {
		ID                                          uint
		FeedName, Title, Description, Link, FeedURL string
		Language, Copyright, Generator              string
		UpdatedAt                                   time.Time
	}
	type oldItem struct {
		ID                                 uint
		FeedID                             uint
		Title, Description, Link, ImageURL string
		Published                          time.Time
		GUID                               string
		CreatedAt                          time.Time
		DeletedAt                          *time.Time
	}
	var feeds []oldFeed
	if err := l.DB.Raw("SELECT id, feed_name, title, description, link, feed_url, language, copyright, generator, updated_at FROM feeds WHERE deleted_at IS NULL").Scan(&feeds).Error; err != nil {
		return err
	}
	names := map[uint]string{}
	for _, f := range feeds {
		names[f.ID] = companion.LegacyName(f.FeedName)
		updated := f.UpdatedAt
		row := Feed{Instance: names[f.ID], URL: f.FeedURL, Title: f.Title, Description: f.Description, Link: f.Link,
			Language: f.Language, Copyright: f.Copyright, Generator: f.Generator, FetchedAt: &updated}
		if !l.Check {
			if err := mc.DB().Save(&row).Error; err != nil {
				return err
			}
		}
	}
	var items []oldItem
	if err := l.DB.Raw("SELECT id, feed_id, title, description, link, image_url, published, guid, created_at, deleted_at FROM feed_items").Scan(&items).Error; err != nil {
		return err
	}
	cats := map[uint][]string{}
	if l.HasTable("feed_item_categories") && l.HasTable("categories") {
		var rows []struct {
			FeedItemID uint
			Name       string
		}
		l.DB.Raw("SELECT fic.feed_item_id, c.name FROM feed_item_categories fic JOIN categories c ON c.id = fic.category_id ORDER BY c.id").Scan(&rows)
		for _, r := range rows {
			cats[r.FeedItemID] = append(cats[r.FeedItemID], r.Name)
		}
	}
	moved, removed := 0, 0
	for _, it := range items {
		name, ok := names[it.FeedID]
		if !ok || it.GUID == "" {
			continue
		}
		row := Item{Instance: name, GUID: it.GUID, Title: it.Title, Description: it.Description, Link: it.Link,
			ImageURL: it.ImageURL, Alt: altText(it.Description), Categories: cats[it.ID], Kind: kindOf("", it.ImageURL),
			Published: it.Published, HasDate: !it.Published.IsZero(), FirstSeen: it.CreatedAt, RemovedAt: it.DeletedAt}
		if it.DeletedAt != nil {
			removed++
		}
		moved++
		if l.Check {
			continue
		}
		var existing Item
		if mc.DB().Where("instance = ? AND guid = ?", name, it.GUID).Limit(1).Find(&existing).RowsAffected > 0 {
			continue
		}
		if err := mc.DB().Create(&row).Error; err != nil {
			return err
		}
	}
	l.Reportf("rss: %d feed(s), %d item(s), %d of them no longer in the feed", len(feeds), moved, removed)
	return nil
}
