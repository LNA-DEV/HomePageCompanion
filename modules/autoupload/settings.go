package autoupload

import (
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
)

// Settings are the caption settings of a connection, as config.yaml's
// connections[] had them.
type Settings struct {
	// Caption is the text of every post. It may use {{.Title}},
	// {{.Summary}}, {{.Link}} and {{.Hashtags}}; a blog post with an empty
	// caption gets "{{.Title}}\n\n{{.Summary}}\n\n{{.Link}}". The link is
	// never cut.
	Caption string `json:"caption"`

	// IncludeAltText puts the image's alt text first.
	IncludeAltText bool `json:"includeAltText"`

	// TagModels mentions the people in a photo on the target, for those
	// whose feed entry has autotag="true". Off: everyone is named without
	// a mention.
	TagModels bool `json:"tagModels"`

	// ModelsPrefix and ModelsAnd are the words of the names line: "With A,
	// B and C". A German connection sets "Mit" and "und".
	ModelsPrefix string `json:"modelsPrefix"`
	ModelsAnd    string `json:"modelsAnd"`

	// RoutingTagsSource selects where meta_skip:<platform> /
	// meta_only:<platform> tags come from: "" (off), "rss" or "exif".
	RoutingTagsSource string `json:"routingTagsSource"`

	// AddExifToCaption appends a compact EXIF line (camera, lens, exposure).
	AddExifToCaption bool `json:"addExifToCaption"`

	// CopyrightSource appends a copyright line: "" (off), "rss" or "exif".
	CopyrightSource string `json:"copyrightSource"`
}

// defaultArticleCaption is the caption of a blog post when none is set.
const defaultArticleCaption = "{{.Title}}\n\n{{.Summary}}\n\n{{.Link}}"

// captionFor returns the caption template for an item.
func (s Settings) captionFor(item *feed.Item) string {
	if strings.TrimSpace(s.Caption) == "" && item != nil && item.Kind == feed.KindArticle {
		return defaultArticleCaption
	}
	return s.Caption
}

// templated reports whether the caption uses placeholders.
func templated(caption string) bool { return strings.Contains(caption, "{{") }

// usesHashtags reports whether the caption places the hashtags itself.
func usesHashtags(caption string) bool { return strings.Contains(caption, ".Hashtags") }

type captionData struct {
	Title, Summary, Link, Hashtags string
}

// expand fills the caption's placeholders. A caption that does not parse
// is posted as written.
func expand(caption string, item *feed.Item, hashtags []string) string {
	if !templated(caption) {
		return caption
	}
	d := captionData{Hashtags: strings.Join(hashtags, " ")}
	if item != nil {
		d.Title, d.Summary, d.Link = item.Title, item.Summary, item.Link
	}
	return render(caption, d)
}

func render(caption string, d captionData) string {
	t, err := template.New("caption").Option("missingkey=zero").Parse(caption)
	if err != nil {
		return caption
	}
	var b strings.Builder
	if err := t.Execute(&b, d); err != nil {
		return caption
	}
	// Empty placeholders leave runs of blank lines.
	out := b.String()
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(out)
}

// fitTemplate renders the caption into limit: the summary is shortened
// first, then the title; the link never.
func fitTemplate(caption string, item *feed.Item, hashtags []string, limit int, length func(string) int) string {
	d := captionData{Hashtags: strings.Join(hashtags, " ")}
	if item != nil {
		d.Title, d.Summary, d.Link = item.Title, item.Summary, item.Link
	}
	if out := render(caption, d); length(out) <= limit {
		return out
	}
	// Longest summary that fits.
	lo, hi := 0, utf8.RuneCountInString(d.Summary)
	full := d.Summary
	best := ""
	for lo <= hi {
		mid := (lo + hi) / 2
		d.Summary = shorten(full, mid, length)
		if out := render(caption, d); length(out) <= limit {
			best, lo = out, mid+1
		} else {
			hi = mid - 1
		}
	}
	if best != "" && strings.Contains(caption, ".Summary") {
		return best
	}
	d.Summary = ""
	fullTitle := d.Title
	lo, hi = 0, utf8.RuneCountInString(fullTitle)
	for lo <= hi {
		mid := (lo + hi) / 2
		d.Title = shorten(fullTitle, mid, length)
		if out := render(caption, d); length(out) <= limit {
			best, lo = out, mid+1
		} else {
			hi = mid - 1
		}
	}
	return best
}
