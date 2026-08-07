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
	"github.com/LNA-DEV/HomePageCompanion/imagemeta"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/mmcdole/gofeed"
	"gorm.io/gorm"
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

	entry, feed := getEntryToPublish(source, target)
	if entry == nil {
		return
	}

	// Download full-image bytes once if any metadata feature is enabled.
	var imageBytes []byte
	if needsImageBytes(connection) {
		bytes, dlErr := downloadImage(metadataImageURL(entry))
		if dlErr != nil {
			log.Printf("autouploader: metadata image download for %q failed: %v", entry.GUID, dlErr)
		} else {
			imageBytes = bytes
		}
	}

	// Evaluate routing-by-meta-tags before doing any network publish.
	if connection.RoutingTagsSource != "" {
		tags := collectRoutingTags(connection, entry, imageBytes)
		decision := EvaluateRouting(tags, target.Platform)
		if !decision.Allow {
			log.Printf("autouploader: skipping %q on %s (%s): %s",
				entry.GUID, target.Platform, target.Name, decision.Reason)
			return
		}
	}

	caption := BuildCaption(connection, entry, feed, imageBytes, captionLimitFor(target.Platform), maxHashtagsFor(target.Platform))

	var err error
	switch target.Platform {
	case "pixelfed":
		err = publishPixelfedEntry(entry, target, caption)
	case "instagram":
		err = publishInstagramEntry(entry, target, caption)
	case "bluesky":
		err = publishBlueskyEntry(entry, target, caption)
	case "mastodon":
		err = publishMastodonEntry(entry, target, caption)
	case "threads":
		err = publishThreadsEntry(entry, target, caption)
	default:
		log.Printf("Unknown platform %q for connection %q", target.Platform, connection.Name)
		return
	}

	if err != nil {
		log.Printf("Failed to publish %q to %s (%s): %v", entry.GUID, target.Platform, target.Name, err)
	}
	RecordAttempt(connection.Name, entry.GUID, target.Platform, target.Name, err, 0)
}

// needsImageBytes returns true when any of the metadata features that require
// reading EXIF/IPTC/XMP from the actual image are enabled on this connection.
func needsImageBytes(c config.Connection) bool {
	return c.AddExifToCaption ||
		c.RoutingTagsSource == "exif" ||
		c.CopyrightSource == "exif"
}

// metadataImageURL picks the best URL for downloading the *original* image so
// that EXIF/IPTC/XMP is intact. Hugo's image-RSS commonly puts the full file
// in <enclosure>; everything else falls back to entry.Image.URL.
func metadataImageURL(entry *gofeed.Item) string {
	if entry == nil {
		return ""
	}
	for _, enc := range entry.Enclosures {
		if enc == nil {
			continue
		}
		if enc.URL != "" {
			return enc.URL
		}
	}
	if entry.Image != nil {
		return entry.Image.URL
	}
	return ""
}

// captionLimitFor returns the per-platform soft cap used by BuildCaption.
func captionLimitFor(platform string) int {
	switch platform {
	case "bluesky":
		return 300
	case "threads":
		return 500
	default:
		return 2000
	}
}

// maxHashtagsFor returns the per-platform hashtag cap used by BuildCaption
// (0 = unlimited). Threads indexes only one topic tag per post and shows every
// additional #tag as dead, non-clickable text, so it is capped at 1.
func maxHashtagsFor(platform string) int {
	switch platform {
	case "threads":
		return 1
	default:
		return 0
	}
}

// collectRoutingTags returns the list of tag strings used by EvaluateRouting,
// drawn from the source declared on the connection.
func collectRoutingTags(c config.Connection, entry *gofeed.Item, imageBytes []byte) []string {
	switch c.RoutingTagsSource {
	case "rss":
		if entry == nil {
			return nil
		}
		return append([]string(nil), entry.Categories...)
	case "exif":
		if len(imageBytes) == 0 {
			return nil
		}
		meta, _ := imagemeta.Extract(imageBytes)
		if meta == nil {
			return nil
		}
		return append([]string(nil), meta.Keywords...)
	default:
		return nil
	}
}

func GetPublishedEntry(itemID string, platform string) (*models.AutoUploadItem, error) {
	var item models.AutoUploadItem
	if err := database.Db.
		Where("item_id = ?", itemID).
		Where("platform = ?", platform).
		First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &item, nil
}

func getEntryToPublish(source config.Datasource, target config.Target) (*gofeed.Item, *gofeed.Feed) {
	feedURL := source.FeedURL
	parser := gofeed.NewParser()
	feed, err := parser.ParseURL(feedURL)
	if err != nil {
		log.Printf("Error parsing feed: %v", err)
		return nil, nil
	}

	specificNames, err := getAlreadyUploadedItems(target.Platform)
	if err != nil {
		log.Printf("Error fetching already-uploaded items: %v", err)
		return nil, feed
	}

	filteredEntries := filterEntries(feed.Items, specificNames)
	if len(filteredEntries) == 0 {
		log.Println("No entries available after filtering.")
		return nil, feed
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
		return nil, feed
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

	return randomEntry, feed
}

func getAlreadyUploadedItems(platform string) ([]string, error) {
	var items []models.AutoUploadItem
	if err := database.Db.Where("platform = ?", platform).Find(&items).Error; err != nil {
		return nil, err
	}

	var names []string
	for _, item := range items {
		names = append(names, item.ItemID)
	}
	return names, nil
}

func publishedEntry(entryName string, platform string, versionId *string, postUrl *string, postId *string) error {
	item := models.AutoUploadItem{
		Platform:  platform,
		ItemID:    entryName,
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
		if !nameMap[entry.GUID] {
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

// DownloadImageBytes is an exported alias of the package-internal
// downloadImage helper. Used by sibling packages (microblog) that need to
// reuse the same fetch + 200-only contract.
func DownloadImageBytes(imageURL string) ([]byte, error) { return downloadImage(imageURL) }
