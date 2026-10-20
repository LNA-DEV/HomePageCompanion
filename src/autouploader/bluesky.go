package autouploader

import (
	"log"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/blueskyapi"
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/imageresize"
	"github.com/mmcdole/gofeed"
)

func publishBlueskyEntry(entry *gofeed.Item, target config.Target, caption string, credits []Credit) error {
	imageURL, err := requireImage(entry, target.Platform)
	if err != nil {
		return err
	}
	session, err := blueskyapi.Login(target.Username, target.PAT)
	if err != nil {
		return err
	}

	altText := extractAltText(entry.Description)

	imageBytes, err := downloadImage(imageURL)
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

	post, err := blueskyapi.CreatePost(session, caption, altText, blob, time.Now(), blueskyMentions(credits))
	if err != nil {
		return err
	}

	log.Println("Entry published successfully:", entry.Title)
	return publishedEntry(entry.GUID, target.Platform, &post.CID, &post.URI, nil)
}

// blueskyMentions maps each mention text to the DID it stands for. A mention
// the length budget turned back into a plain name is not in the caption, so
// it gets no facet.
func blueskyMentions(credits []Credit) map[string]string {
	var mentions map[string]string
	for _, c := range credits {
		if c.Mention == "" {
			continue
		}
		if mentions == nil {
			mentions = map[string]string{}
		}
		mentions[c.Mention] = c.Account
	}
	return mentions
}
