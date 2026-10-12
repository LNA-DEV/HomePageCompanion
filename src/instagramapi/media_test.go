package instagramapi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateMediaContainerUserTags(t *testing.T) {
	var gotTags []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodPost || r.URL.Path != "/17841/media" || q.Get("caption") != "With Jane Doe (@janedoe)" {
			t.Errorf("unexpected request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		_, has := q["user_tags"]
		gotTags = append(gotTags, map[bool]string{true: q.Get("user_tags"), false: "<none>"}[has])
		io.WriteString(w, `{"id":"container-1"}`)
	}))
	defer srv.Close()
	prev := GraphURL
	GraphURL = srv.URL + "/"
	t.Cleanup(func() { GraphURL = prev })

	tags := []UserTag{{Username: "janedoe", X: 0.5, Y: 0.5}}
	if id, err := CreateMediaContainer("17841", "tok", "https://x/img.jpg", "With Jane Doe (@janedoe)", tags); err != nil || id != "container-1" {
		t.Fatalf("CreateMediaContainer = %q, %v", id, err)
	}
	if _, err := CreateMediaContainer("17841", "tok", "https://x/img.jpg", "With Jane Doe (@janedoe)", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{`[{"username":"janedoe","x":0.5,"y":0.5}]`, "<none>"}
	if len(gotTags) != 2 || gotTags[0] != want[0] || gotTags[1] != want[1] {
		t.Errorf("user_tags = %q, want %q", gotTags, want)
	}
}

func TestCreateMediaContainerStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"Invalid user_tags"}}`)
	}))
	defer srv.Close()
	prev := GraphURL
	GraphURL = srv.URL + "/"
	t.Cleanup(func() { GraphURL = prev })

	_, err := CreateMediaContainer("17841", "tok", "https://x/img.jpg", "", nil)
	var status *StatusError
	if !errors.As(err, &status) || status.StatusCode != http.StatusBadRequest {
		t.Fatalf("err = %v, want a StatusError 400", err)
	}
	if want := `instagram create container status 400 body: {"error":{"message":"Invalid user_tags"}}`; err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}
