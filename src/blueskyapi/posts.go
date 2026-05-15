package blueskyapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// BlobRef is the reference Bluesky returns for an uploaded image. Embed in
// the post payload via CreatePost.
type BlobRef struct {
	Blob struct {
		Ref struct {
			Link string `json:"$link"`
		} `json:"ref"`
		MimeType string `json:"mimeType"`
		Size     int    `json:"size"`
	} `json:"blob"`
}

// PostRef identifies a published post by its AT URI and content ID.
type PostRef struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

// UploadImage uploads raw image bytes and returns the blob ref. The mimeType
// argument is sent verbatim in Content-Type; pass e.g. "image/jpeg".
func UploadImage(session *Session, image []byte, mimeType string) (*BlobRef, error) {
	if session == nil {
		return nil, fmt.Errorf("bluesky: nil session")
	}
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	req, _ := http.NewRequest(http.MethodPost, uploadEndpoint, bytes.NewReader(image))
	req.Header.Set("Authorization", "Bearer "+session.AccessJwt)
	req.Header.Set("Content-Type", mimeType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bluesky upload failed, status: %d body: %s", resp.StatusCode, string(body))
	}

	var blob BlobRef
	if err := json.NewDecoder(resp.Body).Decode(&blob); err != nil {
		return nil, err
	}
	return &blob, nil
}

// CreatePost publishes a text post on the authenticated repo, optionally
// with an image embed. Pass blob == nil for a text-only post. altText is
// only used when blob != nil; if empty it falls back to "Alt not found".
// createdAt is sent verbatim; if zero, time.Now() is used.
func CreatePost(session *Session, text, altText string, blob *BlobRef, createdAt time.Time) (*PostRef, error) {
	if session == nil {
		return nil, fmt.Errorf("bluesky: nil session")
	}
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	type postRecord struct {
		Text    string   `json:"text"`
		Created string   `json:"createdAt"`
		Embed   any      `json:"embed,omitempty"`
		Langs   []string `json:"langs"`
		Type    string   `json:"$type"`
		Facets  []any    `json:"facets,omitempty"`
	}
	type createRequest struct {
		Collection string     `json:"collection"`
		Repo       string     `json:"repo"`
		Record     postRecord `json:"record"`
	}

	record := postRecord{
		Text:    text,
		Created: createdAt.Format(time.RFC3339),
		Type:    "app.bsky.feed.post",
		Langs:   []string{"en"},
		Facets:  toAnySlice(ExtractFacets(text)),
	}
	if blob != nil {
		if altText == "" {
			altText = "Alt not found"
		}
		record.Embed = map[string]interface{}{
			"$type": "app.bsky.embed.images",
			"images": []interface{}{
				map[string]interface{}{
					"image": map[string]interface{}{
						"$type": "blob",
						"ref": map[string]interface{}{
							"$link": blob.Blob.Ref.Link,
						},
						"mimeType": blob.Blob.MimeType,
						"size":     blob.Blob.Size,
					},
					"alt": altText,
				},
			},
		}
	}

	payload := createRequest{
		Collection: "app.bsky.feed.post",
		Repo:       session.Did,
		Record:     record,
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, createRecordPath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+session.AccessJwt)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bluesky create post failed, status: %d body: %s", resp.StatusCode, string(respBody))
	}

	var post PostRef
	if err := json.NewDecoder(resp.Body).Decode(&post); err != nil {
		return nil, fmt.Errorf("bluesky decode response: %w", err)
	}
	return &post, nil
}

// ExtractFacets returns Bluesky-style hashtag and link facets for the given
// text. Useful when composing post text outside the package.
func ExtractFacets(text string) []map[string]interface{} {
	var facets []map[string]interface{}

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

func toAnySlice(maps []map[string]interface{}) []any {
	if maps == nil {
		return nil
	}
	result := make([]any, len(maps))
	for i, m := range maps {
		result[i] = m
	}
	return result
}

// deleteRecordEndpoint is the AT-proto repo deletion XRPC call.
const deleteRecordEndpoint = baseURL + "/xrpc/com.atproto.repo.deleteRecord"

// DeleteRecord removes a post from the authenticated session's repo. The
// uri is the AT URI (at://did/.../<rkey>); only the rkey segment is used.
// HTTP 200 and 404 are both treated as success — idempotent delete.
func DeleteRecord(session *Session, uri string) error {
	if session == nil {
		return fmt.Errorf("bluesky: nil session")
	}
	if uri == "" {
		return fmt.Errorf("bluesky: empty uri")
	}
	rkey := uri
	if idx := strings.LastIndex(uri, "/"); idx >= 0 && idx < len(uri)-1 {
		rkey = uri[idx+1:]
	}

	payload := map[string]string{
		"repo":       session.Did,
		"collection": "app.bsky.feed.post",
		"rkey":       rkey,
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, deleteRecordEndpoint, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+session.AccessJwt)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return nil // already gone — fine
	case resp.StatusCode == http.StatusTooManyRequests:
		return ErrRateLimited
	default:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bluesky deleteRecord status %d body: %s", resp.StatusCode, string(respBody))
	}
}
