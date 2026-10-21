package routing

import (
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/geo"
	"github.com/LNA-DEV/HomePageCompanion/companion"
)

type geoPoint = geo.Point

func companionDuration(d time.Duration) companion.Duration { return companion.Duration(d) }
