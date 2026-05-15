package config

type Config struct {
	Security struct {
		ApiKey     string `yaml:"apiKey"`
		Domain     string `yaml:"domain"`
		IPHashSalt string `yaml:"ipHashSalt"`
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
}
