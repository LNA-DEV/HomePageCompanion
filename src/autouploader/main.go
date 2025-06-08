package autouploader

import (
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/mmcdole/gofeed"
)

func Publish(connection config.Connection) {
	var source config.Datasource

	for _, element := range config.Data.Datasources.Rss {
		if element.Name == connection.SourceName {
			source = element
		}
	}

	var target config.Target

	for _, element := range config.Data.Targets {
		if element.Name == connection.TargetName {
			target = element
			break
		}
	}

	entry := getEntryToPublish(source, target)

	switch target.Platform {
	case "pixelfed":
		if err := publishPixelfedEntry(entry, target, connection); err != nil {
			log.Fatalf("Failed to publish: %v", err)
		}

	case "instagram":
		publishInstagramEntry(entry, target, connection)

	case "bluesky":
		if err := publishBlueskyEntry(entry, target, connection); err != nil {
			log.Fatalf("Failed to publish: %v", err)
		}
	}

}

func getEntryToPublish(source config.Datasource, target config.Target) *gofeed.Item {
	feedURL := source.FeedURL
	parser := gofeed.NewParser()
	feed, err := parser.ParseURL(feedURL)
	if err != nil {
		log.Fatalf("Error parsing feed: %v", err)
	}

	specificNames, err := getAlreadyUploadedItems(target.Platform)
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
	var items []models.AutoUploadItem
	if err := database.Db.Where("platform = ?", platform).Find(&items).Error; err != nil {
		return nil, err
	}

	var names []string
	for _, item := range items {
		names = append(names, item.ItemName)
	}
	return names, nil
}

func publishedEntry(entryName string, platform string, versionId *string, postUrl *string, postId *string) error {
	item := models.AutoUploadItem{
		Platform:  platform,
		ItemName:  entryName,
		VersionId: versionId,
		PostUrl:   postUrl,
		PostId:    postId,
	}
	return database.Db.Create(&item).Error
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

func downloadImage(imageURL string) ([]byte, error) {
	resp, err := http.Get(imageURL)
	if err != nil || resp.StatusCode != 200 {
		return nil, errors.New("failed to download image")
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
