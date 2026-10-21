// Package webpush is the webpush module: notify/v1 over Web Push. The
// default instance holds the website's subscribers (the old /api/webpush/
// URLs); further instances are audiences of their own, e.g. "me" for the
// operator's own devices, which other building blocks' notify slots name.
// The VAPID private key lives in the secret store.
package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/notify"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	wp "github.com/SherClockHolmes/webpush-go"
	"github.com/gin-gonic/gin"
)

func init() {
	companion.Register(companion.Info{
		ID:       "webpush",
		Name:     "Web Push",
		New:      func() companion.Instance { return new(Audience) },
		Provides: []companion.Capability{notify.NotifyV1},
		Migrate:  migrate,
		Routes:   routes,
		Aliases:  []string{"/api/webpush"},
		Import:   importLegacy,
		UI: &companion.UI{
			Nav: []companion.NavEntry{
				{Label: "Subscribers", Icon: "bell", Page: "", Group: "Website", Order: 41},
				{Label: "Broadcast", Icon: "megaphone", Page: "broadcast", Group: "Website", Order: 42},
			},
			Pages: []companion.Page{
				{Path: "", Element: "webpush-subscribers", Title: "Subscribers"},
				{Path: "broadcast", Element: "webpush-broadcast", Title: "Broadcast"},
			},
		},
	})
}

// Subscription is one browser's push subscription.
type Subscription struct {
	ID             uint   `gorm:"primaryKey"`
	Instance       string `gorm:"uniqueIndex:idx_push_endpoint;index"`
	Endpoint       string `gorm:"uniqueIndex:idx_push_endpoint"`
	ExpirationTime *int64
	Auth           string
	P256dh         string
	CreatedAt      time.Time
}

func (Subscription) TableName() string { return "webpush_subscriptions" }

// keyRow holds the public VAPID key; the private one is a secret.
type keyRow struct {
	ID        uint `gorm:"primaryKey"`
	PublicKey string
	CreatedAt time.Time
}

func (keyRow) TableName() string { return "webpush_keys" }

func migrate(mc companion.ModuleContext) error {
	if mc.DB() == nil {
		return errors.New("webpush: no database")
	}
	return mc.DB().AutoMigrate(&Subscription{}, &keyRow{})
}

// Audience is one set of subscribers.
type Audience struct {
	// Subscriber is the contact sent to push services ("mailto:…").
	Subscriber string `json:"subscriber"`
	ctx        companion.Context
	// send is replaceable in tests.
	send func(payload []byte, s *wp.Subscription, o *wp.Options) (*http.Response, error)
}

func (a *Audience) Provision(ctx companion.Context) error {
	a.ctx = ctx
	a.send = func(p []byte, s *wp.Subscription, o *wp.Options) (*http.Response, error) {
		return wp.SendNotification(p, s, o)
	}
	if err := migrate(ctx); err != nil {
		return err
	}
	_, _, err := keys(ctx)
	return err
}

// SetSender replaces the push call; for tests.
func (a *Audience) SetSender(f func([]byte, *wp.Subscription, *wp.Options) (*http.Response, error)) {
	a.send = f
}

var keyMu sync.Mutex

// keys returns the VAPID key pair, generating it on first use.
func keys(mc companion.ModuleContext) (public, private string, err error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	var row keyRow
	if mc.DB().Order("id").Limit(1).Find(&row).RowsAffected > 0 {
		priv, ok, err := mc.Secrets().Get("vapid-private-key")
		if err != nil {
			return "", "", fmt.Errorf("webpush: VAPID private key: %w", err)
		}
		if ok {
			return row.PublicKey, priv, nil
		}
	}
	priv, pub, err := wp.GenerateVAPIDKeys()
	if err != nil {
		return "", "", fmt.Errorf("webpush: generating VAPID keys: %w", err)
	}
	if err := mc.Secrets().Put("vapid-private-key", priv); err != nil {
		return "", "", err
	}
	mc.DB().Where("1 = 1").Delete(&keyRow{})
	if err := mc.DB().Create(&keyRow{PublicKey: pub}).Error; err != nil {
		return "", "", err
	}
	mc.Log().Println("Generated and saved new VAPID keys.")
	return pub, priv, nil
}

// Message is the payload the service worker shows.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"`
	Icon  string `json:"icon,omitempty"`
}

func (a *Audience) push(ctx context.Context, s Subscription, m notify.Message) error {
	pub, priv, err := keys(a.ctx)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(Message{Title: m.Title, Body: m.Body, URL: m.URL, Icon: m.Icon})
	resp, err := a.send(payload, &wp.Subscription{Endpoint: s.Endpoint, Keys: wp.Keys{Auth: s.Auth, P256dh: s.P256dh}},
		&wp.Options{Subscriber: a.Subscriber, VAPIDPrivateKey: priv, VAPIDPublicKey: pub, TTL: 30})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// A subscription the push service no longer knows is gone for good.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		a.ctx.DB().Delete(&Subscription{}, s.ID)
		return fmt.Errorf("subscription %d expired, removed", s.ID)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("push service answered %d", resp.StatusCode)
	}
	return nil
}

// Send delivers to one subscription (its id).
func (a *Audience) Send(ctx context.Context, to string, m notify.Message) error {
	id, err := strconv.Atoi(to)
	if err != nil {
		return fmt.Errorf("webpush: subscription id %q", to)
	}
	var s Subscription
	if a.ctx.DB().Where("id = ? AND instance = ?", id, a.ctx.InstanceName()).Limit(1).Find(&s).RowsAffected == 0 {
		return fmt.Errorf("webpush: no subscription %s", to)
	}
	return a.push(ctx, s, m)
}

// Broadcast delivers to every subscriber of this audience.
func (a *Audience) Broadcast(ctx context.Context, m notify.Message) error {
	_, _, err := a.broadcast(ctx, m)
	return err
}

// broadcast sends to every subscriber and counts who got it.
func (a *Audience) broadcast(ctx context.Context, m notify.Message) (sent, failed int, err error) {
	var subs []Subscription
	if err := a.ctx.DB().Where("instance = ?", a.ctx.InstanceName()).Find(&subs).Error; err != nil {
		return 0, 0, err
	}
	a.ctx.Log().Printf("Sending notifications to %d users", len(subs))
	for _, s := range subs {
		if err := a.push(ctx, s, m); err != nil {
			a.ctx.Log().Printf("Failed to send to user %d: %v", s.ID, err)
			failed++
			continue
		}
		sent++
	}
	a.ctx.Metrics().Add("notifications_sent_total", float64(sent), "audience", a.ctx.InstanceName())
	return sent, failed, nil
}

var _ notify.Notifier = (*Audience)(nil)

func audience(mc companion.ModuleContext, name string) *Audience {
	if name == "" {
		name = companion.DefaultInstance
	}
	for _, in := range mc.Instances() {
		if in.Name == name {
			return in.Instance.(*Audience)
		}
	}
	return nil
}

type subscribeRequest struct {
	Endpoint       string `json:"endpoint" binding:"required"`
	ExpirationTime *int64 `json:"expirationTime"`
	Keys           struct {
		P256dh string `json:"p256dh" binding:"required"`
		Auth   string `json:"auth" binding:"required"`
	} `json:"keys" binding:"required"`
}

func subscribe(mc companion.ModuleContext, instance string) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := instance
		if name == "" {
			name = c.Param("instance")
		}
		if audience(mc, name) == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "no such audience"})
			return
		}
		var req subscribeRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s := Subscription{Instance: name, Endpoint: req.Endpoint, ExpirationTime: req.ExpirationTime, Auth: req.Keys.Auth, P256dh: req.Keys.P256dh}
		if err := mc.DB().Where("instance = ? AND endpoint = ?", name, s.Endpoint).FirstOrCreate(&s).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save subscription"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "subscription saved"})
	}
}

// broadcast answers the old API as it did; the admin UI (counts) learns
// how many devices the message reached.
func broadcast(mc companion.ModuleContext, c *gin.Context, name string, counts bool) {
	var m Message
	if err := c.ShouldBindJSON(&m); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON"})
		return
	}
	if m.Title == "" || m.Body == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Title and Body are required"})
		return
	}
	a := audience(mc, name)
	if a == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no such audience"})
		return
	}
	sent, failed, err := a.broadcast(c.Request.Context(), notify.Message{Title: m.Title, Body: m.Body, URL: m.URL, Icon: m.Icon})
	if !counts {
		c.JSON(http.StatusOK, gin.H{"status": "Broadcast sent"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sent": sent, "failed": failed})
}

func routes(mc companion.ModuleContext, r *companion.Router) {
	pub := r.Alias("/api/webpush")
	pub.GET("/vapidkey", func(c *gin.Context) {
		key, _, err := keys(mc)
		if err != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.Data(http.StatusOK, "application/text", []byte(key))
	})
	pub.POST("/subscribe", subscribe(mc, companion.DefaultInstance))
	pub.POST("/broadcast", r.RequireAdmin, func(c *gin.Context) { broadcast(mc, c, companion.DefaultInstance, false) })

	a := r.Admin
	a.GET("/subscribers", func(c *gin.Context) {
		type safe struct {
			ID        uint   `json:"id"`
			Instance  string `json:"instance"`
			Endpoint  string `json:"endpoint"`
			CreatedAt string `json:"createdAt"`
		}
		var subs []Subscription
		q := mc.DB().Order("created_at DESC")
		if in := c.Query("instance"); in != "" {
			q = q.Where("instance = ?", in)
		}
		q.Find(&subs)
		out := []safe{}
		for _, s := range subs {
			out = append(out, safe{ID: s.ID, Instance: s.Instance, Endpoint: s.Endpoint, CreatedAt: s.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")})
		}
		c.JSON(http.StatusOK, out)
	})
	a.DELETE("/subscribers/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		res := mc.DB().Delete(&Subscription{}, id)
		if res.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "Subscriber not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Subscriber deleted"})
	})
	// Subscribing a device of the operator to another audience.
	a.POST("/audiences/:instance/subscribe", subscribe(mc, ""))
	a.POST("/audiences/:instance/broadcast", func(c *gin.Context) { broadcast(mc, c, c.Param("instance"), true) })
	a.GET("/audiences", func(c *gin.Context) {
		out := []gin.H{}
		for _, in := range mc.Instances() {
			var n int64
			mc.DB().Model(&Subscription{}).Where("instance = ?", in.Name).Count(&n)
			out = append(out, gin.H{"name": in.Name, "subscribers": n})
		}
		c.JSON(http.StatusOK, out)
	})
}

// importLegacy moves the subscribers and the VAPID keys over; the private
// key leaves the plain-text table for the secret store.
func importLegacy(mc companion.ModuleContext, l *companion.Legacy) error {
	if l.HasTable("vapid_keys") {
		var k struct{ PublicKey, PrivateKey string }
		if l.DB.Raw("SELECT public_key, private_key FROM vapid_keys ORDER BY id LIMIT 1").Scan(&k); k.PublicKey != "" && !l.Check {
			if err := mc.Secrets().Put("vapid-private-key", k.PrivateKey); err != nil {
				return err
			}
			mc.DB().Where("1 = 1").Delete(&keyRow{})
			mc.DB().Create(&keyRow{PublicKey: k.PublicKey})
		}
		l.Reportf("webpush: VAPID keys moved, the private one into the secret store")
	}
	if l.HasTable("notification_subscriptions") {
		var rows []struct {
			Endpoint       string
			ExpirationTime *int64
			Auth, P256dh   string
			CreatedAt      time.Time
		}
		l.DB.Raw("SELECT endpoint, expiration_time, auth, p256dh, created_at FROM notification_subscriptions WHERE deleted_at IS NULL").Scan(&rows)
		if !l.Check {
			for _, r := range rows {
				mc.DB().Create(&Subscription{Instance: companion.DefaultInstance, Endpoint: r.Endpoint, ExpirationTime: r.ExpirationTime, Auth: r.Auth, P256dh: r.P256dh, CreatedAt: r.CreatedAt})
			}
		}
		l.Reportf("webpush: %d subscriber(s)", len(rows))
	}
	return nil
}
