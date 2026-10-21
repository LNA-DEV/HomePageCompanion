package core

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/gin-gonic/gin"
)

// Connecting accounts through OAuth (docs/concepts/modular-architecture.md
// §5.8, D15; companion/oauth.go). The callback cannot rely on the session
// cookie: it is SameSite=Strict, and the platform's redirect is a
// cross-site navigation. What authenticates it is the state, which only a
// logged-in operator can create, which is used once and lives 15 minutes.

const flowTTL = 15 * time.Minute

type oauthFlow struct {
	block, instance string // the instance's name
	client          companion.OAuthClient
	redirectURI     string
	verifier        string
	expires         time.Time
}

type oauthFlows struct {
	mu sync.Mutex
	m  map[string]oauthFlow
}

func (f *oauthFlows) put(state string, fl oauthFlow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]oauthFlow{}
	}
	now := time.Now()
	for k, v := range f.m {
		if now.After(v.expires) {
			delete(f.m, k)
		}
	}
	f.m[state] = fl
}

// take returns the flow of a state and forgets it: a state works once.
func (f *oauthFlows) take(state string) (oauthFlow, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.m[state]
	delete(f.m, state)
	if !ok || time.Now().After(fl.expires) {
		return oauthFlow{}, false
	}
	return fl, true
}

// connectsAccounts reports whether a building block's instances implement
// companion.OAuthConnector.
func connectsAccounts(info companion.Info) bool {
	if info.New == nil {
		return false
	}
	_, ok := info.New().(companion.OAuthConnector)
	return ok
}

// connector is the running instance block:name, if it connects accounts.
func (c *Core) connector(block, name string) (companion.OAuthConnector, bool) {
	c.mu.RLock()
	in, ok := c.instances[companion.InstanceID(block, name)]
	c.mu.RUnlock()
	if !ok || in.value == nil {
		return nil, false
	}
	conn, ok := in.value.(companion.OAuthConnector)
	return conn, ok
}

// redirectURI is the callback the platform sends the browser back to.
func (c *Core) redirectURI(block string) string {
	base := strings.TrimRight(c.cfg().Core.PublicURL, "/")
	if base == "" {
		return ""
	}
	return base + "/api/b/" + block + "/oauth/callback"
}

func (c *Core) oauthRoutes(r *gin.Engine, admin gin.HandlerFunc, block string) {
	base := "/api/b/" + block + "/oauth"
	r.POST(base+"/start", admin, func(g *gin.Context) { c.oauthStart(g, block) })
	r.POST(base+"/disconnect", admin, func(g *gin.Context) { c.oauthDisconnect(g, block) })
	r.GET(base+"/callback", func(g *gin.Context) { c.oauthCallback(g, block) })
}

func instanceParam(g *gin.Context) string {
	var body struct {
		Instance string `json:"instance"`
	}
	_ = g.ShouldBindJSON(&body)
	if body.Instance == "" {
		body.Instance = companion.DefaultInstance
	}
	return body.Instance
}

func (c *Core) oauthStart(g *gin.Context, block string) {
	name := instanceParam(g)
	conn, ok := c.connector(block, name)
	if !ok {
		g.JSON(http.StatusNotFound, gin.H{"error": "no running instance " + companion.InstanceID(block, name) + " that connects accounts"})
		return
	}
	redirect := c.redirectURI(block)
	if redirect == "" {
		g.JSON(http.StatusConflict, gin.H{"error": "set core.publicUrl in companion.yaml: the platform sends the browser back to <publicUrl>/api/b/" + block + "/oauth/callback"})
		return
	}
	ctx, cancel := context.WithTimeout(g.Request.Context(), 30*time.Second)
	defer cancel()
	client, err := conn.OAuthClient(ctx, redirect)
	if err != nil {
		g.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	state := companion.RandomToken(24)
	var verifier, challenge string
	if client.PKCE {
		verifier, challenge = companion.NewPKCE()
	}
	c.flows.put(state, oauthFlow{block: block, instance: name, client: client, redirectURI: redirect, verifier: verifier, expires: time.Now().Add(flowTTL)})
	g.JSON(http.StatusOK, gin.H{"url": client.AuthCodeURL(redirect, state, challenge), "redirectUri": redirect})
}

func (c *Core) oauthDisconnect(g *gin.Context, block string) {
	name := instanceParam(g)
	conn, ok := c.connector(block, name)
	if !ok {
		g.JSON(http.StatusNotFound, gin.H{"error": "no running instance " + companion.InstanceID(block, name) + " that connects accounts"})
		return
	}
	if err := conn.OAuthDisconnect(g.Request.Context()); err != nil {
		g.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.log.Printf("oauth: %s disconnected", companion.InstanceID(block, name))
	g.JSON(http.StatusOK, gin.H{"status": "disconnected"})
}

func (c *Core) oauthCallback(g *gin.Context, block string) {
	fl, ok := c.flows.take(g.Query("state"))
	if !ok || fl.block != block {
		callbackPage(g, "/settings", "", "This sign-in link is unknown or older than 15 minutes. Start the connection again on the settings page.")
		return
	}
	id := companion.InstanceID(block, fl.instance)
	back := "/settings/" + url.PathEscape(id)
	if e := g.Query("error"); e != "" {
		msg := e
		if d := g.Query("error_description"); d != "" {
			msg += ": " + d
		}
		c.log.Printf("oauth: %s refused: %s", id, msg)
		callbackPage(g, back, "", "The platform did not connect the account: "+msg)
		return
	}
	code := g.Query("code")
	conn, ok := c.connector(block, fl.instance)
	if code == "" || !ok {
		callbackPage(g, back, "", "No code in the answer, or the instance is gone.")
		return
	}
	ctx, cancel := context.WithTimeout(g.Request.Context(), time.Minute)
	defer cancel()
	tok, err := fl.client.Exchange(ctx, c.httpClient, code, fl.redirectURI, fl.verifier)
	if err != nil {
		c.log.Printf("oauth: %s: exchanging the code: %v", id, err)
		callbackPage(g, back, "", "Exchanging the code failed: "+err.Error())
		return
	}
	account, err := conn.OAuthConnect(ctx, fl.client, tok)
	if err != nil {
		c.log.Printf("oauth: %s: %v", id, err)
		callbackPage(g, back, "", "The account could not be connected: "+err.Error())
		return
	}
	c.log.Printf("oauth: %s connected as %s", id, account)
	callbackPage(g, back, account, "")
}

// callbackPage leads back to the shell. A redirect would not do: the
// navigation started on the platform's site, so the browser would leave
// the SameSite=Strict session cookie out and show the login. A page of the
// companion's own that moves on is a same-site navigation.
func callbackPage(g *gin.Context, back, account, failure string) {
	q := url.Values{}
	if failure != "" {
		q.Set("oauth", "error")
		q.Set("message", failure)
	} else {
		q.Set("oauth", "connected")
		q.Set("account", account)
	}
	target := back + "?" + q.Encode()
	g.Header("Cache-Control", "no-store")
	g.Header("Referrer-Policy", "no-referrer")
	g.Header("Content-Type", "text/html; charset=utf-8")
	status := http.StatusOK
	if failure != "" {
		status = http.StatusBadRequest
	}
	g.Status(status)
	_ = callbackTmpl.Execute(g.Writer, map[string]string{"Target": target, "Account": account, "Failure": failure})
}

var callbackTmpl = template.Must(template.New("cb").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<meta http-equiv="refresh" content="{{if .Failure}}8{{else}}0{{end}};url={{.Target}}">
<title>{{if .Failure}}Not connected{{else}}Account connected{{end}}</title>
<style>body{font:16px system-ui,sans-serif;max-width:36rem;margin:4rem auto;padding:0 1rem;color:#1f2937}a{color:#2563eb}</style>
</head><body>
{{if .Failure}}<h1>Not connected</h1><p>{{.Failure}}</p>{{else}}<h1>Account connected</h1><p>Connected as {{.Account}}.</p>{{end}}
<p><a href="{{.Target}}">Back to the settings</a></p>
</body></html>
`))
