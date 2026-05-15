package interactions

import (
	"errors"
	"fmt"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/LNA-DEV/HomePageCompanion/pixelfedapi"
)

// PixelfedLikesResponse keeps the previous public shape so the scheduler does
// not need to change. Internally it is a thin re-export of pixelfedapi.LikesResponse.
type PixelfedLikesResponse = pixelfedapi.LikesResponse

func handlePixelfedLikes(item models.AutoUploadItem, targetName string) (*PixelfedLikesResponse, error) {
	if item.PostUrl == nil || item.PostId == nil || *item.PostUrl == "" || *item.PostId == "" {
		return nil, errors.New("missing PostURL or PostID")
	}

	instance, err := pixelfedapi.InstanceFromPostURL(*item.PostUrl)
	if err != nil {
		return nil, fmt.Errorf("parse instance: %w", err)
	}

	token := pixelfedTokenFor(targetName)
	if token == "" {
		return nil, errors.New("empty Pixelfed token")
	}

	resp, err := pixelfedapi.ListPostLikes(instance, token, *item.PostId)
	if err != nil {
		if errors.Is(err, pixelfedapi.ErrRateLimited) {
			return nil, ErrRateLimited
		}
		return nil, err
	}
	return resp, nil
}

func pixelfedTokenFor(targetName string) string {
	for _, t := range config.Data.Targets {
		if t.Name == targetName {
			return t.PAT
		}
	}
	return ""
}
