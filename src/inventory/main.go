package inventory

import (
	"log"

	"github.com/LNA-DEV/HomePageCompanion/config"
)

// PopulateDatabase mirrors every image feed. A failing feed is logged and
// skipped; it never stops the companion.
func PopulateDatabase() {
	for _, item := range config.Data.Datasources.Rss {
		switch item.ItemType {
		case "image":
			if err := imageRssToDatabase(item.FeedURL, item.Name); err != nil {
				log.Printf("inventory: %v", err)
			}
		}
	}
}
