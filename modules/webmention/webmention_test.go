package webmention_test

import (
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
)

func TestReceive(t *testing.T) {
	h := companiontest.New(t, "modules:\n  fake.notifier: {}\n  webmention: { config: { notify: fake.notifier } }\n")
	for body, want := range map[string]int{
		"source=https://other.example/post&target=https://example.com/en/gallery/photo/x/": 202,
		"source=javascript://x/%0aalert(1)&target=https://example.com/":                    400,
		"source=https://other.example/post":                                                400,
		"":                                                                                 400,
	} {
		if w := h.Public("POST", "/api/webmention", body); w.Code != want {
			t.Errorf("%q: %d, want %d", body, w.Code, want)
		}
	}
	list := h.Admin("GET", "/api/b/webmention/mentions", nil).Body.String()
	if strings.Count(list, `"Source"`) != 1 {
		t.Fatalf("list %s", list)
	}
	n := companiontest.Instance[*companiontest.FakeNotifier](h, "fake.notifier:default")
	for i := 0; i < 100 && len(n.Messages()) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if len(n.Messages()) != 1 {
		t.Fatal("no notification")
	}
}

func TestImport(t *testing.T) {
	h := companiontest.New(t, "modules:\n  webmention: {}\n")
	old := companiontest.LegacyDB(t, `CREATE TABLE webmentions (id integer primary key, source text, target text, created_at datetime)`,
		`INSERT INTO webmentions (source, target, created_at) VALUES ('https://a.example/', 'https://example.com/', '2026-01-01')`)
	info, _ := companion.Lookup("webmention")
	if err := info.Import(h.Module("webmention"), old); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.Admin("GET", "/api/b/webmention/mentions", nil).Body.String(), "a.example") {
		t.Fatal("not imported")
	}
}
