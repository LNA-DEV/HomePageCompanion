package core

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/core/internal/logger"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// privateNetworks are trusted as proxies when TRUSTED_PROXIES is unset: the
// compose network and the host gateway the reverse proxy comes from. A client
// on the internet never has such an address, so it cannot pick its own IP.
var privateNetworks = []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "::1/128", "fc00::/7"}

// TrustedProxies reads TRUSTED_PROXIES: comma-separated IPs or CIDRs whose
// X-Forwarded-For is believed, "none" for no proxy, unset for the private
// networks.
func TrustedProxies(env string) ([]string, error) {
	env = strings.TrimSpace(env)
	switch env {
	case "":
		return privateNetworks, nil
	case "none":
		return nil, nil
	}
	var out []string
	for _, p := range strings.Split(env, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(p); err != nil && net.ParseIP(p) == nil {
			return nil, fmt.Errorf("%q is neither an IP nor a CIDR", p)
		}
		out = append(out, p)
	}
	return out, nil
}

var localhostRegex = regexp.MustCompile(`^https?://localhost(:[0-9]+)?$`)

// originAllowed admits the configured domain and its subdomains, localhost,
// and the exact extra origins (the onion site). Extra origins are compared
// exactly, never as patterns.
func originAllowed(origin string, domain *regexp.Regexp, extra []string) bool {
	return (domain != nil && domain.MatchString(origin)) || localhostRegex.MatchString(origin) || slices.Contains(extra, origin)
}

func domainRegex(domain string) *regexp.Regexp {
	if domain == "" {
		return nil
	}
	return regexp.MustCompile(`^https?://([a-z0-9-]+\.)*` + regexp.QuoteMeta(domain) + `(:[0-9]+)?$`)
}

// coreRoutes are the gateway's own prefixes; an alias may not shadow them.
var coreRoutes = []string{"/api/core", "/api/b", "/health", "/metrics", "/ui", legacyClientLogs}

// legacyClientLogs is the pre-module companion's client log ingest, still
// answered for an old admin UI that may live on in a browser cache.
const legacyClientLogs = "/api/admin/client-logs"

// rebuildGateway builds the HTTP routes for the current building blocks and
// swaps them in.
func (c *Core) rebuildGateway() error {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.LoggerWithWriter(logger.Access(), "/health"), c.recovery())

	proxies, err := TrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		return fmt.Errorf("TRUSTED_PROXIES: %w", err)
	}
	if err := r.SetTrustedProxies(proxies); err != nil {
		return fmt.Errorf("TRUSTED_PROXIES: %w", err)
	}

	r.Use(cors.New(cors.Config{
		AllowOriginFunc: func(origin string) bool {
			f := c.cfg()
			return originAllowed(origin, domainRegex(f.Core.Domain), f.Core.ExtraOrigins)
		},
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Authorization", "Content-Type"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))
	r.Use(c.measure())

	r.GET("/health", func(g *gin.Context) {
		g.Data(http.StatusOK, "application/json", []byte(`{"msg":"this worked"}`))
	})
	r.GET("/metrics", c.requireAdmin(), func(g *gin.Context) {
		g.Header("Content-Type", "text/plain; version=0.0.4")
		c.collectGauges()
		c.metrics.write(g.Writer)
	})
	r.GET(tempPath+":name", c.temp.serve)
	c.coreAPI(r.Group("/api/core"))
	r.POST(legacyClientLogs, c.requireAdminOrToken(), c.ingestClientLogs)

	c.mu.RLock()
	var blocks []*block
	for _, b := range c.blocks {
		blocks = append(blocks, b)
	}
	c.mu.RUnlock()
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].info.ID < blocks[j].info.ID })

	// Aliases: today's public URLs, mapped to their building blocks.
	owner := map[string]string{}
	for _, b := range blocks {
		for _, a := range b.info.Aliases {
			a = "/" + strings.Trim(a, "/")
			for _, cr := range coreRoutes {
				if overlaps(a, cr) {
					return fmt.Errorf("%s: alias %s collides with the core's %s", b.info.ID, a, cr)
				}
			}
			for other, o := range owner {
				if overlaps(a, other) {
					return fmt.Errorf("aliases collide: %s (%s) and %s (%s)", a, b.info.ID, other, o)
				}
			}
			owner[a] = b.info.ID
		}
	}

	admin := c.requireAdmin()
	// The OAuth routes first: a module that defines them itself fails at
	// its Routes below, with its name in the error.
	for _, b := range blocks {
		if connectsAccounts(b.info) {
			c.oauthRoutes(r, admin, b.info.ID)
		}
	}
	for _, b := range blocks {
		if b.info.Routes == nil {
			continue
		}
		aliases := map[string]*gin.RouterGroup{}
		for _, a := range b.info.Aliases {
			aliases[a] = r.Group("/" + strings.Trim(a, "/"))
		}
		base := "/api/b/" + b.info.ID
		router := companion.NewRouter(r.Group(base, admin), r.Group(base+"/public"), admin, aliases)
		if err := safely(func() error { b.info.Routes(b.mc, router); return nil }); err != nil {
			return fmt.Errorf("%s: routes: %w", b.info.ID, err)
		}
	}
	c.services.routes(r, admin)

	r.GET("/ui/b/:id/*file", c.serveModuleUI)
	r.NoRoute(c.serveShell)

	var h http.Handler = r
	c.handler.Store(&h)
	return nil
}

// overlaps reports whether one path prefix contains the other at a segment
// boundary.
func overlaps(a, b string) bool {
	if a == b {
		return true
	}
	return strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func (c *Core) recovery() gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(logger.AccessError(), func(g *gin.Context, err any) {
		c.log.Printf("http: panic serving %s: %v", g.Request.URL.Path, err)
		g.AbortWithStatus(http.StatusInternalServerError)
	})
}

// measure counts requests per building block and status class.
func (c *Core) measure() gin.HandlerFunc {
	return func(g *gin.Context) {
		start := time.Now()
		g.Next()
		block := "core"
		p := g.Request.URL.Path
		if rest, ok := strings.CutPrefix(p, "/api/b/"); ok {
			block, _, _ = strings.Cut(rest, "/")
		} else if !strings.HasPrefix(p, "/api/core") && strings.HasPrefix(p, "/api/") {
			block = c.aliasOwner(p)
		} else if !strings.HasPrefix(p, "/api/") {
			block = "shell"
		}
		code := strconv.Itoa(g.Writer.Status())
		c.metrics.add("companion_http_requests_total", 1, "block", block, "code", code[:1]+"xx")
		c.metrics.observe("companion_http_request_seconds", time.Since(start).Seconds(), "block", block)
	}
}

func (c *Core) aliasOwner(p string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for id, b := range c.blocks {
		for _, a := range b.info.Aliases {
			a = "/" + strings.Trim(a, "/")
			if p == a || strings.HasPrefix(p, a+"/") {
				return id
			}
		}
	}
	return "unknown"
}

// serveModuleUI serves /ui/b/<id>/<file> from the building block's UI
// files; for a service, from its container (services.go).
func (c *Core) serveModuleUI(g *gin.Context) {
	id := g.Param("id")
	file := strings.TrimPrefix(path.Clean(g.Param("file")), "/")
	c.mu.RLock()
	b := c.blocks[id]
	c.mu.RUnlock()
	if b == nil {
		g.Status(http.StatusNotFound)
		return
	}
	if c.services.isService(id) {
		c.services.serveUI(g, id, file)
		return
	}
	files := companion.UIFiles(b.info)
	if files == nil {
		g.Status(http.StatusNotFound)
		return
	}
	if strings.HasPrefix(file, "public/") {
		g.Header("Access-Control-Allow-Origin", "*")
	} else if !c.isAdmin(g.Request) {
		g.Status(http.StatusUnauthorized)
		return
	}
	data, err := fs.ReadFile(files, file)
	if err != nil {
		g.Status(http.StatusNotFound)
		return
	}
	g.Header("Cache-Control", "no-cache")
	g.Data(http.StatusOK, contentType(file, data), data)
}

// serveShell serves the admin shell with the SPA fallback; /_app/ files are
// immutable, as nginx served them.
func (c *Core) serveShell(g *gin.Context) {
	p := g.Request.URL.Path
	if strings.HasPrefix(p, "/api/") {
		g.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if g.Request.Method != http.MethodGet && g.Request.Method != http.MethodHead {
		g.Status(http.StatusNotFound)
		return
	}
	shell := c.opts.Shell
	if shell == nil {
		g.Data(http.StatusOK, "text/html; charset=utf-8", []byte(placeholderShell))
		return
	}
	name := strings.TrimPrefix(path.Clean(p), "/")
	if name == "" {
		name = "index.html"
	}
	data, err := fs.ReadFile(shell, name)
	if err != nil {
		if d, err2 := fs.ReadFile(shell, name+".html"); err2 == nil {
			data, name = d, name+".html"
		} else if d, err2 := fs.ReadFile(shell, path.Join(name, "index.html")); err2 == nil {
			data, name = d, path.Join(name, "index.html")
		} else {
			data, err = fs.ReadFile(shell, "index.html")
			if err != nil {
				g.Status(http.StatusNotFound)
				return
			}
			name = "index.html"
		}
	}
	if strings.HasPrefix(name, "_app/") {
		g.Header("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		g.Header("Cache-Control", "no-cache")
	}
	g.Data(http.StatusOK, contentType(name, data), data)
}

func contentType(name string, data []byte) string {
	switch path.Ext(name) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".json", ".webmanifest":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".wasm":
		return "application/wasm"
	}
	return http.DetectContentType(data)
}

const placeholderShell = `<!doctype html><meta charset="utf-8"><title>Companion</title>
<body style="font-family:system-ui;margin:3rem">
<h1>Companion</h1><p>The admin UI is not part of this build. Build the companion with <code>hpc build</code> to include it.</p>
<p><a href="/health">/health</a></p></body>`
