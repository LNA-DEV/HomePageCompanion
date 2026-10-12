package autouploader

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/imagemeta"
	"github.com/mmcdole/gofeed"
)

// CaptionLimits is what a target accepts in a caption.
type CaptionLimits struct {
	// MaxLen is the longest caption the platform accepts, measured by Len.
	// 0 means 2000.
	MaxLen int
	// MaxHashtags caps how many hashtags are emitted (0 = unlimited). Threads
	// indexes only a single topic tag per post and renders every extra #tag
	// as dead, non-clickable clutter, so Threads gets 1.
	MaxHashtags int
	// Len measures text the way the platform counts it. nil means runeLen.
	Len func(string) int
}

// Caption is what BuildCaption returns: the text to post, and what the
// length budget had to take from the names line, for the caller to log.
type Caption struct {
	Text string
	// Unmentioned lists the people whose mention was turned back into a plain
	// name to fit the limit (step 6), last person first.
	Unmentioned []string
	// NamesCut lists the people no longer named in full because the names
	// line itself was cut (step 7).
	NamesCut []string
}

// BuildCaption composes the final caption for a publish. Sections are joined
// by blank lines:
//
//	<image alt text, when conn.IncludeAltText>
//	<names line: the people in the photo, see namesLine>
//	<conn.Caption>
//	<optional EXIF line>
//	<optional copyright line>
//	<hashtags from entry.Categories, excluding meta_* tags>
//
// The names belong to the description of the photo, so they follow the alt
// text; the configured caption (usually a "More at …" link) stays the last
// line of text. Hashtags go last so Mastodon can lift a trailing hashtag-only
// paragraph into its hashtag bar. To publish without a caption, leave Caption
// empty — there is no separate toggle for it.
//
// credits are the people in the photo, in feed order, with a mention where
// they are tagged on this target. The names line is never dropped. When the
// caption exceeds limits.MaxLen, these steps run in order until it fits:
//
//  1. everything;
//  2. drop copyright;
//  3. drop EXIF;
//  4. drop hashtags one by one from the end;
//  5. shorten the alt text to what is left, or drop it; then the caption;
//  6. turn mentions back into plain names, last person first;
//  7. cut the names line.
//
// imageBytes may be nil when no EXIF/copyright-from-exif feature is enabled.
// feed may be nil when no copyright-from-rss feature is enabled.
func BuildCaption(conn config.Connection, entry *gofeed.Item, feed *gofeed.Feed, imageBytes []byte, credits []Credit, limits CaptionLimits) Caption {
	maxLen := limits.MaxLen
	if maxLen <= 0 {
		maxLen = 2000
	}
	length := limits.Len
	if length == nil {
		length = runeLen
	}

	hashtags := buildHashtags(entry)
	if limits.MaxHashtags > 0 && len(hashtags) > limits.MaxHashtags {
		hashtags = hashtags[:limits.MaxHashtags]
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

	credits = append([]Credit(nil), credits...) // step 6 edits them
	prefix, and := namesWords(conn)
	parts := captionParts{
		alt:       altText(conn, entry),
		names:     namesLine(credits, prefix, and),
		caption:   strings.TrimSpace(conn.Caption),
		exif:      exifLine,
		copyright: copyrightLine,
		hashtags:  hashtags,
	}
	var out Caption
	fits := func() bool {
		out.Text = parts.join()
		return length(out.Text) <= maxLen
	}

	// 1. Try with everything.
	if fits() {
		return out
	}
	// 2. Drop copyright.
	parts.copyright = ""
	if fits() {
		return out
	}
	// 3. Drop EXIF too.
	parts.exif = ""
	if fits() {
		return out
	}
	// 4. Drop trailing hashtags one at a time.
	for len(parts.hashtags) > 0 {
		parts.hashtags = parts.hashtags[:len(parts.hashtags)-1]
		if fits() {
			return out
		}
	}
	// 5. Shorten the alt text to whatever room is left, or drop it; then the
	// same for the caption. The alt text goes first: Pixelfed, Mastodon and
	// Bluesky get it as the image's alt text anyway, and a caption cut in the
	// middle of its link leaves a broken link.
	for _, field := range []*string{&parts.alt, &parts.caption} {
		text := *field
		*field = ""
		room := maxLen
		if rest := parts.join(); rest != "" {
			room -= length(rest) + length(sectionSep)
		}
		*field = shorten(text, room, length)
		if *field != "" && fits() {
			return out
		}
		*field = ""
	}
	if fits() {
		return out
	}
	// 6. Turn mentions back into plain names, last person first.
	for i := len(credits) - 1; i >= 0; i-- {
		if credits[i].Mention == "" {
			continue
		}
		credits[i].Mention = ""
		out.Unmentioned = append(out.Unmentioned, credits[i].Slug)
		parts.names = namesLine(credits, prefix, and)
		if fits() {
			return out
		}
	}
	// 7. Last resort: cut the names line. Only it is left by now.
	names, kept := cutNamesLine(credits, prefix, maxLen, length)
	parts.names = names
	out.Text = parts.join()
	for _, c := range credits[kept:] {
		out.NamesCut = append(out.NamesCut, c.Slug)
	}
	return out
}

// sectionSep separates the sections of a caption.
const sectionSep = "\n\n"

// captionParts are the sections of a caption, in the order they are posted.
type captionParts struct {
	alt, names, caption, exif, copyright string
	hashtags                             []string
}

func (p captionParts) join() string {
	sections := make([]string, 0, 6)
	for _, s := range []string{p.alt, p.names, p.caption, p.exif, p.copyright} {
		if s != "" {
			sections = append(sections, s)
		}
	}
	if len(p.hashtags) > 0 {
		sections = append(sections, strings.Join(p.hashtags, " "))
	}
	return strings.Join(sections, sectionSep)
}

// altText returns the image alt text (parsed from the RSS item's
// description) when the connection includes it.
func altText(conn config.Connection, entry *gofeed.Item) string {
	if !conn.IncludeAltText || entry == nil {
		return ""
	}
	return strings.TrimSpace(extractAltText(entry.Description))
}

// namesWords returns the words of the names line, English unless the
// connection sets them.
func namesWords(conn config.Connection) (prefix, and string) {
	prefix, and = strings.TrimSpace(conn.ModelsPrefix), strings.TrimSpace(conn.ModelsAnd)
	if prefix == "" {
		prefix = "With"
	}
	if and == "" {
		and = "and"
	}
	return prefix, and
}

// namesLine renders the people in the photo as one line:
//
//	With Jane Doe (@janedoe), Max Muster and Markus Penninger
//
// The last two are joined by and, all others by ", ", without a serial comma.
// No emoji, because Threads counts them as UTF-8 bytes.
func namesLine(credits []Credit, prefix, and string) string {
	if len(credits) == 0 {
		return ""
	}
	labels := make([]string, len(credits))
	for i, c := range credits {
		labels[i] = c.label()
	}
	list := labels[len(labels)-1]
	if len(labels) > 1 {
		list = strings.Join(labels[:len(labels)-1], ", ") + " " + and + " " + list
	}
	return prefix + " " + list
}

// cutNamesLine shortens a names line to limit. It cuts after a whole name
// first ("With A, B…") and inside the first name only when not even that one
// fits. It returns the line and how many people are still named in full.
// credits carry no mentions by now (step 6).
func cutNamesLine(credits []Credit, prefix string, limit int, length func(string) int) (string, int) {
	for kept := len(credits) - 1; kept >= 1; kept-- {
		names := make([]string, kept)
		for i := range names {
			names[i] = credits[i].Name
		}
		if line := prefix + " " + strings.Join(names, ", ") + ellipsis; length(line) <= limit {
			return line, kept
		}
	}
	if len(credits) == 0 {
		return "", 0
	}
	return shorten(prefix+" "+credits[0].Name, limit, length), 0
}

// ellipsis marks text that was cut. It is one character and counts against
// the limit.
const ellipsis = "…"

// shorten cuts text to at most limit, measured by length:
//
//  1. after the last sentence (".", "!" or "?" followed by a space) that fits,
//     if that keeps at least half of limit; no ellipsis is needed;
//  2. otherwise at the last space that fits, without trailing punctuation,
//     plus an ellipsis;
//  3. only when not even the first word fits, inside it, plus an ellipsis. A
//     first word that is a URL is dropped instead, never left half-cut as a
//     broken link.
//
// It returns "" when nothing useful fits.
func shorten(text string, limit int, length func(string) int) string {
	if length(text) <= limit {
		return text
	}
	budget := limit - length(ellipsis)
	if budget <= 0 {
		return ""
	}

	sentence, space := -1, -1
	for i, r := range text {
		if !unicode.IsSpace(r) {
			continue
		}
		head := text[:i]
		n := length(head)
		if n > limit {
			break
		}
		if strings.ContainsRune(".!?", lastRune(head)) {
			sentence = i
		}
		if n <= budget {
			space = i
		}
	}
	if sentence > 0 && 2*length(text[:sentence]) >= limit {
		return text[:sentence]
	}
	if space > 0 {
		if head := strings.TrimRight(text[:space], " \t\n,;:.!?-–—"); head != "" {
			return head + ellipsis
		}
	}

	if first := strings.Fields(text); len(first) > 0 && strings.Contains(first[0], "://") {
		return ""
	}
	runes := []rune(strings.TrimSpace(text))
	for len(runes) > 0 && length(string(runes)) > budget {
		runes = runes[:len(runes)-1]
	}
	if len(runes) == 0 {
		return ""
	}
	return string(runes) + ellipsis
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
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

// threadsLen counts like Threads: "emojis are counted as the number of UTF-8
// bytes", every other character as one. An umlaut is one, 📷 is four.
func threadsLen(s string) int {
	n := 0
	for _, r := range s {
		if isEmojiRune(r) {
			n += utf8.RuneLen(r)
		} else {
			n++
		}
	}
	return n
}

// isEmojiRune reports the runes Threads may count as emoji. It errs towards
// counting too much: every "other symbol" (📷, ©, ★, flags), plus the skin
// tone modifiers, joiners, variation selectors and tag characters emoji
// sequences are built from.
func isEmojiRune(r rune) bool {
	switch {
	case unicode.Is(unicode.So, r):
		return true
	case r == 0x200D, r == 0x20E3: // zero width joiner, keycap
		return true
	case r >= 0xFE00 && r <= 0xFE0F: // variation selectors
		return true
	case r >= 0x1F3FB && r <= 0x1F3FF: // skin tone modifiers
		return true
	case r >= 0xE0020 && r <= 0xE007F: // tag characters
		return true
	}
	return false
}

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
