package rss

import "github.com/LNA-DEV/HomePageCompanion/companion"

var ui = &companion.UI{
	Nav:   []companion.NavEntry{{Label: "Feeds", Icon: "rss", Page: "", Group: "Content", Order: 30}},
	Pages: []companion.Page{{Path: "", Element: "rss-feeds", Title: "Feeds"}, {Path: "*", Element: "rss-feeds", Title: "Feed"}},
}
