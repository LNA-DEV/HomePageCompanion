package autoupload

import "github.com/LNA-DEV/HomePageCompanion/companion"

var ui = &companion.UI{
	Nav: []companion.NavEntry{
		{Label: "Posts", Icon: "layout-grid", Page: "", Group: "Autoposting", Order: 10},
		{Label: "Schedule", Icon: "calendar-clock", Page: "schedule", Group: "Autoposting", Order: 11},
		{Label: "Problems", Icon: "triangle-alert", Page: "problems", Group: "Autoposting", Order: 12},
	},
	Pages: []companion.Page{
		{Path: "", Element: "autoupload-posts", Title: "Posts"},
		{Path: "schedule", Element: "autoupload-schedule", Title: "Schedule"},
		{Path: "problems", Element: "autoupload-problems", Title: "Problems"},
	},
	Widgets: []companion.Widget{
		{Element: "autoupload-targets", Size: "M", Title: "Targets"},
		{Element: "autoupload-top", Size: "M", Title: "Top posts"},
		{Element: "autoupload-open-problems", Size: "S", Title: "Open problems"},
	},
}
