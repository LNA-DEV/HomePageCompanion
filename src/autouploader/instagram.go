package autouploader

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/mmcdole/gofeed"
)

var graphURL = "https://graph.instagram.com/v22.0/"

func postInstagramImage(caption, imageURL, accountID, accessToken string) (string, error) {
	endpoint := fmt.Sprintf("%s%s/media", graphURL, accountID)
	params := url.Values{}
	params.Set("access_token", accessToken)
	params.Set("caption", caption)
	params.Set("image_url", imageURL)

	resp, err := http.PostForm(endpoint, params)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var res map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	if id, ok := res["id"].(string); ok {
		return id, nil
	}
	return "", fmt.Errorf("failed to create media container: %v", res)
}

func publishInstagramContainer(creationID, accountID, accessToken string) (*string, error) {
	endpoint := fmt.Sprintf("%s%s/media_publish", graphURL, accountID)
	params := url.Values{}
	params.Set("access_token", accessToken)
	params.Set("creation_id", creationID)

	resp, err := http.PostForm(endpoint, params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var res map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	if id, ok := res["id"].(string); ok {
		return &id, nil
	}
	return nil, fmt.Errorf("failed to publish container: %v", res)
}

func checkInstagramMediaStatus(creationID, accessToken string) (string, error) {
	url := fmt.Sprintf("%s%s?fields=status_code&access_token=%s", graphURL, creationID, accessToken)
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var res map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	if status, ok := res["status_code"].(string); ok {
		return status, nil
	}
	return "", fmt.Errorf("status not found: %v", res)
}

func publishInstagramEntry(entry *gofeed.Item, platform string) {
	// Build caption
	caption := config.Data.Autouploader.Instagram.Caption + "\n\n"
	for _, tag := range entry.Categories {
		caption += "#" + tag + " "
	}

	// Get image and alt text
	mediaURL := entry.Image.URL
	altText := extractAltText(entry.Description)
	if altText == "" {
		altText = "Alt not found"
	}

	fmt.Println("Posting to Instagram...")

	creationID, err := postInstagramImage(caption, mediaURL, config.Data.Autouploader.Instagram.AccountId, config.Data.Autouploader.Instagram.AccessToken)
	if err != nil {
		log.Printf("Error creating media container: %v\n", err)
		return
	}

	// Wait for media processing
	maxRetries := 10
	var status string
	for i := 0; i < maxRetries; i++ {
		status, err = checkInstagramMediaStatus(creationID, config.Data.Autouploader.Instagram.AccessToken)
		if err != nil {
			log.Printf("Status check failed (attempt %d): %v\n", i+1, err)
			time.Sleep(2 * time.Second)
			continue
		}

		log.Printf("Attempt %d: Status = %s\n", i+1, status)
		if status == "FINISHED" {
			break
		}
		time.Sleep(2 * time.Second)
	}

	if status != "FINISHED" {
		log.Println("Media was not ready after waiting. Exiting.")
		return
	}

	publishID, err := publishInstagramContainer(creationID, config.Data.Autouploader.Instagram.AccountId, config.Data.Autouploader.Instagram.AccessToken)
	if err != nil {
		log.Printf("Error publishing media: %v\n", err)
		return
	}

	log.Printf("Published to Instagram: %s\n", *publishID)
	if err := publishedEntry(entry.Title, platform, nil, nil, publishID); err != nil {
		log.Printf("Error recording published entry: %v\n", err)
	}
}
