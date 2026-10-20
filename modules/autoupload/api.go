package autoupload

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/gin-gonic/gin"
	"github.com/robfig/cron"
	"gorm.io/gorm"
)

// Cell states of the Posts table.
const (
	CellPublished = "published"
	CellFailed    = "failed"
	CellNext      = "next"
	CellSkipped   = "skipped"
	CellOpen      = "open"
	CellNone      = "none" // no connection from the post's source to this target
)

// Column is a target in the Posts table.
type Column struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
}

// Cell is one post on one target.
type Cell struct {
	State  string     `json:"state"`
	Likes  int        `json:"likes,omitempty"`
	URL    string     `json:"url,omitempty"`
	Reason string     `json:"reason,omitempty"`
	Time   *time.Time `json:"time,omitempty"`
	Cause  string     `json:"cause,omitempty"`
}

// Row is one post with all its cells.
type Row struct {
	Post
	Cells map[string]Cell `json:"cells"`
	Likes int             `json:"likes"` // platform likes; website likes separately
}

type connRef struct {
	name string
	c    *Connection
}

func connections(mc companion.ModuleContext) []connRef {
	var out []connRef
	for _, in := range mc.Instances() {
		out = append(out, connRef{in.Name, in.Instance.(*Connection)})
	}
	return out
}

func connection(mc companion.ModuleContext, name string) *Connection {
	for _, in := range mc.Instances() {
		if in.Name == name {
			return in.Instance.(*Connection)
		}
	}
	return nil
}

// columns are the targets of all connections, then any other target with
// publications (a microblog target), in a stable order.
func columns(mc companion.ModuleContext) []Column {
	seen := map[string]bool{}
	var out []Column
	for _, cr := range connections(mc) {
		if !seen[cr.c.TargetID] {
			seen[cr.c.TargetID] = true
			lim, _ := cr.c.Target.Describe(context.Background())
			out = append(out, Column{ID: cr.c.TargetID, Platform: lim.Platform})
		}
	}
	var others []struct{ Target, Platform string }
	mc.DB().Model(&Publication{}).Select("DISTINCT target, platform").Scan(&others)
	for _, o := range others {
		if !seen[o.Target] {
			seen[o.Target] = true
			out = append(out, Column{ID: o.Target, Platform: o.Platform})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// targetsOf maps a source to the targets it is connected to.
func targetsOf(mc companion.ModuleContext) map[string]map[string]*Connection {
	out := map[string]map[string]*Connection{}
	for _, cr := range connections(mc) {
		if out[cr.c.SourceID] == nil {
			out[cr.c.SourceID] = map[string]*Connection{}
		}
		out[cr.c.SourceID][cr.c.TargetID] = cr.c
	}
	return out
}

func paging(c *gin.Context, def int) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", strconv.Itoa(def)))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = def
	}
	return page, limit
}

const openFailure = "success = 0 AND resolved_at IS NULL"

// itemsQuery applies the Posts page's filters on the server.
func itemsQuery(mc companion.ModuleContext, c *gin.Context) *gorm.DB {
	db := mc.DB()
	q := db.Model(&Post{})
	switch c.Query("filter") {
	case "photos":
		q = q.Where("kind = ?", feed.KindPhoto)
	case "blog":
		q = q.Where("kind = ?", feed.KindArticle)
	case "microblog":
		q = q.Where("kind = ?", "note")
	case "problems":
		q = q.Where("EXISTS (SELECT 1 FROM autoupload_attempts a WHERE a.item_id = autoupload_posts.item_id AND a." + strings.ReplaceAll(openFailure, "AND ", "AND a.") + ")")
	case "gone":
		q = q.Where("in_feed = ?", false)
	case "incomplete":
		var clauses []string
		var args []any
		for source, targets := range targetsOf(mc) {
			for target := range targets {
				clauses = append(clauses, "(source = ? AND NOT EXISTS (SELECT 1 FROM autoupload_publications p WHERE p.item_id = autoupload_posts.item_id AND p.target = ?))")
				args = append(args, source, target)
			}
		}
		if len(clauses) == 0 {
			q = q.Where("1 = 0")
		} else {
			q = q.Where("in_feed = ?", true).Where(strings.Join(clauses, " OR "), args...)
		}
	}
	if t := c.Query("target"); t != "" {
		var sources []string
		for source, targets := range targetsOf(mc) {
			if targets[t] != nil {
				sources = append(sources, source)
			}
		}
		q = q.Where("source IN ? OR EXISTS (SELECT 1 FROM autoupload_publications p WHERE p.item_id = autoupload_posts.item_id AND p.target = ?)", sources, t)
	}
	if tag := strings.TrimSpace(c.Query("tag")); tag != "" {
		q = q.Where("LOWER(categories) LIKE ?", "%\""+strings.ToLower(tag)+"\"%")
	}
	if term := strings.TrimSpace(c.Query("q")); term != "" {
		like := "%" + strings.ToLower(term) + "%"
		q = q.Where("LOWER(title) LIKE ? OR LOWER(summary) LIKE ? OR LOWER(item_id) LIKE ? OR LOWER(categories) LIKE ?", like, like, like, like)
	}
	return q
}

// cells computes every cell of the given posts.
func cells(ctx context.Context, mc companion.ModuleContext, posts []Post, cols []Column) []Row {
	ids := make([]string, len(posts))
	for i, p := range posts {
		ids[i] = p.ItemID
	}
	var pubs []Publication
	var fails []Attempt
	if len(ids) > 0 {
		mc.DB().Where("item_id IN ?", ids).Find(&pubs)
		mc.DB().Where("item_id IN ? AND "+openFailure, ids).Order("created_at desc").Find(&fails)
	}
	pubOf := map[string]Publication{}
	for _, p := range pubs {
		pubOf[p.ItemID+"|"+p.Target] = p
	}
	failOf := map[string]Attempt{}
	for _, f := range fails {
		if _, ok := failOf[f.ItemID+"|"+f.Target]; !ok {
			failOf[f.ItemID+"|"+f.Target] = f
		}
	}
	// The post each rotation chooses next, and when.
	nextOf := map[string]time.Time{}
	for _, cr := range connections(mc) {
		if it, err := cr.c.next(ctx); err == nil && it != nil {
			if t, ok := cr.c.nextRun(); ok {
				nextOf[it.ID+"|"+cr.c.TargetID] = t
			}
		}
	}
	links := targetsOf(mc)
	rows := make([]Row, len(posts))
	for i, p := range posts {
		r := Row{Post: p, Cells: map[string]Cell{}}
		for _, col := range cols {
			key := p.ItemID + "|" + col.ID
			cell := Cell{State: CellNone}
			if links[p.Source][col.ID] != nil {
				cell.State = CellOpen
			}
			if pub, ok := pubOf[key]; ok {
				t := pub.PublishedAt
				switch pub.Status {
				case StatusSkipped:
					cell = Cell{State: CellSkipped, Reason: pub.Reason, Time: &t}
				default:
					cell = Cell{State: CellPublished, Likes: pub.Likes, URL: pub.PostURL, Time: &t}
					r.Likes += pub.Likes
				}
			} else if f, ok := failOf[key]; ok {
				t := f.CreatedAt
				cell = Cell{State: CellFailed, Reason: shortReason(f), Cause: f.Cause, Time: &t}
			} else if t, ok := nextOf[key]; ok {
				cell = Cell{State: CellNext, Time: &t}
			}
			r.Cells[col.ID] = cell
		}
		rows[i] = r
	}
	return rows
}

// shortReason is the failure as a cell shows it: "503", "token", "limit".
func shortReason(a Attempt) string {
	switch a.Code {
	case "auth":
		return "token"
	case "rate_limited":
		return "limit"
	case "network":
		return "network"
	case "content":
		return "content"
	}
	if a.HTTPStatus != 0 {
		return strconv.Itoa(a.HTTPStatus)
	}
	return a.Cause
}

// nextRun is the connection's next scheduled run.
func (c *Connection) nextRun() (time.Time, bool) {
	if c.Strategy == StrategyAnnounce {
		if a, _ := c.nextAnnouncement(); a != nil {
			return c.inWindow(a.DueAt), true
		}
		return time.Time{}, false
	}
	if c.Cron == "" {
		return time.Time{}, false
	}
	s, err := cron.Parse(c.Cron)
	if err != nil {
		return time.Time{}, false
	}
	return s.Next(c.now()), true
}

func routes(mc companion.ModuleContext, r *companion.Router) {
	a := r.Admin

	a.GET("/items", func(c *gin.Context) {
		page, limit := paging(c, 50)
		q := itemsQuery(mc, c)
		var total int64
		q.Count(&total)
		switch c.Query("sort") {
		case "likes":
			q = q.Order("(SELECT COALESCE(SUM(likes), 0) FROM autoupload_publications p WHERE p.item_id = autoupload_posts.item_id) DESC, published DESC")
		case "posted":
			q = q.Order("(SELECT MAX(published_at) FROM autoupload_publications p WHERE p.item_id = autoupload_posts.item_id AND p.status = 'published') DESC, published DESC")
		default:
			q = q.Order("published DESC")
		}
		var posts []Post
		q.Offset((page - 1) * limit).Limit(limit).Find(&posts)
		cols := columns(mc)
		c.JSON(http.StatusOK, gin.H{"items": cells(c.Request.Context(), mc, posts, cols), "columns": cols, "total": total, "page": page, "limit": limit, "counts": counts(mc)})
	})

	a.GET("/items/:id", func(c *gin.Context) {
		var p Post
		if mc.DB().Where("item_id = ?", c.Param("id")).Limit(1).Find(&p).RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "no such post"})
			return
		}
		cols := columns(mc)
		var attempts []Attempt
		mc.DB().Where("item_id = ?", p.ItemID).Order("created_at desc").Find(&attempts)
		var pubs []Publication
		mc.DB().Where("item_id = ?", p.ItemID).Find(&pubs)
		var days []LikeDay
		mc.DB().Where("item_id = ?", p.ItemID).Order("day").Find(&days)
		c.JSON(http.StatusOK, gin.H{"post": cells(c.Request.Context(), mc, []Post{p}, cols)[0], "columns": cols,
			"publications": pubs, "attempts": attempts, "likeDays": days})
	})

	// The post as the target will show it, without sending it.
	a.GET("/items/:id/preview", func(c *gin.Context) {
		conn, item, err := connectionFor(c.Request.Context(), mc, c.Param("id"), c.Query("target"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		r, err := conn.preview(c.Request.Context(), item)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, r)
	})

	a.POST("/items/:id/targets/:target/:action", func(c *gin.Context) {
		id, target := c.Param("id"), c.Param("target")
		var body struct {
			URL     string `json:"url"`
			Confirm bool   `json:"confirm"`
		}
		_ = c.ShouldBindJSON(&body)
		db := mc.DB()
		switch c.Param("action") {
		case "publish-now":
			conn, _, err := connectionFor(c.Request.Context(), mc, id, target)
			if err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			run, err := conn.publishNow(c.Request.Context(), id)
			if errors.Is(err, errBusy) {
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
				return
			}
			if err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, run)
		case "skip":
			pub := Publication{ItemID: id, Target: target, Status: StatusSkipped, Reason: "skipped by hand", Source: "autoupload", PublishedAt: time.Now()}
			db.Where("item_id = ? AND target = ?", id, target).Assign(pub).FirstOrCreate(&pub)
			db.Model(&Attempt{}).Where("item_id = ? AND target = ? AND "+openFailure, id, target).Update("resolved_at", time.Now())
			c.JSON(http.StatusOK, pub)
		case "mark-published":
			pub := Publication{ItemID: id, Target: target, Status: StatusManual, PostURL: body.URL, Source: "autoupload", PublishedAt: time.Now()}
			db.Where("item_id = ? AND target = ?", id, target).Assign(pub).FirstOrCreate(&pub)
			db.Model(&Attempt{}).Where("item_id = ? AND target = ? AND "+openFailure, id, target).Update("resolved_at", time.Now())
			c.JSON(http.StatusOK, pub)
		case "allow-repost":
			// Releasing a publication means the post goes out again on the
			// next run that chooses it; the UI says so, and asks.
			if !body.Confirm {
				c.JSON(http.StatusPreconditionRequired, gin.H{"error": "confirm: the post will be published on this target again"})
				return
			}
			db.Where("item_id = ? AND target = ?", id, target).Delete(&Publication{})
			_ = mc.Emit(c.Request.Context(), social.EventPostRemoved, social.PostRemoved{ItemID: id, Source: "autoupload", Target: target, Time: time.Now()})
			c.JSON(http.StatusOK, gin.H{"status": "released"})
		default:
			c.JSON(http.StatusNotFound, gin.H{"error": "unknown action"})
		}
	})

	a.GET("/schedule", func(c *gin.Context) {
		out := []gin.H{}
		for _, cr := range connections(mc) {
			out = append(out, scheduleOf(c.Request.Context(), mc, cr))
		}
		c.JSON(http.StatusOK, out)
	})

	run := func(c *gin.Context, name string, legacy bool) {
		conn := connection(mc, name)
		if conn == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "unknown connection: " + name})
			return
		}
		if conn.Strategy == StrategyAnnounce {
			go func() {
				defer recoverLog(mc, "announce "+name)
				_ = conn.announceDue(context.Background())
			}()
			c.JSON(http.StatusAccepted, gin.H{"status": "announcements scheduled", "connection": name})
			return
		}
		pre := &Run{Connection: name, Trigger: "manual", StartedAt: time.Now(), Result: "running", Target: conn.TargetID}
		mc.DB().Create(pre)
		go func() {
			defer recoverLog(mc, "run "+name)
			_, _ = conn.runInto(context.Background(), pre)
		}()
		if legacy {
			c.JSON(http.StatusAccepted, gin.H{"status": "publish scheduled", "connection": name})
			return
		}
		c.JSON(http.StatusAccepted, pre)
	}
	a.POST("/connections/:name/run", func(c *gin.Context) { run(c, c.Param("name"), false) })
	a.POST("/connections/:name/resume", func(c *gin.Context) {
		conn := connection(mc, c.Param("name"))
		if conn == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "unknown connection"})
			return
		}
		conn.resume()
		c.JSON(http.StatusOK, gin.H{"status": "resumed"})
	})
	a.POST("/connections/:name/backfill", func(c *gin.Context) {
		conn := connection(mc, c.Param("name"))
		if conn == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "unknown connection"})
			return
		}
		go func() {
			defer recoverLog(mc, "backfill")
			_ = conn.backfill(context.Background())
		}()
		c.JSON(http.StatusAccepted, gin.H{"status": "backfill started"})
	})
	a.GET("/runs/:id", func(c *gin.Context) {
		var run Run
		if mc.DB().Where("id = ?", c.Param("id")).Limit(1).Find(&run).RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "no such run"})
			return
		}
		c.JSON(http.StatusOK, run)
	})
	a.GET("/runs", func(c *gin.Context) {
		var runs []Run
		q := mc.DB().Order("id desc").Limit(50)
		if n := c.Query("connection"); n != "" {
			q = q.Where("connection = ?", n)
		}
		q.Find(&runs)
		c.JSON(http.StatusOK, runs)
	})

	a.GET("/problems", func(c *gin.Context) { c.JSON(http.StatusOK, problems(mc)) })
	a.POST("/problems/retry", func(c *gin.Context) {
		var body struct {
			Cause string `json:"cause"`
		}
		_ = c.ShouldBindJSON(&body)
		var open []Attempt
		q := mc.DB().Where(openFailure)
		if body.Cause != "" {
			q = q.Where("cause = ?", body.Cause)
		}
		q.Find(&open)
		seen := map[string]bool{}
		var todo [][2]string
		for _, f := range open {
			if k := f.ItemID + "|" + f.Target; !seen[k] {
				seen[k] = true
				todo = append(todo, [2]string{f.ItemID, f.Target})
			}
		}
		go func() {
			defer recoverLog(mc, "retry")
			for _, t := range todo {
				if conn, _, err := connectionFor(context.Background(), mc, t[0], t[1]); err == nil {
					_, _ = conn.publishNow(context.Background(), t[0])
				}
			}
		}()
		c.JSON(http.StatusAccepted, gin.H{"retrying": len(todo)})
	})

	// The log: every attempt, filtered on the server.
	a.GET("/attempts", func(c *gin.Context) {
		page, limit := paging(c, 50)
		q := mc.DB().Model(&Attempt{})
		switch c.Query("status") {
		case "success":
			q = q.Where("success = ?", true)
		case "failed":
			q = q.Where("success = ?", false)
		case "open":
			q = q.Where(openFailure)
		}
		for _, f := range []string{"target", "cause", "source", "connection", "platform"} {
			if v := c.Query(f); v != "" {
				q = q.Where(f+" = ?", v)
			}
		}
		if term := strings.TrimSpace(c.Query("q")); term != "" {
			like := "%" + strings.ToLower(term) + "%"
			q = q.Where("LOWER(item_id) LIKE ? OR LOWER(error) LIKE ? OR item_id IN (SELECT item_id FROM autoupload_posts WHERE LOWER(title) LIKE ?)", like, like, like)
		}
		var total int64
		q.Count(&total)
		var rows []Attempt
		q.Order("created_at desc").Offset((page - 1) * limit).Limit(limit).Find(&rows)
		titles := map[string]Post{}
		var ids []string
		for _, r := range rows {
			ids = append(ids, r.ItemID)
		}
		if len(ids) > 0 {
			var posts []Post
			mc.DB().Where("item_id IN ?", ids).Find(&posts)
			for _, p := range posts {
				titles[p.ItemID] = p
			}
		}
		type view struct {
			Attempt
			Title    string `json:"title"`
			ImageURL string `json:"imageUrl,omitempty"`
		}
		out := make([]view, len(rows))
		for i, r := range rows {
			out[i] = view{Attempt: r, Title: titles[r.ItemID].Title, ImageURL: titles[r.ItemID].ImageURL}
		}
		c.JSON(http.StatusOK, gin.H{"items": out, "total": total, "page": page, "limit": limit})
	})

	a.GET("/dashboard", func(c *gin.Context) { c.JSON(http.StatusOK, dashboard(mc)) })

	// Legacy URLs: the trigger the Bruno collection and scripts use.
	r.Alias("/api/upload").POST("/:connectionName", r.RequireAdmin, func(c *gin.Context) { run(c, c.Param("connectionName"), true) })
	r.Alias("/api/backfill").POST("", r.RequireAdmin, func(c *gin.Context) {
		go func() {
			defer recoverLog(mc, "backfill")
			for _, cr := range connections(mc) {
				_ = cr.c.backfill(context.Background())
			}
		}()
		c.JSON(http.StatusOK, gin.H{"status": "Backfill started"})
	})
}

func recoverLog(mc companion.ModuleContext, what string) {
	if r := recover(); r != nil {
		mc.Log().Printf("%s: panic: %v\n%s", what, r, debug.Stack())
	}
}

// connectionFor finds the connection that posts an item to a target.
func connectionFor(ctx context.Context, mc companion.ModuleContext, itemID, target string) (*Connection, feed.Item, error) {
	var p Post
	mc.DB().Where("item_id = ?", itemID).Limit(1).Find(&p)
	for _, cr := range connections(mc) {
		if cr.c.TargetID != target || (p.Source != "" && cr.c.SourceID != p.Source) {
			continue
		}
		it, err := cr.c.Source.GetItem(ctx, itemID)
		if err != nil {
			continue
		}
		return cr.c, it, nil
	}
	return nil, feed.Item{}, errors.New("no connection posts this item to " + target)
}

func counts(mc companion.ModuleContext) gin.H {
	db := mc.DB()
	var all, photos, blog, notes, gone, problems int64
	db.Model(&Post{}).Count(&all)
	db.Model(&Post{}).Where("kind = ?", feed.KindPhoto).Count(&photos)
	db.Model(&Post{}).Where("kind = ?", feed.KindArticle).Count(&blog)
	db.Model(&Post{}).Where("kind = ?", "note").Count(&notes)
	db.Model(&Post{}).Where("in_feed = ?", false).Count(&gone)
	db.Model(&Attempt{}).Where(openFailure).Distinct("item_id").Count(&problems)
	return gin.H{"all": all, "photos": photos, "blog": blog, "microblog": notes, "gone": gone, "problems": problems}
}

func scheduleOf(ctx context.Context, mc companion.ModuleContext, cr connRef) gin.H {
	c := cr.c
	st := c.state()
	row := gin.H{"connection": cr.name, "source": c.SourceID, "target": c.TargetID, "strategy": c.Strategy, "cron": c.Cron,
		"paused": st.PausedAt != nil, "pauseReason": st.PauseReason}
	if t, ok := c.nextRun(); ok {
		row["nextRun"] = t
	}
	if it, err := c.next(ctx); err == nil && it != nil {
		next := gin.H{"id": it.ID, "title": it.Title, "kind": it.Kind, "published": it.Published}
		if it.Image != nil {
			next["imageUrl"] = it.Image.URL
		}
		if r, err := c.preview(ctx, *it); err == nil {
			next["preview"] = r
		}
		row["next"] = next
	} else if err != nil {
		row["error"] = err.Error()
	}
	if c.Strategy == StrategyAnnounce {
		_, n := c.nextAnnouncement()
		row["waiting"] = n
		row["window"], row["delay"] = c.Window, c.Delay
	}
	var last Run
	if mc.DB().Where("connection = ? AND result <> ?", cr.name, "running").Order("id desc").Limit(1).Find(&last).RowsAffected > 0 {
		row["lastRun"] = last
	}
	return row
}

// Problem is an open failure: no later success for the same post and
// target.
type Problem struct {
	ItemID    string     `json:"itemId"`
	Title     string     `json:"title"`
	ImageURL  string     `json:"imageUrl,omitempty"`
	Target    string     `json:"target"`
	Cause     string     `json:"cause"`
	Error     string     `json:"error"`
	Attempts  int        `json:"attempts"`
	LastAt    time.Time  `json:"lastAt"`
	NextRetry *time.Time `json:"nextRetry,omitempty"`
}

func problems(mc companion.ModuleContext) gin.H {
	var open []Attempt
	mc.DB().Where(openFailure).Order("created_at desc").Find(&open)
	byKey := map[string]*Problem{}
	var order []string
	for _, f := range open {
		k := f.ItemID + "|" + f.Target
		p, ok := byKey[k]
		if !ok {
			p = &Problem{ItemID: f.ItemID, Target: f.Target, Cause: f.Cause, Error: f.Error, LastAt: f.CreatedAt}
			byKey[k] = p
			order = append(order, k)
		}
		p.Attempts++
		if f.Cause == social.CauseAccount || f.Cause == social.CauseContent {
			p.Cause = f.Cause
		}
	}
	var ids []string
	for _, k := range order {
		ids = append(ids, byKey[k].ItemID)
	}
	posts := map[string]Post{}
	if len(ids) > 0 {
		var rows []Post
		mc.DB().Where("item_id IN ?", ids).Find(&rows)
		for _, p := range rows {
			posts[p.ItemID] = p
		}
	}
	groups := map[string][]Problem{social.CauseAccount: {}, social.CauseContent: {}, social.CauseTransient: {}}
	for _, k := range order {
		p := byKey[k]
		p.Title, p.ImageURL = posts[p.ItemID].Title, posts[p.ItemID].ImageURL
		if p.Cause == social.CauseTransient && p.Attempts <= len(retryGaps) {
			t := p.LastAt.Add(retryGaps[p.Attempts-1])
			p.NextRetry = &t
		}
		groups[p.Cause] = append(groups[p.Cause], *p)
	}
	return gin.H{"groups": groups, "total": len(order)}
}

// dashboard is the data of the module's widgets. A target's status comes
// from account and connection problems only (#5): one bad image does not
// mark it down.
func dashboard(mc companion.ModuleContext) gin.H {
	db := mc.DB()
	var targets []gin.H
	for _, col := range columns(mc) {
		status := "ok"
		var accountOpen int64
		db.Model(&Attempt{}).Where("target = ? AND cause = ? AND "+openFailure, col.ID, social.CauseAccount).Count(&accountOpen)
		var lastErr string
		if accountOpen > 0 {
			status = "account"
			var a Attempt
			db.Where("target = ? AND cause = ? AND "+openFailure, col.ID, social.CauseAccount).Order("created_at desc").Limit(1).Find(&a)
			lastErr = a.Error
		}
		for _, cr := range connections(mc) {
			if cr.c.TargetID == col.ID {
				if st := cr.c.state(); st.PausedAt != nil {
					status, lastErr = "paused", st.PauseReason
				}
			}
		}
		var published int64
		db.Model(&Publication{}).Where("target = ? AND status = ?", col.ID, StatusPublished).Count(&published)
		var last Publication
		db.Where("target = ? AND status = ?", col.ID, StatusPublished).Order("published_at desc").Limit(1).Find(&last)
		row := gin.H{"id": col.ID, "platform": col.Platform, "status": status, "published": published}
		if lastErr != "" {
			row["error"] = lastErr
		}
		if last.ID != 0 {
			row["lastPublished"] = last.PublishedAt
		}
		targets = append(targets, row)
	}
	type top struct {
		ItemID       string `json:"itemId"`
		Title        string `json:"title"`
		ImageURL     string `json:"imageUrl"`
		Likes        int    `json:"likes"`
		WebsiteLikes int    `json:"websiteLikes"`
	}
	var tops []top
	db.Raw(`SELECT p.item_id, p.title, p.image_url, p.website_likes, COALESCE(SUM(u.likes), 0) AS likes
		FROM autoupload_posts p JOIN autoupload_publications u ON u.item_id = p.item_id
		GROUP BY p.item_id ORDER BY likes DESC LIMIT 5`).Scan(&tops)
	pr := problems(mc)
	byCause := gin.H{}
	for cause, list := range pr["groups"].(map[string][]Problem) {
		byCause[cause] = len(list)
	}
	return gin.H{"targets": targets, "top": tops, "problems": byCause}
}
