package interactions

import (
	"errors"
	"fmt"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/LNA-DEV/HomePageCompanion/threadsapi"
)

// ThreadsLikesResponse mirrors InstagramLikesResponse: the scheduler's
// RetryWithBackoff is platform-tagged, and Threads' insights endpoint returns
// a single int rather than a list of accounts.
type ThreadsLikesResponse struct {
	MediaID   string `json:"media_id"`
	LikeCount int    `json:"like_count"`
}

// handleThreadsLikes resolves the Threads target's access token and asks the
// Graph API for the `likes` insight on the published media id, translating
// the platform's rate-limit sentinel into the local one.
func handleThreadsLikes(item models.AutoUploadItem, targetName string) (*ThreadsLikesResponse, error) {
	if item.PostId == nil || *item.PostId == "" {
		return nil, errors.New("missing PostID")
	}

	token := threadsTokenFor(targetName)
	if token == "" {
		return nil, errors.New("empty Threads access token")
	}

	count, err := threadsapi.MediaLikeCount(*item.PostId, token)
	if err != nil {
		if errors.Is(err, threadsapi.ErrRateLimited) {
			return nil, ErrRateLimited
		}
		return nil, fmt.Errorf("failed to get Threads likes: %w", err)
	}
	return &ThreadsLikesResponse{MediaID: *item.PostId, LikeCount: count}, nil
}

func threadsTokenFor(targetName string) string {
	for _, t := range config.Data.Targets {
		if t.Name == targetName {
			return t.AccessToken
		}
	}
	return ""
}
