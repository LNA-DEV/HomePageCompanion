package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// facet is the part of a facet the tests compare.
type facet struct {
	start, end int
	kind, val  string
}

func facetsOf(t *testing.T, text string, mentions map[string]string) []facet {
	t.Helper()
	var out []facet
	for _, f := range ExtractFacets(text, mentions) {
		idx := f["index"].(map[string]int)
		feat := f["features"].([]interface{})[0].(map[string]interface{})
		var val string
		for _, k := range []string{"did", "tag", "uri"} {
			if v, ok := feat[k]; ok {
				val = v.(string)
			}
		}
		out = append(out, facet{idx["byteStart"], idx["byteEnd"], feat["$type"].(string), val})
	}
	return out
}

func TestExtractFacets_Mentions(t *testing.T) {
	// Non-ASCII before the mention: offsets are UTF-8 bytes, not runes.
	text := "Jöhn Döe (@x.bsky.social)\n\n#portrait"
	got := facetsOf(t, text, map[string]string{"@x.bsky.social": "did:plc:x"})

	start := len("Jöhn Döe (")
	want := []facet{
		{start, start + len("@x.bsky.social"), "app.bsky.richtext.facet#mention", "did:plc:x"},
		{len(text) - len("#portrait"), len(text), "app.bsky.richtext.facet#tag", "portrait"},
	}
	if len(got) != len(want) {
		t.Fatalf("facets = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("facet %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if start != 12 {
		t.Errorf("byte offset %d, want 12 (ö is two bytes)", start)
	}
	if text[got[0].start:got[0].end] != "@x.bsky.social" {
		t.Errorf("facet covers %q", text[got[0].start:got[0].end])
	}
}

func TestExtractFacets_MentionBoundaries(t *testing.T) {
	m := map[string]string{"@x.bsky.social": "did:plc:x"}
	cases := map[string]int{
		"With A (@x.bsky.social) and B (@x.bsky.social)": 2,
		"@x.bsky.social. Next sentence":                  1,
		"@x.bsky.socialite":                              0,
		"@x.bsky.social.uk":                              0,
		"no mention here":                                0,
	}
	for text, want := range cases {
		if got := len(ExtractFacets(text, m)); got != want {
			t.Errorf("%q: %d facets, want %d", text, got, want)
		}
	}
	if got := len(ExtractFacets("With A (@x.bsky.social)", nil)); got != 0 {
		t.Errorf("nil mentions: %d facets", got)
	}
}

func TestGetProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/xrpc/app.bsky.actor.getProfile" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("actor") != "jane.example" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"did":"did:plc:jane","handle":"jane.example","displayName":"Jane"}`)
	}))
	defer srv.Close()
	prev := PublicURL
	PublicURL = srv.URL
	t.Cleanup(func() { PublicURL = prev })

	p, err := GetProfile(context.Background(), "jane.example")
	if err != nil || p.Did != "did:plc:jane" || p.Handle != "jane.example" {
		t.Errorf("GetProfile = %+v, %v", p, err)
	}
	if _, err := GetProfile(context.Background(), "nobody.example"); err == nil {
		t.Error("unknown profile found")
	}
}
