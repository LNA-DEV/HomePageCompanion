package autouploader

import (
	"strings"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/mmcdole/gofeed"
)

func TestMaxHashtagsFor(t *testing.T) {
	if got := maxHashtagsFor("threads"); got != 1 {
		t.Errorf("maxHashtagsFor(threads) = %d, want 1", got)
	}
	for _, p := range []string{"instagram", "mastodon", "pixelfed", "bluesky", "unknown"} {
		if got := maxHashtagsFor(p); got != 0 {
			t.Errorf("maxHashtagsFor(%q) = %d, want 0 (unlimited)", p, got)
		}
	}
}

func TestBuildCaption_BaseText(t *testing.T) {
	entry := &gofeed.Item{
		Description: `<img src="x.jpg" alt="Sunset over the sea with a ferry and birds" />`,
	}
	conn := config.Connection{Caption: "More at lna-dev.net"}

	// Default (IncludeAltText unset): caption only — matches historical behaviour.
	if got := BuildCaption(conn, entry, nil, nil, 500, 0); got != "More at lna-dev.net" {
		t.Errorf("default base = %q, want caption only", got)
	}

	// Alt text only: enable alt and leave the caption empty.
	connAlt := config.Connection{IncludeAltText: true}
	if got := BuildCaption(connAlt, entry, nil, nil, 500, 0); got != "Sunset over the sea with a ferry and birds" {
		t.Errorf("alt-only base = %q, want alt text only", got)
	}

	// Both present: alt text first, then caption, separated by a blank line.
	connBoth := conn
	connBoth.IncludeAltText = true
	want := "Sunset over the sea with a ferry and birds\n\nMore at lna-dev.net"
	if got := BuildCaption(connBoth, entry, nil, nil, 500, 0); got != want {
		t.Errorf("both base = %q, want %q", got, want)
	}

	// Alt requested but the item has none: falls back cleanly to the caption.
	if got := BuildCaption(connBoth, &gofeed.Item{Description: "<p>no image</p>"}, nil, nil, 500, 0); got != "More at lna-dev.net" {
		t.Errorf("missing-alt base = %q, want caption fallback", got)
	}
}

func TestBuildCaption_HashtagCap(t *testing.T) {
	entry := &gofeed.Item{
		Categories: []string{"photo", "photography", "sunset", "sea"},
	}
	conn := config.Connection{Caption: "More at lna-dev.net"}

	// Threads caps hashtags at 1: only the first category survives, so the
	// post does not get cluttered with dead, non-clickable extra tags.
	threads := BuildCaption(conn, entry, nil, nil, 500, 1)
	if !strings.Contains(threads, "#photo") {
		t.Errorf("threads caption missing first hashtag: %q", threads)
	}
	for _, unwanted := range []string{"#photography", "#sunset", "#sea"} {
		if strings.Contains(threads, unwanted) {
			t.Errorf("threads caption should not contain %q (Threads indexes one tag): %q", unwanted, threads)
		}
	}

	// Unlimited (0) keeps every hashtag, as before.
	all := BuildCaption(conn, entry, nil, nil, 2000, 0)
	for _, want := range []string{"#photo", "#photography", "#sunset", "#sea"} {
		if !strings.Contains(all, want) {
			t.Errorf("unlimited caption missing %q: %q", want, all)
		}
	}
}
