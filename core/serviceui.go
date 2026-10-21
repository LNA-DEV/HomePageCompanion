package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
	"github.com/gin-gonic/gin"
)

// A service's admin UI (docs/concepts/modular-architecture.md §5.7) runs in
// an <iframe sandbox="allow-scripts allow-forms"> of the shell: an opaque
// origin with no access to the shell's cookies, storage or DOM. The shell
// asks for a short-lived token valid only for that service, loads the UI
// from /ui/s/<token>/… and calls the service's API (/api/b/<service>/…)
// with the token on the UI's behalf, over postMessage.

// uiTokenTTL is how long a UI token lasts; the shell asks for a new one
// before.
const uiTokenTTL = time.Hour

// uiCSP sandboxes a service's UI even when it is opened outside the shell's
// iframe, as a page of its own: otherwise its scripts would run with the
// companion's origin and the admin's session.
const uiCSP = "sandbox allow-scripts allow-forms; frame-ancestors 'self'"

func (m *serviceManager) uiSign(id string, exp int64) string {
	h := hmac.New(sha256.New, m.c.temp.signingKey())
	fmt.Fprintf(h, "ui-token|%s|%d", id, exp)
	return hex.EncodeToString(h.Sum(nil))
}

// uiToken is "<service>.<expiry>.<signature>", the service base64url
// encoded.
func (m *serviceManager) uiToken(id string, now time.Time) (string, time.Time) {
	exp := now.Add(uiTokenTTL)
	return base64.RawURLEncoding.EncodeToString([]byte(id)) + "." + strconv.FormatInt(exp.Unix(), 10) + "." + m.uiSign(id, exp.Unix()), exp
}

// checkUIToken returns the service a token is valid for.
func (m *serviceManager) checkUIToken(tok string) (string, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	exp, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || err2 != nil || time.Now().Unix() > exp {
		return "", false
	}
	id := string(raw)
	if !hmac.Equal([]byte(parts[2]), []byte(m.uiSign(id, exp))) {
		return "", false
	}
	return id, true
}

// uiTokenFor reads a UI token for the service from the request: the
// Authorization header (the shell's bridge) or ?uiToken= (an <img> in the
// UI). The query parameter is taken out before the request goes on.
func (m *serviceManager) uiTokenFor(g *gin.Context, id string) bool {
	tok, ok := strings.CutPrefix(g.GetHeader("Authorization"), "Bearer ")
	if !ok {
		q := g.Request.URL.Query()
		tok = q.Get("uiToken")
		if tok == "" {
			return false
		}
		q.Del("uiToken")
		g.Request.URL.RawQuery = q.Encode()
	}
	got, valid := m.checkUIToken(strings.TrimSpace(tok))
	return valid && got == id
}

// issueUIToken is POST /api/core/services/:id/ui-token, for the admin.
func (c *Core) issueUIToken(g *gin.Context) {
	id := g.Param("id")
	if !c.services.isService(id) {
		g.JSON(http.StatusNotFound, gin.H{"error": "no service " + id})
		return
	}
	tok, exp := c.services.uiToken(id, time.Now())
	g.JSON(http.StatusOK, gin.H{"token": tok, "expiresAt": exp, "base": "/ui/s/" + tok + "/"})
}

// serveServiceUI is GET /ui/s/<token>/<file>: the service's /ui/<file>.
func (c *Core) serveServiceUI(g *gin.Context) {
	id, ok := c.services.checkUIToken(g.Param("token"))
	if !ok {
		g.String(http.StatusUnauthorized, "this page's token has expired: reload the page")
		return
	}
	b := c.services.get(id)
	if b == nil || !c.services.isService(id) {
		g.Status(http.StatusNotFound)
		return
	}
	file := strings.TrimPrefix(path.Clean("/"+g.Param("file")), "/")
	if file == "" || file == "." {
		file = "index.html"
	}
	u := b.baseURL() + "/ui/" + file
	if q := g.Request.URL.RawQuery; q != "" {
		u += "?" + q
	}
	req, err := http.NewRequestWithContext(g.Request.Context(), http.MethodGet, u, nil)
	if err != nil {
		g.Status(http.StatusBadRequest)
		return
	}
	for _, h := range []string{"Accept", "Accept-Language", "If-None-Match", "If-Modified-Since"} {
		if v := g.GetHeader(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	b.signer("")(req, nil)
	resp, err := c.services.client.Do(req)
	if err != nil {
		g.String(http.StatusBadGateway, "service %s: %v", id, err)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Content-Length", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			g.Header(h, v)
		}
	}
	g.Header("Content-Security-Policy", uiCSP)
	g.Header("Referrer-Policy", "no-referrer")
	g.Header("X-Content-Type-Options", "nosniff")
	g.Header("Cache-Control", "no-cache")
	g.Status(resp.StatusCode)
	io.Copy(g.Writer, io.LimitReader(resp.Body, wire.MaxBody))
}

// serveUISDK is GET /ui/sdk/<file>: @companion/ui as one module for
// service UIs, from the shell's build. A sandboxed frame has an opaque
// origin, so a module script needs CORS.
func (c *Core) serveUISDK(g *gin.Context) {
	file := strings.TrimPrefix(path.Clean("/"+g.Param("file")), "/")
	if c.opts.Shell == nil || file == "" {
		g.Status(http.StatusNotFound)
		return
	}
	data, err := fs.ReadFile(c.opts.Shell, "ui-sdk/"+file)
	if err != nil {
		g.Status(http.StatusNotFound)
		return
	}
	g.Header("Access-Control-Allow-Origin", "*")
	g.Header("Cache-Control", "no-cache")
	g.Data(http.StatusOK, contentType(file, data), data)
}
