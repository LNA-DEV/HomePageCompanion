package inventory

import "github.com/LNA-DEV/HomePageCompanion/config"

func PopulateDatabase() {
	for _, item := range config.Data.Datasources.Rss {
		switch item.ItemType {
		case "image":
			imageRssToDatabase(item.FeedURL, item.Name)
		}
	}
}
