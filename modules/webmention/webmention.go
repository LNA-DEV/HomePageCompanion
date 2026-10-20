// Package webmention is the webmention module: the receiving endpoint
// POST /api/webmention, the list in the admin UI, and an optional notify
// slot told about every new mention. Verification and sending are
// docs/concepts/webmention.md, not built yet.
package webmention

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/notify"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/gin-gonic/gin"
)

func init() {
	companion.Register(companion.Info{
		ID:      "webmention",
		Name:    "Webmentions",
		New:     func() companion.Instance { return new(Receiver) },
		Slots:   []companion.Slot{{Name: "notify", Needs: notify.NotifyV1, Optional: true}},
		Start:   migrate,
		Routes:  routes,
		Aliases: []string{"/api/webmention"},
		Import:  importLegacy,
		UI: &companion.UI{
			Nav:   []companion.NavEntry{{Label: "Webmentions", Icon: "at-sign", Page: "", Group: "Website", Order: 40}},
			Pages: []companion.Page{{Path: "", Element: "webmention-page", Title: "Webmentions"}},
		},
	})
}

// Webmention is a received mention.
type Webmention struct {
	ID        uint      `gorm:"primaryKey" json:"ID"`
	Source    string    `gorm:"not null" json:"Source"`
	Target    string    `gorm:"not null" json:"Target"`
	CreatedAt time.Time `json:"CreatedAt"`
}

func (Webmention) TableName() string { return "webmentions" }

func migrate(mc companion.ModuleContext) error {
	if mc.DB() == nil {
		return errors.New("webmention: no database")
	}
	return mc.DB().AutoMigrate(&Webmention{})
}

// Receiver is the module's one instance.
type Receiver struct {
	Notify notify.Notifier `companion:"slot=notify"`
	ctx    companion.Context
}

func (r *Receiver) Provision(ctx companion.Context) error { r.ctx = ctx; return migrate(ctx) }

// isValidURL admits absolute http and https URLs only (webmention.md §3.1).
func isValidURL(str string) bool {
	u, err := url.ParseRequestURI(str)
	if err != nil || u.Host == "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme == "http" || scheme == "https"
}

func receiver(mc companion.ModuleContext) *Receiver {
	for _, in := range mc.Instances() {
		return in.Instance.(*Receiver)
	}
	return nil
}

func routes(mc companion.ModuleContext, r *companion.Router) {
	r.Alias("/api/webmention").POST("", func(c *gin.Context) {
		source, target := c.PostForm("source"), c.PostForm("target")
		if source == "" || target == "" || !isValidURL(source) || !isValidURL(target) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid or missing 'source' and/or 'target'"})
			return
		}
		m := Webmention{Source: source, Target: target, CreatedAt: time.Now()}
		if err := mc.DB().Create(&m).Error; err != nil {
			mc.Log().Println("Error saving mention:", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to store webmention"})
			return
		}
		mc.Log().Printf("Stored webmention: source=%s target=%s", source, target)
		if rc := receiver(mc); rc != nil && rc.Notify != nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				_ = rc.Notify.Broadcast(ctx, notify.Message{Title: "New webmention", Body: source + " → " + target, URL: source})
			}()
		}
		c.Status(http.StatusAccepted)
	})
	r.Admin.GET("/mentions", func(c *gin.Context) {
		var rows []Webmention
		q := mc.DB().Order("created_at DESC")
		if term := strings.TrimSpace(c.Query("q")); term != "" {
			q = q.Where("source LIKE ? OR target LIKE ?", "%"+term+"%", "%"+term+"%")
		}
		q.Find(&rows)
		if rows == nil {
			rows = []Webmention{}
		}
		c.JSON(http.StatusOK, rows)
	})
	r.Admin.DELETE("/mentions/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		if res := mc.DB().Delete(&Webmention{}, id); res.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "deleted"})
	})
}

func importLegacy(mc companion.ModuleContext, l *companion.Legacy) error {
	if !l.HasTable("webmentions") {
		return nil
	}
	var rows []Webmention
	l.DB.Raw("SELECT id, source, target, created_at FROM webmentions").Scan(&rows)
	if !l.Check {
		for _, r := range rows {
			mc.DB().Create(&r)
		}
	}
	l.Reportf("webmention: %d mention(s)", len(rows))
	return nil
}
