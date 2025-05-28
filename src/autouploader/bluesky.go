package autouploader

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/mmcdole/gofeed"
)

type BlueskySession struct {
	AccessJwt string `json:"accessJwt"`
	Did       string `json:"did"`
	Handle    string `json:"handle"`
}

type BlueskyImageBlob struct {
	Blob struct {
		Ref struct {
			Link string `json:"$link"`
		} `json:"ref"`
		MimeType string `json:"mimeType"`
		Size     int    `json:"size"`
	} `json:"blob"`
}

type BlueskyPostRequest struct {
	Collection string `json:"collection"`
	Repo       string `json:"repo"`
	Record     struct {
		Text    string   `json:"text"`
		Created string   `json:"createdAt"`
		Embed   any      `json:"embed"`
		Langs   []string `json:"langs"`
		Type    string   `json:"$type"`
		Facets  []any    `json:"facets,omitempty"`
	} `json:"record"`
}

func publishBlueskyEntry(entry *gofeed.Item, platform string) error {
	bskyUsername := config.Data.Autouploader.Bluesky.Username
	bskyPassword := config.Data.Autouploader.Bluesky.PAT

	// Login to Bluesky
	session, err := blueskyLogin(bskyUsername, bskyPassword)
	if err != nil {
		return err
	}

	// Build caption
	var caption strings.Builder
	caption.WriteString("More at https://lna-dev.net/en/gallery\n\n")

	count := len(caption.String())
	for _, tag := range entry.Categories {
		tagText := "#" + tag
		if count+len(tagText)+1 <= 300 {
			caption.WriteString(tagText + " ")
			count += len(tagText) + 1
		}
	}

	mediaURL := entry.Image.URL

	// Extract alt text
	altText := extractAltText(entry.Description)
	if altText == "" {
		altText = "Alt not found"
	}

	// Download image
	imageBytes, err := downloadImage(mediaURL)
	if err != nil {
		return err
	}

	// Upload image to Bluesky
	blobRef, err := blueskyUploadImage(session.AccessJwt, imageBytes, altText)
	if err != nil {
		return err
	}

	// Build post payload
	post := BlueskyPostRequest{
		Collection: "app.bsky.feed.post",
		Repo:       session.Did,
	}
	post.Record.Text = caption.String()
	post.Record.Created = time.Now().Format(time.RFC3339)
	post.Record.Type = "app.bsky.feed.post"
	post.Record.Langs = []string{"en"}
	post.Record.Embed = map[string]interface{}{
		"$type": "app.bsky.embed.images",
		"images": []interface{}{
			map[string]interface{}{
				"image": map[string]interface{}{
					"$type": "blob",
					"ref": map[string]interface{}{
						"$link": blobRef.Blob.Ref.Link,
					},
					"mimeType": blobRef.Blob.MimeType,
					"size":     blobRef.Blob.Size,
				},
				"alt": altText,
			},
		},
	}

	bodyBytes, _ := json.Marshal(post)
	req, _ := http.NewRequest("POST", "https://bsky.social/xrpc/com.atproto.repo.createRecord", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Authorization", "Bearer "+session.AccessJwt)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode >= 300 {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}
		defer resp.Body.Close()

		return fmt.Errorf("failed to publish entry, status: %d Message: %s", resp.StatusCode, string(bodyBytes))
	}
	defer resp.Body.Close()

	// Mark as published
	if err := publishedEntry(entry.Title, platform); err != nil {
		return err
	}

	log.Println("Entry published successfully:", entry.Title)
	return nil
}

func blueskyLogin(username, password string) (*BlueskySession, error) {
	payload := map[string]string{
		"identifier": username,
		"password":   password,
	}
	data, _ := json.Marshal(payload)
	resp, err := http.Post("https://bsky.social/xrpc/com.atproto.server.createSession", "application/json", bytes.NewBuffer(data))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to authenticate, status code: %d", resp.StatusCode)
	}

	var session BlueskySession
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, err
	}
	return &session, nil
}

func blueskyUploadImage(token string, image []byte, alt string) (*BlueskyImageBlob, error) {
	req, _ := http.NewRequest("POST", "https://bsky.social/xrpc/com.atproto.repo.uploadBlob", bytes.NewReader(image))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "image/jpeg")

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return nil, fmt.Errorf("upload failed, status: %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	var blob BlueskyImageBlob
	if err := json.NewDecoder(resp.Body).Decode(&blob); err != nil {
		return nil, err
	}
	return &blob, nil
}
