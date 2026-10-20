package autoupload

import (
	"context"
	"errors"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"gorm.io/gorm/clause"
)

func migrate(mc companion.ModuleContext) error {
	if mc.DB() == nil {
		return errors.New("autoupload: no database")
	}
	return mc.DB().AutoMigrate(tables...)
}

func start(mc companion.ModuleContext) error {
	if err := migrate(mc); err != nil {
		return err
	}
	go func() {
		defer func() { recover() }()
		if err := syncIndex(context.Background(), mc); err != nil {
			mc.Log().Printf("index: %v", err)
		}
	}()
	return nil
}

func postOf(source string, it feed.Item, inFeed bool) Post {
	p := Post{ItemID: it.ID, Source: source, Kind: it.Kind, Title: it.Title, Summary: it.Summary, Link: it.Link,
		Categories: it.Categories, Published: it.Published, InFeed: inFeed}
	if it.Image != nil {
		p.ImageURL = it.Image.URL
	}
	return p
}

// upsertPost writes the item's index row, keeping its website likes.
func upsertPost(mc companion.ModuleContext, p Post) {
	mc.DB().Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "item_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"source", "kind", "title", "summary", "link", "image_url", "categories", "published", "in_feed", "updated_at"}),
	}).Create(&p)
}

// indexItem refreshes one item's index row after it was posted or failed.
func (c *Connection) indexItem(it feed.Item) {
	upsertPost(c.ctx, postOf(c.SourceID, it, true))
}

// syncIndex mirrors every connected source into the posts index. Items no
// longer in their feed stay, marked, with their publications and likes.
func syncIndex(ctx context.Context, mc companion.ModuleContext) error {
	seenSource := map[string]bool{}
	for _, in := range mc.Instances() {
		c := in.Instance.(*Connection)
		if seenSource[c.SourceID] {
			continue
		}
		seenSource[c.SourceID] = true
		items, err := c.Source.ListItems(ctx)
		if err != nil {
			mc.Log().Printf("index: %s: %v", c.SourceID, err)
			continue
		}
		inFeed := map[string]bool{}
		for _, it := range items {
			inFeed[it.ID] = true
			upsertPost(mc, postOf(c.SourceID, it, true))
		}
		var gone []Post
		mc.DB().Where("source = ? AND in_feed = ?", c.SourceID, true).Find(&gone)
		for _, p := range gone {
			if !inFeed[p.ItemID] {
				mc.DB().Model(&Post{}).Where("item_id = ?", p.ItemID).Update("in_feed", false)
			}
		}
	}
	return nil
}

// onEvent keeps publications made by others (microblog posts) in the
// table, and the likes of every publication current.
func onEvent(mc companion.ModuleContext, ev companion.Event) error {
	db := mc.DB()
	switch ev.Type {
	case social.EventPostPublished:
		var p social.PostPublished
		if err := ev.Decode(&p); err != nil {
			return err
		}
		if p.Source == "autoupload" {
			return nil
		}
		pub := Publication{ItemID: p.ItemID, Target: p.Target, Platform: p.Platform, Source: p.Source, Status: StatusPublished,
			PostID: p.Ref.ID, PostURL: p.Ref.URL, Version: p.Ref.Version, PublishedAt: p.Time}
		db.Where("item_id = ? AND target = ?", p.ItemID, p.Target).Assign(pub).FirstOrCreate(&pub)
		db.Create(&Attempt{ItemID: p.ItemID, Target: p.Target, Platform: p.Platform, Source: p.Source, Success: true, CreatedAt: p.Time})
		db.Model(&Attempt{}).Where("item_id = ? AND target = ? AND success = ? AND resolved_at IS NULL", p.ItemID, p.Target, false).Update("resolved_at", p.Time)
		ensureNote(mc, p.ItemID, p.Kind, p.Title, p.Link, p.Source, p.Time)
	case social.EventPostFailed:
		var p social.PostFailed
		if err := ev.Decode(&p); err != nil {
			return err
		}
		if p.Source == "autoupload" {
			return nil
		}
		db.Create(&Attempt{ItemID: p.ItemID, Target: p.Target, Platform: p.Platform, Source: p.Source, Cause: p.Cause,
			HTTPStatus: p.HTTPStatus, Error: p.Error, CreatedAt: p.Time})
		ensureNote(mc, p.ItemID, p.Kind, p.Title, p.Link, p.Source, p.Time)
	case social.EventPostRemoved:
		var p social.PostRemoved
		if err := ev.Decode(&p); err != nil {
			return err
		}
		if p.Source != "autoupload" {
			db.Where("item_id = ? AND target = ?", p.ItemID, p.Target).Delete(&Publication{})
		}
	case social.EventStatsUpdated:
		var s social.StatsUpdated
		if err := ev.Decode(&s); err != nil {
			return err
		}
		if s.Target == social.WebsiteTarget {
			db.Model(&Post{}).Where("item_id = ?", s.ItemID).Update("website_likes", s.Likes)
			return nil
		}
		at := s.Time
		db.Model(&Publication{}).Where("item_id = ? AND target = ?", s.ItemID, s.Target).Updates(map[string]any{"likes": s.Likes, "likes_at": at})
		db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&LikeDay{ItemID: s.ItemID, Target: s.Target, Day: at.Format(time.DateOnly), Likes: s.Likes})
	case feed.EventItemDiscovered:
		var d feed.ItemDiscovered
		if err := ev.Decode(&d); err != nil {
			return err
		}
		upsertPost(mc, postOf(d.Source, d.Item, true))
	}
	return nil
}

// ensureNote indexes a post another building block published (a microblog
// note), so it gets a row of its own.
func ensureNote(mc companion.ModuleContext, id, kind, title, link, source string, at time.Time) {
	var existing Post
	if mc.DB().Where("item_id = ?", id).Limit(1).Find(&existing).RowsAffected > 0 {
		return
	}
	if kind == "" {
		kind = "note"
	}
	upsertPost(mc, Post{ItemID: id, Source: source, Kind: kind, Title: title, Link: link, Published: at, InFeed: true})
}
