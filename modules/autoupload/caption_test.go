package autoupload

import (
	"reflect"
	"strings"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
)

func TestBuildCaption_BaseText(t *testing.T) {
	entry := &feed.Item{
		Image: &feed.Image{Alt: "Sunset over the sea with a ferry and birds"},
	}
	conn := Settings{Caption: "More at lna-dev.net"}

	// Default (IncludeAltText unset): caption only — matches historical behaviour.
	if got := BuildCaption(conn, entry, nil, nil, CaptionLimits{MaxLen: 500}).Text; got != "More at lna-dev.net" {
		t.Errorf("default base = %q, want caption only", got)
	}

	// Alt text only: enable alt and leave the caption empty.
	connAlt := Settings{IncludeAltText: true}
	if got := BuildCaption(connAlt, entry, nil, nil, CaptionLimits{MaxLen: 500}).Text; got != "Sunset over the sea with a ferry and birds" {
		t.Errorf("alt-only base = %q, want alt text only", got)
	}

	// Both present: alt text first, then caption, separated by a blank line.
	connBoth := conn
	connBoth.IncludeAltText = true
	want := "Sunset over the sea with a ferry and birds\n\nMore at lna-dev.net"
	if got := BuildCaption(connBoth, entry, nil, nil, CaptionLimits{MaxLen: 500}).Text; got != want {
		t.Errorf("both base = %q, want %q", got, want)
	}

	// Alt requested but the item has none: falls back cleanly to the caption.
	if got := BuildCaption(connBoth, &feed.Item{}, nil, nil, CaptionLimits{MaxLen: 500}).Text; got != "More at lna-dev.net" {
		t.Errorf("missing-alt base = %q, want caption fallback", got)
	}
}

func TestBuildCaption_HashtagCap(t *testing.T) {
	entry := &feed.Item{
		Categories: []string{"photo", "photography", "sunset", "sea"},
	}
	conn := Settings{Caption: "More at lna-dev.net"}

	// Threads caps hashtags at 1: only the first category survives, so the
	// post does not get cluttered with dead, non-clickable extra tags.
	threads := BuildCaption(conn, entry, nil, nil, CaptionLimits{MaxLen: 500, MaxHashtags: 1}).Text
	if !strings.Contains(threads, "#photo") {
		t.Errorf("threads caption missing first hashtag: %q", threads)
	}
	for _, unwanted := range []string{"#photography", "#sunset", "#sea"} {
		if strings.Contains(threads, unwanted) {
			t.Errorf("threads caption should not contain %q (Threads indexes one tag): %q", unwanted, threads)
		}
	}

	// Unlimited (0) keeps every hashtag, as before.
	all := BuildCaption(conn, entry, nil, nil, CaptionLimits{MaxLen: 2000}).Text
	for _, want := range []string{"#photo", "#photography", "#sunset", "#sea"} {
		if !strings.Contains(all, want) {
			t.Errorf("unlimited caption missing %q: %q", want, all)
		}
	}
}

func TestBuildCaption_SectionOrder(t *testing.T) {
	entry := &feed.Item{Categories: []string{"photo", "sunset"}, Copyright: "Lukas Nagel"}
	conn := Settings{Caption: "More at lna-dev.net", CopyrightSource: "rss"}

	// Hashtags come last so Mastodon can render them in its hashtag bar.
	want := "More at lna-dev.net\n\n© Lukas Nagel\n\n#photo #sunset"
	if got := BuildCaption(conn, entry, nil, nil, CaptionLimits{MaxLen: 2000}).Text; got != want {
		t.Errorf("caption = %q, want %q", got, want)
	}
}

func TestThreadsLen(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"plain", 5},
		{"Jöhn Döe", 8},        // umlauts count as one
		{"📷 Sony", 9},          // 📷 is four bytes
		{"© Lukas", 8},         // © is a symbol: two bytes
		{"👍🏽", 8},              // emoji + skin tone modifier
		{"❤️", 6},              // U+2764 + variation selector
		{"f/2.8 · ISO100", 14}, // the middle dot is punctuation
	}
	for _, c := range cases {
		if got := threadsLen(c.in); got != c.want {
			t.Errorf("threadsLen(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestBuildCaption_ThreadsCountsEmojiBytes(t *testing.T) {
	// "<caption>\n\n© X" is 497 runes but 498 for Threads (© is two bytes).
	caption := strings.Repeat("a", 492)
	entry := &feed.Item{Copyright: "X"}
	conn := Settings{Caption: caption, CopyrightSource: "rss"}

	runes := BuildCaption(conn, entry, nil, nil, CaptionLimits{MaxLen: 497}).Text
	if !strings.HasSuffix(runes, "© X") {
		t.Errorf("rune-counted caption dropped the copyright line: %q", runes)
	}
	threads := BuildCaption(conn, entry, nil, nil, CaptionLimits{MaxLen: 497, Len: threadsLen}).Text
	if threads != caption {
		t.Errorf("threads caption = %q, want the copyright line dropped", threads)
	}
}

func TestNamesLine(t *testing.T) {
	a := Credit{Slug: "a", Name: "Jane Doe", Mention: "@janedoe"}
	b := Credit{Slug: "b", Name: "Max Muster"}
	c := Credit{Slug: "c", Name: "Markus Penninger"}
	cases := []struct {
		credits     []Credit
		prefix, and string
		want        string
	}{
		{nil, "With", "and", ""},
		{[]Credit{c}, "With", "and", "With Markus Penninger"},
		{[]Credit{a, c}, "With", "and", "With Jane Doe (@janedoe) and Markus Penninger"},
		{[]Credit{a, b, c}, "With", "and", "With Jane Doe (@janedoe), Max Muster and Markus Penninger"},
		{[]Credit{a, b, c}, "Mit", "und", "Mit Jane Doe (@janedoe), Max Muster und Markus Penninger"},
	}
	for _, tc := range cases {
		if got := namesLine(tc.credits, tc.prefix, tc.and); got != tc.want {
			t.Errorf("namesLine = %q, want %q", got, tc.want)
		}
	}
}

func TestBuildCaption_Names(t *testing.T) {
	entry := &feed.Item{
		Image:      &feed.Image{Alt: "Two people on a pier"},
		Categories: []string{"portrait"},
		Copyright:  "Lukas Nagel",
	}
	credits := []Credit{{Slug: "jane-doe", Name: "Jane Doe"}, {Slug: "markus-penninger", Name: "Markus Penninger"}}
	conn := Settings{IncludeAltText: true, Caption: "More at lna-dev.net", CopyrightSource: "rss"}

	// The names follow the alt text; the caption stays the last line of text.
	want := "Two people on a pier\n\nWith Jane Doe and Markus Penninger\n\nMore at lna-dev.net\n\n© Lukas Nagel\n\n#portrait"
	if got := BuildCaption(conn, entry, nil, credits, CaptionLimits{MaxLen: 2000}).Text; got != want {
		t.Errorf("caption = %q\nwant      %q", got, want)
	}

	// Without alt text the names come first.
	conn.IncludeAltText = false
	conn.ModelsPrefix, conn.ModelsAnd = "Mit", "und"
	want = "Mit Jane Doe und Markus Penninger\n\nMore at lna-dev.net\n\n© Lukas Nagel\n\n#portrait"
	if got := BuildCaption(conn, entry, nil, credits, CaptionLimits{MaxLen: 2000}).Text; got != want {
		t.Errorf("caption = %q\nwant      %q", got, want)
	}
}

// longAlt is longer than the longest alt text in the live feed (404).
var longAlt = strings.Repeat("A long description of the photo. ", 12) + "It ends here, finally."

func TestBuildCaption_BlueskyCutsAltTextFirst(t *testing.T) {
	if n := runeLen(longAlt); n != 418 {
		t.Fatalf("fixture length %d", n)
	}
	entry := &feed.Item{
		Image:      &feed.Image{Alt: longAlt},
		Categories: []string{"portrait", "people"},
	}
	credits := []Credit{{Slug: "jane-doe", Name: "Jane Doe"}, {Slug: "markus-penninger", Name: "Markus Penninger"}}
	conn := Settings{IncludeAltText: true, Caption: "More at https://lna-dev.net/en/gallery/"}

	got := BuildCaption(conn, entry, nil, credits, CaptionLimits{MaxLen: 300})
	if runeLen(got.Text) > 300 {
		t.Fatalf("caption is %d characters: %q", runeLen(got.Text), got.Text)
	}
	sections := strings.Split(got.Text, "\n\n")
	if len(sections) != 3 {
		t.Fatalf("sections = %q, want alt text, names, caption (hashtags dropped first)", sections)
	}
	if !strings.HasPrefix(longAlt, sections[0]) || !strings.HasSuffix(sections[0], ".") {
		t.Errorf("alt text not cut after a sentence: %q", sections[0])
	}
	if sections[1] != "With Jane Doe and Markus Penninger" || sections[2] != conn.Caption {
		t.Errorf("names or caption not whole: %q", sections[1:])
	}
}

func TestBuildCaption_CutsCaptionAfterAltText(t *testing.T) {
	entry := &feed.Item{Image: &feed.Image{Alt: longAlt}}
	credits := []Credit{{Slug: "jane-doe", Name: "Jane Doe"}}
	conn := Settings{IncludeAltText: true, Caption: "See the whole series and many more photos at https://lna-dev.net/en/gallery/"}

	// Room for the names line and part of the caption, nothing for the alt text.
	got := BuildCaption(conn, entry, nil, credits, CaptionLimits{MaxLen: 40}).Text
	want := "With Jane Doe\n\nSee the whole series and…"
	if got != want {
		t.Errorf("caption = %q, want %q", got, want)
	}
}

func TestBuildCaption_MentionsFallBackToNames(t *testing.T) {
	credits := []Credit{
		{Slug: "jane-doe", Name: "Jane Doe", Mention: "@janedoe"},
		{Slug: "max-muster", Name: "Max Muster", Mention: "@maxmuster"},
	}
	conn := Settings{Caption: "More at lna-dev.net"}

	full := "With Jane Doe (@janedoe) and Max Muster (@maxmuster)"
	got := BuildCaption(conn, nil, nil, credits, CaptionLimits{MaxLen: runeLen(full)})
	// The caption goes first (step 5), then the last person's mention (step 6).
	if got.Text != full || got.Unmentioned != nil {
		t.Errorf("caption = %q, unmentioned %v", got.Text, got.Unmentioned)
	}

	got = BuildCaption(conn, nil, nil, credits, CaptionLimits{MaxLen: runeLen(full) - 1})
	if want := "With Jane Doe (@janedoe) and Max Muster"; got.Text != want {
		t.Errorf("caption = %q, want %q", got.Text, want)
	}
	if !reflect.DeepEqual(got.Unmentioned, []string{"max-muster"}) || got.NamesCut != nil {
		t.Errorf("unmentioned = %v, names cut = %v", got.Unmentioned, got.NamesCut)
	}
	if credits[1].Mention == "" {
		t.Error("BuildCaption changed the caller's credits")
	}
}

func TestBuildCaption_NamesLineCutLast(t *testing.T) {
	credits := []Credit{
		{Slug: "jane-doe", Name: "Jane Doe", Mention: "@janedoe"},
		{Slug: "max-muster", Name: "Max Muster"},
		{Slug: "markus-penninger", Name: "Markus Penninger"},
	}
	entry := &feed.Item{Image: &feed.Image{Alt: "Three people."}, Categories: []string{"people"}}
	conn := Settings{IncludeAltText: true, Caption: "More at lna-dev.net"}

	got := BuildCaption(conn, entry, nil, credits, CaptionLimits{MaxLen: 30})
	if want := "With Jane Doe, Max Muster…"; got.Text != want {
		t.Errorf("caption = %q, want %q", got.Text, want)
	}
	if !reflect.DeepEqual(got.Unmentioned, []string{"jane-doe"}) {
		t.Errorf("unmentioned = %v", got.Unmentioned)
	}
	if !reflect.DeepEqual(got.NamesCut, []string{"markus-penninger"}) {
		t.Errorf("names cut = %v", got.NamesCut)
	}

	// Not even the first name fits: cut inside the line.
	got = BuildCaption(conn, entry, nil, credits[:1], CaptionLimits{MaxLen: 11})
	if want := "With Jane…"; got.Text != want {
		t.Errorf("caption = %q, want %q", got.Text, want)
	}
	if !reflect.DeepEqual(got.NamesCut, []string{"jane-doe"}) {
		t.Errorf("names cut = %v", got.NamesCut)
	}
}

func TestShorten(t *testing.T) {
	cases := []struct {
		name, text string
		max        int
		want       string
	}{
		{"fits", "Short.", 10, "Short."},
		{"after a sentence", "First sentence here. Second one is longer.", 30, "First sentence here."},
		{"sentence keeps under half", "Hi. Then a long second sentence follows.", 30, "Hi. Then a long second…"},
		{"at a space", "Sunset over the sea, with a ferry", 22, "Sunset over the sea…"},
		{"hard cut of one long word", "Donaudampfschifffahrt", 10, "Donaudamp…"},
		{"URL kept whole", "More at https://lna-dev.net/en/gallery/ today", 40, "More at https://lna-dev.net/en/gallery/…"},
		{"URL dropped", "More at https://lna-dev.net/en/gallery/", 30, "More at…"},
		{"URL alone dropped", "https://lna-dev.net/en/gallery/", 20, ""},
		{"no room", "Anything", 1, ""},
	}
	for _, tc := range cases {
		if got := shorten(tc.text, tc.max, runeLen); got != tc.want {
			t.Errorf("%s: shorten(%q, %d) = %q, want %q", tc.name, tc.text, tc.max, got, tc.want)
		}
	}

	// Never longer than max, whatever max is.
	for _, length := range []func(string) int{runeLen, threadsLen} {
		for max := 0; max <= runeLen(longAlt)+5; max++ {
			if got := shorten("📷 "+longAlt, max, length); length(got) > max {
				t.Fatalf("shorten(…, %d) is %d long: %q", max, length(got), got)
			}
		}
	}
}
