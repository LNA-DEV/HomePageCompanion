package autouploader

import (
	"fmt"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/imagemeta"
	"github.com/mmcdole/gofeed"
)

// BuildCaption composes the final caption for a publish. Sections are joined
// by blank lines:
//
//	<base text: image alt text and/or connection.Caption — see buildBaseText>
//	<hashtags from entry.Categories, excluding meta_* tags>
//	<optional EXIF line>
//	<optional copyright line>
//
// When the resulting caption exceeds maxLen, optional sections are dropped in
// reverse priority order (copyright → EXIF → trailing hashtags) until it fits.
//
// maxHashtags caps how many hashtags are emitted (0 = unlimited). Threads
// indexes only a single topic tag per post and renders every extra #tag as
// dead, non-clickable clutter, so the Threads path passes 1.
//
// imageBytes may be nil when no EXIF/copyright-from-exif feature is enabled.
// feed may be nil when no copyright-from-rss feature is enabled.
func BuildCaption(conn config.Connection, entry *gofeed.Item, feed *gofeed.Feed, imageBytes []byte, maxLen, maxHashtags int) string {
	if maxLen <= 0 {
		maxLen = 2000
	}

	base := buildBaseText(conn, entry)
	hashtags := buildHashtags(entry)
	if maxHashtags > 0 && len(hashtags) > maxHashtags {
		hashtags = hashtags[:maxHashtags]
	}
	exifLine := ""
	copyrightLine := ""

	var meta *imagemeta.Metadata
	if conn.AddExifToCaption || conn.CopyrightSource == "exif" {
		if len(imageBytes) > 0 {
			if extracted, _ := imagemeta.Extract(imageBytes); extracted != nil {
				meta = extracted
			}
		}
	}

	if conn.AddExifToCaption && meta != nil {
		exifLine = formatExifLine(meta)
	}

	if conn.CopyrightSource == "rss" && feed != nil && strings.TrimSpace(feed.Copyright) != "" {
		copyrightLine = "© " + strings.TrimSpace(feed.Copyright)
	} else if conn.CopyrightSource == "exif" && meta != nil {
		switch {
		case strings.TrimSpace(meta.Copyright) != "":
			copyrightLine = "© " + strings.TrimSpace(meta.Copyright)
		case strings.TrimSpace(meta.Artist) != "":
			copyrightLine = "© " + strings.TrimSpace(meta.Artist)
		}
	}

	// Assemble sections in priority order; drop low-priority sections until
	// the joined result fits within maxLen.
	assemble := func(hashtagSlice []string, withExif, withCopyright bool) string {
		sections := []string{}
		if base != "" {
			sections = append(sections, base)
		}
		if len(hashtagSlice) > 0 {
			sections = append(sections, strings.Join(hashtagSlice, " "))
		}
		if withExif && exifLine != "" {
			sections = append(sections, exifLine)
		}
		if withCopyright && copyrightLine != "" {
			sections = append(sections, copyrightLine)
		}
		return strings.Join(sections, "\n\n")
	}

	// 1. Try with everything.
	if out := assemble(hashtags, true, true); runeLen(out) <= maxLen {
		return out
	}
	// 2. Drop copyright.
	if out := assemble(hashtags, true, false); runeLen(out) <= maxLen {
		return out
	}
	// 3. Drop EXIF too.
	if out := assemble(hashtags, false, false); runeLen(out) <= maxLen {
		return out
	}
	// 4. Drop trailing hashtags one at a time.
	for len(hashtags) > 0 {
		hashtags = hashtags[:len(hashtags)-1]
		if out := assemble(hashtags, false, false); runeLen(out) <= maxLen {
			return out
		}
	}
	// 5. Last resort: truncate the base caption.
	if runeLen(base) > maxLen {
		runes := []rune(base)
		return string(runes[:maxLen])
	}
	return base
}

// buildBaseText composes the leading text block of a caption. The configured
// Caption is always included when non-empty; when IncludeAltText is set, the
// image alt text (parsed from the RSS item) is prepended above it. Sections are
// separated by a blank line to match the rest of BuildCaption's layout.
//
// To publish without a caption, leave Caption empty — there is no separate
// toggle for it.
func buildBaseText(conn config.Connection, entry *gofeed.Item) string {
	parts := make([]string, 0, 2)

	if conn.IncludeAltText && entry != nil {
		if alt := strings.TrimSpace(extractAltText(entry.Description)); alt != "" {
			parts = append(parts, alt)
		}
	}

	if caption := strings.TrimSpace(conn.Caption); caption != "" {
		parts = append(parts, caption)
	}

	return strings.Join(parts, "\n\n")
}

// buildHashtags converts entry.Categories into `#tag` strings, skipping any
// meta_* routing tags so they don't leak into the public post.
func buildHashtags(entry *gofeed.Item) []string {
	if entry == nil {
		return nil
	}
	out := make([]string, 0, len(entry.Categories))
	for _, c := range entry.Categories {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(c), "meta_") {
			continue
		}
		out = append(out, "#"+c)
	}
	return out
}

// runeLen counts visible characters, not bytes — Bluesky's 300-char limit
// is measured in characters.
func runeLen(s string) int { return len([]rune(s)) }

// --- EXIF caption-line presentation ------------------------------------------

// formatExifLine renders a compact "📷 Camera · Lens · f/X 1/Ys ISOZ" line
// from extracted metadata. Returns "" when nothing useful is present.
func formatExifLine(m *imagemeta.Metadata) string {
	if m == nil {
		return ""
	}
	camera := joinCamera(m.Make, m.Model)
	lensExposure := joinLensExposure(m.Lens, m.FocalLengthMM, m.FNumber, m.ExposureSeconds, m.ISO)
	switch {
	case camera != "" && lensExposure != "":
		return "📷 " + camera + " · " + lensExposure
	case camera != "":
		return "📷 " + camera
	case lensExposure != "":
		return "📷 " + lensExposure
	default:
		return ""
	}
}

// joinCamera composes the camera name, stripping a duplicate make-prefix
// from model (e.g. "Sony Sony α6400" → "Sony α6400").
func joinCamera(make, model string) string {
	make = strings.TrimSpace(make)
	model = strings.TrimSpace(model)
	if model == "" {
		return make
	}
	if make != "" && strings.HasPrefix(strings.ToLower(model), strings.ToLower(make)+" ") {
		model = strings.TrimSpace(model[len(make):])
	}
	if make == "" {
		return model
	}
	return make + " " + model
}

// joinLensExposure assembles the lens / focal length / aperture / shutter
// speed / ISO segment of the caption line, omitting any zero-valued fields.
func joinLensExposure(lens string, focal, fnumber, exposureSeconds float64, iso int) string {
	parts := make([]string, 0, 5)
	lens = strings.TrimSpace(lens)
	if lens != "" {
		parts = append(parts, lens)
	}
	if focal > 0 {
		parts = append(parts, fmt.Sprintf("%gmm", roundFloat(focal, 0)))
	}
	if fnumber > 0 {
		parts = append(parts, fmt.Sprintf("f/%g", roundFloat(fnumber, 1)))
	}
	if exposureSeconds > 0 {
		parts = append(parts, formatExposureSeconds(exposureSeconds))
	}
	if iso > 0 {
		parts = append(parts, fmt.Sprintf("ISO%d", iso))
	}
	return strings.Join(parts, " ")
}

// formatExposureSeconds renders an exposure time:
//   - < 1 s  → "1/N s" (e.g. 0.005 → "1/200s")
//   - ≥ 1 s  → "Ns"    (e.g. 4 → "4s")
//
// Returns "" for non-positive input.
func formatExposureSeconds(f float64) string {
	if f <= 0 {
		return ""
	}
	if f >= 1 {
		return fmt.Sprintf("%gs", roundFloat(f, 1))
	}
	n := int(roundFloat(1/f, 0))
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("1/%ds", n)
}

// roundFloat rounds to a fixed number of decimal places via half-up.
func roundFloat(f float64, decimals int) float64 {
	scale := 1.0
	for range decimals {
		scale *= 10
	}
	return float64(int(f*scale+0.5)) / scale
}
