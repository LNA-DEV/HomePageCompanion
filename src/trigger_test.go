package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/gin-gonic/gin"
)

// "Trigger Upload" runs the publish in the background. A panic there must
// not stop the companion (autoposting-ui.md §5.1 B2).
func TestManualUploadSurvivesAPanic(t *testing.T) {
	prevPublish, prevConfig := publish, config.Data
	t.Cleanup(func() { publish, config.Data = prevPublish, prevConfig })
	config.Data.Connections = []config.Connection{{Name: "photos-mastodon"}}
	done := make(chan struct{})
	publish = func(config.Connection) {
		defer close(done)
		var p *struct{ URL string }
		_ = p.URL // the nil dereference of an item without an image
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/upload/:connectionName", uploadNext)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/upload/photos-mastodon", nil))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d", w.Code)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish never ran")
	}
	time.Sleep(50 * time.Millisecond)
}
