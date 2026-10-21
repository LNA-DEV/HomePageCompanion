package rss

import (
	"html"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/mmcdole/gofeed"
)

var altPattern = regexp.MustCompile(`alt="(.*?)"`)

// altText is the image's alt text from the item's HTML description.
func altText(description string) string {
	if m := altPattern.FindStringSubmatch(description); len(m) > 1 {
		return m[1]
	}
	return ""
}

var tagPattern = regexp.MustCompile(`<[^>]*>`)

// summaryOf is a plain-text excerpt: the feed's description without HTML,
// at most 300 characters, cut at a word.
func summaryOf(item *gofeed.Item) string {
	text := item.Description
	if text == "" {
		text = item.Content
	}
	text = html.UnescapeString(tagPattern.ReplaceAllString(text, " "))
	text = strings.Join(strings.Fields(text), " ")
	const max = 300
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	r := []rune(text)[:max]
	cut := string(r)
	if i := strings.LastIndexByte(cut, ' '); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

// imageOf returns the item's image (media:content or <image>) and the
// original file (the first enclosure), which keeps the EXIF intact.
func imageOf(item *gofeed.Item) (url, original string) {
	if item.Image != nil {
		url = item.Image.URL
	}
	for _, enc := range item.Enclosures {
		if enc != nil && enc.URL != "" && (enc.Type == "" || strings.HasPrefix(enc.Type, "image/")) {
			original = enc.URL
			break
		}
	}
	if url == "" {
		url = original
	}
	if original == url {
		original = ""
	}
	return url, original
}

// peopleOf reads the lna:model blocks of an item in feed order. A block
// without a name is skipped, because the person cannot be named
// (docs/concepts/autouploader-model-tags.md).
func peopleOf(item *gofeed.Item, skipped func(slug string)) []feed.Person {
	var people []feed.Person
	for _, m := range item.Extensions["lna"]["model"] {
		slug := strings.TrimSpace(m.Attrs["slug"])
		name := strings.Join(strings.Fields(m.Attrs["name"]), " ")
		if name == "" {
			if skipped != nil {
				skipped(slug)
			}
			continue
		}
		p := feed.Person{Slug: slug, Name: name, Autotag: m.Attrs["autotag"] == "true"}
		for _, pr := range m.Children["profile"] {
			p.Profiles = append(p.Profiles, feed.Profile{
				Platform: strings.ToLower(strings.TrimSpace(pr.Attrs["platform"])),
				URL:      strings.TrimSpace(pr.Attrs["url"]),
			})
		}
		people = append(people, p)
	}
	return people
}

// kindOf is "photo" for an item with an image, "article" otherwise, unless
// the feed's config fixes the kind.
func kindOf(fixed string, imageURL string) string {
	switch fixed {
	case feed.KindPhoto, feed.KindArticle:
		return fixed
	}
	if imageURL != "" {
		return feed.KindPhoto
	}
	return feed.KindArticle
}

// toItem maps a parsed item to a row; seen is when it was fetched.
func toItem(instance, fixedKind string, it *gofeed.Item, seen time.Time, skipped func(string)) Item {
	img, orig := imageOf(it)
	row := Item{
		Instance: instance, GUID: it.GUID, Title: it.Title, Summary: summaryOf(it), Description: it.Description,
		Link: it.Link, ImageURL: img, OriginalURL: orig, Alt: altText(it.Description),
		Categories: append([]string(nil), it.Categories...), People: peopleOf(it, skipped),
		Kind: kindOf(fixedKind, img), Published: seen,
	}
	if it.PublishedParsed != nil && it.PublishedParsed.Year() > 1 {
		row.Published, row.HasDate = *it.PublishedParsed, true
	} else if it.UpdatedParsed != nil && it.UpdatedParsed.Year() > 1 {
		row.Published, row.HasDate = *it.UpdatedParsed, true
	}
	return row
}
