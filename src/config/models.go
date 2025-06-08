package config

type Config struct {
	Security struct {
		ApiKey string `yaml:"apiKey"`
	} `yaml:"security"`
	Datasources struct {
		Rss []Datasource `yaml:"rss"`
	} `yaml:"datasources"`
	Targets     []Target     `yaml:"targets"`
	Connections []Connection `yaml:"connections"`
	Webpush     struct {
		Subscriber string `yaml:"subscriberMail"`
	} `yaml:"webpush"`
}

type Connection struct {
	Name       string  `yaml:"name"`
	SourceName string  `yaml:"sourceName"`
	TargetName string  `yaml:"targetName"`
	Caption    string  `yaml:"caption"`
	Cron       *string `yaml:"cron"`
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
