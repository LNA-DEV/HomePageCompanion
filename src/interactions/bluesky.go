package interactions

import (
	"errors"
	"fmt"

	"github.com/LNA-DEV/HomePageCompanion/blueskyapi"
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/models"
)

// BlueskyLikesResponse retains the existing public shape so the scheduler can
// dispatch without changes. Internally it is filled from
// blueskyapi.ListPostLikes.
type BlueskyLikesResponse struct {
	Uri    string             `json:"uri"`
	Cid    string             `json:"cid"`
	Likes  []blueskyapi.Like  `json:"likes"`
	Cursor string             `json:"cursor,omitempty"`
}

func handleBlueskyLikes(item models.AutoUploadItem, targetName string) (*BlueskyLikesResponse, error) {
	if item.PostUrl == nil || item.VersionId == nil {
		return nil, fmt.Errorf("post URL or version ID is nil")
	}

	var target config.Target
	for _, t := range config.Data.Targets {
		if t.Name == targetName {
			target = t
			break
		}
	}

	session, err := blueskyapi.Login(target.Username, target.PAT)
	if err != nil {
		return nil, translateBlueskyRateLimit(err)
	}

	resp, err := blueskyapi.ListPostLikes(session, *item.PostUrl, *item.VersionId)
	if err != nil {
		return nil, translateBlueskyRateLimit(err)
	}
	return &BlueskyLikesResponse{
		Uri:    resp.Uri,
		Cid:    resp.Cid,
		Likes:  resp.Likes,
		Cursor: resp.Cursor,
	}, nil
}

func translateBlueskyRateLimit(err error) error {
	if errors.Is(err, blueskyapi.ErrRateLimited) {
		return ErrRateLimited
	}
	return err
}
