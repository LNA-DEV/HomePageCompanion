package autouploader

import (
	"fmt"
	"log"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/threadsapi"
	"github.com/mmcdole/gofeed"
)

const (
	threadsMaxStatusPolls = 10
	threadsStatusPollGap  = 2 * time.Second
)

// publishThreadsEntry mirrors publishInstagramEntry: Threads fetches the
// image_url itself, so bytes never cross our process and client-side
// downsizing (imageresize) doesn't apply. We create the container, poll its
// status until FINISHED, publish, then resolve the permalink for PostUrl.
func publishThreadsEntry(entry *gofeed.Item, target config.Target, caption string) error {
	if entry.Image == nil || entry.Image.URL == "" {
		return fmt.Errorf("threads: entry %q has no image URL", entry.GUID)
	}

	creationID, err := threadsapi.CreateImageContainer(target.AccountId, target.AccessToken, entry.Image.URL, caption)
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}

	var status string
	var statusErr error
	for i := 0; i < threadsMaxStatusPolls; i++ {
		status, statusErr = threadsapi.CheckMediaStatus(creationID, target.AccessToken)
		if statusErr != nil {
			log.Printf("Threads status check failed (attempt %d): %v", i+1, statusErr)
			time.Sleep(threadsStatusPollGap)
			continue
		}
		log.Printf("Threads attempt %d: status = %s", i+1, status)
		if status == "FINISHED" {
			break
		}
		time.Sleep(threadsStatusPollGap)
	}

	if status != "FINISHED" {
		if statusErr != nil {
			return fmt.Errorf("media not ready: %w", statusErr)
		}
		return fmt.Errorf("media not ready after %d attempts (last status: %q)", threadsMaxStatusPolls, status)
	}

	mediaID, err := threadsapi.PublishContainer(target.AccountId, target.AccessToken, creationID)
	if err != nil {
		return fmt.Errorf("publish container: %w", err)
	}

	permalink, permErr := threadsapi.GetPermalink(mediaID, target.AccessToken)
	if permErr != nil {
		log.Printf("Threads permalink fetch failed for %s: %v", mediaID, permErr)
	}

	log.Printf("Published to Threads: %s", mediaID)
	var postUrl *string
	if permalink != "" {
		postUrl = &permalink
	}
	if err := publishedEntry(entry.GUID, target.Platform, nil, postUrl, &mediaID); err != nil {
		log.Printf("Error recording published entry: %v", err)
	}
	return nil
}
