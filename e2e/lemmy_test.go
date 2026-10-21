//go:build integration

package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/core"
	"github.com/LNA-DEV/HomePageCompanion/e2e/fakes"
)

// lemmySetup is the Lemmy service, in Python, next to a real core: a fake
// Lemmy, a photo feed, autoupload posting through the service.
type lemmySetup struct {
	lemmy   *fakes.Lemmy
	feed    *httptest.Server
	core    *core.Core
	svcURL  string
	dataDir string
}

func startLemmy(t *testing.T) *lemmySetup {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	root, _ := filepath.Abs("..")
	s := &lemmySetup{lemmy: fakes.NewLemmy("")}
	lemmySrv := s.lemmy.Serve()
	t.Cleanup(lemmySrv.Close)
	var feedURL string
	s.feed = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fakes.Feed(feedURL, 3).ServeHTTP(w, r) }))
	feedURL = s.feed.URL
	t.Cleanup(s.feed.Close)

	dir := t.TempDir()
	s.dataDir = filepath.Join(dir, "data")
	servicesAddr := freeAddr(t)
	svcAddr := freeAddr(t)
	_, port, _ := net.SplitHostPort(svcAddr)
	s.svcURL = "http://" + svcAddr
	env := append(os.Environ(),
		"PYTHONPATH="+filepath.Join(root, "sdk", "python"),
		"PORT="+port,
		"COMPANION_URL=http://"+servicesAddr,
		"COMPANION_TOKEN_FILE="+filepath.Join(s.dataDir, "services", "lemmy", "run", "token"),
		"COMPANION_DATA="+filepath.Join(dir, "lemmy-data"),
		"HTTP_PROXY=", "HTTPS_PROXY=", "http_proxy=", "https_proxy=")

	// The manifest, as hpc apply reads it.
	man := exec.Command(py, filepath.Join(root, "services", "lemmy", "lemmy.py"))
	man.Env = append(env, "COMPANION_MANIFEST=print")
	out, err := man.Output()
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("manifest: %v\n%s", err, out)
	}
	lock := wire.Lock{Services: map[string]wire.LockedService{"lemmy": {From: "path:services/lemmy", URL: s.svcURL, Manifest: m, Grants: wire.Grants{Egress: []string{"127.0.0.1"}}}}}
	raw, _ := json.Marshal(lock)
	os.WriteFile(filepath.Join(dir, wire.LockFile), raw, 0o600)

	proc := exec.Command(py, filepath.Join(root, "services", "lemmy", "lemmy.py"))
	proc.Env = env
	proc.Stdout, proc.Stderr = testLog{t}, testLog{t}
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Process.Kill(); proc.Wait() })

	cfg := fmt.Sprintf(`core:
  domain: example.com
  adminKey: k
  publicUrl: http://companion.test
modules:
  db.sqlite: {}
  blob.fs: {}
  media: {}
  rss:
    instances:
      photos: { url: "%s/gallery.xml" }
  autoupload:
    instances:
      photos-lemmy: { source: rss:photos, target: lemmy:main, media: media, cron: "0 0 18 * * *", caption: "More at https://example.com/gallery" }
services:
  lemmy:
    url: %s
    instances:
      main: { server: %q, username: bot, password: bot-password, community: photography }
      wrong: { server: %q, username: bot, password: not-it, community: photography }
`, s.feed.URL, s.svcURL, lemmySrv.URL, lemmySrv.URL)
	os.WriteFile(filepath.Join(dir, "companion.yaml"), []byte(cfg), 0o600)
	t.Setenv("COMPANION_SECRET_KEY", "d29ybGQtc2VjcmV0LWtleS0zMi1ieXRlcy1sb25nISE=")
	s.core = core.New(core.Options{ConfigPath: filepath.Join(dir, "companion.yaml"), DataDir: s.dataDir, Logs: testLog{t}, ServicesListen: servicesAddr})
	if err := s.core.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.core.Stop(context.Background()) })
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, body := s.admin("GET", "/api/core/instances/lemmy:main")
		if strings.Contains(body, `"running":true`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Lemmy service did not come up: %s", body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return s
}

func (s *lemmySetup) admin(method, path string) (int, string) {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "ApiKey k")
	rec := httptest.NewRecorder()
	s.core.Handler().ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b)
}

// publisher calls the service directly, as the core does: signed, for one
// instance.
func (s *lemmySetup) publisher(t *testing.T, instance string) social.Publisher {
	t.Helper()
	tok, _ := s.core.ServiceToken("lemmy")
	a, _ := wire.Lookup("social.publisher/v1")
	h := &wire.HTTP{URL: s.svcURL + "/capabilities/social.publisher/v1", Prepare: func(r *http.Request, body []byte) {
		r.Header.Set(wire.InstanceHeader, instance)
		wire.Sign(r, body, []byte(tok), time.Now())
	}}
	return a.Client(h, []string{"describe", "preview", "publish", "resolveMention", "listPosts", "delete"}).(social.Publisher)
}

// The gate of Stage 3 (docs/concepts/modular-architecture.md §7): a service
// in another language offers social.publisher/v1, and autoupload, connected
// to it, posts through it, with the image fetched through the core.
func TestLemmyServiceWithAutoupload(t *testing.T) {
	s := startLemmy(t)
	if err := s.core.RunJob("autoupload:photos-lemmy/publish"); err != nil {
		t.Fatal(err)
	}
	posts := s.lemmy.Posts()
	if len(posts) != 1 {
		t.Fatalf("Lemmy got %d posts", len(posts))
	}
	p := posts[0]
	if !strings.HasPrefix(p.Name, "Lake at dawn") || !strings.Contains(p.Body, "More at https://example.com/gallery") || p.CommunityID != 7 {
		t.Fatalf("post %+v", p)
	}
	file := strings.TrimPrefix(p.URL, s.lemmy.Base+"/pictrs/image/")
	if img := s.lemmy.Image(file); len(img) < 100 || img[0] != 0xFF || img[1] != 0xD8 {
		t.Fatalf("no JPEG uploaded for %s (%d bytes)", p.URL, len(img))
	}
	_, items := s.admin("GET", "/api/b/autoupload/items")
	if !strings.Contains(items, s.lemmy.Base+"/post/1") {
		t.Fatalf("autoupload did not record the post: %s", items)
	}
	// Its token is kept in the core's secret store: one login only.
	s.core.RunJob("autoupload:photos-lemmy/publish")
	if n := s.lemmy.Logins(); n != 1 {
		t.Fatalf("%d logins for two posts", n)
	}
}

// The conformance suite of social.publisher/v1, and how Lemmy's failures
// arrive: classified as the Problems page groups them.
func TestLemmyServiceContract(t *testing.T) {
	s := startLemmy(t)
	p := s.publisher(t, "main")
	companiontest.PublisherConformance(t, p, s.feed.URL+"/img/photo-1.jpg")
	ctx := context.Background()

	if m, err := p.ResolveMention(ctx, "https://lemmy.example/u/jane"); err != nil || m.Text != "@jane@lemmy.example" {
		t.Fatalf("mention %+v %v", m, err)
	}
	if _, err := p.ResolveMention(ctx, "https://example.com/about"); !errors.Is(err, social.ErrNotAProfile) {
		t.Fatalf("not a profile: %v", err)
	}
	post := social.Post{Kind: "article", Text: "A blog post", Link: &social.LinkCard{URL: "https://example.com/blog/1", Title: "Two weeks in Norway"}}
	res, err := p.Publish(ctx, post)
	if err != nil {
		t.Fatal(err)
	}
	last := s.lemmy.Posts()[len(s.lemmy.Posts())-1]
	if last.Name != "Two weeks in Norway" || last.URL != "https://example.com/blog/1" || last.Body != "A blog post" {
		t.Fatalf("a link post: %+v", last)
	}

	// A lost answer: the same idempotency key gives the same post.
	key := social.Post{Kind: "photo", Text: "Once only", IdempotencyKey: "k-1"}
	r1, err1 := p.Publish(ctx, key)
	r2, err2 := p.Publish(ctx, key)
	if err1 != nil || err2 != nil || r1.ID != r2.ID {
		t.Fatalf("idempotency: %+v %v / %+v %v", r1, err1, r2, err2)
	}

	// A token Lemmy no longer takes is renewed without anybody noticing.
	s.lemmy.ExpireTokens()
	if _, err := p.Publish(ctx, social.Post{Kind: "photo", Text: "After a password change"}); err != nil {
		t.Fatalf("after the tokens expired: %v", err)
	}
	for _, c := range []struct {
		status int
		code   string
		cause  string
		is     error
	}{
		{400, "rate_limit_error", social.CauseTransient, social.ErrRateLimited},
		{502, "bad_gateway", social.CauseTransient, nil},
		{400, "invalid_post_title", social.CauseContent, nil},
	} {
		s.lemmy.FailNext(1, c.status, c.code)
		_, err := p.Publish(ctx, social.Post{Kind: "photo", Text: "x y z"})
		if err == nil || social.Classify(err).Cause != c.cause || (c.is != nil && !errors.Is(err, c.is)) {
			t.Errorf("%s: %v (%+v)", c.code, err, social.Classify(err))
		}
	}
	if _, err := s.publisher(t, "wrong").Publish(ctx, social.Post{Kind: "photo", Text: "x"}); social.Classify(err).Cause != social.CauseAccount {
		t.Errorf("a wrong password: %v (%+v)", err, social.Classify(err))
	}

	// The optional operations, and interactions.
	l := p.(social.Lister)
	listed, err := l.ListPosts(ctx)
	if err != nil || len(listed) < 3 || listed[0].CreatedAt.IsZero() {
		t.Fatalf("list: %+v %v", listed, err)
	}
	if err := p.(social.Deleter).Delete(ctx, res); err != nil {
		t.Fatalf("delete: %v", err)
	}
	ia, _ := wire.Lookup("social.interactions/v1")
	tok, _ := s.core.ServiceToken("lemmy")
	inter := ia.Client(&wire.HTTP{URL: s.svcURL + "/capabilities/social.interactions/v1", Prepare: func(r *http.Request, body []byte) {
		r.Header.Set(wire.InstanceHeader, "main")
		wire.Sign(r, body, []byte(tok), time.Now())
	}}, nil).(social.Interactions)
	s.lemmy.SetUpvotes(1, 12)
	if st, err := inter.GetStats(ctx, social.Result{ID: "1"}); err != nil || st.Likes != 12 || st.Replies != 1 {
		t.Fatalf("stats %+v %v", st, err)
	}
	if rs, err := inter.ListReplies(ctx, social.Result{ID: "1"}); err != nil || len(rs) != 1 || rs[0].Author != "jane@lemmy.example" {
		t.Fatalf("replies %+v %v", rs, err)
	}
	// The instance with the wrong password is the account's problem, shown
	// on its instance.
	_, view := s.admin("GET", "/api/core/instances/lemmy:wrong")
	if !strings.Contains(view, `"running":true`) {
		t.Fatalf("a wrong password stopped the instance: %s", view)
	}
}

// hpc test, as a service's author runs it: the Lemmy service, started by
// its command next to hpc's test core, passes social.publisher/v1's
// conformance suite against the fake Lemmy; with the wrong password it
// fails, saying why.
func TestLemmyWithHpcTest(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	root, _ := filepath.Abs("..")
	hpc := filepath.Join(t.TempDir(), "hpc")
	if out, err := exec.Command("go", "build", "-o", hpc, "github.com/LNA-DEV/HomePageCompanion/cmd/hpc").CombinedOutput(); err != nil {
		t.Fatalf("build hpc: %v\n%s", err, out)
	}
	lemmy := fakes.NewLemmy("")
	srv := lemmy.Serve()
	defer srv.Close()
	config := filepath.Join(t.TempDir(), "lemmy.yaml")
	os.WriteFile(config, []byte(fmt.Sprintf(`instances:
  main: { server: %q, username: bot, password: bot-password, community: photography }
`, srv.URL)), 0o600)

	cmd := exec.Command(hpc, "test", "-v", "--run", "python3 "+filepath.Join(root, "services", "lemmy", "lemmy.py"), "--config", config)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(root, "sdk", "python"))
	out, err := cmd.CombinedOutput()
	t.Logf("hpc test:\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "social.publisher/v1 main ok") {
		t.Fatal("the suite did not run")
	}
	if posts := lemmy.Posts(); len(posts) != 1 || posts[0].Name != "Conformance test" {
		t.Fatalf("the fake Lemmy got %+v", posts)
	}

	os.WriteFile(config, []byte(fmt.Sprintf(`instances:
  main: { server: %q, username: bot, password: not-it, community: photography }
`, srv.URL)), 0o600)
	cmd = exec.Command(hpc, "test", "--run", "python3 "+filepath.Join(root, "services", "lemmy", "lemmy.py"), "--config", config)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(root, "sdk", "python"))
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "social.publisher/v1 main FAIL: publish: lemmy: incorrect_login") {
		t.Fatalf("with the wrong password: %v\n%s", err, out)
	}
}
