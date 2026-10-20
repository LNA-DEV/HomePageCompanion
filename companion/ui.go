package companion

import "io/fs"

// UI is a building block's admin UI. Files holds at least entry.js, which
// registers the custom elements the manifest names; the shell loads it from
// /ui/b/<id>/entry.js. A hpc build fills Files from the module's ui/ folder
// when the module does not embed it itself.
type UI struct {
	Files fs.FS `json:"-"`

	Nav      []NavEntry `json:"nav,omitempty"`
	Pages    []Page     `json:"pages,omitempty"`
	Widgets  []Widget   `json:"widgets,omitempty"`
	Settings string     `json:"settings,omitempty"` // element for an instance's settings; empty: generated form
	Actions  []Action   `json:"actions,omitempty"`
	Public   []string   `json:"public,omitempty"` // files served to the website under /ui/b/<id>/public/
}

// NavEntry is a sidebar entry pointing to one of the pages.
type NavEntry struct {
	Label string `json:"label"`
	Icon  string `json:"icon,omitempty"` // a lucide icon name
	Page  string `json:"page"`           // Page.Path
	Group string `json:"group,omitempty"`
	Order int    `json:"order,omitempty"`
}

// Page is a page under /b/<id>/<Path>; Element is its custom element. A path
// may end in "/*" for sub-paths, which the element reads from its "path"
// attribute.
type Page struct {
	Path    string `json:"path"`
	Element string `json:"element"`
	Title   string `json:"title,omitempty"`
}

// Widget is a dashboard card.
type Widget struct {
	Element string `json:"element"`
	Size    string `json:"size"` // S, M or L
	Title   string `json:"title,omitempty"`
}

// Action is a button on an instance; it POSTs to /api/b/<id>/<Endpoint>
// with {"instance": "<name>"}.
type Action struct {
	Label    string `json:"label"`
	Endpoint string `json:"endpoint"`
	Confirm  string `json:"confirm,omitempty"`
}

// uiFiles lets a hpc build attach UI files to a module that does not embed
// them itself.
var uiFiles = map[string]fs.FS{}

// AttachUI is called by a generated main for every module whose UI hpc built.
func AttachUI(id string, files fs.FS) { uiFiles[id] = files }

// UIFiles returns the UI files of a building block: its own, else the
// attached ones.
func UIFiles(info Info) fs.FS {
	if info.UI != nil && info.UI.Files != nil {
		return info.UI.Files
	}
	return uiFiles[info.ID]
}
