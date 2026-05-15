package interactions

import (
	"errors"
	"fmt"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/instagramapi"
	"github.com/LNA-DEV/HomePageCompanion/models"
)

type InstagramLikesResponse struct {
	MediaID   string `json:"media_id"`
	LikeCount int    `json:"like_count"`
}

func handleInstagramLikes(item models.AutoUploadItem, targetName string) (*InstagramLikesResponse, error) {
	if item.PostId == nil || *item.PostId == "" {
		return nil, errors.New("missing PostID")
	}

	token := instagramTokenFor(targetName)
	if token == "" {
		return nil, errors.New("empty Instagram access token")
	}

	count, err := instagramapi.MediaLikeCount(*item.PostId, token)
	if err != nil {
		if errors.Is(err, instagramapi.ErrRateLimited) {
			return nil, ErrRateLimited
		}
		return nil, fmt.Errorf("failed to get Instagram likes: %w", err)
	}
	return &InstagramLikesResponse{MediaID: *item.PostId, LikeCount: count}, nil
}

func instagramTokenFor(targetName string) string {
	for _, t := range config.Data.Targets {
		if t.Name == targetName {
			return t.AccessToken
		}
	}
	return ""
}
