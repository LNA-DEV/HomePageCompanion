package pixelfedapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// Media represents an uploaded media object on a Pixelfed instance.
type Media struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// UploadMedia uploads raw image bytes with an accessibility description and
// returns the resulting media object. instance should be the full HTTPS URL
// of the Pixelfed instance (matches config.Target.InstanceUrl).
func UploadMedia(instance, pat string, image []byte, description string) (*Media, error) {
	if strings.TrimSpace(instance) == "" {
		return nil, fmt.Errorf("pixelfed: empty instance")
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
	if err := writer.WriteField("description", description); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(instance, "/")+"/api/v1/media", body)
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
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pixelfed upload failed, status: %d body: %s", resp.StatusCode, string(respBody))
	}

	var raw struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("pixelfed decode media response: %w", err)
	}
	if raw.ID == "" {
		return nil, fmt.Errorf("pixelfed: empty media id in response")
	}
	return &Media{ID: raw.ID, URL: raw.URL}, nil
}
