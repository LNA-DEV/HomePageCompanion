package autoupload

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
)

// A blog post gets title, excerpt and link; when space runs out the excerpt
// is cut, never the link (autoposting-ui.md §5.3).
func TestArticleCaptionKeepsTheLink(t *testing.T) {
	item := &feed.Item{Kind: feed.KindArticle, Title: "Unterwegs auf den Lofoten",
		Summary:    strings.Repeat("Zwei Wochen nördlich des Polarkreises. ", 10),
		Link:       "https://example.com/de/posts/2026/06/lofoten/",
		Categories: []string{"Reise", "Norwegen", "meta_skip:instagram"}}
	got := BuildCaption(Settings{}, item, nil, nil, CaptionLimits{MaxLen: 2000}).Text
	if !strings.HasPrefix(got, "Unterwegs auf den Lofoten\n\nZwei Wochen") || !strings.Contains(got, "\n\n"+item.Link+"\n\n#Reise #Norwegen") {
		t.Fatalf("caption:\n%s", got)
	}
	for _, max := range []int{300, 120, 70} {
		got := BuildCaption(Settings{}, item, nil, nil, CaptionLimits{MaxLen: max}).Text
		if runeLen(got) > max || !strings.Contains(got, item.Link) {
			t.Fatalf("max %d: %d chars, link kept: %v\n%s", max, runeLen(got), strings.Contains(got, item.Link), got)
		}
	}
	// A custom template with its own hashtags.
	got = BuildCaption(Settings{Caption: "Neu: {{.Title}} {{.Link}} {{.Hashtags}}"}, item, nil, nil, CaptionLimits{MaxLen: 500, MaxHashtags: 1}).Text
	if got != "Neu: Unterwegs auf den Lofoten https://example.com/de/posts/2026/06/lofoten/ #Reise" {
		t.Fatalf("template: %q", got)
	}
}

// fakeTarget resolves mastodon profiles to "@user@host" except "gone",
// and knows its own account.
type fakeTarget struct {
	social.Publisher
	self     string
	selfErr  error
	resolved int
}

func (f *fakeTarget) ResolveMention(_ context.Context, url string) (social.Mention, error) {
	if !strings.HasPrefix(url, "https://social.example/@") {
		return social.Mention{}, social.ErrNotAProfile
	}
	if f.selfErr != nil {
		return social.Mention{}, f.selfErr
	}
	f.resolved++
	user := strings.TrimPrefix(url, "https://social.example/@")
	switch user {
	case "gone":
		return social.Mention{}, errors.New("webfinger: not found")
	case f.self:
		return social.Mention{}, social.ErrSelf
	}
	return social.Mention{Text: "@" + user + "@social.example", Account: user + "@social.example"}, nil
}

func person(slug string, autotag bool, profiles ...string) feed.Person {
	p := feed.Person{Slug: slug, Name: strings.ToUpper(slug[:1]) + slug[1:], Autotag: autotag}
	for _, u := range profiles {
		kind := "mastodon"
		if strings.Contains(u, "instagram") {
			kind = "instagram"
		}
		p.Profiles = append(p.Profiles, feed.Profile{Platform: kind, URL: u})
	}
	return p
}

func TestResolveCredits(t *testing.T) {
	lim := social.Limits{MentionProfiles: []string{"mastodon", "pixelfed"}}
	people := []feed.Person{
		person("jane", true, "https://instagram.com/jane", "https://social.example/@jane"),
		person("max", false, "https://social.example/@max"), // no consent
		person("gone", true, "https://social.example/@gone"),
		person("lna", true, "https://social.example/@lna"), // the posting account
		person("ina", true), // no profile
	}
	var notTagged []string
	tgt := &fakeTarget{self: "lna"}
	credits := resolveCredits(context.Background(), people, true, lim, tgt, func(slug, reason string) { notTagged = append(notTagged, slug) })
	if credits[0].Mention != "@jane@social.example" || credits[1].Mention != "" || credits[2].Mention != "" || credits[3].Mention != "" || credits[4].Mention != "" {
		t.Fatalf("credits %+v", credits)
	}
	if strings.Join(notTagged, ",") != "gone,lna" {
		t.Fatalf("not tagged %v", notTagged)
	}
	// Tagging off: everyone named, nobody looked up.
	tgt.resolved = 0
	if c := resolveCredits(context.Background(), people, false, lim, tgt, func(string, string) {}); c[0].Mention != "" || tgt.resolved != 0 {
		t.Fatal("mentioned with tagging off")
	}
	// Own identity unknown: nobody is mentioned.
	tgt = &fakeTarget{selfErr: social.ErrIdentityUnknown}
	if c := resolveCredits(context.Background(), people, true, lim, tgt, func(string, string) {}); c[0].Mention != "" {
		t.Fatal("mentioned without knowing the own identity")
	}
	// At most MaxMentions.
	lim.MaxMentions = 1
	many := []feed.Person{person("a", true, "https://social.example/@a"), person("b", true, "https://social.example/@b")}
	c := resolveCredits(context.Background(), many, true, lim, &fakeTarget{}, func(string, string) {})
	if c[0].Mention == "" || c[1].Mention != "" {
		t.Fatalf("max mentions: %+v", c)
	}
}

func TestEvaluateRouting(t *testing.T) {
	for _, tc := range []struct {
		tags     []string
		platform string
		allow    bool
	}{
		{nil, "threads", true},
		{[]string{"Vögel"}, "threads", true},
		{[]string{"META_SKIP:Threads"}, "threads", false},
		{[]string{"meta_skip:threads"}, "mastodon", true},
		{[]string{"meta_only:instagram"}, "instagram", true},
		{[]string{"meta_only:instagram"}, "mastodon", false},
		{[]string{"meta_only:instagram", "meta_skip:instagram"}, "instagram", false},
	} {
		if got := EvaluateRouting(tc.tags, tc.platform); got.Allow != tc.allow {
			t.Errorf("%v on %s: %+v", tc.tags, tc.platform, got)
		}
	}
}

func date(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 12, 0, 0, 0, time.UTC) }

func photo(id string, published time.Time) feed.Item {
	return feed.Item{ID: id, Kind: feed.KindPhoto, Published: published, HasDate: !published.IsZero(), Image: &feed.Image{URL: id}}
}

func TestChooseRotation(t *testing.T) {
	now := date(2026, 10, 20)
	items := []feed.Item{photo("far", date(2020, 3, 1)), photo("b", date(2019, 10, 19)), photo("a", date(2023, 10, 19)),
		photo("near", date(2021, 10, 20)), photo("undated", time.Time{}), photo("done", date(2022, 10, 20))}
	hist := map[string]history{"done": {done: true}}
	got := chooseRotation(items, hist, now)
	var ids []string
	for _, c := range got {
		ids = append(ids, c.item.ID)
	}
	// Closest to today ignoring the year; equal distance by ID; undated last.
	if strings.Join(ids, ",") != "near,a,b,far,undated" {
		t.Fatalf("order %v", ids)
	}

	// A transient failure comes back after 1, 3 and 7 days, first in line;
	// account and content failures only by hand.
	hist = map[string]history{
		"near": {openFails: 1, lastFail: now.Add(-25 * time.Hour)},
		"a":    {openFails: 2, lastFail: now.Add(-2 * 24 * time.Hour)},
		"b":    {openFails: 1, lastFail: now.Add(-48 * time.Hour), manualOnly: true},
		"far":  {openFails: 4, lastFail: now.Add(-30 * 24 * time.Hour)},
	}
	got = chooseRotation(items, hist, now)
	ids = ids[:0]
	for _, c := range got {
		ids = append(ids, c.item.ID)
	}
	if strings.Join(ids, ",") != "near,done,undated" || !got[0].retry {
		t.Fatalf("with failures: %v", ids)
	}
}

func TestWindow(t *testing.T) {
	c := &Connection{Window: "08:00-21:00", location: time.UTC}
	for in, want := range map[string]string{
		"2026-10-20T06:30:00Z": "2026-10-20T08:00:00Z",
		"2026-10-20T12:00:00Z": "2026-10-20T12:00:00Z",
		"2026-10-20T21:30:00Z": "2026-10-21T08:00:00Z",
	} {
		ti, _ := time.Parse(time.RFC3339, in)
		if got := c.inWindow(ti).Format(time.RFC3339); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
	for _, bad := range []string{"8-21", "21:00-08:00", "x"} {
		if _, _, err := parseWindow(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestLegacyRef(t *testing.T) {
	id, url, v := legacyRef("bluesky", "at://did:plc:abc/app.bsky.feed.post/3l5x", "cid1", "")
	if id != "at://did:plc:abc/app.bsky.feed.post/3l5x" || url != "https://bsky.app/profile/did:plc:abc/post/3l5x" || v != "cid1" {
		t.Fatal(id, url, v)
	}
	if id, url, _ := legacyRef("mastodon", "https://m/1", "", "1"); id != "1" || url != "https://m/1" {
		t.Fatal(id, url)
	}
}
