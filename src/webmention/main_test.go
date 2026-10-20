package webmention

import "testing"

// Only http and https URLs are stored; anything else could end up as a
// javascript: link in the admin UI (webmention.md §3.1).
func TestIsValidURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://example.com/post":    true,
		"http://example.com/":         true,
		"HTTPS://example.com/":        true,
		"javascript://x/%0aalert(1)":  false,
		"data://x/text/html,hi":       false,
		"file://localhost/etc/passwd": false,
		"ftp://example.com/":          false,
		"https:///nohost":             false,
		"example.com/post":            false,
		"":                            false,
	} {
		if got := isValidURL(u); got != want {
			t.Errorf("%q: %v, want %v", u, got, want)
		}
	}
}
