package autouploader

import (
	"log"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/imageresize"
	"github.com/LNA-DEV/HomePageCompanion/pixelfedapi"
	"github.com/mmcdole/gofeed"
)

func publishPixelfedEntry(entry *gofeed.Item, target config.Target, caption string) error {
	imageBytes, err := downloadImage(entry.Image.URL)
	if err != nil {
		return err
	}
	if prepared, prepErr := imageresize.PrepareForTarget(imageBytes, resolveLimits(target)); prepErr != nil {
		log.Printf("autouploader: pixelfed image prep failed for %s, sending original: %v", entry.GUID, prepErr)
	} else {
		imageBytes = prepared
	}

	description := extractAltText(entry.Description)
	media, err := pixelfedapi.UploadMedia(target.InstanceUrl, target.PAT, imageBytes, description)
	if err != nil {
		return err
	}

	post, err := pixelfedapi.CreatePost(target.InstanceUrl, target.PAT, caption, media.ID)
	if err != nil {
		return err
	}

	log.Println("Pixelfed post published:", post.URL)
	return publishedEntry(entry.GUID, target.Platform, nil, &post.URL, &post.ID)
}
