package world

import (
	"embed"
	"io/fs"
	"net/http"
	"sync"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/gin-gonic/gin"
)

// notes is a service with an admin UI of its own, plain HTML with
// /ui/sdk/companion-ui.js as a service in another language would write it.
// The UI tests use it to check the sandboxed iframe and its bridge
// (docs/concepts/modular-architecture.md §5.7).

//go:embed notes-ui
var notesFiles embed.FS

type note struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

type notesStore struct {
	mu    sync.Mutex
	notes []note
}

// Notes is an instance; the notes are the service's, not the instance's.
type Notes struct{}

func notesInfo() companion.Info {
	ui, _ := fs.Sub(notesFiles, "notes-ui")
	store := &notesStore{}
	return companion.Info{
		ID:   "notes",
		Name: "Notes",
		New:  func() companion.Instance { return new(Notes) },
		Routes: func(mc companion.ModuleContext, r *companion.Router) {
			r.Admin.GET("/notes", func(g *gin.Context) {
				store.mu.Lock()
				defer store.mu.Unlock()
				g.JSON(http.StatusOK, append([]note{}, store.notes...))
			})
			r.Admin.POST("/notes", func(g *gin.Context) {
				var n note
				if err := g.ShouldBindJSON(&n); err != nil || n.Text == "" {
					g.JSON(http.StatusBadRequest, gin.H{"error": "a note needs a text"})
					return
				}
				store.mu.Lock()
				n.ID = len(store.notes) + 1
				store.notes = append(store.notes, n)
				store.mu.Unlock()
				g.JSON(http.StatusCreated, n)
			})
		},
		UI: &companion.UI{
			Files:   ui,
			Nav:     []companion.NavEntry{{Label: "Notes", Icon: "sticky-note", Page: "", Group: "Services"}},
			Pages:   []companion.Page{{Path: "", Title: "Notes"}, {Path: "*", Title: "Note"}},
			Widgets: []companion.Widget{{Element: "notes-count", Size: "S", Title: "Notes"}},
		},
	}
}
