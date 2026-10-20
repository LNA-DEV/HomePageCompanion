package autouploader

import (
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/instagramapi"
	"github.com/mmcdole/gofeed"
)

const (
	instagramMaxStatusPolls = 10
	instagramStatusPollGap  = 2 * time.Second
)

func publishInstagramEntry(entry *gofeed.Item, target config.Target, caption string, credits []Credit) error {
	if _, err := requireImage(entry, target.Platform); err != nil {
		return err
	}
	creationID, err := createInstagramContainer(entry, target, caption, credits)
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

// createInstagramContainer creates the media container, with a photo tag for
// everyone the caption mentions. What Instagram does with a private or
// missing account in user_tags is not known, so a 4xx other than 429 while
// tags are set gets one more try without them; the caption mentions stay.
//
// Instagram's Graph API fetches the image from entry.Image.URL itself
// (image_url is a query parameter). Bytes never cross our process, so
// client-side downsizing (imageresize) doesn't apply here.
func createInstagramContainer(entry *gofeed.Item, target config.Target, caption string, credits []Credit) (string, error) {
	tags, slugs := instagramUserTags(credits)
	creationID, err := instagramapi.CreateMediaContainer(target.AccountId, target.AccessToken, entry.Image.URL, caption, tags)
	var status *instagramapi.StatusError
	if err != nil && tags != nil && errors.As(err, &status) && status.StatusCode >= 400 && status.StatusCode < 500 {
		log.Printf("autouploader: %s on %s: photo tags rejected, retrying without them: %s: %v",
			entry.GUID, target.Platform, strings.Join(slugs, ", "), err)
		creationID, err = instagramapi.CreateMediaContainer(target.AccountId, target.AccessToken, entry.Image.URL, caption, nil)
	}
	return creationID, err
}

// instagramUserTags tags everyone with a mention on the photo, and returns
// their slugs for logging. The feed has no positions, so they are fixed: one
// person in the centre, several spread evenly along the middle row in feed
// order. In most portraits the person stands in the middle, and Instagram
// shows tags only on tap.
func instagramUserTags(credits []Credit) ([]instagramapi.UserTag, []string) {
	var tagged []Credit
	for _, c := range credits {
		if c.Mention != "" {
			tagged = append(tagged, c)
		}
	}
	if len(tagged) == 0 {
		return nil, nil
	}
	tags := make([]instagramapi.UserTag, len(tagged))
	slugs := make([]string, len(tagged))
	for i, c := range tagged {
		x := float64(i+1) / float64(len(tagged)+1)
		tags[i] = instagramapi.UserTag{Username: c.Account, X: math.Round(x*100) / 100, Y: 0.5}
		slugs[i] = c.Slug
	}
	return tags, slugs
}
