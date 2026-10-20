package autoupload_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/modules/autoupload"
)

const config = `
modules:
  fake.source: { instances: { photos: {}, blog: {} } }
  fake.publisher:
    instances:
      mastodon: { platform: mastodon }
      insta: { platform: instagram, requiresImage: true }
  fake.notifier: {}
  autoupload:
    instances:
      photos-mastodon: { source: fake.source:photos, target: fake.publisher:mastodon, caption: "More at example.com", cron: "0 0 18 * * *", notify: fake.notifier, routingTagsSource: rss }
      photos-insta: { source: fake.source:photos, target: fake.publisher:insta, cron: "0 30 18 * * *" }
      blog-mastodon: { source: fake.source:blog, target: fake.publisher:mastodon, strategy: announce, delay: 10m, window: "08:00-21:00", timezone: UTC, since: "2026-01-01" }
`

type env struct {
	h     *companiontest.Harness
	src   *companiontest.FakeSource
	blog  *companiontest.FakeSource
	masto *companiontest.FakePublisher
	insta *companiontest.FakePublisher
	now   time.Time
}

func setup(t *testing.T) *env {
	h := companiontest.New(t, config)
	e := &env{h: h,
		src:   companiontest.Instance[*companiontest.FakeSource](h, "fake.source:photos"),
		blog:  companiontest.Instance[*companiontest.FakeSource](h, "fake.source:blog"),
		masto: companiontest.Instance[*companiontest.FakePublisher](h, "fake.publisher:mastodon"),
		insta: companiontest.Instance[*companiontest.FakePublisher](h, "fake.publisher:insta"),
		now:   time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC),
	}
	for _, n := range []string{"photos-mastodon", "photos-insta", "blog-mastodon"} {
		companiontest.Instance[*autoupload.Connection](h, "autoupload:"+n).SetClock(func() time.Time { return e.now })
	}
	e.src.SetItems(
		companiontest.Photo("eisvogel", time.Date(2024, 10, 19, 9, 0, 0, 0, time.UTC), "Eisvogel am Teich"),
		companiontest.Photo("salamander", time.Date(2025, 5, 17, 9, 0, 0, 0, time.UTC), "Feuersalamander nach Regen"),
		companiontest.Photo("watzmann", time.Date(2023, 10, 21, 9, 0, 0, 0, time.UTC), "Alpenglühen am Watzmann"),
	)
	return e
}

func (e *env) conn(name string) *autoupload.Connection {
	return companiontest.Instance[*autoupload.Connection](e.h, "autoupload:"+name)
}

func (e *env) run(t *testing.T, name string) *autoupload.Run {
	t.Helper()
	r, err := e.conn(name).Run(context.Background(), "schedule")
	if err != nil && r == nil {
		t.Fatalf("run %s: %v", name, err)
	}
	return r
}

func TestRotationPublishesOnePhotoPerTarget(t *testing.T) {
	e := setup(t)
	r := e.run(t, "photos-mastodon")
	if r.Result != "published" || r.ItemID != "watzmann" {
		t.Fatalf("first run: %+v", r)
	}
	posted := e.masto.Posted()
	if len(posted) != 1 || posted[0].Text != "More at example.com" || posted[0].Images[0].URL != "https://example.com/watzmann.jpg" {
		t.Fatalf("posted %+v", posted)
	}
	// Publications are per target instance (#12): the same photo is still
	// open on Instagram.
	if r := e.run(t, "photos-insta"); r.ItemID != "watzmann" || r.Result != "published" {
		t.Fatalf("instagram run: %+v", r)
	}
	if r := e.run(t, "photos-mastodon"); r.ItemID != "eisvogel" {
		t.Fatalf("second run: %+v", r)
	}
	e.run(t, "photos-mastodon")
	if r := e.run(t, "photos-mastodon"); r.Result != "nothing" {
		t.Fatalf("all posted: %+v", r)
	}
}

func TestRoutingSkipIsRecordedAndTheNextPhotoGoes(t *testing.T) {
	e := setup(t)
	items, _ := e.src.ListItems(context.Background())
	items[2].Categories = []string{"meta_skip:mastodon"}
	e.src.SetItems(items...)
	r := e.run(t, "photos-mastodon")
	if r.Result != "published" || r.ItemID != "eisvogel" {
		t.Fatalf("run: %+v", r)
	}
	// The skipped photo is never chosen again (#11).
	w := e.h.Admin("GET", "/api/b/autoupload/items/watzmann", nil)
	if !strings.Contains(w.Body.String(), `"state":"skipped"`) || !strings.Contains(w.Body.String(), "meta_skip:mastodon") {
		t.Fatalf("detail: %s", w.Body)
	}
}

func TestFailuresRetriesAndPause(t *testing.T) {
	e := setup(t)
	e.masto.FailOnce(errors.New("mastodon create status: status: 503 body: down"))
	if r := e.run(t, "photos-mastodon"); r.Result != "failed" {
		t.Fatalf("run: %+v", r)
	}
	// A transient failure waits a day; until then the next photo goes.
	if r := e.run(t, "photos-mastodon"); r.ItemID != "eisvogel" {
		t.Fatalf("next run: %+v", r)
	}
	e.now = e.now.Add(25 * time.Hour)
	if r := e.run(t, "photos-mastodon"); r.ItemID != "watzmann" || r.Result != "published" {
		t.Fatalf("retry: %+v", r)
	}
	// The success resolves the failure: no open problem left.
	p := companiontest.JSON[map[string]any](e.h, e.h.Admin("GET", "/api/b/autoupload/problems", nil))
	if p["total"].(float64) != 0 {
		t.Fatalf("problems: %v", p)
	}

	// An account error pauses the connection and says so.
	e.masto.FailOnce(errors.New("status: 401 The access token is invalid"))
	if r := e.run(t, "photos-mastodon"); r.Result != "failed" {
		t.Fatalf("run: %+v", r)
	}
	if r := e.run(t, "photos-mastodon"); r.Result != "paused" {
		t.Fatalf("paused run: %+v", r)
	}
	n := companiontest.Instance[*companiontest.FakeNotifier](e.h, "fake.notifier:default")
	var bodies []string
	for _, m := range n.Messages() {
		bodies = append(bodies, m.Title)
	}
	if !strings.Contains(strings.Join(bodies, "|"), "Autoposting paused") {
		t.Fatalf("notifications %v", bodies)
	}
	// Account failures are retried by hand only: the problems page groups it.
	p = companiontest.JSON[map[string]any](e.h, e.h.Admin("GET", "/api/b/autoupload/problems", nil))
	groups := p["groups"].(map[string]any)
	if len(groups["account"].([]any)) != 1 {
		t.Fatalf("groups %v", groups)
	}
	// The dashboard shows the target paused, not down for one bad image.
	d := e.h.Admin("GET", "/api/b/autoupload/dashboard", nil).Body.String()
	if !strings.Contains(d, `"status":"paused"`) || !strings.Contains(d, `"status":"ok"`) {
		t.Fatalf("dashboard %s", d)
	}
	// "Run now" ignores the pause and lifts it on success. The photo with
	// the account failure waits for a retry by hand, so a new one goes.
	items, _ := e.src.ListItems(context.Background())
	e.src.SetItems(append(items, companiontest.Photo("libelle", time.Date(2022, 10, 1, 0, 0, 0, 0, time.UTC), "Libelle"))...)
	r, err := e.conn("photos-mastodon").Run(context.Background(), "manual")
	if err != nil || r.Result != "published" {
		t.Fatalf("manual run %+v %v", r, err)
	}
	if r := e.run(t, "photos-mastodon"); r.Result == "paused" {
		t.Fatal("still paused")
	}
}

func TestContentErrorDoesNotMarkTheTargetDown(t *testing.T) {
	e := setup(t)
	e.masto.FailOnce(errors.New("status: 413 file too large"))
	e.run(t, "photos-mastodon")
	d := e.h.Admin("GET", "/api/b/autoupload/dashboard", nil).Body.String()
	if strings.Contains(d, `"status":"account"`) || strings.Contains(d, `"status":"paused"`) {
		t.Fatalf("dashboard %s", d)
	}
}

func TestAnnouncements(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	old := companiontest.Article("old", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), "Old post")
	fresh := companiontest.Article("lofoten", e.now.Add(-time.Minute), "Lofoten")
	e.blog.SetItems(old, fresh)
	conn := e.conn("blog-mastodon")

	// The event of the rss module queues it: 10 minutes after it appeared,
	// inside the window.
	ev := feed.ItemDiscovered{Source: "fake.source:blog", Item: fresh}
	if err := conn.HandleEvent(ctx, eventOf(feed.EventItemDiscovered, ev)); err != nil {
		t.Fatal(err)
	}
	e.h.MustRunJob("autoupload:blog-mastodon/announce")
	if len(e.masto.Posted()) != 0 {
		t.Fatal("announced before the delay")
	}
	e.now = e.now.Add(11 * time.Minute)
	e.h.MustRunJob("autoupload:blog-mastodon/announce")
	posted := e.masto.Posted()
	if len(posted) != 1 || !strings.Contains(posted[0].Text, "Lofoten") || !strings.Contains(posted[0].Text, fresh.Link) || posted[0].Link == nil {
		t.Fatalf("posted %+v", posted)
	}
	// Posts older than the connection are never announced, and nothing
	// twice.
	e.now = e.now.Add(time.Hour)
	e.h.MustRunJob("autoupload:blog-mastodon/announce")
	if len(e.masto.Posted()) != 1 {
		t.Fatalf("posted %d", len(e.masto.Posted()))
	}
	// Outside the window it waits for the morning.
	late := companiontest.Article("night", e.now, "Night post")
	e.now = time.Date(2026, 10, 20, 22, 0, 0, 0, time.UTC)
	late.Published = e.now
	e.blog.SetItems(old, fresh, late)
	e.h.MustRunJob("autoupload:blog-mastodon/announce")
	if len(e.masto.Posted()) != 1 {
		t.Fatal("posted at night")
	}
	e.now = time.Date(2026, 10, 21, 8, 1, 0, 0, time.UTC)
	e.h.MustRunJob("autoupload:blog-mastodon/announce")
	if len(e.masto.Posted()) != 2 {
		t.Fatalf("morning: %d", len(e.masto.Posted()))
	}
}

func TestArticleOnAnImageOnlyTargetIsSkipped(t *testing.T) {
	e := setup(t)
	e.src.SetItems(companiontest.Article("text", time.Date(2024, 10, 20, 0, 0, 0, 0, time.UTC), "Text only"))
	if r := e.run(t, "photos-insta"); r.Result != "skipped" {
		t.Fatalf("run %+v", r)
	}
	w := e.h.Admin("GET", "/api/b/autoupload/items/text", nil).Body.String()
	if !strings.Contains(w, "needs an image") {
		t.Fatalf("detail %s", w)
	}
}

func TestPostsPage(t *testing.T) {
	e := setup(t)
	e.run(t, "photos-mastodon")
	e.masto.FailOnce(errors.New("status: 503"))
	e.run(t, "photos-mastodon")
	e.h.MustRunJob("autoupload/index")

	// Microblog posts reach the table through events.
	mc := e.h.Module("autoupload")
	pub := social.PostPublished{ItemID: "note-1", Kind: "note", Title: "Hello fediverse", Source: "microblog", Target: "fake.publisher:mastodon",
		Platform: "mastodon", Ref: social.Result{ID: "9", URL: "https://m/9"}, Time: e.now}
	if err := mc.Emit(context.Background(), social.EventPostPublished, pub); err != nil {
		t.Fatal(err)
	}
	stats := social.StatsUpdated{ItemID: "watzmann", Target: "fake.publisher:mastodon", Likes: 31, Time: e.now}
	mc.Emit(context.Background(), social.EventStatsUpdated, stats)
	mc.Emit(context.Background(), social.EventStatsUpdated, social.StatsUpdated{ItemID: "watzmann", Target: social.WebsiteTarget, Likes: 7, Time: e.now})
	e.h.Deliver()

	type page struct {
		Items []struct {
			ID           string                     `json:"id"`
			Kind         string                     `json:"kind"`
			Likes        int                        `json:"likes"`
			WebsiteLikes int                        `json:"websiteLikes"`
			Cells        map[string]autoupload.Cell `json:"cells"`
		} `json:"items"`
		Columns []autoupload.Column `json:"columns"`
		Total   int                 `json:"total"`
		Counts  map[string]int      `json:"counts"`
	}
	p := companiontest.JSON[page](e.h, e.h.Admin("GET", "/api/b/autoupload/items?sort=likes", nil))
	if p.Total != 4 || p.Counts["microblog"] != 1 || p.Counts["problems"] != 1 || len(p.Columns) != 2 {
		t.Fatalf("page: total %d counts %v columns %v", p.Total, p.Counts, p.Columns)
	}
	top := p.Items[0]
	if top.ID != "watzmann" || top.Likes != 31 || top.WebsiteLikes != 7 || top.Cells["fake.publisher:mastodon"].State != "published" {
		t.Fatalf("top row %+v", top)
	}
	byID := map[string]int{}
	for i, it := range p.Items {
		byID[it.ID] = i
	}
	if c := p.Items[byID["eisvogel"]].Cells["fake.publisher:mastodon"]; c.State != "failed" || c.Reason != "503" {
		t.Fatalf("failed cell %+v", c)
	}
	if c := p.Items[byID["note-1"]].Cells["fake.publisher:insta"]; c.State != "none" {
		t.Fatalf("note on instagram %+v", c)
	}
	// The photo the next Instagram run will post is marked as next.
	if c := p.Items[byID["watzmann"]].Cells["fake.publisher:insta"]; c.State != "next" || c.Time == nil {
		t.Fatalf("next cell %+v", c)
	}

	// Filters and search run on the server.
	for q, want := range map[string]int{
		"filter=photos": 3, "filter=microblog": 1, "filter=problems": 1, "filter=incomplete": 3,
		"q=salamander": 1, "q=eisvogel&filter=problems": 1, "target=fake.publisher:insta": 3,
	} {
		got := companiontest.JSON[page](e.h, e.h.Admin("GET", "/api/b/autoupload/items?"+q, nil))
		if got.Total != want {
			t.Errorf("%s: %d, want %d", q, got.Total, want)
		}
	}
	got := companiontest.JSON[page](e.h, e.h.Admin("GET", "/api/b/autoupload/items?limit=2&page=2", nil))
	if len(got.Items) != 2 || got.Total != 4 {
		t.Fatalf("paging %d of %d", len(got.Items), got.Total)
	}
}

func TestActions(t *testing.T) {
	e := setup(t)
	e.run(t, "photos-mastodon")
	base := "/api/b/autoupload/items/watzmann/targets/fake.publisher:mastodon/"
	// Allowing a repost needs a confirmation, because the photo goes out
	// again (#10).
	if w := e.h.Admin("POST", base+"allow-repost", nil); w.Code != 428 {
		t.Fatalf("without confirm: %d", w.Code)
	}
	if w := e.h.Admin("POST", base+"allow-repost", map[string]bool{"confirm": true}); w.Code != 200 {
		t.Fatalf("allow-repost: %d", w.Code)
	}
	if r := e.run(t, "photos-mastodon"); r.ItemID != "watzmann" {
		t.Fatalf("not reposted: %+v", r)
	}
	if w := e.h.Admin("POST", "/api/b/autoupload/items/salamander/targets/fake.publisher:insta/mark-published", map[string]string{"url": "https://instagram.com/p/x"}); w.Code != 200 {
		t.Fatalf("mark-published: %d", w.Code)
	}
	if w := e.h.Admin("POST", "/api/b/autoupload/items/eisvogel/targets/fake.publisher:insta/publish-now", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"result":"published"`) {
		t.Fatalf("publish-now: %d %s", w.Code, w.Body)
	}
	w := e.h.Admin("GET", "/api/b/autoupload/items/salamander/preview?target=fake.publisher:mastodon", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "More at example.com") {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
}

func TestScheduleAndRunNow(t *testing.T) {
	e := setup(t)
	w := e.h.Admin("GET", "/api/b/autoupload/schedule", nil).Body.String()
	if !strings.Contains(w, `"connection":"photos-mastodon"`) || !strings.Contains(w, `"title":"Alpenglühen am Watzmann"`) || !strings.Contains(w, `"nextRun"`) {
		t.Fatalf("schedule %s", w)
	}
	// Run now answers with the run; its result says which post went out (#8).
	run := companiontest.JSON[autoupload.Run](e.h, e.h.Admin("POST", "/api/b/autoupload/connections/photos-mastodon/run", nil))
	var final autoupload.Run
	for i := 0; i < 100; i++ {
		final = companiontest.JSON[autoupload.Run](e.h, e.h.Admin("GET", fmt.Sprintf("/api/b/autoupload/runs/%d", run.ID), nil))
		if final.Result != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Result != "published" || final.ItemID != "watzmann" || final.PostURL == "" {
		t.Fatalf("run %+v", final)
	}
	// The old trigger URL still works.
	if w := e.h.Admin("POST", "/api/upload/photos-mastodon", nil); w.Code != 202 {
		t.Fatalf("legacy trigger: %d", w.Code)
	}
	if w := e.h.Public("POST", "/api/upload/photos-mastodon", nil); w.Code != 401 {
		t.Fatalf("legacy trigger without key: %d", w.Code)
	}
}

func TestAttemptLogFilters(t *testing.T) {
	e := setup(t)
	e.masto.FailOnce(errors.New("status: 503"))
	e.run(t, "photos-mastodon")
	e.run(t, "photos-mastodon")
	e.run(t, "photos-insta")
	type page struct{ Total int }
	for q, want := range map[string]int{"": 3, "status=failed": 1, "status=success": 2, "target=fake.publisher:insta": 1, "q=watzmann": 2, "cause=transient": 1} {
		if got := companiontest.JSON[page](e.h, e.h.Admin("GET", "/api/b/autoupload/attempts?"+q, nil)); got.Total != want {
			t.Errorf("%q: %d, want %d", q, got.Total, want)
		}
	}
}

func eventOf(typ string, data any) companion.Event {
	b, _ := jsonMarshal(data)
	return companion.Event{ID: "e1", Type: typ, Data: b}
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
