// Package interactions is the interactions module: it counts the likes of
// every post any building block published (it learns of them through
// social.post.published/v1), paced so a large catalogue never floods a
// network, keeps the likes given on the website itself, serves both to the
// website under the old /api/interactions/ URLs, and tells the others
// through social.stats.updated/v1.
package interactions

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
)

func init() {
	companion.Register(companion.Info{
		ID:   "interactions",
		Name: "Likes",
		New:  func() companion.Instance { return new(Counter) },
		Slots: []companion.Slot{
			{Name: "platforms", Needs: social.InteractionsV1, Multiple: true, Optional: true},
		},
		Subscribes: []string{social.EventPostPublished, social.EventPostRemoved},
		OnEvent:    onEvent,
		Migrate:    migrate,
		Routes:     routes,
		Aliases:    []string{"/api/interactions"},
		Import:     importLegacy,
	})
}

// Pacing: the whole catalogue is refreshed within TargetCycle, at most
// MaxPerTick posts a minute, with a gap between calls to one network.
const (
	TargetCycle      = 6 * time.Hour
	TickInterval     = time.Minute
	MinPerTick       = 1
	MaxPerTick       = 10
	InterRequestBase = 500 * time.Millisecond
	InterRequestJit  = 500 * time.Millisecond
	SameNetworkGap   = time.Second
	// rateLimitPause keeps a network that answered 429 alone for a while.
	rateLimitPause = 15 * time.Minute
)

// Tracked is a published post whose likes are counted.
type Tracked struct {
	ID        uint   `gorm:"primaryKey"`
	ItemID    string `gorm:"uniqueIndex:idx_tracked;index"`
	Target    string `gorm:"uniqueIndex:idx_tracked"` // "mastodon:main"
	Source    string // who published it
	Platform  string
	PostID    string
	PostURL   string
	Version   string
	Likes     int
	LikesAt   *time.Time `gorm:"index"`
	LastError string
	CreatedAt time.Time
}

func (Tracked) TableName() string { return "interactions_tracked" }

// NativeLike is a like given on the website. The IP is stored only as a
// salted hash (GDPR); the token lets the visitor take it back.
type NativeLike struct {
	ID        uint   `gorm:"primaryKey"`
	ItemID    string `gorm:"column:item_id;index;not null"`
	IPHash    string `gorm:"column:ip_hash;index;not null"`
	Token     string `gorm:"index;not null"`
	CreatedAt time.Time
}

func (NativeLike) TableName() string { return "interactions_native_likes" }

// NativeCount is an item's website like count. Its row exists from the first
// like on, also after every like was taken back, as the old interaction row
// did: the website gets {"platform":"native","likes":0} then.
type NativeCount struct {
	ItemID string `gorm:"primaryKey"`
	Likes  int
}

func (NativeCount) TableName() string { return "interactions_native_counts" }

func migrate(mc companion.ModuleContext) error {
	if mc.DB() == nil {
		return errors.New("interactions: no database")
	}
	return mc.DB().AutoMigrate(&Tracked{}, &NativeLike{}, &NativeCount{})
}

// Counter is the module's one instance.
type Counter struct {
	// IPHashSalt salts the hashed IPs of website likes.
	IPHashSalt string                         `json:"ipHashSalt" companion:"secret"`
	Platforms  map[string]social.Interactions `companion:"slot=platforms"`

	ctx    companion.Context
	runMu  sync.Mutex
	gapMu  sync.Mutex
	last   map[string]time.Time // last call per platform
	paused map[string]time.Time // platforms paused after a 429
	sleep  func(time.Duration)
}

func (c *Counter) Provision(ctx companion.Context) error {
	c.ctx = ctx
	c.last, c.paused = map[string]time.Time{}, map[string]time.Time{}
	c.sleep = time.Sleep
	return migrate(ctx)
}

// NoPacing removes the gaps between calls; for tests.
func (c *Counter) NoPacing() { c.sleep = func(time.Duration) {} }

func (c *Counter) Jobs() []companion.Job {
	return []companion.Job{
		{Name: "tick", Schedule: "0 * * * * *", Timeout: 5 * time.Minute, Run: c.Tick},
		{Name: "sweep", Timeout: 6 * time.Hour, Run: c.Sweep},
	}
}

// perTick is how many posts one tick refreshes, so the catalogue rotates
// through within TargetCycle.
func perTick(total int) int {
	if total <= 0 {
		return 0
	}
	k := int(math.Ceil(float64(total) * float64(TickInterval) / float64(TargetCycle)))
	return max(MinPerTick, min(MaxPerTick, k))
}

// stalest returns the tracked posts, never refreshed first, then oldest.
func (c *Counter) stalest() ([]Tracked, error) {
	var rows []Tracked
	if err := c.ctx.DB().Find(&rows).Error; err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].LikesAt, rows[j].LikesAt
		switch {
		case a == nil && b != nil:
			return true
		case a != nil && b == nil:
			return false
		case a == nil && b == nil:
			return rows[i].ID < rows[j].ID
		}
		return a.Before(*b)
	})
	return rows, nil
}

// Tick refreshes the most stale posts. A tick that finds another refresh
// running does nothing.
func (c *Counter) Tick(ctx context.Context) error {
	if !c.runMu.TryLock() {
		return nil
	}
	defer c.runMu.Unlock()
	rows, err := c.stalest()
	if err != nil {
		return err
	}
	return c.refresh(ctx, rows[:min(len(rows), perTick(len(rows)))])
}

// Sweep refreshes every post, with the same pacing; "refresh now".
func (c *Counter) Sweep(ctx context.Context) error {
	if !c.runMu.TryLock() {
		return errors.New("a refresh is already running")
	}
	defer c.runMu.Unlock()
	rows, err := c.stalest()
	if err != nil {
		return err
	}
	return c.refresh(ctx, rows)
}

// RefreshItem refreshes every publication of one item, e.g. a microblog
// post the admin looks at.
func (c *Counter) RefreshItem(ctx context.Context, itemID string) error {
	if !c.runMu.TryLock() {
		return errors.New("a refresh is already running")
	}
	defer c.runMu.Unlock()
	var rows []Tracked
	c.ctx.DB().Where("item_id = ?", itemID).Find(&rows)
	return c.refresh(ctx, rows)
}

func (c *Counter) refresh(ctx context.Context, rows []Tracked) error {
	ok := 0
	for i, t := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if until, paused := c.paused[t.Platform]; paused && time.Now().Before(until) {
			continue
		}
		if i > 0 {
			c.wait(t.Platform)
		}
		if err := c.refreshOne(ctx, t); err != nil {
			c.ctx.Log().Printf("%s on %s: %v", t.ItemID, t.Target, err)
			continue
		}
		ok++
	}
	if len(rows) > 0 {
		c.ctx.Log().Printf("refreshed %d/%d", ok, len(rows))
	}
	return nil
}

// wait keeps the gap between two calls, and between calls to one network.
func (c *Counter) wait(platform string) {
	d := InterRequestBase + time.Duration(rand.Int64N(int64(InterRequestJit)))
	c.gapMu.Lock()
	if last, ok := c.last[platform]; ok {
		if need := SameNetworkGap - time.Since(last); need > d {
			d = need
		}
	}
	c.gapMu.Unlock()
	c.sleep(d)
	c.gapMu.Lock()
	c.last[platform] = time.Now()
	c.gapMu.Unlock()
}

func (c *Counter) refreshOne(ctx context.Context, t Tracked) error {
	p, ok := c.Platforms[t.Target]
	if !ok {
		return fmt.Errorf("%s is not connected to the platforms slot", t.Target)
	}
	stats, err := p.GetStats(ctx, social.Result{ID: t.PostID, URL: t.PostURL, Version: t.Version})
	now := time.Now()
	if err != nil {
		if errors.Is(err, social.ErrRateLimited) {
			c.paused[t.Platform] = now.Add(rateLimitPause)
		}
		c.ctx.DB().Model(&Tracked{}).Where("id = ?", t.ID).Updates(map[string]any{"last_error": err.Error(), "likes_at": now})
		return err
	}
	c.ctx.DB().Model(&Tracked{}).Where("id = ?", t.ID).Updates(map[string]any{"likes": stats.Likes, "likes_at": now, "last_error": ""})
	c.ctx.Metrics().Add("likes_refreshed_total", 1, "target", t.Target)
	return c.ctx.Emit(ctx, social.EventStatsUpdated, social.StatsUpdated{ItemID: t.ItemID, Source: t.Source, Target: t.Target,
		Platform: t.Platform, Likes: stats.Likes, Time: now})
}

// onEvent tracks every new publication and drops released ones.
func onEvent(mc companion.ModuleContext, ev companion.Event) error {
	switch ev.Type {
	case social.EventPostPublished:
		var p social.PostPublished
		if err := ev.Decode(&p); err != nil {
			return err
		}
		if p.Ref.ID == "" {
			return nil
		}
		source := p.Source
		if source == "backfill" {
			source = "autoupload"
		}
		t := Tracked{ItemID: p.ItemID, Target: p.Target, Source: source, Platform: p.Platform, PostID: p.Ref.ID, PostURL: p.Ref.URL, Version: p.Ref.Version}
		return mc.DB().Where("item_id = ? AND target = ?", p.ItemID, p.Target).
			Assign(map[string]any{"source": source, "platform": p.Platform, "post_id": p.Ref.ID, "post_url": p.Ref.URL, "version": p.Ref.Version}).
			FirstOrCreate(&t).Error
	case social.EventPostRemoved:
		var p social.PostRemoved
		if err := ev.Decode(&p); err != nil {
			return err
		}
		return mc.DB().Where("item_id = ? AND target = ?", p.ItemID, p.Target).Delete(&Tracked{}).Error
	}
	return nil
}

// ---- website likes ----

func (c *Counter) hashIP(ip string) string {
	sum := sha256.Sum256([]byte(ip + c.IPHashSalt))
	return hex.EncodeToString(sum[:])
}

func newToken() string {
	b := make([]byte, 32)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}

func (c *Counter) nativeCount(itemID string) int {
	var n int64
	c.ctx.DB().Model(&NativeLike{}).Where("item_id = ?", itemID).Count(&n)
	return int(n)
}

// nativeChanged stores the new website like count and tells the others.
func (c *Counter) nativeChanged(ctx context.Context, itemID string) {
	n := c.nativeCount(itemID)
	c.ctx.DB().Save(&NativeCount{ItemID: itemID, Likes: n})
	_ = c.ctx.Emit(ctx, social.EventStatsUpdated, social.StatsUpdated{ItemID: itemID, Target: social.WebsiteTarget,
		Platform: "native", Likes: n, Time: time.Now()})
}
