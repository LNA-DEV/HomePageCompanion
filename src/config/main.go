package config

import (
	"os"

	"gopkg.in/yaml.v2"
)

var Data Config

func LoadConfig() {
	file, err := os.ReadFile("data/config.yaml")
	if err != nil {
		panic(err)
	}

	var config Config
	err = yaml.Unmarshal(file, &config)
	if err != nil {
		panic(err)
	}

	Data = config
}
