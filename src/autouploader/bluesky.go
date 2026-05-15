package autouploader

import (
	"log"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/blueskyapi"
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/imageresize"
	"github.com/mmcdole/gofeed"
)

func publishBlueskyEntry(entry *gofeed.Item, target config.Target, caption string) error {
	session, err := blueskyapi.Login(target.Username, target.PAT)
	if err != nil {
		return err
	}

	altText := extractAltText(entry.Description)

	imageBytes, err := downloadImage(entry.Image.URL)
	if err != nil {
		return err
	}
	if prepared, prepErr := imageresize.PrepareForTarget(imageBytes, resolveLimits(target)); prepErr != nil {
		log.Printf("autouploader: bluesky image prep failed for %s, sending original: %v", entry.GUID, prepErr)
	} else {
		imageBytes = prepared
	}

	blob, err := blueskyapi.UploadImage(session, imageBytes, "image/jpeg")
	if err != nil {
		return err
	}

	post, err := blueskyapi.CreatePost(session, caption, altText, blob, time.Now())
	if err != nil {
		return err
	}

	log.Println("Entry published successfully:", entry.Title)
	return publishedEntry(entry.GUID, target.Platform, &post.CID, &post.URI, nil)
}
