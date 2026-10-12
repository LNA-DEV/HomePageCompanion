package instagramapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/LNA-DEV/HomePageCompanion/redact"
)

// UserTag tags an account on the photo. X and Y are the position of the tag
// on the image, from 0.0 to 1.0. The username has no "@" and must belong to a
// public account.
type UserTag struct {
	Username string  `json:"username"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
}

// StatusError is a response Instagram rejected with a status other than 429
// (which is ErrRateLimited).
type StatusError struct {
	Op         string
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("instagram %s status %d body: %s", e.Op, e.StatusCode, e.Body)
}

// CreateMediaContainer uploads (by URL) an image to Instagram as a media
// container and returns the creation ID. Caption can be empty. userTags tag
// accounts on the photo; nil means none.
func CreateMediaContainer(accountID, accessToken, imageURL, caption string, userTags []UserTag) (string, error) {
	endpoint := fmt.Sprintf("%s%s/media", GraphURL, accountID)
	params := url.Values{}
	params.Set("access_token", accessToken)
	params.Set("caption", caption)
	params.Set("image_url", imageURL)
	if len(userTags) > 0 {
		tags, err := json.Marshal(userTags)
		if err != nil {
			return "", err
		}
		params.Set("user_tags", string(tags))
	}

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
		return "", redact.URLError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", &StatusError{Op: "create container", StatusCode: resp.StatusCode, Body: string(body)}
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
		return "", redact.URLError(err)
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
		return "", redact.URLError(err)
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
