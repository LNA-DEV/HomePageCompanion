package core

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/core/internal/logger"
	"github.com/LNA-DEV/HomePageCompanion/internal/blocks"
	"github.com/gin-gonic/gin"
)

// coreAPI mounts /api/core: login, the UI manifest, instances, jobs,
// events, logs and backups.
func (c *Core) coreAPI(g *gin.RouterGroup) {
	g.POST("/login", c.login)
	g.POST("/logout", c.logout)
	g.GET("/session", c.session)

	a := g.Group("", c.requireAdmin())
	a.GET("/ui", c.uiManifest)
	a.GET("/overview", c.overview)
	a.GET("/instances", c.listInstances)
	a.GET("/instances/:id", c.getInstance)
	a.PUT("/instances/:id/config", c.putInstanceConfig)
	a.GET("/jobs", func(g *gin.Context) { g.JSON(http.StatusOK, c.jobs.list()) })
	a.POST("/jobs/run", c.runJob)
	a.GET("/jobs/runs/:id", c.getJobRun)
	a.GET("/events", c.eventOverview)
	a.POST("/events/retry", func(g *gin.Context) {
		g.JSON(http.StatusOK, gin.H{"requeued": c.events.retryFailed()})
	})
	a.POST("/services/:id/ui-token", c.issueUIToken)
	a.GET("/logs", c.getLogs)
	a.POST("/client-logs", c.ingestClientLogs)
	a.GET("/metrics/history", func(g *gin.Context) { g.JSON(http.StatusOK, c.metrics.historyCopy()) })
	a.POST("/reload", func(g *gin.Context) {
		if err := c.Reload(g.Request.Context()); err != nil {
			g.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
			return
		}
		g.JSON(http.StatusOK, gin.H{"status": "reloaded"})
	})
	a.GET("/backups", c.listBackups)
	a.POST("/backups", c.createBackup)
	a.GET("/backups/:name", c.downloadBackup)
}

// UIModule is one building block in the UI manifest.
type UIModule struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Service   bool     `json:"service,omitempty"`
	HasUI     bool     `json:"hasUi"`
	Entry     string   `json:"entry,omitempty"` // URL of entry.js, or the iframe page of a service
	Instances []string `json:"instances"`
	*companion.UI
}

func (c *Core) uiManifest(g *gin.Context) {
	c.mu.RLock()
	var out []UIModule
	for id, b := range c.blocks {
		m := UIModule{ID: id, Name: b.info.DisplayName(), UI: b.info.UI}
		for _, in := range c.instances {
			if in.block == id {
				m.Instances = append(m.Instances, in.name)
			}
		}
		sort.Strings(m.Instances)
		if c.services.isService(id) {
			// Its pages and widgets come from the service in an iframe,
			// /ui/s/<token>/index.html.
			m.Service = true
			m.HasUI = b.info.UI != nil && (len(b.info.UI.Pages) > 0 || len(b.info.UI.Widgets) > 0)
		} else if companion.UIFiles(b.info) != nil {
			m.HasUI = true
			m.Entry = "/ui/b/" + id + "/entry.js"
		}
		out = append(out, m)
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	g.JSON(http.StatusOK, gin.H{"modules": out, "version": c.version})
}

// InstanceView is an instance for the UI: status, masked config, schema.
type InstanceView struct {
	ID        string              `json:"id"`
	Block     string              `json:"block"`
	Name      string              `json:"name"`
	Service   bool                `json:"service,omitempty"`
	Running   bool                `json:"running"`
	Error     string              `json:"error,omitempty"`
	StartedAt time.Time           `json:"startedAt,omitempty"`
	Provides  []string            `json:"provides"`
	Slots     map[string][]string `json:"slots"`
	Config    map[string]any      `json:"config,omitempty"`
	Schema    []Field             `json:"schema,omitempty"`
	// OAuth is set for an instance that connects its account through
	// OAuth (companion.OAuthConnector).
	OAuth *companion.OAuthState `json:"oauth,omitempty"`
}

func (c *Core) viewOf(in *instance, full bool) InstanceView {
	v := InstanceView{ID: in.id, Block: in.block, Name: in.name, Service: in.service != nil, Running: in.running,
		Error: in.err, StartedAt: in.startedAt, Slots: in.slots}
	for _, p := range in.info.Provides {
		v.Provides = append(v.Provides, p.String())
	}
	if in.service != nil {
		v.Provides = in.service.provideNames()
		// Running means the service answers and took its configuration.
		if ready, problem := in.service.b.state(in.name); in.running && !ready {
			v.Running, v.Error = false, problem
		}
	}
	if full {
		var secrets map[string]bool
		if in.info.New != nil {
			secrets = blocks.SecretFields(reflectTypeOf(in.info.New()))
		}
		v.Config = masked(in.values, secrets)
		v.Schema = schemaOf(in.info)
	}
	return v
}

func (c *Core) listInstances(g *gin.Context) {
	c.mu.RLock()
	out := make([]InstanceView, 0, len(c.instances))
	for _, id := range c.order {
		if in, ok := c.instances[id]; ok {
			out = append(out, c.viewOf(in, false))
		}
	}
	c.mu.RUnlock()
	g.JSON(http.StatusOK, out)
}

func (c *Core) getInstance(g *gin.Context) {
	c.mu.RLock()
	in, ok := c.instances[g.Param("id")]
	var v InstanceView
	var value companion.Instance
	if ok {
		v = c.viewOf(in, true)
		value = in.value
	}
	c.mu.RUnlock()
	if !ok {
		g.JSON(http.StatusNotFound, gin.H{"error": "no such instance"})
		return
	}
	// Outside the lock: the state comes from the secret store.
	if conn, ok := value.(companion.OAuthConnector); ok {
		st := conn.OAuthState()
		st.RedirectURI = c.redirectURI(v.Block)
		v.OAuth = &st
	}
	g.JSON(http.StatusOK, v)
}

// Overview is the dashboard's core card.
type Overview struct {
	Version     string           `json:"version"`
	Uptime      string           `json:"uptime"`
	StartedAt   time.Time        `json:"startedAt"`
	Instances   int              `json:"instances"`
	Failed      []InstanceView   `json:"failed"`
	Events      map[string]int64 `json:"events"`
	JobsFailing []JobInfo        `json:"jobsFailing"`
	GoVersion   string           `json:"goVersion"`
}

func (c *Core) overview(g *gin.Context) {
	o := Overview{Version: c.version, StartedAt: c.started, Uptime: time.Since(c.started).Round(time.Second).String(),
		Events: c.events.stats(), GoVersion: runtime.Version(), Failed: []InstanceView{}, JobsFailing: []JobInfo{}}
	c.mu.RLock()
	o.Instances = len(c.instances)
	for _, in := range c.instances {
		if !in.running {
			o.Failed = append(o.Failed, c.viewOf(in, false))
		}
	}
	c.mu.RUnlock()
	for _, j := range c.jobs.list() {
		if j.LastRun != nil && j.LastRun.FinishedAt != nil && !j.LastRun.OK {
			o.JobsFailing = append(o.JobsFailing, j)
		}
	}
	g.JSON(http.StatusOK, o)
}

func (c *Core) runJob(g *gin.Context) {
	var body struct {
		Key  string `json:"key"`
		Wait bool   `json:"wait"`
	}
	if err := g.ShouldBindJSON(&body); err != nil || body.Key == "" {
		g.JSON(http.StatusBadRequest, gin.H{"error": "key required"})
		return
	}
	r, err := c.jobs.run(body.Key, "manual", body.Wait)
	switch {
	case errors.Is(err, ErrJobRunning):
		g.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case err != nil:
		g.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	default:
		g.JSON(http.StatusAccepted, r)
	}
}

func (c *Core) getJobRun(g *gin.Context) {
	id, _ := strconv.Atoi(g.Param("id"))
	var r jobRun
	if c.db == nil || c.db.Where("id = ?", id).Limit(1).Find(&r).RowsAffected == 0 {
		g.JSON(http.StatusNotFound, gin.H{"error": "no such run"})
		return
	}
	g.JSON(http.StatusOK, r)
}

func (c *Core) eventOverview(g *gin.Context) {
	type failed struct {
		EventID    string    `json:"eventId"`
		Type       string    `json:"type"`
		Subscriber string    `json:"subscriber"`
		Attempts   int       `json:"attempts"`
		LastError  string    `json:"lastError"`
		UpdatedAt  time.Time `json:"updatedAt"`
	}
	var rows []failed
	if c.db != nil {
		c.db.Table("core_deliveries d").Select("d.event_id, e.type, d.subscriber, d.attempts, d.last_error, d.updated_at").
			Joins("JOIN core_events e ON e.id = d.event_id").Where("d.status = ?", "failed").
			Order("d.updated_at desc").Limit(100).Scan(&rows)
	}
	if rows == nil {
		rows = []failed{}
	}
	g.JSON(http.StatusOK, gin.H{"counts": c.events.stats(), "failed": rows})
}

// getLogs returns the tail of a daily log file; ?module= filters the app
// log by building block.
func (c *Core) getLogs(g *gin.Context) {
	files, err := logger.ListFiles()
	if err != nil {
		g.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list log files"})
		return
	}
	if files == nil {
		files = []string{}
	}
	file := g.Query("file")
	if file == "" && len(files) > 0 {
		file = files[0]
	}
	if file == "" {
		g.JSON(http.StatusOK, gin.H{"files": files, "file": "", "lines": []string{}})
		return
	}
	tail, _ := strconv.Atoi(g.DefaultQuery("tail", "200"))
	if tail < 1 {
		tail = 200
	}
	if tail > 1000 {
		tail = 1000
	}
	search, module := g.Query("search"), g.Query("module")
	first := search
	if module != "" {
		first = "module=" + module + " "
	}
	lines, err := logger.TailLines(file, tail, first)
	if err != nil {
		g.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if module != "" && search != "" {
		kept := lines[:0]
		for _, l := range lines {
			if strings.Contains(strings.ToLower(l), strings.ToLower(search)) {
				kept = append(kept, l)
			}
		}
		lines = kept
	}
	if lines == nil {
		lines = []string{}
	}
	g.JSON(http.StatusOK, gin.H{"files": files, "file": file, "lines": lines})
}

// collectGauges refreshes the gauges /metrics reports.
func (c *Core) collectGauges() {
	for status, n := range c.events.stats() {
		c.metrics.set("companion_event_deliveries", float64(n), "status", status)
	}
	c.mu.RLock()
	for _, in := range c.instances {
		v := 0.0
		if in.running {
			v = 1
		}
		c.metrics.set("companion_instance_up", v, "instance", in.id)
	}
	c.mu.RUnlock()
	c.services.gauges()
}

// startCoreJobs registers the core's own housekeeping.
func (c *Core) startCoreJobs() {
	c.jobs.add("core", companion.Job{Name: "temp-cleanup", Schedule: "0 15 4 * * *", Run: c.temp.cleanup})
	c.jobs.add("core", companion.Job{Name: "events-prune", Schedule: "0 25 4 * * *", Run: func(context.Context) error { return c.events.prune() }})
	c.jobs.add("core", companion.Job{Name: "sessions-prune", Schedule: "0 35 4 * * *", Run: func(context.Context) error { c.sessions.prune(); return nil }})
	c.jobs.add("core", companion.Job{Name: "metrics-sample", Schedule: "0 * * * * *", Run: func(context.Context) error { c.metrics.sample(time.Now()); return nil }})
	c.jobs.add("core", companion.Job{Name: "cache-limit", Schedule: "0 50 4 * * *", Run: c.enforceCacheLimit})
	// A changed companion.yaml is picked up within a minute.
	c.jobs.add("core", companion.Job{Name: "config-reload", Schedule: "30 * * * * *", Run: c.Reload})
	if s := c.cfg().Core.Backups.Schedule; s != "" {
		c.jobs.add("core", companion.Job{Name: "backup", Schedule: s, Timeout: 6 * time.Hour, Run: c.scheduledBackup})
	}
}
