package autoupload

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/LNA-DEV/HomePageCompanion/capability/media"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/lib/imagemeta"
)

// maxSkipsPerRun bounds how many items one run marks skipped (routing tags,
// targets that need an image) before it gives up for the day.
const maxSkipsPerRun = 20

// Run publishes the next item of a rotation. It records the run, so "run
// now" shows which post went where, or why none did.
func (c *Connection) Run(ctx context.Context, trigger string) (*Run, error) {
	run := &Run{Connection: c.name(), Trigger: trigger, StartedAt: c.now(), Result: "running", Target: c.TargetID}
	c.ctx.DB().Create(run)
	return c.runInto(ctx, run)
}

// runInto does the run and fills in the given row.
func (c *Connection) runInto(ctx context.Context, run *Run) (*Run, error) {
	trigger := run.Trigger
	if !c.mu.TryLock() {
		now := c.now()
		run.FinishedAt, run.Result, run.Error = &now, "failed", errBusy.Error()
		c.ctx.DB().Save(run)
		return run, errBusy
	}
	defer c.mu.Unlock()
	finish := func(result string, err error) (*Run, error) {
		now := c.now()
		run.FinishedAt, run.Result = &now, result
		if err != nil {
			run.Error = err.Error()
		}
		c.ctx.DB().Save(run)
		return run, err
	}

	if st := c.state(); st.PausedAt != nil && trigger == "schedule" {
		return finish("paused", nil)
	}
	items, err := c.Source.ListItems(ctx)
	if err != nil {
		return finish("failed", fmt.Errorf("source %s: %w", c.SourceID, err))
	}
	hist, err := c.history()
	if err != nil {
		return finish("failed", err)
	}
	lim, err := c.Target.Describe(ctx)
	if err != nil {
		return finish("failed", fmt.Errorf("target %s: %w", c.TargetID, err))
	}
	skips := 0
	for _, cand := range chooseRotation(items, hist, c.now()) {
		res, perr := c.publishItem(ctx, cand.item, lim, "autoupload")
		run.ItemID, run.Title = cand.item.ID, cand.item.Title
		switch {
		case res.skipped:
			skips++
			if skips >= maxSkipsPerRun {
				return finish("skipped", nil)
			}
			continue
		case perr != nil:
			return finish("failed", perr)
		}
		if trigger == "manual" {
			c.resume()
		}
		run.PostURL = res.url
		return finish("published", nil)
	}
	if skips > 0 {
		return finish("skipped", nil)
	}
	return finish("nothing", nil)
}

// history of every item on this connection's target.
func (c *Connection) history() (map[string]history, error) {
	var pubs []Publication
	if err := c.ctx.DB().Where("target = ?", c.TargetID).Find(&pubs).Error; err != nil {
		return nil, err
	}
	var atts []Attempt
	if err := c.ctx.DB().Where("target = ? AND success = ? AND resolved_at IS NULL", c.TargetID, false).Find(&atts).Error; err != nil {
		return nil, err
	}
	return historyFrom(pubs, atts), nil
}

type publishResult struct {
	skipped bool
	url     string
}

// publishItem builds the post for one item and sends it, recording the
// publication or the attempt either way.
func (c *Connection) publishItem(ctx context.Context, item feed.Item, lim social.Limits, source string) (publishResult, error) {
	post, reason, err := c.buildPost(ctx, item, lim)
	if err != nil {
		c.recordFailure(ctx, item, lim.Platform, source, err)
		return publishResult{}, err
	}
	if reason != "" {
		c.ctx.Log().Printf("%s: skipping %s on %s: %s", c.name(), item.ID, c.TargetID, reason)
		c.recordSkip(item, lim.Platform, reason)
		return publishResult{skipped: true}, nil
	}
	res, err := c.Target.Publish(ctx, post)
	if errors.Is(err, social.ErrNeedsImage) {
		c.recordSkip(item, lim.Platform, "needs an image")
		return publishResult{skipped: true}, nil
	}
	if err != nil {
		c.ctx.Log().Printf("%s: failed to publish %s on %s: %v", c.name(), item.ID, c.TargetID, err)
		c.recordFailure(ctx, item, lim.Platform, source, err)
		return publishResult{}, err
	}
	c.ctx.Log().Printf("%s: published %s on %s: %s", c.name(), item.ID, c.TargetID, res.URL)
	c.recordSuccess(ctx, item, lim.Platform, source, res)
	return publishResult{url: res.URL}, nil
}

// buildPost composes the post. A non-empty reason means the item is skipped
// on this target.
func (c *Connection) buildPost(ctx context.Context, item feed.Item, lim social.Limits) (social.Post, string, error) {
	if lim.RequiresImage && item.Image == nil {
		return social.Post{}, "needs an image", nil
	}
	var meta *imagemeta.Metadata
	if c.needsMetadata() && item.Image != nil {
		meta = c.metadata(ctx, item)
	}
	if c.RoutingTagsSource != "" {
		if d := EvaluateRouting(routingTags(c.RoutingTagsSource, item, meta), lim.Platform); !d.Allow {
			return social.Post{}, d.Reason, nil
		}
	}
	credits := resolveCredits(ctx, item.People, c.TagModels, lim, c.Target, func(slug, reason string) {
		c.ctx.Log().Printf("%s: %s on %s: model %s not tagged: %s", c.name(), item.ID, lim.Platform, slug, reason)
	})
	limits := CaptionLimits{MaxLen: lim.MaxLength, MaxHashtags: lim.MaxHashtags}
	if lim.LengthUnit == "utf8-emoji" {
		limits.Len = threadsLen
	}
	caption := BuildCaption(c.Settings, &item, meta, credits, limits)
	for _, slug := range caption.Unmentioned {
		c.ctx.Log().Printf("%s: %s on %s: model %s not tagged: caption too long", c.name(), item.ID, lim.Platform, slug)
	}
	if len(caption.NamesCut) > 0 {
		c.ctx.Log().Printf("%s: %s on %s: names line cut to fit, not named in full: %s", c.name(), item.ID, lim.Platform, strings.Join(caption.NamesCut, ", "))
	}
	post := social.Post{ItemID: item.ID, Kind: item.Kind, Text: caption.Text, Mentions: mentionsOf(credits)}
	if item.Image != nil {
		post.Images = []social.Image{{URL: item.Image.URL, Alt: item.Image.Alt}}
	}
	if item.Kind == feed.KindArticle && item.Link != "" {
		card := &social.LinkCard{URL: item.Link, Title: item.Title, Description: item.Summary}
		if item.Image != nil {
			card.ImageURL = item.Image.URL
		}
		post.Link = card
		if lim.RequiresImage || lim.LinkCard == "" {
			// Instagram and Pixelfed post the cover image with the text.
			post.Link = nil
		}
	}
	return post, "", nil
}

func (c *Connection) needsMetadata() bool {
	return c.AddExifToCaption || c.RoutingTagsSource == "exif" || c.CopyrightSource == "exif"
}

// metadata reads EXIF, IPTC and XMP from the original image, through the
// media slot when one is connected.
func (c *Connection) metadata(ctx context.Context, item feed.Item) *imagemeta.Metadata {
	data, _, err := companion.Fetch(ctx, c.ctx.HTTP(), item.Image.MetadataURL())
	if err != nil {
		c.ctx.Log().Printf("%s: metadata image download for %s failed: %v", c.name(), item.ID, err)
		return nil
	}
	if c.Media != nil {
		m, err := c.Media.ReadExif(ctx, data)
		if err != nil && m.Make == "" && len(m.Keywords) == 0 {
			return nil
		}
		return fromMedia(m)
	}
	m, _ := imagemeta.Extract(data)
	return m
}

func fromMedia(m media.Metadata) *imagemeta.Metadata {
	return &imagemeta.Metadata{Make: m.Make, Model: m.Model, Lens: m.Lens, FNumber: m.FNumber, ExposureSeconds: m.ExposureSeconds,
		ISO: m.ISO, FocalLengthMM: m.FocalLength, Copyright: m.Copyright, Artist: m.Artist, Keywords: m.Keywords}
}

// routingTags are the meta_skip:/meta_only: tags from the source the
// connection names.
func routingTags(src string, item feed.Item, meta *imagemeta.Metadata) []string {
	switch src {
	case "rss":
		return append([]string(nil), item.Categories...)
	case "exif":
		if meta == nil {
			return nil
		}
		return append([]string(nil), meta.Keywords...)
	}
	return nil
}

// ---- recording ----

func (c *Connection) recordSkip(item feed.Item, platform, reason string) {
	pub := Publication{ItemID: item.ID, Target: c.TargetID, Platform: platform, Source: "autoupload", Connection: c.name(),
		Status: StatusSkipped, Reason: reason, PublishedAt: c.now()}
	c.ctx.DB().Where("item_id = ? AND target = ?", item.ID, c.TargetID).Assign(pub).FirstOrCreate(&pub)
	c.indexItem(item)
}

func (c *Connection) recordSuccess(ctx context.Context, item feed.Item, platform, source string, res social.Result) {
	now := c.now()
	db := c.ctx.DB()
	pub := Publication{ItemID: item.ID, Target: c.TargetID, Platform: platform, Source: source, Connection: c.name(),
		Status: StatusPublished, PostID: res.ID, PostURL: res.URL, Version: res.Version, PublishedAt: now}
	db.Where("item_id = ? AND target = ?", item.ID, c.TargetID).Assign(pub).FirstOrCreate(&pub)
	db.Create(&Attempt{ItemID: item.ID, Target: c.TargetID, Platform: platform, Source: source, Connection: c.name(), Success: true, CreatedAt: now})
	db.Model(&Attempt{}).Where("item_id = ? AND target = ? AND success = ? AND resolved_at IS NULL", item.ID, c.TargetID, false).Update("resolved_at", now)
	c.indexItem(item)
	c.ctx.Metrics().Add("uploads_total", 1, "target", c.TargetID, "result", "published")
	ev := social.PostPublished{ItemID: item.ID, Kind: item.Kind, Title: item.Title, Link: item.Link, Source: "autoupload",
		Connection: c.ctx.InstanceID(), Target: c.TargetID, Platform: platform, Ref: res, Time: now}
	if err := c.ctx.Emit(ctx, social.EventPostPublished, ev); err != nil {
		c.ctx.Log().Printf("%s: event: %v", c.name(), err)
	}
}

func (c *Connection) recordFailure(ctx context.Context, item feed.Item, platform, source string, err error) {
	f := social.Classify(err)
	msg := err.Error()
	if len(msg) > 2048 {
		msg = msg[:2048] + "…"
	}
	now := c.now()
	c.ctx.DB().Create(&Attempt{ItemID: item.ID, Target: c.TargetID, Platform: platform, Source: source, Connection: c.name(),
		Cause: f.Cause, Code: f.Code, HTTPStatus: f.HTTPStatus, Error: msg, CreatedAt: now})
	c.indexItem(item)
	c.ctx.Metrics().Add("uploads_total", 1, "target", c.TargetID, "result", "failed", "cause", f.Cause)
	ev := social.PostFailed{ItemID: item.ID, Kind: item.Kind, Title: item.Title, Link: item.Link, Source: "autoupload",
		Connection: c.ctx.InstanceID(), Target: c.TargetID, Platform: platform, Error: msg, Cause: f.Cause, HTTPStatus: f.HTTPStatus, Time: now}
	if eerr := c.ctx.Emit(ctx, social.EventPostFailed, ev); eerr != nil {
		c.ctx.Log().Printf("%s: event: %v", c.name(), eerr)
	}
	c.notifyFailure(ctx, item, err)
	if f.Cause == social.CauseAccount {
		c.pause(ctx, msg)
	}
}

// ---- announcements ----

// enqueue schedules an announcement: Delay after the post appeared, moved
// into the window. Posts older than Since are never announced.
func (c *Connection) enqueue(item feed.Item, round int) error {
	if item.Published.Before(c.since()) {
		return nil
	}
	due := c.inWindow(c.now().Add(c.Delay.D()))
	a := Announcement{Connection: c.name(), ItemID: item.ID, Round: round, DueAt: due}
	return c.ctx.DB().Where("connection = ? AND item_id = ? AND round = ?", a.Connection, a.ItemID, a.Round).FirstOrCreate(&a).Error
}

// announceDue publishes the announcements whose time has come, oldest post
// first. It also picks up new posts the event missed, and reshares.
func (c *Connection) announceDue(ctx context.Context) error {
	if !c.mu.TryLock() {
		return nil
	}
	defer c.mu.Unlock()
	if st := c.state(); st.PausedAt != nil {
		return nil
	}
	if err := c.catchUp(ctx); err != nil {
		c.ctx.Log().Printf("%s: %v", c.name(), err)
	}
	now := c.now()
	if c.inWindow(now) != now {
		return nil
	}
	var due []Announcement
	c.ctx.DB().Where("connection = ? AND done_at IS NULL AND due_at <= ?", c.name(), now).Order("due_at").Find(&due)
	if len(due) == 0 {
		return nil
	}
	lim, err := c.Target.Describe(ctx)
	if err != nil {
		return err
	}
	hist, err := c.history()
	if err != nil {
		return err
	}
	for _, a := range due {
		item, err := c.Source.GetItem(ctx, a.ItemID)
		if err != nil {
			c.ctx.DB().Model(&a).Update("done_at", now)
			continue
		}
		switch h := hist[a.ItemID]; {
		case a.Round == 0 && h.done, h.manualOnly:
			// Posted meanwhile, or waiting for a retry by hand.
			c.ctx.DB().Model(&a).Update("done_at", now)
			continue
		case h.openFails > 0 && !retryDue(h, now):
			continue
		}
		run := &Run{Connection: c.name(), Trigger: "announce", StartedAt: now, ItemID: item.ID, Title: item.Title, Target: c.TargetID}
		if a.Round > 0 {
			c.ctx.DB().Where("item_id = ? AND target = ?", item.ID, c.TargetID).Delete(&Publication{})
		}
		res, perr := c.publishItem(ctx, item, lim, "autoupload")
		fin := c.now()
		run.FinishedAt = &fin
		switch {
		case res.skipped:
			run.Result = "skipped"
			c.ctx.DB().Model(&a).Update("done_at", now)
		case perr != nil:
			run.Result, run.Error = "failed", perr.Error()
		default:
			run.Result, run.PostURL = "published", res.url
			c.ctx.DB().Model(&a).Update("done_at", now)
		}
		c.ctx.DB().Create(run)
		if perr != nil && social.Classify(perr).Cause == social.CauseAccount {
			return perr
		}
	}
	return nil
}

// catchUp queues posts that appeared without an event reaching the
// connection, and reshares due ones.
func (c *Connection) catchUp(ctx context.Context) error {
	items, err := c.Source.ListItems(ctx)
	if err != nil {
		return err
	}
	since := c.since()
	var known []Announcement
	c.ctx.DB().Where("connection = ?", c.name()).Find(&known)
	queued := map[string]map[int]bool{}
	for _, a := range known {
		if queued[a.ItemID] == nil {
			queued[a.ItemID] = map[int]bool{}
		}
		queued[a.ItemID][a.Round] = true
	}
	var pubs []Publication
	c.ctx.DB().Where("target = ?", c.TargetID).Find(&pubs)
	published := map[string]Publication{}
	for _, p := range pubs {
		published[p.ItemID] = p
	}
	for _, it := range items {
		if it.Published.Before(since) {
			continue
		}
		p, done := published[it.ID]
		switch {
		case !done && !queued[it.ID][0]:
			if err := c.enqueue(it, 0); err != nil {
				return err
			}
		case done && p.Status == StatusPublished && c.ReshareAfter > 0 && !queued[it.ID][1] &&
			c.now().Sub(p.PublishedAt) >= c.ReshareAfter.D():
			a := Announcement{Connection: c.name(), ItemID: it.ID, Round: 1, DueAt: c.inWindow(c.now())}
			if err := c.ctx.DB().Create(&a).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// nextAnnouncement is the next queued post, for the schedule.
func (c *Connection) nextAnnouncement() (*Announcement, int64) {
	var a Announcement
	var n int64
	c.ctx.DB().Model(&Announcement{}).Where("connection = ? AND done_at IS NULL", c.name()).Count(&n)
	if c.ctx.DB().Where("connection = ? AND done_at IS NULL", c.name()).Order("due_at").Limit(1).Find(&a).RowsAffected == 0 {
		return nil, 0
	}
	return &a, n
}

// next is the post a rotation would choose now, for the schedule and the
// "scheduled next" cell.
func (c *Connection) next(ctx context.Context) (*feed.Item, error) {
	if c.Strategy == StrategyAnnounce {
		a, _ := c.nextAnnouncement()
		if a == nil {
			return nil, nil
		}
		it, err := c.Source.GetItem(ctx, a.ItemID)
		if err != nil {
			return nil, err
		}
		return &it, nil
	}
	items, err := c.Source.ListItems(ctx)
	if err != nil {
		return nil, err
	}
	hist, err := c.history()
	if err != nil {
		return nil, err
	}
	lim, err := c.Target.Describe(ctx)
	if err != nil {
		return nil, err
	}
	for _, cand := range chooseRotation(items, hist, c.now()) {
		if lim.RequiresImage && cand.item.Image == nil {
			continue
		}
		if c.RoutingTagsSource == "rss" && !EvaluateRouting(cand.item.Categories, lim.Platform).Allow {
			continue
		}
		it := cand.item
		return &it, nil
	}
	return nil, nil
}

// publishNow posts one item at once, by hand: it ignores the schedule, the
// pause and earlier skips.
func (c *Connection) publishNow(ctx context.Context, itemID string) (*Run, error) {
	if !c.mu.TryLock() {
		return nil, errBusy
	}
	defer c.mu.Unlock()
	item, err := c.Source.GetItem(ctx, itemID)
	if err != nil {
		return nil, err
	}
	lim, err := c.Target.Describe(ctx)
	if err != nil {
		return nil, err
	}
	c.ctx.DB().Where("item_id = ? AND target = ? AND status = ?", itemID, c.TargetID, StatusSkipped).Delete(&Publication{})
	run := &Run{Connection: c.name(), Trigger: "manual", StartedAt: c.now(), ItemID: item.ID, Title: item.Title, Target: c.TargetID}
	res, perr := c.publishItem(ctx, item, lim, "autoupload")
	fin := c.now()
	run.FinishedAt = &fin
	switch {
	case res.skipped:
		run.Result = "skipped"
	case perr != nil:
		run.Result, run.Error = "failed", perr.Error()
	default:
		run.Result, run.PostURL = "published", res.url
		c.resume()
	}
	c.ctx.DB().Create(run)
	return run, nil
}

// preview renders the post without sending it.
func (c *Connection) preview(ctx context.Context, item feed.Item) (social.Rendered, error) {
	lim, err := c.Target.Describe(ctx)
	if err != nil {
		return social.Rendered{}, err
	}
	post, reason, err := c.buildPost(ctx, item, lim)
	if err != nil {
		return social.Rendered{}, err
	}
	if reason != "" {
		return social.Rendered{Warnings: []string{"skipped: " + reason}}, nil
	}
	return c.Target.Preview(ctx, post)
}
