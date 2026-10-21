package legacyconf

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func convertExample(t *testing.T) *Result {
	t.Helper()
	raw, err := os.ReadFile("testdata/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Convert(c, "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestConvertsTheExample(t *testing.T) {
	r := convertExample(t)
	t.Logf("companion.yaml:\n%s\nbuild.yaml:\n%s\nnotes:\n%s", r.Companion, r.Build, strings.Join(r.Notes, "\n"))

	var doc struct {
		Core struct {
			AdminKey  string `yaml:"adminKey"`
			Domain    string `yaml:"domain"`
			PublicURL string `yaml:"publicUrl"`
			Database  string `yaml:"database"`
			Blobs     string `yaml:"blobs"`
		}
		Modules map[string]struct {
			Config    map[string]any
			Instances map[string]map[string]any
		}
	}
	if err := yaml.Unmarshal(r.Companion, &doc); err != nil {
		t.Fatal(err)
	}
	c := doc.Core
	if c.AdminKey != "${ADMIN_API_KEY}" || c.Domain != "${COMPANION_DOMAIN:-example.com}" || c.PublicURL != "https://companion.example.com" ||
		c.Database != "db.sqlite" || c.Blobs != "blob.s3" {
		t.Fatalf("core %+v", c)
	}
	m := doc.Modules
	// Secrets stay placeholders; names become instance names.
	if m["rss"].Instances["photo-rss"]["url"] != "https://example.com/gallery/index.xml" {
		t.Errorf("rss %v", m["rss"])
	}
	if tok := m["mastodon"].Instances["main-mastodon"]; tok["token"] != "${MASTODON_PAT}" || tok["instance"] != "${MASTODON_INSTANCE}" {
		t.Errorf("mastodon %v", tok)
	}
	if b := m["bluesky"].Instances["main-bluesky"]; b["username"] != "${BLUESKY_USERNAME}" || b["password"] != "${BLUESKY_PAT}" {
		t.Errorf("bluesky %v", b)
	}
	if th := m["threads"].Instances["main-threads"]; th["userId"] != "${THREADS_USER_ID}" || th["accessToken"] != "${THREADS_ACCESS_TOKEN}" {
		t.Errorf("threads %v", th)
	}
	conn := m["autoupload"].Instances["instagram"]
	if conn["source"] != "rss:photo-rss" || conn["target"] != "instagram:main-instagram" || conn["strategy"] != "rotate" ||
		conn["caption"] != "More at example.com or the link in my profile" || conn["media"] != "media" {
		t.Errorf("connection %v", conn)
	}
	if pt := m["microblog"].Config["publishTo"].([]any); len(pt) != 3 || pt[0] != "mastodon:main-mastodon" {
		t.Errorf("publishTo %v", pt)
	}
	if pl := m["interactions"].Config["platforms"].([]any); len(pl) != 5 {
		t.Errorf("platforms %v", pl)
	}
	if bm := m["basemap"].Config; bm["schemaMajor"] != 4 || bm["schedule"] != "0 0 3 1 * *" || bm["publicUrl"] != "https://companion.example.com/api/tiles/basemap" {
		t.Errorf("basemap %v", bm)
	}
	if s3 := m["blob.s3"].Config; s3["prefix"] != "home-page-companion" {
		t.Errorf("blob.s3 %v", s3)
	}
	for _, id := range []string{"db.sqlite", "media", "webmention", "gbif", "trips", "routing", "webpush"} {
		if _, ok := m[id]; !ok {
			t.Errorf("no %s", id)
		}
	}

	var build struct {
		Core    string
		Modules []string
	}
	yaml.Unmarshal(r.Build, &build)
	if build.Core != "v1.2.3" || len(build.Modules) != len(r.Modules) {
		t.Fatalf("build %+v", build)
	}
	for _, p := range build.Modules {
		if strings.Contains(p, "db.sqlite") || strings.Contains(p, "blob.s3") {
			t.Errorf("module path %s", p)
		}
	}
	if len(r.Targets) != 5 || len(r.Connections) != 5 || len(r.Feeds) != 1 {
		t.Errorf("legacy names %d %d %d", len(r.Targets), len(r.Connections), len(r.Feeds))
	}
	notes := strings.Join(r.Notes, "\n")
	for _, want := range []string{`feed "Photo RSS" is now the instance "photo-rss"`, "posts now go to bluesky:main-bluesky as well", `connection "pixelfed" has no cron`} {
		if !strings.Contains(notes, want) {
			t.Errorf("no note %q", want)
		}
	}
}

func TestNumbersAndFlagsKeepTheirType(t *testing.T) {
	c, err := Parse([]byte(`
security: { apiKey: k }
datasources: { rss: [ { name: photos, feedUrl: "https://x/i.xml", itemType: image }, { name: posts, feedUrl: "https://x/p.xml", itemType: article } ] }
targets:
  - { name: m, platform: mastodon, pat: t, instance: "https://m", maxCaptionLength: "${MAX:-1000}" }
  - { name: weird, platform: myspace }
connections:
  - { name: c, sourceName: photos, targetName: m, cron: "0 0 18 * * *", includeAltText: true, tagModels: false, caption: "500" }
  - { name: d, sourceName: posts, targetName: m }
storage: { bucketUrl: "file:///srv/tiles" }
basemap: { enabled: true, publicUrl: "https://c/api/tiles/basemap", retainDays: 3 }
`))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Convert(c, "main")
	if err != nil {
		t.Fatal(err)
	}
	out := string(r.Companion)
	for _, want := range []string{"maxLength: ${MAX:-1000}", "includeAltText: true", `caption: "500"`, "retainDays: 3", "dir: /srv/tiles", "blobs: blob.fs", "cron: 0 0 18 * * *"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "tagModels") || strings.Contains(out, "myspace") || strings.Contains(out, "posts") {
		t.Errorf("left-out parts in\n%s", out)
	}
	notes := strings.Join(r.Notes, "\n")
	for _, want := range []string{`feed "posts" (itemType "article") is left out`, `target "weird" (platform "myspace") is left out`, `connection "d" is left out`} {
		if !strings.Contains(notes, want) {
			t.Errorf("no note %q in\n%s", want, notes)
		}
	}
}

func TestClashingNamesAreRefused(t *testing.T) {
	c, _ := Parse([]byte("targets:\n  - { name: Main Account, platform: mastodon }\n  - { name: main account, platform: pixelfed }\n"))
	if _, err := Convert(c, "main"); err == nil || !strings.Contains(err.Error(), "main-account") {
		t.Fatalf("err %v", err)
	}
}
