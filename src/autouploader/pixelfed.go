package autouploader

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/src/config"
	"github.com/mmcdole/gofeed"
)

func downloadImage(imageURL string) ([]byte, error) {
	resp, err := http.Get(imageURL)
	if err != nil || resp.StatusCode != 200 {
		return nil, errors.New("failed to download image")
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func uploadPixelfedMedia(entry *gofeed.Item) (string, error) {
	imageURL := entry.Image.URL
	imageData, err := downloadImage(imageURL)
	if err != nil {
		return "", err
	}

	description := extractAltText(entry.Description)

	body := &bytes.Buffer{}
	writer := multipartWriter(body, imageData, description)

	req, err := http.NewRequest("POST", config.Data.Autouploader.Pixelfed.InstanceUrl + "/api/v1/media", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer " + config.Data.Autouploader.Pixelfed.PAT)
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

func publishPixelfedPost(caption, mediaID string) error {
	if strings.TrimSpace(caption) == "" {
		return errors.New("caption cannot be empty")
	}
	data := url.Values{}
	data.Set("status", caption)
	data.Add("media_ids[]", mediaID)

	req, err := http.NewRequest("POST", config.Data.Autouploader.Pixelfed.InstanceUrl + "/api/v1/statuses", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer " + config.Data.Autouploader.Pixelfed.PAT)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return errors.New("failed to publish post")
	}
	return nil
}

func publishPixelfedEntry(entry *gofeed.Item, platform string) error {
	caption := "More at https://lna-dev.net/en/gallery\n\n"
	for _, tag := range entry.Categories {
		caption += "#" + tag + " "
	}

	mediaID, err := uploadPixelfedMedia(entry)
	if err != nil {
		return err
	}
	if err := publishPixelfedPost(caption, mediaID); err != nil {
		return err
	}
	return publishedEntry(entry.Title, platform)
}

func multipartWriter(body *bytes.Buffer, image []byte, description string) *multipart.Writer {
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "image.jpg")
	part.Write(image)
	writer.WriteField("description", description)
	writer.Close()
	return writer
}
