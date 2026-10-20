package core

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// sessionCookie holds the admin session. It is HttpOnly and SameSite=Strict:
// scripts cannot read it, and other sites cannot send it.
const sessionCookie = "companion_session"

const sessionTTL = 30 * 24 * time.Hour

// sessionRow is a login; only the token's hash is stored.
type sessionRow struct {
	Hash      string `gorm:"primaryKey"`
	ExpiresAt time.Time
	CreatedAt time.Time
}

func (sessionRow) TableName() string { return "core_sessions" }

type sessions struct{ c *Core }

func newSessions(c *Core) *sessions { return &sessions{c: c} }

func hashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

func (s *sessions) create() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	if err := s.c.db.Create(&sessionRow{Hash: hashToken(tok), ExpiresAt: time.Now().Add(sessionTTL)}).Error; err != nil {
		return "", err
	}
	return tok, nil
}

func (s *sessions) valid(tok string) bool {
	if tok == "" || s.c.db == nil {
		return false
	}
	var row sessionRow
	if s.c.db.Where("hash = ?", hashToken(tok)).Limit(1).Find(&row).RowsAffected == 0 {
		return false
	}
	return time.Now().Before(row.ExpiresAt)
}

func (s *sessions) drop(tok string) {
	if tok != "" && s.c.db != nil {
		s.c.db.Where("hash = ?", hashToken(tok)).Delete(&sessionRow{})
	}
}

func (s *sessions) prune() {
	if s.c.db != nil {
		s.c.db.Where("expires_at < ?", time.Now()).Delete(&sessionRow{})
	}
}

// keyMatches compares the admin key in constant time. An empty key never
// matches: an installation without one has no admin access at all.
func (c *Core) keyMatches(given string) bool {
	want := c.cfg().Core.AdminKey
	if want == "" || given == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(want)) == 1
}

// isAdmin accepts the session cookie, or "Authorization: ApiKey <key>" for
// scripts and the Bruno collection.
func (c *Core) isAdmin(r *http.Request) bool {
	if k, ok := strings.CutPrefix(r.Header.Get("Authorization"), "ApiKey "); ok && c.keyMatches(k) {
		return true
	}
	if ck, err := r.Cookie(sessionCookie); err == nil && c.sessions.valid(ck.Value) {
		return true
	}
	return false
}

func (c *Core) requireAdmin() gin.HandlerFunc {
	return func(g *gin.Context) {
		if c.isAdmin(g.Request) {
			g.Next()
			return
		}
		g.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"status": http.StatusUnauthorized, "message": "Authentication failed"})
	}
}

func (c *Core) secureCookies(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || strings.HasPrefix(c.cfg().Core.PublicURL, "https://")
}

func (c *Core) login(g *gin.Context) {
	var body struct {
		Key string `json:"key"`
	}
	if err := g.ShouldBindJSON(&body); err != nil || !c.keyMatches(body.Key) {
		time.Sleep(500 * time.Millisecond) // slows down guessing
		g.JSON(http.StatusUnauthorized, gin.H{"error": "wrong key"})
		return
	}
	tok, err := c.sessions.create()
	if err != nil {
		g.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	http.SetCookie(g.Writer, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: c.secureCookies(g.Request),
	})
	g.JSON(http.StatusOK, gin.H{"authenticated": true})
}

func (c *Core) logout(g *gin.Context) {
	if ck, err := g.Request.Cookie(sessionCookie); err == nil {
		c.sessions.drop(ck.Value)
	}
	http.SetCookie(g.Writer, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: c.secureCookies(g.Request)})
	g.JSON(http.StatusOK, gin.H{"authenticated": false})
}

func (c *Core) session(g *gin.Context) {
	g.JSON(http.StatusOK, gin.H{"authenticated": c.isAdmin(g.Request)})
}
