package autouploader

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	blueskyapi "github.com/LNA-DEV/HomePageCompanion/blue_sky_api"
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/mmcdole/gofeed"
)

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

func publishBlueskyEntry(entry *gofeed.Item, target config.Target, connection config.Connection) error {
	bskyUsername := target.Username
	bskyPassword := target.PAT

	// Login to Bluesky
	session, httpErr := blueskyapi.BlueskyLogin(bskyUsername, bskyPassword)
	if httpErr != nil {
		return httpErr
	}

	// Build caption
	var caption strings.Builder
	caption.WriteString(connection.Caption + "\n\n")

	count := len(caption.String())
	for _, tag := range entry.Categories {
		tagText := "#" + tag
		if count+len(tagText)+1 <= 300 {
			caption.WriteString(tagText + " ")
			count += len(tagText) + 1
		}
	}

	text := caption.String()
	facets := extractFacets(text)

	mediaURL := entry.Image.URL

	// Extract alt text
	altText := extractAltText(entry.Description)
	if altText == "" {
		altText = "Alt not found"
	}

	// Download image
	imageBytes, httpErr := downloadImage(mediaURL)
	if httpErr != nil {
		return httpErr
	}

	// Upload image to Bluesky
	blobRef, httpErr := blueskyUploadImage(session.AccessJwt, imageBytes, altText)
	if httpErr != nil {
		return httpErr
	}

	// Build post payload
	post := BlueskyPostRequest{
		Collection: "app.bsky.feed.post",
		Repo:       session.Did,
	}
	post.Record.Text = text
	post.Record.Facets = toAnySlice(facets)
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

	resp, httpErr := http.DefaultClient.Do(req)
	if httpErr != nil || resp.StatusCode >= 300 {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}
		defer resp.Body.Close()

		return fmt.Errorf("failed to publish entry, status: %d Message: %s", resp.StatusCode, string(bodyBytes))
	}
	defer resp.Body.Close()

	// Read the response body into bytes
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("failed to read response body: %w", err)
	}
	defer resp.Body.Close()

	bodyStr := string(bodyBytes)
	fmt.Println("Bluesky raw response:", bodyStr)

	var postResponse struct {
		URI string `json:"uri"`
		CID string `json:"cid"`
	}

	if err := json.Unmarshal(bodyBytes, &postResponse); err != nil {
		fmt.Println("Bluesky response could not be decoded:", err)
	}

	// Mark as published
	if err := publishedEntry(entry.Title, target.Platform, &postResponse.CID, &postResponse.URI, nil); err != nil {
		return err
	}

	log.Println("Entry published successfully:", entry.Title)
	return nil
}

func toAnySlice(maps []map[string]interface{}) []any {
	result := make([]any, len(maps))
	for i, m := range maps {
		result[i] = m
	}
	return result
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

// Returns facets for hashtags and URLs
func extractFacets(text string) []map[string]interface{} {
	var facets []map[string]interface{}

	// Hashtag pattern
	hashtagPattern := regexp.MustCompile(`#\w+`)
	for _, match := range hashtagPattern.FindAllStringIndex(text, -1) {
		start, end := match[0], match[1]
		tag := text[start+1 : end]
		facets = append(facets, map[string]interface{}{
			"index": map[string]int{
				"byteStart": start,
				"byteEnd":   end,
			},
			"features": []interface{}{
				map[string]interface{}{
					"$type": "app.bsky.richtext.facet#tag",
					"tag":   tag,
				},
			},
		})
	}

	// URL pattern
	urlPattern := regexp.MustCompile(`https?://[^\s]+`)
	for _, match := range urlPattern.FindAllStringIndex(text, -1) {
		start, end := match[0], match[1]
		url := text[start:end]
		facets = append(facets, map[string]interface{}{
			"index": map[string]int{
				"byteStart": start,
				"byteEnd":   end,
			},
			"features": []interface{}{
				map[string]interface{}{
					"$type": "app.bsky.richtext.facet#link",
					"uri":   url,
				},
			},
		})
	}

	return facets
}
