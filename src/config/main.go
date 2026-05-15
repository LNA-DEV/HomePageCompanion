package config

import (
	"os"

	"gopkg.in/yaml.v2"
)

var Data Config

func LoadConfig() {
	// Best-effort load .env files. Either side is optional; values already
	// present in the OS environment win (so docker-compose's env_file: or a
	// shell `KEY=foo go run .` always overrides the file).
	loadDotenv("data/.env")
	loadDotenv(".env")

	raw, err := os.ReadFile("data/config.yaml")
	if err != nil {
		panic(err)
	}

	expanded := expandEnv(raw)

	var config Config
	err = yaml.Unmarshal(expanded, &config)
	if err != nil {
		panic(err)
	}

	Data = config
}
