package interactions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/models"
)

var instagramGraphURL = "https://graph.instagram.com/v22.0/"

type InstagramLikesResponse struct {
	MediaID   string `json:"media_id"`
	LikeCount int    `json:"like_count"`
}

func handleInstagramLikes(item models.AutoUploadItem, targetName string) (*InstagramLikesResponse, error) {
	if item.PostId == nil || *item.PostId == "" {
		return nil, errors.New("missing PostID")
	}

	token := getInstagramToken(targetName)
	if token == "" {
		return nil, errors.New("empty Instagram access token")
	}

	likeCount, err := getInstagramLikeCount(*item.PostId, token)
	if err != nil {
		return nil, fmt.Errorf("failed to get Instagram likes: %w", err)
	}

	return &InstagramLikesResponse{
		MediaID:   *item.PostId,
		LikeCount: likeCount,
	}, nil
}

func getInstagramLikeCount(mediaID, accessToken string) (int, error) {
	endpoint := fmt.Sprintf("%s%s?fields=like_count&access_token=%s", instagramGraphURL, mediaID, accessToken)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return 0, ErrRateLimited
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("Instagram Graph API returned status %s: %s", resp.Status, string(body))
	}

	var result struct {
		LikeCount int    `json:"like_count"`
		ID        string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}

	return result.LikeCount, nil
}

func getInstagramToken(targetName string) string {
	for _, element := range config.Data.Targets {
		if element.Name == targetName {
			return element.AccessToken
		}
	}
	return ""
}
