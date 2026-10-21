// Package fakes are fake platforms small enough to run in a test
// container (the probe's PROBE_MODE=lemmy and =feed) as well as in tests:
// a Lemmy server and a photo feed.
package fakes

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LemmyPost is a post the fake Lemmy received.
type LemmyPost struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Body        string    `json:"body,omitempty"`
	URL         string    `json:"url,omitempty"`
	AltText     string    `json:"alt_text,omitempty"`
	NSFW        bool      `json:"nsfw"`
	CommunityID int       `json:"community_id"`
	Published   time.Time `json:"published"`
	Deleted     bool      `json:"deleted"`
	Upvotes     int       `json:"upvotes"`
}

// Lemmy is a Lemmy 0.19 server (/api/v3) as far as a poster needs it: one
// account, one community, pict-rs images, posts, scores and comments.
type Lemmy struct {
	User, Password, Community string
	// Base is how the server names itself in URLs.
	Base string

	mu     sync.Mutex
	jwts   map[string]bool
	posts  []*LemmyPost
	images map[string][]byte
	logins int
	fail   []failure
}

type failure struct {
	status int
	code   string
}

// NewLemmy is a fake with the account bot/bot-password and the community
// photography (id 7).
func NewLemmy(base string) *Lemmy {
	return &Lemmy{User: "bot", Password: "bot-password", Community: "photography", Base: strings.TrimSuffix(base, "/"),
		jwts: map[string]bool{}, images: map[string][]byte{}}
}

// Serve runs the fake on a local port.
func (l *Lemmy) Serve() *httptest.Server {
	srv := httptest.NewServer(l)
	l.Base = srv.URL
	return srv
}

// Posts are the posts received, deleted ones included.
func (l *Lemmy) Posts() []LemmyPost {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]LemmyPost, len(l.posts))
	for i, p := range l.posts {
		out[i] = *p
	}
	return out
}

// Image is an uploaded image.
func (l *Lemmy) Image(file string) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.images[file]
}

// Logins counts the logins.
func (l *Lemmy) Logins() int { l.mu.Lock(); defer l.mu.Unlock(); return l.logins }

// FailNext answers the next n API calls with status and {"error": code}.
func (l *Lemmy) FailNext(n, status int, code string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := 0; i < n; i++ {
		l.fail = append(l.fail, failure{status, code})
	}
}

// ExpireTokens makes every issued token invalid, as a password change does.
func (l *Lemmy) ExpireTokens() { l.mu.Lock(); l.jwts = map[string]bool{}; l.mu.Unlock() }

// SetUpvotes sets a post's upvotes.
func (l *Lemmy) SetUpvotes(id, n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.posts {
		if p.ID == id {
			p.Upvotes = n
		}
	}
}

func lemmyError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func lemmyJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (l *Lemmy) authed(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && l.jwts[tok]
}

func (l *Lemmy) view(p *LemmyPost) map[string]any {
	return map[string]any{
		"post": map[string]any{"id": p.ID, "name": p.Name, "body": p.Body, "url": p.URL, "alt_text": p.AltText, "nsfw": p.NSFW,
			"community_id": p.CommunityID, "published": p.Published.Format(time.RFC3339), "deleted": p.Deleted,
			"ap_id": fmt.Sprintf("%s/post/%d", l.Base, p.ID)},
		"creator":   map[string]any{"name": l.User, "actor_id": l.Base + "/u/" + l.User},
		"community": map[string]any{"id": 7, "name": l.Community},
		"counts":    map[string]any{"score": p.Upvotes, "upvotes": p.Upvotes, "downvotes": 0, "comments": 1},
	}
}

func (l *Lemmy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	path := r.URL.Path
	switch {
	case path == "/_posts":
		lemmyJSON(w, l.posts)
		return
	case strings.HasPrefix(path, "/pictrs/image/") && r.Method == http.MethodGet:
		img, ok := l.images[strings.TrimPrefix(path, "/pictrs/image/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(img)
		return
	}
	if len(l.fail) > 0 && path != "/api/v3/user/login" {
		f := l.fail[0]
		l.fail = l.fail[1:]
		lemmyError(w, f.status, f.code)
		return
	}
	switch {
	case path == "/api/v3/user/login" && r.Method == http.MethodPost:
		var body struct {
			User     string `json:"username_or_email"`
			Password string `json:"password"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.User != l.User || body.Password != l.Password {
			lemmyError(w, http.StatusUnauthorized, "incorrect_login")
			return
		}
		b := make([]byte, 12)
		rand.Read(b)
		tok := hex.EncodeToString(b)
		l.jwts[tok] = true
		l.logins++
		lemmyJSON(w, map[string]any{"jwt": tok, "registration_created": false, "verify_email_sent": false})
	case path == "/api/v3/community" && r.Method == http.MethodGet:
		name := r.URL.Query().Get("name")
		if n, _, _ := strings.Cut(name, "@"); n != l.Community {
			lemmyError(w, http.StatusNotFound, "couldnt_find_community")
			return
		}
		lemmyJSON(w, map[string]any{"community_view": map[string]any{"community": map[string]any{"id": 7, "name": l.Community, "actor_id": l.Base + "/c/" + l.Community}}})
	case path == "/pictrs/image" && r.Method == http.MethodPost:
		if !l.authed(r) {
			lemmyError(w, http.StatusBadRequest, "not_logged_in")
			return
		}
		if err := r.ParseMultipartForm(16 << 20); err != nil {
			lemmyError(w, http.StatusBadRequest, "no_file_uploaded")
			return
		}
		f, _, err := r.FormFile("images[]")
		if err != nil {
			lemmyError(w, http.StatusBadRequest, "no_file_uploaded")
			return
		}
		data, _ := io.ReadAll(f)
		file := fmt.Sprintf("img%d.jpg", len(l.images)+1)
		l.images[file] = data
		lemmyJSON(w, map[string]any{"msg": "ok", "files": []map[string]string{{"file": file, "delete_token": "d"}}})
	case path == "/api/v3/post" && r.Method == http.MethodPost:
		if !l.authed(r) {
			lemmyError(w, http.StatusBadRequest, "not_logged_in")
			return
		}
		var p LemmyPost
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			lemmyError(w, http.StatusBadRequest, "invalid_body_field")
			return
		}
		name := strings.TrimSpace(p.Name)
		if len([]rune(name)) < 3 || len([]rune(name)) > 200 || strings.ContainsAny(name, "\r\n") {
			lemmyError(w, http.StatusBadRequest, "invalid_post_title")
			return
		}
		if p.CommunityID != 7 {
			lemmyError(w, http.StatusBadRequest, "couldnt_find_community")
			return
		}
		p.ID, p.Published, p.Name = len(l.posts)+1, time.Now().UTC(), name
		l.posts = append(l.posts, &p)
		lemmyJSON(w, map[string]any{"post_view": l.view(&p)})
	case path == "/api/v3/post" && r.Method == http.MethodGet:
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		for _, p := range l.posts {
			if p.ID == id && !p.Deleted {
				lemmyJSON(w, map[string]any{"post_view": l.view(p)})
				return
			}
		}
		lemmyError(w, http.StatusNotFound, "couldnt_find_post")
	case path == "/api/v3/post/delete" && r.Method == http.MethodPost:
		if !l.authed(r) {
			lemmyError(w, http.StatusBadRequest, "not_logged_in")
			return
		}
		var body struct {
			PostID  int  `json:"post_id"`
			Deleted bool `json:"deleted"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		for _, p := range l.posts {
			if p.ID == body.PostID {
				p.Deleted = body.Deleted
				lemmyJSON(w, map[string]any{"post_view": l.view(p)})
				return
			}
		}
		lemmyError(w, http.StatusNotFound, "couldnt_find_post")
	case path == "/api/v3/user" && r.Method == http.MethodGet:
		var views []map[string]any
		ps := append([]*LemmyPost(nil), l.posts...)
		sort.Slice(ps, func(i, j int) bool { return ps[i].ID > ps[j].ID })
		for _, p := range ps {
			if !p.Deleted {
				views = append(views, l.view(p))
			}
		}
		lemmyJSON(w, map[string]any{"person_view": map[string]any{"person": map[string]any{"name": l.User}}, "posts": views})
	case path == "/api/v3/comment/list" && r.Method == http.MethodGet:
		id := r.URL.Query().Get("post_id")
		lemmyJSON(w, map[string]any{"comments": []map[string]any{{
			"comment": map[string]any{"id": 100, "content": "Lovely light on post " + id, "ap_id": l.Base + "/comment/100", "published": "2026-10-20T10:00:00Z"},
			"creator": map[string]any{"name": "jane", "actor_id": "https://lemmy.example/u/jane", "avatar": "https://lemmy.example/pictrs/image/jane.png"},
		}}})
	default:
		lemmyError(w, http.StatusNotFound, "not_found")
	}
}
