package conf

import (
	"testing"
)

func TestParseAndInstances(t *testing.T) {
	t.Setenv("MASTODON_PAT", "secret-pat")
	f, err := Parse([]byte(`
core:
  domain: example.com
  adminKey: key
modules:
  gbif: {}
  rss:
    instances:
      photos: { url: https://example.com/gallery/index.xml }
      blog:   { url: https://example.com/posts/index.xml }
  mastodon:
    instances:
      main: { instance: https://mastodon.social, token: "${MASTODON_PAT}" }
  microblog:
    config:
      publishTo: [mastodon:main]
services:
  lemmy:
    from: oci://ghcr.io/someone/hpc-lemmy:0.3.1
    grants:
      egress: [lemmy.world]
    instances:
      main: { instance: https://lemmy.world }
`))
	if err != nil {
		t.Fatal(err)
	}
	if f.Core.Domain != "example.com" || f.Core.AdminKey != "key" {
		t.Fatalf("core: %+v", f.Core)
	}
	var ids []string
	for _, i := range f.Instances() {
		ids = append(ids, i.ID())
	}
	want := []string{"gbif:default", "lemmy:main", "mastodon:main", "microblog:default", "rss:blog", "rss:photos"}
	if len(ids) != len(want) {
		t.Fatalf("instances %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("instances %v, want %v", ids, want)
		}
	}
	for _, i := range f.Instances() {
		if i.ID() == "mastodon:main" && i.Values["token"] != "secret-pat" {
			t.Fatalf("placeholder not expanded: %v", i.Values)
		}
		if i.ID() == "lemmy:main" && !i.Service {
			t.Fatal("service instance not marked")
		}
	}
	if !f.HasBlock("lemmy") || f.HasBlock("trips") {
		t.Fatal("HasBlock")
	}
}

func TestParseRejectsUnknownCoreKeys(t *testing.T) {
	if _, err := Parse([]byte("core:\n  domian: example.com\n")); err == nil {
		t.Fatal("a misspelt core key is accepted")
	}
}

func TestParseEmpty(t *testing.T) {
	f, err := Parse(nil)
	if err != nil || len(f.Instances()) != 0 {
		t.Fatalf("%v %v", f, err)
	}
}

func TestRef(t *testing.T) {
	for in, want := range map[string][2]string{
		"rss:photos": {"rss", "photos"},
		"gbif":       {"gbif", "default"},
		" a:b ":      {"a", "b"},
	} {
		b, n := Ref(in)
		if b != want[0] || n != want[1] {
			t.Errorf("%q: %s %s", in, b, n)
		}
	}
}
