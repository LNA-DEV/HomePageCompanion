package autouploader

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/instagramapi"
	"github.com/mmcdole/gofeed"
)

func TestInstagramUserTags(t *testing.T) {
	jane := Credit{Slug: "jane", Name: "Jane", Mention: "@janedoe", Account: "janedoe"}
	maxm := Credit{Slug: "max", Name: "Max", Mention: "@maxmuster", Account: "maxmuster"}
	ana := Credit{Slug: "ana", Name: "Ana", Mention: "@ana", Account: "ana"}
	plain := Credit{Slug: "plain", Name: "Plain"}

	cases := []struct {
		credits []Credit
		want    []instagramapi.UserTag
	}{
		{[]Credit{plain}, nil},
		{[]Credit{plain, jane}, []instagramapi.UserTag{{Username: "janedoe", X: 0.5, Y: 0.5}}},
		{[]Credit{jane, plain, maxm}, []instagramapi.UserTag{{Username: "janedoe", X: 0.33, Y: 0.5}, {Username: "maxmuster", X: 0.67, Y: 0.5}}},
		{[]Credit{jane, maxm, ana}, []instagramapi.UserTag{{Username: "janedoe", X: 0.25, Y: 0.5}, {Username: "maxmuster", X: 0.5, Y: 0.5}, {Username: "ana", X: 0.75, Y: 0.5}}},
	}
	for _, c := range cases {
		got, _ := instagramUserTags(c.credits)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("tags = %+v, want %+v", got, c.want)
		}
	}
	if _, slugs := instagramUserTags([]Credit{jane, plain, maxm}); !reflect.DeepEqual(slugs, []string{"jane", "max"}) {
		t.Errorf("slugs = %v", slugs)
	}
}

func TestCreateInstagramContainer_Retry(t *testing.T) {
	credits := []Credit{{Slug: "jane", Name: "Jane", Mention: "@janedoe", Account: "janedoe"}}
	entry := &gofeed.Item{GUID: "guid", Image: &gofeed.Image{URL: "https://x/img.jpg"}}
	target := config.Target{Platform: "instagram", AccountId: "17841", AccessToken: "tok"}

	cases := []struct {
		name       string
		credits    []Credit
		withTags   int // status for a request with user_tags
		wantCalls  []bool
		wantErrIs  error
		wantErrAny bool
	}{
		{"accepted", credits, http.StatusOK, []bool{true}, nil, false},
		{"4xx: retry without tags", credits, http.StatusBadRequest, []bool{true, false}, nil, false},
		{"429: no retry", credits, http.StatusTooManyRequests, []bool{true}, instagramapi.ErrRateLimited, true},
		{"5xx: no retry", credits, http.StatusInternalServerError, []bool{true}, nil, true},
		{"no mentions: no user_tags", nil, http.StatusBadRequest, []bool{false}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, tagged := r.URL.Query()["user_tags"]
				calls = append(calls, tagged)
				if tagged && c.withTags != http.StatusOK {
					w.WriteHeader(c.withTags)
					return
				}
				io.WriteString(w, `{"id":"container-1"}`)
			}))
			defer srv.Close()
			prev := instagramapi.GraphURL
			instagramapi.GraphURL = srv.URL + "/"
			defer func() { instagramapi.GraphURL = prev }()

			id, err := createInstagramContainer(entry, target, "caption", c.credits)
			if !reflect.DeepEqual(calls, c.wantCalls) {
				t.Errorf("calls with user_tags = %v, want %v", calls, c.wantCalls)
			}
			if c.wantErrAny {
				if err == nil || (c.wantErrIs != nil && !errors.Is(err, c.wantErrIs)) {
					t.Errorf("err = %v, want %v", err, c.wantErrIs)
				}
			} else if err != nil || id != "container-1" {
				t.Errorf("id = %q, err = %v", id, err)
			}
		})
	}
}
