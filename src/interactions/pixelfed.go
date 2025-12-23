package interactions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/models"
)

type PixelfedAccount struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Acct        string `json:"acct"`
	DisplayName string `json:"display_name"`
	Avatar      string `json:"avatar"`
	URL         string `json:"url"`
}

type PixelfedLikesResponse struct {
	Instance string            `json:"instance"`
	PostID   string            `json:"post_id"`
	Accounts []PixelfedAccount `json:"accounts"`
}

func handlePixelfedLikes(item models.AutoUploadItem, targetName string) (*PixelfedLikesResponse, error) {
	if item.PostUrl == nil || item.PostId == nil || *item.PostUrl == "" || *item.PostId == "" {
		return nil, errors.New("missing PostURL or PostID")
	}

	instance, err := extractInstance(*item.PostUrl)
	if err != nil {
		return nil, fmt.Errorf("parse instance: %w", err)
	}

	token := getPixelfedToken(targetName)

	if token == "" {
		return nil, errors.New("empty Pixelfed token")
	}

	endpoint := fmt.Sprintf("https://%s/api/v1/statuses/%s/favourited_by", instance, url.PathEscape(*item.PostId))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("pixelfed API %s -> %s", endpoint, resp.Status)
	}

	var accounts []PixelfedAccount
	if err := json.NewDecoder(resp.Body).Decode(&accounts); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	out := &PixelfedLikesResponse{
		Instance: instance,
		PostID:   *item.PostId,
		Accounts: accounts,
	}

	return out, nil
}

func extractInstance(postURL string) (string, error) {
	u, err := url.Parse(postURL)
	if err != nil {
		return "", err
	}
	h := strings.TrimSpace(u.Host)
	if h == "" {
		return "", errors.New("no host in post URL")
	}
	return h, nil
}

func getPixelfedToken(targetName string) string {
	var target config.Target

	for _, element := range config.Data.Targets {
		if element.Name == targetName {
			target = element
			break
		}
	}

	return target.PAT
}
