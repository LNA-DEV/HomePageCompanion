package autouploader

import (
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/imageresize"
)

// resolveLimits returns the image-prep limits to apply for a target,
// merging optional per-target overrides on top of the platform default.
// Either field set to 0 in config falls back to the platform default.
func resolveLimits(target config.Target) imageresize.Limits {
	lim := imageresize.DefaultsForPlatform(target.Platform)
	if target.MaxImageBytes > 0 {
		lim.MaxBytes = target.MaxImageBytes
	}
	if target.MaxImageLongEdge > 0 {
		lim.MaxLongEdge = target.MaxImageLongEdge
	}
	return lim
}
