package config

import (
	"os"

	"gopkg.in/yaml.v2"
)

var Data config

func LoadConfig() {
	file, err := os.ReadFile("config.yaml")
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
	Autouploader struct {
		FeedUrl  string `yaml:"feedUrl"`
		Pixelfed struct {
			PAT         string `yaml:"pat"`
			InstanceUrl string `yaml:"instance"`
		} `yaml:"pixelfed"`
	} `yaml:"autouploader"`
}
