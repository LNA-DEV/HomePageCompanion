// Command probe is the service the Compose tests run in a container
// (docs/concepts/modular-architecture.md §6, "Service"): a small
// social.publisher/v1 whose admin API tries what a service may and may not
// do, so the tests can check the rights from the outside.
//
// With PROBE_MODE=web it is a plain web server instead, standing in for a
// host on the internet ("hello from <PROBE_NAME>"); with =lemmy a fake
// Lemmy, with =feed a photo feed (package fakes).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/notify"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/e2e/fakes"
	"github.com/LNA-DEV/HomePageCompanion/service"
	"github.com/gin-gonic/gin"
)

// Probe publishes nowhere: it remembers the posts.
type Probe struct {
	Notify notify.Notifier `companion:"slot=notify"`

	ctx    companion.Context
	posted []social.Post
}

func (p *Probe) Provision(ctx companion.Context) error {
	p.ctx = ctx
	probes[ctx.InstanceName()] = p
	return nil
}

func (p *Probe) Describe(context.Context) (social.Limits, error) {
	return social.Limits{Platform: "probe", MaxLength: 500}, nil
}
func (p *Probe) Preview(_ context.Context, post social.Post) (social.Rendered, error) {
	return social.Rendered{Text: post.Text}, nil
}
func (p *Probe) Publish(_ context.Context, post social.Post) (social.Result, error) {
	p.posted = append(p.posted, post)
	return social.Result{ID: fmt.Sprint(len(p.posted))}, nil
}
func (p *Probe) ResolveMention(context.Context, string) (social.Mention, error) {
	return social.Mention{}, social.ErrNotAProfile
}

var probes = map[string]*Probe{}

// result is what every probe route answers: the status reached, or the
// error on the way.
type result struct {
	Status int    `json:"status,omitempty"`
	Body   string `json:"body,omitempty"`
	Error  string `json:"error,omitempty"`
}

func fetch(c *http.Client, method, url string, header map[string]string) result {
	req, err := http.NewRequest(method, url, strings.NewReader("{}"))
	if err != nil {
		return result{Error: err.Error()}
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return result{Error: err.Error()}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return result{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
}

func token() string {
	b, _ := os.ReadFile(os.Getenv("COMPANION_TOKEN_FILE"))
	return strings.TrimSpace(string(b))
}

var info = companion.Info{
	ID:       "probe",
	Name:     "Probe",
	Version:  "1.0.0",
	New:      func() companion.Instance { return new(Probe) },
	Provides: []companion.Capability{social.PublisherV1},
	Slots:    []companion.Slot{{Name: "notify", Needs: notify.NotifyV1, Optional: true}},
	Routes: func(mc companion.ModuleContext, r *companion.Router) {
		// Through the egress proxy, as every client of the service goes.
		r.Admin.GET("/fetch", func(g *gin.Context) {
			g.JSON(http.StatusOK, fetch(mc.HTTP(), "GET", g.Query("url"), nil))
		})
		// Around it: no proxy, straight out of the container.
		r.Admin.GET("/direct", func(g *gin.Context) {
			c := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
			g.JSON(http.StatusOK, fetch(c, "GET", g.Query("url"), nil))
		})
		// The core's own routes, with the service's token.
		r.Admin.GET("/core", func(g *gin.Context) {
			c := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
			h := map[string]string{"Authorization": "Bearer " + token(), "Companion-Instance": "main"}
			if t := g.Query("target"); t != "" {
				h["Companion-Target"] = t
			}
			g.JSON(http.StatusOK, fetch(c, g.DefaultQuery("method", "POST"), g.Query("url"), h))
		})
		// Its own slot, as the SDK calls it.
		r.Admin.POST("/notify", func(g *gin.Context) {
			p := probes["main"]
			if p == nil || p.Notify == nil {
				g.JSON(http.StatusOK, result{Error: "notify is not connected"})
				return
			}
			if err := p.Notify.Broadcast(g.Request.Context(), notify.Message{Title: "probe", Body: "hello"}); err != nil {
				g.JSON(http.StatusOK, result{Error: err.Error()})
				return
			}
			g.JSON(http.StatusOK, result{Status: 200})
		})
		// Its volume is writable, the rest of its file system is not.
		r.Admin.POST("/write", func(g *gin.Context) {
			err := os.WriteFile(g.Query("path"), []byte("probe"), 0o644)
			if err != nil {
				g.JSON(http.StatusOK, result{Error: err.Error()})
				return
			}
			g.JSON(http.StatusOK, result{Status: 200})
		})
		r.Admin.GET("/posted", func(g *gin.Context) {
			if p := probes["main"]; p != nil {
				g.JSON(http.StatusOK, p.posted)
				return
			}
			g.JSON(http.StatusOK, []social.Post{})
		})
	},
}

func main() {
	switch os.Getenv("PROBE_MODE") {
	case "lemmy":
		// A fake Lemmy, naming itself by PROBE_BASE ("http://lemmy-fake:8080").
		http.ListenAndServe(":8080", fakes.NewLemmy(os.Getenv("PROBE_BASE")))
		return
	case "feed":
		http.ListenAndServe(":8080", fakes.Feed(os.Getenv("PROBE_BASE"), 3))
		return
	}
	if os.Getenv("PROBE_MODE") == "web" {
		name := os.Getenv("PROBE_NAME")
		http.ListenAndServe(":8080", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"hello": name, "path": r.URL.Path})
		}))
		return
	}
	service.Serve(info)
}
