package config

import (
	"os"

	"gopkg.in/yaml.v2"
)

var Data config

func LoadConfig() {
	file, err := os.ReadFile("data/config.yaml")
	if err != nil {
		panic(err)
	}

	var config config
	err = yaml.Unmarshal(file, &config)
	if err != nil {
		panic(err)
	}

	Data = config
}

type config struct {
	Security struct {
		ApiKey string `yaml:"apiKey"`
	} `yaml:"security"`
	Datasources struct {
		Rss []struct {
			Name    string `yaml:"name"`
			FeedURL string `yaml:"feedUrl"`
			ItemType string `yaml:"itemType"`
		} `yaml:"rss"`
	} `yaml:"datasources"`
	Autouploader struct {
		Pixelfed struct {
			PAT         string  `yaml:"pat"`
			InstanceUrl string  `yaml:"instance"`
			Caption     string  `yaml:"caption"`
			Cron        *string `yaml:"cron"`
		} `yaml:"pixelfed"`
		Bluesky struct {
			PAT      string  `yaml:"pat"`
			Username string  `yaml:"username"`
			Caption  string  `yaml:"caption"`
			Cron     *string `yaml:"cron"`
		} `yaml:"bluesky"`
		Instagram struct {
			AccessToken string  `yaml:"accessToken"`
			AccountId   string  `yaml:"accountId"`
			Caption     string  `yaml:"caption"`
			Cron        *string `yaml:"cron"`
		} `yaml:"instagram"`
	} `yaml:"autouploader"`
	Webpush struct {
		Subscriber string `yaml:"subscriberMail"`
	} `yaml:"webpush"`
}
