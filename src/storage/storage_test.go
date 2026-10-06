package storage

import (
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/config"
)

func TestPrefix(t *testing.T) {
	defer func(old config.Storage) { config.Data.Storage = old }(config.Data.Storage)

	config.Data.Storage.Prefix = ""
	if got := Prefix("maps/basemap"); got != "home-page-companion/maps/basemap" {
		t.Errorf("default: %q", got)
	}
	config.Data.Storage.Prefix = "/spike/home-page-companion/"
	if got := Prefix("/maps/basemap/"); got != "spike/home-page-companion/maps/basemap" {
		t.Errorf("configured: %q", got)
	}
}
