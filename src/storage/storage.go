// Package storage names where in the shared bucket each module keeps its
// objects: "<storage.prefix>/<module>/…", by default
// "home-page-companion/<module>/…". One folder for the companion, one
// subfolder per module, so the bucket can be shared with anything else.
package storage

import (
	"path"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/config"
)

// DefaultPrefix is the companion's folder when storage.prefix is empty.
const DefaultPrefix = "home-page-companion"

// BucketURL is the configured bucket, "" when none is.
func BucketURL() string {
	return strings.TrimSpace(config.Data.Storage.BucketURL)
}

// Prefix returns the key prefix of a module, e.g. Prefix("maps/basemap") is
// "home-page-companion/maps/basemap". No leading or trailing slash.
func Prefix(module string) string {
	root := strings.Trim(config.Data.Storage.Prefix, "/")
	if root == "" {
		root = DefaultPrefix
	}
	return strings.Trim(path.Join(root, strings.Trim(module, "/")), "/")
}
