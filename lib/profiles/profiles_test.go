package profiles

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseInstagramURL(t *testing.T) {
	cases := map[string]string{
		"https://www.instagram.com/janedoe/":    "janedoe",
		"https://instagram.com/jane.doe_99":     "jane.doe_99",
		"https://Instagram.com/JaneDoe/?hl=de":  "JaneDoe",
		"https://www.instagram.com/p/C0abc123/": "",
		"https://www.instagram.com/reel/xyz/":   "",
		"https://www.instagram.com/explore/":    "",
		"https://www.instagram.com/stories/x/":  "",
		"https://www.instagram.com/":            "",
		"https://www.instagram.com/jane-doe/":   "",
		"https://evil.example/janedoe/":         "",
		"ftp://instagram.com/janedoe":           "",
		"not a url":                             "",
	}
	for raw, want := range cases {
		a, ok := Instagram(raw)
		if ok != (want != "") || a.Name != want {
			t.Errorf("Instagram(%q) = %q, %v; want %q", raw, a.Name, ok, want)
		}
	}
}

func TestParseThreadsURL(t *testing.T) {
	cases := map[string]string{
		"https://www.threads.net/@janedoe":  "janedoe",
		"https://threads.com/@jane.doe/":    "jane.doe",
		"https://www.threads.com/@janedoe/": "janedoe",
		"https://threads.net/janedoe":       "",
		"https://threads.net/@":             "",
		"https://instagram.com/@janedoe":    "",
	}
	for raw, want := range cases {
		a, ok := Threads(raw)
		if ok != (want != "") || a.Name != want {
			t.Errorf("Threads(%q) = %q, %v; want %q", raw, a.Name, ok, want)
		}
	}
}

func TestParseFediverseURL(t *testing.T) {
	cases := []struct{ raw, base, name string }{
		{"https://mastodon.social/@janedoe", "https://mastodon.social", "janedoe@mastodon.social"},
		{"https://mastodon.social/@janedoe/", "https://mastodon.social", "janedoe@mastodon.social"},
		{"https://mastodon.social/@janedoe@other.example", "https://mastodon.social", "janedoe@other.example"},
		{"https://mastodon.social/users/janedoe", "https://mastodon.social", "janedoe@mastodon.social"},
		{"https://pixelfed.de/LNA-DEV", "https://pixelfed.de", "LNA-DEV@pixelfed.de"},
		{"https://Pixelfed.de/LNA-DEV/", "https://pixelfed.de", "LNA-DEV@pixelfed.de"},
		{"https://mastodon.social/@janedoe/123456", "", ""},
		{"https://mastodon.social/", "", ""},
		{"https://mastodon.social/@", "", ""},
		{"mastodon.social/@janedoe", "", ""},
	}
	for _, c := range cases {
		a, ok := Fediverse(c.raw)
		if ok != (c.name != "") || a.Name != c.name || a.Base != c.base {
			t.Errorf("Fediverse(%q) = %+v, %v; want %s %s", c.raw, a, ok, c.base, c.name)
		}
	}
}

func TestFediverseLookups(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.TrimPrefix(srv.URL, "http://")
		switch r.URL.Query().Get("resource") {
		case "acct:jane@" + host:
			io.WriteString(w, `{"subject":"acct:jane@social.example"}`)
		case "acct:lna_dev@" + host:
			io.WriteString(w, `{"subject":"acct:lna_dev@social.example"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	a, ok := Fediverse(srv.URL + "/@jane")
	if !ok {
		t.Fatal("test URL does not parse")
	}
	mention, id, err := FediverseMention(ctx, a)
	if err != nil || mention != "@jane@social.example" || id != "jane@social.example" {
		t.Errorf("mention = %q, %q, %v", mention, id, err)
	}
	if _, _, err := FediverseMention(ctx, Account{Base: srv.URL, Name: "nobody@x"}); err == nil {
		t.Error("unknown account resolved")
	}

	self, err := FediverseSelf(ctx, srv.URL+"/", "lna_dev")
	if err != nil || self != "lna_dev@social.example" {
		t.Errorf("self = %q, %v", self, err)
	}
}

func TestParseBlueskyURL(t *testing.T) {
	cases := map[string]string{
		"https://bsky.app/profile/janedoe.bsky.social":            "janedoe.bsky.social",
		"https://bsky.app/profile/jane.example/":                  "jane.example",
		"https://bsky.app/profile/did:plc:abc123xyz":              "did:plc:abc123xyz",
		"https://bsky.app/profile/janedoe.bsky.social/post/3l5xy": "",
		"https://bsky.app/profile/janedoe":                        "",
		"https://bsky.app/janedoe.bsky.social":                    "",
		"https://staging.bsky.app/profile/janedoe.bsky.social":    "",
	}
	for raw, want := range cases {
		a, ok := Bluesky(raw)
		if ok != (want != "") || a.Name != want {
			t.Errorf("Bluesky(%q) = %q, %v; want %q", raw, a.Name, ok, want)
		}
	}
}
