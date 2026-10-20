package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Image is an uploaded image for an images embed.
type Image struct {
	Blob *BlobRef
	Alt  string
}

func blobJSON(b *BlobRef) map[string]any {
	return map[string]any{
		"$type":    "blob",
		"ref":      map[string]any{"$link": b.Blob.Ref.Link},
		"mimeType": b.Blob.MimeType,
		"size":     b.Blob.Size,
	}
}

// ImagesEmbed is app.bsky.embed.images for up to four images.
func ImagesEmbed(images []Image) map[string]any {
	var list []any
	for _, img := range images {
		alt := img.Alt
		if alt == "" {
			alt = "Alt not found"
		}
		list = append(list, map[string]any{"image": blobJSON(img.Blob), "alt": alt})
	}
	return map[string]any{"$type": "app.bsky.embed.images", "images": list}
}

// ExternalEmbed is app.bsky.embed.external: the link card Bluesky shows for
// a link. Bluesky does not build it from the text; the sender supplies
// title, description and an optional thumbnail.
func ExternalEmbed(uri, title, description string, thumb *BlobRef) map[string]any {
	ext := map[string]any{"uri": uri, "title": title, "description": description}
	if thumb != nil {
		ext["thumb"] = blobJSON(thumb)
	}
	return map[string]any{"$type": "app.bsky.embed.external", "external": ext}
}

// CreateRecord publishes a post with an optional embed (ImagesEmbed or
// ExternalEmbed). mentions maps mention text to DIDs, for the facets.
func CreateRecord(session *Session, text string, embed map[string]any, langs []string, createdAt time.Time, mentions map[string]string) (*PostRef, error) {
	if session == nil {
		return nil, fmt.Errorf("bluesky: nil session")
	}
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	if len(langs) == 0 {
		langs = []string{"en"}
	}
	record := map[string]any{
		"$type":     "app.bsky.feed.post",
		"text":      text,
		"createdAt": createdAt.Format(time.RFC3339),
		"langs":     langs,
	}
	if facets := ExtractFacets(text, mentions); len(facets) > 0 {
		record["facets"] = toAnySlice(facets)
	}
	if embed != nil {
		record["embed"] = embed
	}
	body, _ := json.Marshal(map[string]any{"collection": "app.bsky.feed.post", "repo": session.Did, "record": record})
	req, _ := http.NewRequest(http.MethodPost, session.url("/xrpc/com.atproto.repo.createRecord"), bytes.NewReader(body))
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
