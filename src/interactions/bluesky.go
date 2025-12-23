package interactions

import (
	"encoding/json"
	"fmt"
	"net/http"

	blueskyapi "github.com/LNA-DEV/HomePageCompanion/blue_sky_api"
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/models"
)

func handleBlueskyLikes(item models.AutoUploadItem, targetName string) (*BlueskyLikesResponse, error) {
	if item.PostUrl == nil || item.VersionId == nil {
		return nil, fmt.Errorf("post URL or version ID is nil")
	}

	result, err := GetBlueskyLikes(*item.PostUrl, *item.VersionId, targetName)
	if err != nil {
		return nil, fmt.Errorf("GetBlueskyLikes failed: %w", err)
	}

	fmt.Printf("Post URI: %s\n", result.Uri)
	fmt.Printf("CID: %s\n", result.Cid)
	fmt.Printf("Likes count: %d\n", len(result.Likes))

	return result, nil
}

// GetBlueskyLikes retrieves like details for a given AT URI and version (CID)
func GetBlueskyLikes(uri, cid string, targetName string) (*BlueskyLikesResponse, error) {
	var target config.Target

	for _, element := range config.Data.Targets {
		if element.Name == targetName {
			target = element
			break
		}
	}

	session, loginErr := blueskyapi.BlueskyLogin(target.Username, target.PAT)
	if loginErr != nil {
		return nil, loginErr
	}

	client := &http.Client{}
	var allLikes []BlueskyLike
	var result *BlueskyLikesResponse
	cursor := ""

	for {
		apiURL := fmt.Sprintf("https://bsky.social/xrpc/app.bsky.feed.getLikes?uri=%s&cid=%s&limit=100", uri, cid)
		if cursor != "" {
			apiURL += "&cursor=" + cursor
		}

		req, err := http.NewRequest("GET", apiURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+session.AccessJwt)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to call Bluesky API: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, ErrRateLimited
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Bluesky API returned status %d", resp.StatusCode)
		}

		var data BlueskyLikesResponse
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return nil, fmt.Errorf("failed to parse JSON: %w", err)
		}

		allLikes = append(allLikes, data.Likes...)

		if result == nil {
			result = &data
		}

		if data.Cursor == "" {
			break
		}
		cursor = data.Cursor
	}

	result.Likes = allLikes
	return result, nil
}

// BlueskyLike represents a single like entry
type BlueskyLike struct {
	CreatedAt string `json:"createdAt"`
	Actor     struct {
		Did         string `json:"did"`
		Handle      string `json:"handle"`
		DisplayName string `json:"displayName"`
	} `json:"actor"`
}

// BlueskyLikesResponse represents the response from app.bsky.feed.getLikes
type BlueskyLikesResponse struct {
	Uri    string        `json:"uri"`
	Cid    string        `json:"cid"`
	Likes  []BlueskyLike `json:"likes"`
	Cursor string        `json:"cursor,omitempty"`
}
