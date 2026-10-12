package autouploader

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/blueskyapi"
	"github.com/LNA-DEV/HomePageCompanion/config"
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
		a, ok := parseInstagramURL(raw)
		if ok != (want != "") || a.name != want {
			t.Errorf("parseInstagramURL(%q) = %q, %v; want %q", raw, a.name, ok, want)
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
		a, ok := parseThreadsURL(raw)
		if ok != (want != "") || a.name != want {
			t.Errorf("parseThreadsURL(%q) = %q, %v; want %q", raw, a.name, ok, want)
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
		a, ok := parseFediverseURL(c.raw)
		if ok != (c.name != "") || a.name != c.name || a.base != c.base {
			t.Errorf("parseFediverseURL(%q) = %+v, %v; want %s %s", c.raw, a, ok, c.base, c.name)
		}
	}
}

// fakeTarget mentions "@<name>" for every parsed account and reports its
// own identity as self. resolved counts lookups.
func fakeTarget(self string, selfErr error, resolved *int) mentionTarget {
	return mentionTarget{
		profiles: []string{"mastodon", "pixelfed"},
		parse:    parseFediverseURL,
		resolve: func(_ context.Context, a account) (string, string, error) {
			*resolved++
			if strings.HasPrefix(a.name, "gone@") {
				return "", "", errors.New("webfinger: not found")
			}
			return "@" + a.name, a.name, nil
		},
		self: func(context.Context) (string, error) { return self, selfErr },
	}
}

func TestResolveWith(t *testing.T) {
	people := []Person{
		{Slug: "jane", Name: "Jane", Autotag: true, Profiles: []Profile{
			{Platform: "website", URL: "https://jane.example/"},
			{Platform: "pixelfed", URL: "https://pixelfed.example/jane"},
			{Platform: "mastodon", URL: "https://mastodon.example/@jane"},
		}},
		{Slug: "no-consent", Name: "No Consent", Profiles: []Profile{{Platform: "mastodon", URL: "https://mastodon.example/@nc"}}},
		{Slug: "no-profile", Name: "No Profile", Autotag: true, Profiles: []Profile{{Platform: "instagram", URL: "https://instagram.com/np"}}},
		{Slug: "gone", Name: "Gone", Autotag: true, Profiles: []Profile{{Platform: "mastodon", URL: "https://gone.example/@gone"}}},
		{Slug: "owner", Name: "Owner", Autotag: true, Profiles: []Profile{{Platform: "mastodon", URL: "https://mastodon.example/@Owner"}}},
	}

	resolved := 0
	got := resolveWith("guid", "mastodon", people, fakeTarget("owner@mastodon.example", nil, &resolved))
	want := []Credit{
		{Slug: "jane", Name: "Jane", Mention: "@jane@mastodon.example", Account: "jane@mastodon.example"}, // same platform first
		{Slug: "no-consent", Name: "No Consent"},
		{Slug: "no-profile", Name: "No Profile"},
		{Slug: "gone", Name: "Gone"},   // does not resolve
		{Slug: "owner", Name: "Owner"}, // the posting account, case-insensitive
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("credits = %+v\nwant      %+v", got, want)
	}
	if resolved != 3 {
		t.Errorf("%d lookups, want 3 (one per person to be tagged)", resolved)
	}

	// Own identity unknown: everyone is named, no one is mentioned.
	resolved = 0
	got = resolveWith("guid", "mastodon", people, fakeTarget("", errors.New("401"), &resolved))
	if !reflect.DeepEqual(got, plainCredits(people)) || resolved != 0 {
		t.Errorf("credits = %+v after %d lookups, want plain names", got, resolved)
	}

	// No one to tag: the own identity is not even fetched.
	selfCalled := false
	mt := fakeTarget("", nil, &resolved)
	mt.self = func(context.Context) (string, error) { selfCalled = true; return "", nil }
	resolveWith("guid", "mastodon", people[1:3], mt)
	if selfCalled {
		t.Error("own identity fetched although no one can be tagged")
	}
}

func TestResolveWith_MaxMentions(t *testing.T) {
	var people []Person
	for i := 0; i < instagramMaxMentions+2; i++ {
		u := "user" + string(rune('a'+i))
		people = append(people, Person{Slug: u, Name: u, Autotag: true, Profiles: []Profile{{Platform: "instagram", URL: "https://instagram.com/" + u}}})
	}
	mt := mentionTarget{
		profiles:    []string{"instagram"},
		maxMentions: instagramMaxMentions,
		parse:       parseInstagramURL,
		resolve:     metaMention,
		self:        func(context.Context) (string, error) { return "lna_dev", nil },
	}
	got := resolveWith("guid", "instagram", people, mt)
	for i, c := range got {
		if mentioned := c.Mention != ""; mentioned != (i < instagramMaxMentions) {
			t.Errorf("credit %d mention %q", i, c.Mention)
		}
	}
}

func TestResolveCredits_TagModelsOff(t *testing.T) {
	people := []Person{{Slug: "jane", Name: "Jane", Autotag: true, Profiles: []Profile{{Platform: "instagram", URL: "https://instagram.com/jane"}}}}
	target := config.Target{Platform: "instagram"}

	// No network: the connection does not tag.
	if got := resolveCredits("guid", people, config.Connection{}, target); !reflect.DeepEqual(got, plainCredits(people)) {
		t.Errorf("credits = %+v, want plain", got)
	}
	// A person without a profile for the target needs no lookup at all.
	if got := resolveCredits("guid", people, config.Connection{TagModels: true}, config.Target{Platform: "bluesky"}); !reflect.DeepEqual(got, plainCredits(people)) {
		t.Errorf("credits = %+v, want plain", got)
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

	a, ok := parseFediverseURL(srv.URL + "/@jane")
	if !ok {
		t.Fatal("test URL does not parse")
	}
	mention, id, err := fediverseMention(ctx, a)
	if err != nil || mention != "@jane@social.example" || id != "jane@social.example" {
		t.Errorf("mention = %q, %q, %v", mention, id, err)
	}
	if _, _, err := fediverseMention(ctx, account{base: srv.URL, name: "nobody@x"}); err == nil {
		t.Error("unknown account resolved")
	}

	self, err := fediverseSelf(ctx, srv.URL+"/", "lna_dev")
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
		a, ok := parseBlueskyURL(raw)
		if ok != (want != "") || a.name != want {
			t.Errorf("parseBlueskyURL(%q) = %q, %v; want %q", raw, a.name, ok, want)
		}
	}
}

func TestBlueskyLookups(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("actor") {
		case "janedoe.bsky.social", "did:plc:jane":
			io.WriteString(w, `{"did":"did:plc:jane","handle":"jane.example"}`)
		case "broken.bsky.social":
			io.WriteString(w, `{"did":"did:plc:broken","handle":"handle.invalid"}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"InvalidRequest","message":"Profile not found"}`)
		}
	}))
	defer srv.Close()
	prev := blueskyapi.PublicURL
	blueskyapi.PublicURL = srv.URL
	t.Cleanup(func() { blueskyapi.PublicURL = prev })
	ctx := context.Background()

	// An old handle or the DID in the URL: the mention shows the current handle.
	for _, actor := range []string{"janedoe.bsky.social", "did:plc:jane"} {
		mention, did, err := blueskyMention(ctx, account{name: actor})
		if err != nil || mention != "@jane.example" || did != "did:plc:jane" {
			t.Errorf("blueskyMention(%s) = %q, %q, %v", actor, mention, did, err)
		}
	}
	if _, _, err := blueskyMention(ctx, account{name: "broken.bsky.social"}); err == nil {
		t.Error("invalid handle resolved")
	}
	if _, _, err := blueskyMention(ctx, account{name: "nobody.bsky.social"}); err == nil {
		t.Error("unknown profile resolved")
	}

	mt, _ := mentionTargetFor(config.Target{Platform: "bluesky", Username: "janedoe.bsky.social"})
	if self, err := mt.self(ctx); err != nil || self != "did:plc:jane" {
		t.Errorf("self = %q, %v", self, err)
	}
}

func TestBlueskyMentions(t *testing.T) {
	credits := []Credit{
		{Slug: "jane", Name: "Jane", Mention: "@jane.example", Account: "did:plc:jane"},
		{Slug: "max", Name: "Max"},
	}
	want := map[string]string{"@jane.example": "did:plc:jane"}
	if got := blueskyMentions(credits); !reflect.DeepEqual(got, want) {
		t.Errorf("mentions = %v, want %v", got, want)
	}
	if got := blueskyMentions(credits[1:]); got != nil {
		t.Errorf("mentions = %v, want nil", got)
	}
}
