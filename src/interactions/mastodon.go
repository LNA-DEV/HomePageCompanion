package interactions

import (
	"errors"
	"fmt"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/mastodonapi"
	"github.com/LNA-DEV/HomePageCompanion/models"
)

// MastodonLikesResponse keeps the existing per-platform-likes-response type
// pattern; it is a thin re-export of mastodonapi.LikesResponse so the
// scheduler's RetryWithBackoff generic stays platform-tagged.
type MastodonLikesResponse = mastodonapi.LikesResponse

// handleMastodonLikes resolves the Mastodon target's credentials by name and
// fetches the favourited_by list for a published status, translating the
// platform's rate-limit sentinel into the local one.
func handleMastodonLikes(item models.AutoUploadItem, targetName string) (*MastodonLikesResponse, error) {
	if item.PostId == nil || *item.PostId == "" {
		return nil, errors.New("missing PostID")
	}

	var target config.Target
	for _, t := range config.Data.Targets {
		if t.Name == targetName {
			target = t
			break
		}
	}
	if target.PAT == "" {
		return nil, errors.New("empty Mastodon token")
	}
	if target.InstanceUrl == "" {
		return nil, errors.New("empty Mastodon instance")
	}

	resp, err := mastodonapi.ListStatusLikes(target.InstanceUrl, target.PAT, *item.PostId)
	if err != nil {
		if errors.Is(err, mastodonapi.ErrRateLimited) {
			return nil, ErrRateLimited
		}
		return nil, fmt.Errorf("mastodon likes: %w", err)
	}
	return resp, nil
}
