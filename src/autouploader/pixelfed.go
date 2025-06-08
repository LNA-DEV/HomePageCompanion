package autouploader

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/mmcdole/gofeed"
)

func uploadPixelfedMedia(entry *gofeed.Item, target config.Target) (string, error) {
	imageURL := entry.Image.URL
	imageData, err := downloadImage(imageURL)
	if err != nil {
		return "", err
	}

	description := extractAltText(entry.Description)

	body := &bytes.Buffer{}
	writer := multipartWriter(body, imageData, description)

	req, err := http.NewRequest("POST", target.InstanceUrl + "/api/v1/media", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer " + target.PAT)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload failed: %s", respBody)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	return result["id"].(string), nil
}

type PixelfedResponse struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func publishPixelfedPost(caption, mediaID string, target config.Target) (error, *PixelfedResponse) {
	if strings.TrimSpace(caption) == "" {
		return errors.New("caption cannot be empty"), nil
	}

	data := url.Values{}
	data.Set("status", caption)
	data.Add("media_ids[]", mediaID)

	req, err := http.NewRequest("POST", target.InstanceUrl + "/api/v1/statuses", strings.NewReader(data.Encode()))
	if err != nil {
		return err, nil
	}
	req.Header.Set("Authorization", "Bearer " + target.PAT)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err), nil
	}
	defer resp.Body.Close()

	// Handle HTTP errors
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to publish post, status: %d, body: %s", resp.StatusCode, body), nil
	}

	// Read and parse response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("failed to read response: %w", err)
	}

	fmt.Println("Pixelfed raw response:", string(body))

	var postResponse PixelfedResponse

	if err := json.Unmarshal(body, &postResponse); err != nil {
		fmt.Println("failed to parse Pixelfed response: %w", err)
	}

	return nil, &postResponse
}

func publishPixelfedEntry(entry *gofeed.Item, target config.Target, connection config.Connection) error {
	caption := connection.Caption + "\n\n"
	for _, tag := range entry.Categories {
		caption += "#" + tag + " "
	}

	mediaID, err := uploadPixelfedMedia(entry, target)
	if err != nil {
		return err
	}

	err, response := publishPixelfedPost(caption, mediaID, target)
	if err != nil {
		return fmt.Errorf("failed to publish post: %w", err)
	}

	log.Println("Pixelfed post published:", response.URL)

	return publishedEntry(entry.Title, target.Platform, nil, &response.URL, &response.ID)
}

func multipartWriter(body *bytes.Buffer, image []byte, description string) *multipart.Writer {
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "image.jpg")
	part.Write(image)
	writer.WriteField("description", description)
	writer.Close()
	return writer
}
