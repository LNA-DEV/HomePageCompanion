package instagramapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// CreateMediaContainer uploads (by URL) an image to Instagram as a media
// container and returns the creation ID. Caption can be empty.
func CreateMediaContainer(accountID, accessToken, imageURL, caption string) (string, error) {
	endpoint := fmt.Sprintf("%s%s/media", GraphURL, accountID)
	params := url.Values{}
	params.Set("access_token", accessToken)
	params.Set("caption", caption)
	params.Set("image_url", imageURL)

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.URL.RawQuery = params.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("instagram create container status %d body: %s", resp.StatusCode, string(body))
	}

	var res struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.ID == "" {
		return "", fmt.Errorf("instagram: empty creation id in response")
	}
	return res.ID, nil
}

// CheckMediaStatus returns the current `status_code` of a creation container
// (e.g. "IN_PROGRESS", "FINISHED", "ERROR"). Callers poll this between
// CreateMediaContainer and PublishContainer.
func CheckMediaStatus(creationID, accessToken string) (string, error) {
	endpoint := fmt.Sprintf("%s%s", GraphURL, creationID)
	params := url.Values{}
	params.Set("fields", "status_code")
	params.Set("access_token", accessToken)

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.URL.RawQuery = params.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("instagram status check status %d body: %s", resp.StatusCode, string(body))
	}

	var res struct {
		StatusCode string `json:"status_code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.StatusCode == "" {
		return "", fmt.Errorf("instagram: empty status_code in response")
	}
	return res.StatusCode, nil
}

// PublishContainer publishes a previously-created media container and
// returns the resulting media id.
func PublishContainer(accountID, accessToken, creationID string) (string, error) {
	endpoint := fmt.Sprintf("%s%s/media_publish", GraphURL, accountID)
	params := url.Values{}
	params.Set("access_token", accessToken)
	params.Set("creation_id", creationID)

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.URL.RawQuery = params.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("instagram publish status %d body: %s", resp.StatusCode, string(body))
	}

	var res struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.ID == "" {
		return "", fmt.Errorf("instagram: empty media id in publish response")
	}
	return res.ID, nil
}
