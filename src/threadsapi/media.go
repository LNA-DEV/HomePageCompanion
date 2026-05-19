package threadsapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// CreateImageContainer uploads (by URL) an image as a Threads media container
// and returns the creation id. Caption may be empty.
//
// Threads fetches imageURL itself, so bytes never cross our process — the
// same shape as Instagram's Graph API container create.
func CreateImageContainer(userID, accessToken, imageURL, caption string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("threads: empty user id")
	}
	endpoint := fmt.Sprintf("%s%s/threads", GraphURL, userID)
	params := url.Values{}
	params.Set("access_token", accessToken)
	params.Set("media_type", "IMAGE")
	params.Set("image_url", imageURL)
	if caption != "" {
		params.Set("text", caption)
	}
	return postContainer(endpoint, params)
}

// CreateTextContainer creates a text-only Threads media container and returns
// the creation id. Text must be non-empty; Threads rejects empty TEXT posts.
func CreateTextContainer(userID, accessToken, text string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("threads: empty user id")
	}
	if text == "" {
		return "", fmt.Errorf("threads: empty text for TEXT container")
	}
	endpoint := fmt.Sprintf("%s%s/threads", GraphURL, userID)
	params := url.Values{}
	params.Set("access_token", accessToken)
	params.Set("media_type", "TEXT")
	params.Set("text", text)
	return postContainer(endpoint, params)
}

// postContainer is the shared body of CreateImageContainer + CreateTextContainer:
// POSTs the params and decodes the {id} response.
func postContainer(endpoint string, params url.Values) (string, error) {
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
		return "", fmt.Errorf("threads create container status %d body: %s", resp.StatusCode, string(body))
	}

	var res struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.ID == "" {
		return "", fmt.Errorf("threads: empty creation id in response")
	}
	return res.ID, nil
}

// CheckMediaStatus returns the current `status` of a creation container
// (e.g. "IN_PROGRESS", "FINISHED", "ERROR", "EXPIRED"). Callers poll between
// container create and PublishContainer.
func CheckMediaStatus(creationID, accessToken string) (string, error) {
	if creationID == "" {
		return "", fmt.Errorf("threads: empty creation id")
	}
	endpoint := fmt.Sprintf("%s%s", GraphURL, creationID)
	params := url.Values{}
	params.Set("fields", "status,error_message")
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
		return "", fmt.Errorf("threads status check status %d body: %s", resp.StatusCode, string(body))
	}

	var res struct {
		Status       string `json:"status"`
		ErrorMessage string `json:"error_message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.Status == "" {
		return "", fmt.Errorf("threads: empty status in response")
	}
	return res.Status, nil
}

// PublishContainer publishes a previously-created media container and returns
// the resulting media id.
func PublishContainer(userID, accessToken, creationID string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("threads: empty user id")
	}
	endpoint := fmt.Sprintf("%s%s/threads_publish", GraphURL, userID)
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
		return "", fmt.Errorf("threads publish status %d body: %s", resp.StatusCode, string(body))
	}

	var res struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.ID == "" {
		return "", fmt.Errorf("threads: empty media id in publish response")
	}
	return res.ID, nil
}

// GetPermalink resolves a published media's human-clickable URL.
func GetPermalink(mediaID, accessToken string) (string, error) {
	if mediaID == "" {
		return "", fmt.Errorf("threads: empty media id")
	}
	endpoint := fmt.Sprintf("%s%s", GraphURL, mediaID)
	params := url.Values{}
	params.Set("fields", "permalink")
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
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("threads permalink status %d body: %s", resp.StatusCode, string(body))
	}

	var res struct {
		Permalink string `json:"permalink"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	return res.Permalink, nil
}

// DeleteMedia removes a published Threads media item. 404 is folded into
// ErrNotFound so callers can treat it as success (idempotent delete).
func DeleteMedia(mediaID, accessToken string) error {
	if mediaID == "" {
		return fmt.Errorf("threads: empty media id")
	}
	endpoint := fmt.Sprintf("%s%s", GraphURL, mediaID)
	params := url.Values{}
	params.Set("access_token", accessToken)

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.URL.RawQuery = params.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		return ErrRateLimited
	default:
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("threads delete media status %d body: %s", resp.StatusCode, string(body))
	}
}
