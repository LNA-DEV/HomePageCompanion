package autouploader

import (
	"fmt"
	"log"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/instagramapi"
	"github.com/mmcdole/gofeed"
)

const (
	instagramMaxStatusPolls = 10
	instagramStatusPollGap  = 2 * time.Second
)

func publishInstagramEntry(entry *gofeed.Item, target config.Target, caption string) error {
	// Note: Instagram's Graph API fetches the image from entry.Image.URL itself
	// (see instagramapi.CreateMediaContainer — image_url is a query parameter).
	// Bytes never cross our process, so client-side downsizing (imageresize)
	// doesn't apply here.
	creationID, err := instagramapi.CreateMediaContainer(target.AccountId, target.AccessToken, entry.Image.URL, caption)
	if err != nil {
		return fmt.Errorf("create media container: %w", err)
	}

	var status string
	var statusErr error
	for i := 0; i < instagramMaxStatusPolls; i++ {
		status, statusErr = instagramapi.CheckMediaStatus(creationID, target.AccessToken)
		if statusErr != nil {
			log.Printf("Status check failed (attempt %d): %v", i+1, statusErr)
			time.Sleep(instagramStatusPollGap)
			continue
		}
		log.Printf("Attempt %d: Status = %s", i+1, status)
		if status == "FINISHED" {
			break
		}
		time.Sleep(instagramStatusPollGap)
	}

	if status != "FINISHED" {
		if statusErr != nil {
			return fmt.Errorf("media not ready: %w", statusErr)
		}
		return fmt.Errorf("media not ready after %d attempts (last status: %q)", instagramMaxStatusPolls, status)
	}

	publishID, err := instagramapi.PublishContainer(target.AccountId, target.AccessToken, creationID)
	if err != nil {
		return fmt.Errorf("publish container: %w", err)
	}

	log.Printf("Published to Instagram: %s", publishID)
	if err := publishedEntry(entry.GUID, target.Platform, nil, nil, &publishID); err != nil {
		log.Printf("Error recording published entry: %v", err)
	}
	return nil
}
