package mastodonapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// Media represents an uploaded media object on a Mastodon instance.
type Media struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// UploadMedia uploads raw image bytes with an accessibility description.
// instance is the full HTTPS URL of the instance; pat is an app access
// token with write:media scope.
func UploadMedia(instance, pat string, image []byte, description string) (*Media, error) {
	instance = trimInstance(instance)
	if instance == "" {
		return nil, fmt.Errorf("mastodon: empty instance")
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "image.jpg")
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(image); err != nil {
		return nil, err
	}
	if description != "" {
		if err := writer.WriteField("description", description); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, instance+"/api/v1/media", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mastodon upload media status: %d body: %s", resp.StatusCode, string(respBody))
	}

	var m Media
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("mastodon decode media: %w", err)
	}
	if m.ID == "" {
		return nil, fmt.Errorf("mastodon: empty media id in response")
	}
	return &m, nil
}
