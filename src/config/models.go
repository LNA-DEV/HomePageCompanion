package config

type Config struct {
	Security struct {
		ApiKey     string `yaml:"apiKey"`
		Domain     string `yaml:"domain"`
		IPHashSalt string `yaml:"ipHashSalt"`
		// ExtraOrigins are admitted by CORS on top of Domain's subdomains and
		// localhost. Exact origins, never patterns — the onion site is the
		// reason this exists ("http://<address>.onion").
		ExtraOrigins []string `yaml:"extraOrigins"`
	} `yaml:"security"`
	Datasources struct {
		Rss []Datasource `yaml:"rss"`
	} `yaml:"datasources"`
	Targets     []Target     `yaml:"targets"`
	Connections []Connection `yaml:"connections"`
	Webpush     struct {
		Subscriber string `yaml:"subscriberMail"`
	} `yaml:"webpush"`
	Microblog Microblog `yaml:"microblog"`
	Storage   Storage   `yaml:"storage"`
	Basemap   Basemap   `yaml:"basemap"`
	Routing   Routing   `yaml:"routing"`
	Gbif      Gbif      `yaml:"gbif"`
}

// Storage is the object store the companion's modules share. Each module
// keeps its objects under "<Prefix>/<module>/…" (storage.Prefix), so the
// bucket can hold other things and the companion's own part stays one folder:
// home-page-companion/maps/basemap/20261005.pmtiles.
type Storage struct {
	// BucketURL is a gocloud bucket URL:
	// "s3://<bucket>?endpoint=https://nbg1.your-objectstorage.com&region=nbg1"
	// in production, "file:///abs/dir" for local development. S3 credentials
	// come from AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY.
	BucketURL string `yaml:"bucketUrl"`
	// Prefix is the companion's folder in the bucket ("home-page-companion"
	// when empty). Nothing outside it is ever written or deleted.
	Prefix string `yaml:"prefix"`
}

// Basemap configures the self-hosted vector basemap: a Protomaps planet build
// copied into the storage bucket (<storage prefix>/maps/basemap/) by a
// monthly job and served as z/x/y tiles. Off unless Enabled and a storage
// bucket is configured. See docs/concepts/self-hosted-maps.md in the
// Home-Page repository.
type Basemap struct {
	Enabled bool `yaml:"enabled"`
	// Source is the base URL of the daily builds, "<Source>/<YYYYMMDD>.pmtiles".
	Source string `yaml:"source"`
	// SchemaMajor is the tile schema the site's style is written for. A build
	// whose metadata "version" has another major is refused before copying.
	SchemaMajor int `yaml:"schemaMajor"`
	// Schedule is a robfig/cron spec with seconds. Empty: manual runs only.
	Schedule string `yaml:"schedule"`
	// RetainDays keeps the previous version this long after a switch, so a
	// browser holding the old TileJSON keeps getting tiles.
	RetainDays int `yaml:"retainDays"`
	// PublicURL is the tile base as browsers see it; TileJSON's tiles URL is
	// "<PublicURL>/<version>/{z}/{x}/{y}.mvt".
	PublicURL string `yaml:"publicUrl"`
	// PartSizeMiB and Concurrency shape the multipart copy (64 and 4 when 0).
	PartSizeMiB int `yaml:"partSizeMiB"`
	Concurrency int `yaml:"concurrency"`
	// CacheSizeMB is go-pmtiles' directory cache (64 when 0).
	CacheSizeMB int `yaml:"cacheSizeMB"`
}

// Routing configures the server-side route geometry for trip legs. Both URLs
// default to the public services the website used to call from the browser.
type Routing struct {
	OSRMURL       string `yaml:"osrmUrl"`
	TransitousURL string `yaml:"transitousUrl"`
}

// Gbif configures the proxy for GBIF's occurrence-density tiles.
type Gbif struct {
	// BaseURL is the density tile endpoint (default
	// "https://api.gbif.org/v2/map/occurrence/density").
	BaseURL string `yaml:"baseUrl"`
	// CacheMB caps the on-disk tile cache (1024 when 0).
	CacheMB int `yaml:"cacheMB"`
}

// Microblog holds the federation settings for the locally-authored
// microblog. PublishTo names entries from Targets; targets must declare
// platform: mastodon (other platforms are ignored).
type Microblog struct {
	PublishTo []string `yaml:"publishTo"`
}

type Connection struct {
	Name       string  `yaml:"name"`
	SourceName string  `yaml:"sourceName"`
	TargetName string  `yaml:"targetName"`
	Caption    string  `yaml:"caption"`
	Cron       *string `yaml:"cron"`

	// IncludeAltText injects the image's alt text (parsed from the RSS item's
	// description) into the post body. Defaults to false. When set alongside a
	// non-empty Caption, the alt text is placed first, then the caption. To
	// omit the caption entirely, simply leave Caption empty.
	IncludeAltText bool `yaml:"includeAltText,omitempty"`

	// RoutingTagsSource selects where to look for meta_skip:<platform> /
	// meta_only:<platform> routing tags. Empty (default) disables routing.
	// Allowed values: "" | "rss" | "exif".
	RoutingTagsSource string `yaml:"routingTagsSource,omitempty"`

	// AddExifToCaption appends a compact EXIF line (camera, lens, exposure)
	// to the published caption. Defaults to false.
	AddExifToCaption bool `yaml:"addExifToCaption,omitempty"`

	// CopyrightSource appends a copyright line to the caption. Empty (default)
	// disables it. Allowed values: "" | "rss" | "exif".
	CopyrightSource string `yaml:"copyrightSource,omitempty"`
}

type Datasource struct {
	Name     string `yaml:"name"`
	FeedURL  string `yaml:"feedUrl"`
	ItemType string `yaml:"itemType"`
}

type Target struct {
	Name        string `yaml:"name"`
	Platform    string `yaml:"platform"`
	PAT         string `yaml:"pat"`
	InstanceUrl string `yaml:"instance"`
	Username    string `yaml:"username"`
	AccessToken string `yaml:"accessToken"`
	AccountId   string `yaml:"accountId"`

	// MaxImageBytes / MaxImageLongEdge are optional per-instance image-prep
	// overrides. 0 = use the platform default from imageresize.DefaultsForPlatform.
	MaxImageBytes    int `yaml:"maxImageBytes,omitempty"`
	MaxImageLongEdge int `yaml:"maxImageLongEdge,omitempty"`
}
