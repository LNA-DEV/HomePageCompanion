package autouploader

import (
	"strings"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/mmcdole/gofeed"
)

// A feed item without an image (a blog post) must fail with an error on the
// platforms that need one, never panic (autoposting-ui.md §5.1 B2). The check
// runs before any network call, so the targets here point nowhere.
func TestPublishWithoutImageFails(t *testing.T) {
	entry := &gofeed.Item{GUID: "post-1", Title: "A blog post"}
	target := config.Target{Name: "t", InstanceUrl: "http://127.0.0.1:1", PAT: "x", Username: "x", AccountId: "1", AccessToken: "x"}

	for name, publish := range map[string]func() error{
		"bluesky":   func() error { return publishBlueskyEntry(entry, target, "caption", nil) },
		"pixelfed":  func() error { return publishPixelfedEntry(entry, target, "caption") },
		"instagram": func() error { return publishInstagramEntry(entry, target, "caption", nil) },
		"threads":   func() error { return publishThreadsEntry(entry, target, "caption") },
	} {
		t.Run(name, func(t *testing.T) {
			err := publish()
			if err == nil || !strings.Contains(err.Error(), "no image") {
				t.Fatalf("err = %v, want a no-image error", err)
			}
		})
	}
}
