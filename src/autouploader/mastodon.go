package autouploader

import (
	"log"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/mastodonapi"
	"github.com/mmcdole/gofeed"
)

// publishMastodonEntry mirrors publishPixelfedEntry: uploads the image (when
// present), creates the public status with the prepared caption, and records
// the local AutoUploadItem with the resulting URL + status ID. Mastodon has
// no native "version id" concept, so VersionId is left nil.
func publishMastodonEntry(entry *gofeed.Item, target config.Target, caption string) error {
	var mediaIDs []string
	if entry.Image != nil && entry.Image.URL != "" {
		imageBytes, err := downloadImage(entry.Image.URL)
		if err != nil {
			return err
		}
		description := extractAltText(entry.Description)
		media, err := mastodonapi.UploadMedia(target.InstanceUrl, target.PAT, imageBytes, description)
		if err != nil {
			return err
		}
		mediaIDs = []string{media.ID}
	}

	status, err := mastodonapi.CreateStatus(target.InstanceUrl, target.PAT, caption, "", mediaIDs, "public")
	if err != nil {
		return err
	}

	log.Println("Mastodon post published:", status.URL)
	return publishedEntry(entry.GUID, target.Platform, nil, &status.URL, &status.ID)
}
