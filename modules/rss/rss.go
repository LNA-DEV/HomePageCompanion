// Package rss is the rss module: one instance per feed, each a
// feed.source/v1. It mirrors its feed into its own database area every hour
// (and before a read when the mirror is older), announces new items with
// feed.item.discovered/v1, and keeps items that left the feed, so their
// publications and likes stay attached.
package rss

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/gin-gonic/gin"
	"github.com/mmcdole/gofeed"
	"gorm.io/gorm"
)

func init() {
	companion.Register(companion.Info{
		ID:       "rss",
		Name:     "Feeds",
		New:      func() companion.Instance { return new(Source) },
		Provides: []companion.Capability{feed.SourceV1},
		Start:    migrate,
		Routes:   routes,
		Import:   importLegacy,
		UI:       ui,
	})
}

func migrate(mc companion.ModuleContext) error {
	if mc.DB() == nil {
		return errors.New("rss: no database")
	}
	return mc.DB().AutoMigrate(&Feed{}, &Item{})
}

// Source is one feed.
type Source struct {
	URL string `json:"url"`
	// Kind fixes the kind of every item: "photo" or "article". Empty: an
	// item with an image is a photo, one without an article.
	Kind string `json:"kind"`
	// Interval between fetches ("1h" when empty).
	Interval companion.Duration `json:"interval"`
	// Schedule overrides Interval with a cron spec (with seconds).
	Schedule string `json:"schedule"`

	ctx companion.Context
	mu  sync.Mutex // one refresh at a time
}

func (s *Source) Provision(ctx companion.Context) error {
	s.ctx = ctx
	if s.Interval == 0 {
		s.Interval = companion.Duration(time.Hour)
	}
	return migrate(ctx)
}

func (s *Source) Validate() error {
	if strings.TrimSpace(s.URL) == "" {
		return errors.New("url is required")
	}
	switch s.Kind {
	case "", feed.KindPhoto, feed.KindArticle:
	default:
		return fmt.Errorf("kind %q: photo, article or empty", s.Kind)
	}
	return nil
}

// Start mirrors the feed once in the background; a feed that cannot be
// fetched is logged and tried again by the next run.
func (s *Source) Start() error {
	go func() {
		if err := s.Refresh(context.Background()); err != nil {
			s.ctx.Log().Printf("%s: %v", s.ctx.InstanceName(), err)
		}
	}()
	return nil
}

func (s *Source) Jobs() []companion.Job {
	spec := s.Schedule
	if spec == "" {
		spec = cronEvery(s.Interval.D())
	}
	return []companion.Job{{Name: "refresh", Schedule: spec, Timeout: 10 * time.Minute, Run: s.Refresh}}
}

// cronEvery turns an interval into a cron spec: whole hours on the hour,
// minutes on the minute.
func cronEvery(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return "0 0 4 * * *"
	case d >= time.Hour:
		return fmt.Sprintf("0 0 */%d * * *", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("0 */%d * * * *", int(d/time.Minute))
	}
	return "0 * * * * *"
}

func (s *Source) db() *gorm.DB { return s.ctx.DB() }

// Refresh fetches the feed and updates the mirror. Items no longer in the
// feed are marked removed; items that come back are restored.
func (s *Source) Refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := s.ctx.InstanceName()
	parser := gofeed.NewParser()
	parser.Client = s.ctx.HTTP()
	fctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	parsed, err := parser.ParseURLWithContext(s.URL, fctx)
	now := time.Now()
	var row Feed
	s.db().Where("instance = ?", name).Limit(1).Find(&row)
	row.Instance, row.URL = name, s.URL
	if err != nil {
		row.LastError = err.Error()
		s.db().Save(&row)
		s.ctx.Metrics().Add("rss_fetch_errors_total", 1, "feed", name)
		return fmt.Errorf("fetch %s: %w", name, err)
	}
	firstMirror := row.FetchedAt == nil
	row.Title, row.Description, row.Link = parsed.Title, parsed.Description, parsed.Link
	row.Language, row.Copyright, row.Generator = parsed.Language, parsed.Copyright, parsed.Generator
	row.FetchedAt, row.LastError = &now, ""
	if err := s.db().Save(&row).Error; err != nil {
		return err
	}

	var existing []Item
	s.db().Where("instance = ?", name).Find(&existing)
	byGUID := make(map[string]Item, len(existing))
	for _, it := range existing {
		byGUID[it.GUID] = it
	}
	seen := map[string]bool{}
	var discovered []Item
	for _, pi := range parsed.Items {
		if pi.GUID == "" {
			s.ctx.Log().Printf("%s: item %q has no GUID, skipped", name, pi.Title)
			continue
		}
		if seen[pi.GUID] {
			continue
		}
		seen[pi.GUID] = true
		it := toItem(name, s.Kind, pi, now, func(slug string) {
			s.ctx.Log().Printf("%s: %s: model %q has no name, skipped", name, pi.GUID, slug)
		})
		if old, ok := byGUID[pi.GUID]; ok {
			it.ID, it.FirstSeen = old.ID, old.FirstSeen
			if !it.HasDate {
				it.Published = old.Published
			}
			if err := s.db().Save(&it).Error; err != nil {
				s.ctx.Log().Printf("%s: saving %s: %v", name, pi.GUID, err)
			}
			continue
		}
		it.FirstSeen = now
		if err := s.db().Create(&it).Error; err != nil {
			s.ctx.Log().Printf("%s: saving %s: %v", name, pi.GUID, err)
			continue
		}
		discovered = append(discovered, it)
	}
	for guid, it := range byGUID {
		if !seen[guid] && it.RemovedAt == nil {
			s.db().Model(&Item{}).Where("id = ?", it.ID).Update("removed_at", now)
		}
	}
	s.ctx.Metrics().Set("rss_items", float64(len(seen)), "feed", name)

	// The first mirror of a feed announces nothing: its items were there
	// before the companion knew the feed.
	if firstMirror && len(existing) == 0 {
		if len(discovered) > 0 {
			s.ctx.Log().Printf("%s: first mirror, %d item(s) known", name, len(discovered))
		}
		return nil
	}
	for _, it := range discovered {
		ev := feed.ItemDiscovered{Source: s.ctx.InstanceID(), Item: it.toFeedItem(&row)}
		if err := s.ctx.Emit(ctx, feed.EventItemDiscovered, ev); err != nil {
			s.ctx.Log().Printf("%s: announcing %s: %v", name, it.GUID, err)
		}
	}
	return nil
}

// fresh refreshes the mirror when it is older than the interval, so a
// publish right after a new photo appeared already sees it.
func (s *Source) fresh(ctx context.Context) {
	var row Feed
	s.db().Where("instance = ?", s.ctx.InstanceName()).Limit(1).Find(&row)
	if row.FetchedAt != nil && time.Since(*row.FetchedAt) < s.Interval.D() {
		return
	}
	if err := s.Refresh(ctx); err != nil {
		s.ctx.Log().Printf("%s: %v", s.ctx.InstanceName(), err)
	}
}

func (s *Source) feedRow() *Feed {
	var row Feed
	if s.db().Where("instance = ?", s.ctx.InstanceName()).Limit(1).Find(&row).RowsAffected == 0 {
		return nil
	}
	return &row
}

// ListItems returns the items currently in the feed, newest first.
func (s *Source) ListItems(ctx context.Context) ([]feed.Item, error) {
	s.fresh(ctx)
	var rows []Item
	if err := s.db().Where("instance = ? AND removed_at IS NULL", s.ctx.InstanceName()).Order("published desc").Find(&rows).Error; err != nil {
		return nil, err
	}
	f := s.feedRow()
	out := make([]feed.Item, len(rows))
	for i, r := range rows {
		out[i] = r.toFeedItem(f)
	}
	return out, nil
}

// GetItem returns one item, also one that left the feed.
func (s *Source) GetItem(ctx context.Context, id string) (feed.Item, error) {
	var row Item
	if s.db().Where("instance = ? AND guid = ?", s.ctx.InstanceName(), id).Limit(1).Find(&row).RowsAffected == 0 {
		return feed.Item{}, feed.ErrNotFound
	}
	return row.toFeedItem(s.feedRow()), nil
}

var _ feed.Source = (*Source)(nil)

// ---- admin API ----

type feedView struct {
	Name      string     `json:"name"`
	URL       string     `json:"url"`
	Title     string     `json:"title"`
	Link      string     `json:"link"`
	Kind      string     `json:"kind,omitempty"`
	Items     int64      `json:"items"`
	Removed   int64      `json:"removed"`
	FetchedAt *time.Time `json:"fetchedAt,omitempty"`
	LastError string     `json:"lastError,omitempty"`
}

func routes(mc companion.ModuleContext, r *companion.Router) {
	source := func(name string) *Source {
		for _, in := range mc.Instances() {
			if in.Name == name {
				return in.Instance.(*Source)
			}
		}
		return nil
	}
	r.Admin.GET("/feeds", func(c *gin.Context) {
		out := []feedView{}
		for _, in := range mc.Instances() {
			s := in.Instance.(*Source)
			v := feedView{Name: in.Name, URL: s.URL, Kind: s.Kind}
			if f := s.feedRow(); f != nil {
				v.Title, v.Link, v.FetchedAt, v.LastError = f.Title, f.Link, f.FetchedAt, f.LastError
			}
			mc.DB().Model(&Item{}).Where("instance = ? AND removed_at IS NULL", in.Name).Count(&v.Items)
			mc.DB().Model(&Item{}).Where("instance = ? AND removed_at IS NOT NULL", in.Name).Count(&v.Removed)
			out = append(out, v)
		}
		c.JSON(http.StatusOK, out)
	})
	// Items of a feed, paged, searched and filtered on the server.
	r.Admin.GET("/feeds/:name/items", func(c *gin.Context) {
		s := source(c.Param("name"))
		if s == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "no such feed"})
			return
		}
		page, limit := paging(c)
		q := mc.DB().Model(&Item{}).Where("instance = ?", c.Param("name"))
		switch c.Query("state") {
		case "removed":
			q = q.Where("removed_at IS NOT NULL")
		case "all":
		default:
			q = q.Where("removed_at IS NULL")
		}
		if k := c.Query("kind"); k != "" {
			q = q.Where("kind = ?", k)
		}
		if term := strings.TrimSpace(c.Query("q")); term != "" {
			like := "%" + strings.ToLower(term) + "%"
			q = q.Where("LOWER(title) LIKE ? OR LOWER(summary) LIKE ? OR LOWER(guid) LIKE ? OR LOWER(categories) LIKE ?", like, like, like, like)
		}
		var total int64
		q.Count(&total)
		var rows []Item
		q.Order("published desc").Offset((page - 1) * limit).Limit(limit).Find(&rows)
		f := s.feedRow()
		items := make([]itemView, len(rows))
		for i, row := range rows {
			items[i] = itemView{Item: row.toFeedItem(f), RemovedAt: row.RemovedAt, FirstSeen: row.FirstSeen}
		}
		c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "limit": limit})
	})
	r.Admin.POST("/feeds/:name/refresh", func(c *gin.Context) {
		s := source(c.Param("name"))
		if s == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "no such feed"})
			return
		}
		if err := s.Refresh(c.Request.Context()); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "refreshed"})
	})
	// Lookup of items by GUID across all feeds, for other modules' UIs.
	r.Admin.POST("/items/lookup", func(c *gin.Context) {
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
			return
		}
		out := map[string]itemView{}
		if len(body.IDs) > 0 {
			var rows []Item
			mc.DB().Where("guid IN ?", body.IDs).Find(&rows)
			for _, row := range rows {
				out[row.GUID] = itemView{Item: row.toFeedItem(nil), Feed: row.Instance, RemovedAt: row.RemovedAt}
			}
		}
		c.JSON(http.StatusOK, out)
	})
}

type itemView struct {
	feed.Item
	Feed      string     `json:"feed,omitempty"`
	FirstSeen time.Time  `json:"firstSeen,omitempty"`
	RemovedAt *time.Time `json:"removedAt,omitempty"`
}

func paging(c *gin.Context) (int, int) {
	page, limit := atoi(c.DefaultQuery("page", "1"), 1), atoi(c.DefaultQuery("limit", "50"), 50)
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return page, limit
}

func atoi(s string, def int) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	if s == "" {
		return def
	}
	return n
}
