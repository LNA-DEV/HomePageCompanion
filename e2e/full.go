// Package e2e compiles in every building block of this repository, as the
// full image does, for the tests that run a whole companion.
package e2e

import (
	_ "github.com/LNA-DEV/HomePageCompanion/modules/autoupload"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/basemap"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/blob-fs"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/blob-s3"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/bluesky"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/db-sqlite"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/gbif"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/instagram"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/interactions"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/mastodon"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/media"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/microblog"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/pixelfed"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/routing"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/rss"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/threads"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/trips"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/webmention"
	_ "github.com/LNA-DEV/HomePageCompanion/modules/webpush"
)
