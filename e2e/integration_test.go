//go:build integration

// The integration tests (docs/concepts/modular-architecture.md §6): the
// core with every standard module as one program, the local test feed and
// fake platforms, and what happens when the outside fails.
//
//	go test -tags integration ./...
package e2e_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/e2e/world"
)

func start(t *testing.T, seed bool) *world.World {
	t.Helper()
	w, err := world.Start(world.Options{Seed: seed, Logs: testLog{t}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	return w
}

type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Logf("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// get decodes an admin GET into v.
func get(t *testing.T, w *world.World, path string, v any) {
	t.Helper()
	code, b, err := w.Do("GET", path, nil)
	if err != nil || code != http.StatusOK {
		t.Fatalf("GET %s: %d %s %v", path, code, b, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("GET %s: %v: %s", path, err, b)
	}
}

type cell struct {
	State string `json:"state"`
	Likes int    `json:"likes"`
	URL   string `json:"url"`
	Cause string `json:"cause"`
}

type row struct {
	ID     string          `json:"id"`
	InFeed bool            `json:"inFeed"`
	Likes  int             `json:"likes"`
	Cells  map[string]cell `json:"cells"`
}

type items struct {
	Items  []row          `json:"items"`
	Total  int            `json:"total"`
	Counts map[string]int `json:"counts"`
}

type problems struct {
	Groups map[string][]struct {
		ItemID, Target, Cause, Error string
		NextRetry                    *string
	} `json:"groups"`
	Total int `json:"total"`
}

func item(t *testing.T, w *world.World, id string) row {
	t.Helper()
	var p items
	get(t, w, "/api/b/autoupload/items?q="+id, &p)
	for _, r := range p.Items {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no item %s in %+v", id, p)
	return row{}
}

// waitFor polls cond for up to ten seconds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func lastPostID(url string) string { return url[strings.LastIndex(url, "/")+1:] }

func TestSeededWorld(t *testing.T) {
	w := start(t, true)
	var p items
	get(t, w, "/api/b/autoupload/items?limit=1", &p)
	want := map[string]int{"all": 71, "photos": 60, "blog": 8, "microblog": 3, "problems": 3}
	for k, n := range want {
		if p.Counts[k] != n {
			t.Errorf("count %s = %d, want %d", k, p.Counts[k], n)
		}
	}
	// 24 rotations and the 3 microblog posts reached Mastodon; Instagram
	// refused one image; Bluesky's last run met an expired password.
	for name, got := range map[string]int{"mastodon": len(w.Mastodon.Posts()), "pixelfed": len(w.Pixelfed.Posts()), "bluesky": len(w.Bluesky.Posts()),
		"instagram": len(w.Instagram.Posts()), "threads": len(w.Threads.Posts())} {
		if n := map[string]int{"mastodon": 27, "pixelfed": 12, "bluesky": 10, "instagram": 5, "threads": 7}[name]; got != n {
			t.Errorf("%s received %d posts, want %d", name, got, n)
		}
	}
	for _, post := range w.Pixelfed.Posts() {
		if post.Images != 1 {
			t.Errorf("pixelfed post %s has %d images", post.ID, post.Images)
		}
	}
	var pr problems
	get(t, w, "/api/b/autoupload/problems", &pr)
	if len(pr.Groups["account"]) != 1 || pr.Groups["account"][0].Target != "bluesky:main" || len(pr.Groups["content"]) != 1 || len(pr.Groups["transient"]) != 1 {
		t.Fatalf("problems %+v", pr)
	}

	// A like on the platform reaches the website.
	first := item(t, w, "photo-001")
	masto := first.Cells["mastodon:main"]
	if masto.State != "published" {
		t.Fatalf("photo-001 on mastodon: %+v", masto)
	}
	w.Mastodon.SetLikes(lastPostID(masto.URL), 42)
	if err := w.Likes(); err != nil {
		t.Fatal(err)
	}
	code, b, _ := w.Public("GET", "/api/interactions/post/all/photo-001", nil)
	var likes []struct {
		Platform string
		Likes    int
	}
	json.Unmarshal(b, &likes)
	var mastoLikes int
	for _, l := range likes {
		if l.Platform == "mastodon" {
			mastoLikes = l.Likes
		}
	}
	if code != 200 || mastoLikes != 42 {
		t.Fatalf("website likes %d %s", code, b)
	}
	if got := item(t, w, "photo-001").Cells["mastodon:main"].Likes; got != 42 {
		t.Fatalf("posts page likes %d", got)
	}
}

func TestFeedGone(t *testing.T) {
	w := start(t, false)
	if err := w.Index(); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{http.StatusNotFound, http.StatusGone, http.StatusServiceUnavailable} {
		w.Feed.Gone(status)
		if err := w.RunJob("rss:photos/refresh"); err == nil {
			t.Fatalf("refresh of a feed that answers %d succeeded", status)
		}
		// The companion keeps its copy and keeps posting from it.
		var feeds []struct {
			Name, LastError string
			Items           int
		}
		get(t, w, "/api/b/rss/feeds", &feeds)
		if feeds[1].Name != "photos" || feeds[1].Items != 60 || feeds[1].LastError == "" {
			t.Fatalf("feeds after %d: %+v", status, feeds)
		}
		if err := w.Rotate("photos-mastodon"); err != nil {
			t.Fatalf("posting while the feed answers %d: %v", status, err)
		}
	}
	if n := len(w.Mastodon.Posts()); n != 3 {
		t.Fatalf("posts while the feed was gone: %d", n)
	}
	if code, _, _ := w.Public("GET", "/health", nil); code != 200 {
		t.Fatalf("health %d", code)
	}

	// Back, but without one photo: it stays, marked, and is not posted.
	w.Feed.Back()
	w.Feed.Remove("photo-010")
	if err := w.Index(); err != nil {
		t.Fatal(err)
	}
	if r := item(t, w, "photo-010"); r.InFeed {
		t.Fatalf("photo-010 still in the feed: %+v", r)
	}
	var p items
	get(t, w, "/api/b/autoupload/items?filter=gone", &p)
	if p.Total != 1 {
		t.Fatalf("gone filter %d", p.Total)
	}
	for i := 0; i < 15; i++ {
		w.Rotate("photos-mastodon")
	}
	for _, post := range w.Mastodon.Posts() {
		if strings.Contains(post.Text, "Partnach") && strings.Contains(post.Text, "at dawn") {
			t.Fatalf("posted the removed photo: %q", post.Text)
		}
	}
}

func TestRateLimitAndServerErrors(t *testing.T) {
	w := start(t, false)
	if err := w.Index(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		status int
		body   string
	}{
		{http.StatusTooManyRequests, `{"error":"Too many requests"}`},
		{http.StatusServiceUnavailable, `<html>503 Service Temporarily Unavailable</html>`},
	} {
		w.Mastodon.FailNext(1, c.status, c.body)
		if err := w.Rotate("photos-mastodon"); err == nil {
			t.Fatalf("a %d did not fail the run", c.status)
		}
	}
	var pr problems
	get(t, w, "/api/b/autoupload/problems", &pr)
	if len(pr.Groups["transient"]) != 2 || len(pr.Groups["account"]) != 0 {
		t.Fatalf("problems %+v", pr)
	}
	for _, p := range pr.Groups["transient"] {
		if p.NextRetry == nil {
			t.Errorf("no automatic retry for %s: %s", p.ItemID, p.Error)
		}
	}
	// Not an account problem: the connection keeps running.
	var sched []struct {
		Connection string
		Paused     bool
	}
	get(t, w, "/api/b/autoupload/schedule", &sched)
	for _, s := range sched {
		if s.Paused {
			t.Fatalf("%s paused after a transient failure", s.Connection)
		}
	}
	if err := w.Rotate("photos-mastodon"); err != nil {
		t.Fatalf("the next run: %v", err)
	}
	// Retrying by hand posts both and closes the problems.
	if code, b, _ := w.Do("POST", "/api/b/autoupload/problems/retry", map[string]string{"cause": "transient"}); code != http.StatusAccepted || !strings.Contains(string(b), `"retrying":2`) {
		t.Fatalf("retry %d %s", code, b)
	}
	waitFor(t, func() bool {
		var pr problems
		get(t, w, "/api/b/autoupload/problems", &pr)
		return pr.Total == 0
	})
	if n := len(w.Mastodon.Posts()); n != 3 {
		t.Fatalf("mastodon has %d posts, want 3", n)
	}
}

func TestExpiredToken(t *testing.T) {
	w := start(t, false)
	if err := w.Index(); err != nil {
		t.Fatal(err)
	}
	w.Mastodon.Expire()
	if err := w.Rotate("photos-mastodon"); err == nil {
		t.Fatal("posting with an expired token succeeded")
	}
	var pr problems
	get(t, w, "/api/b/autoupload/problems", &pr)
	if len(pr.Groups["account"]) != 1 {
		t.Fatalf("problems %+v", pr)
	}
	var sched []struct {
		Connection  string
		Paused      bool
		PauseReason string
	}
	get(t, w, "/api/b/autoupload/schedule", &sched)
	paused := map[string]bool{}
	for _, s := range sched {
		paused[s.Connection] = s.Paused
	}
	if !paused["photos-mastodon"] || paused["photos-pixelfed"] {
		t.Fatalf("paused %v", paused)
	}
	// While paused, the schedule does not hammer the platform.
	before := len(w.Mastodon.Posts())
	w.Rotate("photos-mastodon")
	var runs []struct{ Result string }
	get(t, w, "/api/b/autoupload/runs?connection=photos-mastodon", &runs)
	if len(runs) == 0 || runs[0].Result != "paused" {
		t.Fatalf("runs while paused %+v", runs)
	}
	// A new token, resume: it posts again and the problem is resolved.
	w.Mastodon.Renew()
	if code, _, _ := w.Do("POST", "/api/b/autoupload/connections/photos-mastodon/resume", nil); code != 200 {
		t.Fatalf("resume %d", code)
	}
	if err := w.Rotate("photos-mastodon"); err != nil {
		t.Fatal(err)
	}
	if len(w.Mastodon.Posts()) != before+1 {
		t.Fatalf("posts %d, want %d", len(w.Mastodon.Posts()), before+1)
	}

	// A Meta token that expired: the refresh fails visibly, posting is an
	// account problem.
	w.Instagram.Expire()
	if err := w.RunJob("instagram:main/token-refresh"); err == nil {
		t.Fatal("refreshing an expired Instagram token succeeded")
	}
	var tokens []struct{ Status, LastError string }
	get(t, w, "/api/b/instagram/tokens", &tokens)
	if tokens[0].Status != "expired" || !strings.Contains(tokens[0].LastError, "190") {
		t.Fatalf("instagram token %+v", tokens)
	}
	w.Rotate("photos-instagram")
	get(t, w, "/api/b/autoupload/problems", &pr)
	found := false
	for _, p := range pr.Groups["account"] {
		found = found || p.Target == "instagram:main"
	}
	if !found {
		t.Fatalf("no account problem for instagram: %+v", pr)
	}
}
