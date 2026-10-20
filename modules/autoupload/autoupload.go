// Package autoupload is the autoupload module: every instance is one
// connection from a feed (feed.source/v1) to a target (social.publisher/v1).
// A connection either rotates photos from the archive on a schedule
// (strategy rotate: the photo whose date is closest to today, ignoring the
// year) or announces every new post once (strategy announce). The module
// keeps one publication per post and target instance, every attempt with
// its cause, and the posts index the admin UI pages, searches and filters
// on the server. See docs/concepts/autoposting-ui.md.
package autoupload

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/LNA-DEV/HomePageCompanion/capability/media"
	"github.com/LNA-DEV/HomePageCompanion/capability/notify"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/robfig/cron"
)

func init() {
	companion.Register(companion.Info{
		ID:   "autoupload",
		Name: "Autoposting",
		New:  func() companion.Instance { return new(Connection) },
		Slots: []companion.Slot{
			{Name: "source", Needs: feed.SourceV1},
			{Name: "target", Needs: social.PublisherV1},
			{Name: "media", Needs: media.ImageV1, Optional: true},
			{Name: "notify", Needs: notify.NotifyV1, Optional: true},
		},
		Subscribes: []string{social.EventPostPublished, social.EventPostFailed, social.EventPostRemoved, social.EventStatsUpdated, feed.EventItemDiscovered},
		OnEvent:    onEvent,
		Start:      start,
		Routes:     routes,
		Aliases:    []string{"/api/upload", "/api/backfill"},
		Jobs: []companion.Job{
			{Name: "index", Schedule: "0 */15 * * * *", RunModule: syncIndex},
		},
		Import: importLegacy,
		UI:     ui,
	})
}

// Strategies.
const (
	StrategyRotate   = "rotate"
	StrategyAnnounce = "announce"
)

// Connection is one connection: source → target.
type Connection struct {
	Source   feed.Source      `companion:"slot=source"`
	SourceID string           `companion:"slotid=source"`
	Target   social.Publisher `companion:"slot=target"`
	TargetID string           `companion:"slotid=target"`
	Media    media.Image      `companion:"slot=media"`
	Notify   notify.Notifier  `companion:"slot=notify"`

	// Strategy is "rotate" (default) or "announce".
	Strategy string `json:"strategy"`
	// Cron is the schedule of a rotation, with seconds ("0 0 18 * * *").
	Cron string `json:"cron"`

	Settings

	// Announcements: wait Delay after a post appears, post only inside
	// Window ("08:00-21:00", local time), never posts older than Since
	// (default: when the connection was set up), share again after
	// ReshareAfter (off when 0).
	Delay        companion.Duration `json:"delay"`
	Window       string             `json:"window"`
	Since        string             `json:"since"`
	ReshareAfter companion.Duration `json:"reshareAfter"`
	Timezone     string             `json:"timezone"`

	ctx      companion.Context
	mu       sync.Mutex // one run at a time
	location *time.Location
	now      func() time.Time
}

func (c *Connection) Provision(ctx companion.Context) error {
	c.ctx = ctx
	c.now = time.Now
	if c.Strategy == "" {
		c.Strategy = StrategyRotate
	}
	c.location = time.Local
	if c.Timezone != "" {
		loc, err := time.LoadLocation(c.Timezone)
		if err != nil {
			return fmt.Errorf("timezone: %w", err)
		}
		c.location = loc
	}
	if err := migrate(ctx); err != nil {
		return err
	}
	return c.ensureState()
}

func (c *Connection) Validate() error {
	switch c.Strategy {
	case StrategyRotate:
		if c.Cron != "" {
			if _, err := cron.Parse(c.Cron); err != nil {
				return fmt.Errorf("cron %q: %w", c.Cron, err)
			}
		}
	case StrategyAnnounce:
		if _, _, err := parseWindow(c.Window); err != nil {
			return err
		}
		if c.Since != "" {
			if _, err := time.Parse(time.DateOnly, c.Since); err != nil {
				return fmt.Errorf("since %q: want YYYY-MM-DD", c.Since)
			}
		}
	default:
		return fmt.Errorf("strategy %q: rotate or announce", c.Strategy)
	}
	for _, v := range []struct{ name, val string }{{"routingTagsSource", c.RoutingTagsSource}, {"copyrightSource", c.CopyrightSource}} {
		switch v.val {
		case "", "rss", "exif":
		default:
			return fmt.Errorf("%s %q: empty, rss or exif", v.name, v.val)
		}
	}
	return nil
}

// SetClock replaces the clock; for tests.
func (c *Connection) SetClock(now func() time.Time) { c.now = now }

func (c *Connection) name() string { return c.ctx.InstanceName() }

func (c *Connection) Jobs() []companion.Job {
	switch c.Strategy {
	case StrategyAnnounce:
		return []companion.Job{
			{Name: "announce", Schedule: "0 * * * * *", Run: func(ctx context.Context) error { return c.announceDue(ctx) }},
			{Name: "backfill", Timeout: 6 * time.Hour, Run: c.backfill},
		}
	default:
		return []companion.Job{
			{Name: "publish", Schedule: c.Cron, Timeout: 30 * time.Minute, Run: func(ctx context.Context) error {
				_, err := c.Run(ctx, "schedule")
				return err
			}},
			{Name: "backfill", Timeout: 6 * time.Hour, Run: c.backfill},
		}
	}
}

// HandleEvent: an announce connection learns about new posts of its source.
func (c *Connection) HandleEvent(ctx context.Context, ev companion.Event) error {
	if ev.Type != feed.EventItemDiscovered || c.Strategy != StrategyAnnounce {
		return nil
	}
	var d feed.ItemDiscovered
	if err := ev.Decode(&d); err != nil {
		return err
	}
	if d.Source != c.SourceID {
		return nil
	}
	return c.enqueue(d.Item, 0)
}

// ensureState records when the connection was set up: an announce
// connection never announces older posts.
func (c *Connection) ensureState() error {
	db := c.ctx.DB()
	var st State
	if db.Where("connection = ?", c.name()).Limit(1).Find(&st).RowsAffected > 0 {
		return nil
	}
	now := time.Now()
	return db.Create(&State{Connection: c.name(), Since: &now}).Error
}

func (c *Connection) state() State {
	var st State
	c.ctx.DB().Where("connection = ?", c.name()).Limit(1).Find(&st)
	return st
}

// since is the oldest post an announcement may share.
func (c *Connection) since() time.Time {
	if c.Since != "" {
		if t, err := time.ParseInLocation(time.DateOnly, c.Since, c.location); err == nil {
			return t
		}
	}
	if st := c.state(); st.Since != nil {
		return *st.Since
	}
	return time.Time{}
}

// pause stops scheduled runs after an account error and says so through
// the notify slot.
func (c *Connection) pause(ctx context.Context, reason string) {
	now := c.now()
	c.ctx.DB().Model(&State{}).Where("connection = ?", c.name()).Updates(map[string]any{"paused_at": now, "pause_reason": reason})
	c.ctx.Log().Printf("%s: paused: %s", c.name(), reason)
	if c.Notify != nil {
		msg := notify.Message{Title: "Autoposting paused", Body: fmt.Sprintf("%s → %s: %s", c.name(), c.TargetID, reason)}
		if err := c.Notify.Broadcast(ctx, msg); err != nil {
			c.ctx.Log().Printf("%s: notify: %v", c.name(), err)
		}
	}
}

func (c *Connection) resume() {
	c.ctx.DB().Model(&State{}).Where("connection = ?", c.name()).Updates(map[string]any{"paused_at": nil, "pause_reason": ""})
}

// notifyFailure tells the notify slot about a failed upload.
func (c *Connection) notifyFailure(ctx context.Context, item feed.Item, err error) {
	if c.Notify == nil {
		return
	}
	title := item.Title
	if title == "" {
		title = item.ID
	}
	msg := notify.Message{Title: "Upload failed", Body: fmt.Sprintf("%s on %s: %v", title, c.TargetID, err), URL: item.Link}
	if nerr := c.Notify.Broadcast(ctx, msg); nerr != nil {
		c.ctx.Log().Printf("%s: notify: %v", c.name(), nerr)
	}
}

// parseWindow reads "08:00-21:00"; empty means all day.
func parseWindow(w string) (from, to time.Duration, err error) {
	if strings.TrimSpace(w) == "" {
		return 0, 24 * time.Hour, nil
	}
	a, b, ok := strings.Cut(w, "-")
	if !ok {
		return 0, 0, fmt.Errorf("window %q: want HH:MM-HH:MM", w)
	}
	parse := func(s string) (time.Duration, error) {
		t, err := time.Parse("15:04", strings.TrimSpace(s))
		if err != nil {
			return 0, fmt.Errorf("window %q: want HH:MM-HH:MM", w)
		}
		return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
	}
	if from, err = parse(a); err != nil {
		return 0, 0, err
	}
	if to, err = parse(b); err != nil {
		return 0, 0, err
	}
	if to <= from {
		return 0, 0, fmt.Errorf("window %q: the end must be after the start", w)
	}
	return from, to, nil
}

// inWindow moves t into the posting window: unchanged inside it, to the
// window's start when earlier that day, to the next day's start when later.
func (c *Connection) inWindow(t time.Time) time.Time {
	from, to, err := parseWindow(c.Window)
	if err != nil {
		return t
	}
	local := t.In(c.location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, c.location)
	switch offset := local.Sub(day); {
	case offset < from:
		return day.Add(from)
	case offset >= to:
		return day.AddDate(0, 0, 1).Add(from)
	}
	return t
}

var errBusy = errors.New("a run of this connection is already going on")
