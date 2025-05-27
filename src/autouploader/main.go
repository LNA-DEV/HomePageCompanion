package autouploader

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/src/config"
	"github.com/mmcdole/gofeed"
)

func Publish(platform string) {
	entry := getEntryToPublish(platform)

	switch platform {
	case "pixelfed":
		if err := publishPixelfedEntry(entry, platform); err != nil {
			log.Fatalf("Failed to publish: %v", err)
		}
	}
}

func getEntryToPublish(platform string) *gofeed.Item {
	feedURL := config.Data.Autouploader.FeedUrl
	parser := gofeed.NewParser()
	feed, err := parser.ParseURL(feedURL)
	if err != nil {
		log.Fatalf("Error parsing feed: %v", err)
	}

	specificNames, err := getAlreadyUploadedItems(platform)
	if err != nil {
		log.Fatal(err)
	}

	filteredEntries := filterEntries(feed.Items, specificNames)
	if len(filteredEntries) == 0 {
		log.Println("No entries available after filtering.")
		return nil
	}

	now := time.Now()
	var closestEntry *gofeed.Item
	var skipped []*gofeed.Item
	minDiff := math.MaxFloat64

	for _, entry := range filteredEntries {
		published := entry.PublishedParsed
		if published == nil || published.Year() <= 1 {
			skipped = append(skipped, entry)
			continue
		}

		adjustedNow := time.Date(published.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second(), 0, time.UTC)
		diff := math.Abs(adjustedNow.Sub(*published).Seconds())

		if diff < minDiff {
			minDiff = diff
			closestEntry = entry
		}
	}

	if closestEntry == nil {
		log.Println("No valid entries available after filtering.")
		return nil
	}

	var closestEntries []*gofeed.Item
	for _, entry := range filteredEntries {
		if entry.Published == closestEntry.Published {
			closestEntries = append(closestEntries, entry)
		}
	}
	closestEntries = append(closestEntries, skipped...)

	randomEntry := closestEntries[rand.Intn(len(closestEntries))]
	fmt.Println("Random entry closest to current date/time (ignoring year):")
	fmt.Println("Title:", randomEntry.Title)
	fmt.Println("URL:", randomEntry.Link)
	fmt.Println("Published Date:", randomEntry.Published)

	return randomEntry
}

func getAlreadyUploadedItems(platform string) ([]string, error) {
	resp, err := http.Get("https://api.lna-dev.net/autouploader/" + platform)
	if err != nil || resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to fetch uploaded items, status code: %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	var uploaded []string
	if err := json.NewDecoder(resp.Body).Decode(&uploaded); err != nil {
		return nil, err
	}
	return uploaded, nil
}

func publishedEntry(entryName string, platform string) error {
	urlStr := "https://api.lna-dev.net/autouploader/" + url.PathEscape(platform) + "?item=" + url.QueryEscape(entryName)

	req, err := http.NewRequest("POST", urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "ApiKey " + config.Data.Security.ApiKey)
	_, err = http.DefaultClient.Do(req)
	return err
}

func filterEntries(entries []*gofeed.Item, nameList []string) []*gofeed.Item {
	nameMap := make(map[string]bool)
	for _, name := range nameList {
		nameMap[name] = true
	}

	var filtered []*gofeed.Item
	for _, entry := range entries {
		if !nameMap[entry.Title] {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func extractAltText(html string) string {
	re := regexp.MustCompile(`alt="(.*?)"`)
	match := re.FindStringSubmatch(html)
	if len(match) > 1 {
		return match[1]
	}
	return ""
}
