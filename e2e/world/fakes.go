package world

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Faults make a fake misbehave the way platforms do: the next writes fail
// with a status, the token expires, or the next write hangs until it is
// released (for a restart in the middle of a job).
type Faults struct {
	mu      sync.Mutex
	fails   []failure
	expired bool
	hangOp  string
	hang    chan struct{}
	hanging chan struct{} // closed once a call hangs
}

type failure struct {
	status int
	body   string
}

// FailNext makes the next n writes answer status with body.
func (f *Faults) FailNext(n, status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := 0; i < n; i++ {
		f.fails = append(f.fails, failure{status, body})
	}
}

// Expire makes the token invalid until Renew.
func (f *Faults) Expire() { f.mu.Lock(); f.expired = true; f.mu.Unlock() }

// Renew makes the token valid again.
func (f *Faults) Renew() { f.mu.Lock(); f.expired = false; f.mu.Unlock() }

// Expired reports whether the token is invalid now.
func (f *Faults) Expired() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.expired }

// Hang makes the next write of op block until release is called;
// hanging is closed once a call is held. The ops: "media" and "status"
// (Mastodon, Pixelfed), "blob" and "record" (Bluesky), "container" and
// "publish" (Instagram, Threads).
func (f *Faults) Hang(op string) (hanging <-chan struct{}, release func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hangOp, f.hang, f.hanging = op, make(chan struct{}), make(chan struct{})
	h := f.hang
	var once sync.Once
	return f.hanging, func() { once.Do(func() { close(h) }) }
}

// write applies the faults to a write of op; it reports false when it
// answered.
func (f *Faults) write(w http.ResponseWriter, op string) bool {
	f.mu.Lock()
	var hang, hanging chan struct{}
	if f.hang != nil && f.hangOp == op {
		hang, hanging = f.hang, f.hanging
		f.hang, f.hanging = nil, nil
	}
	var fail *failure
	if len(f.fails) > 0 {
		fail = &f.fails[0]
		f.fails = f.fails[1:]
	}
	f.mu.Unlock()
	if hang != nil {
		close(hanging)
		<-hang
	}
	if fail != nil {
		w.WriteHeader(fail.status)
		io.WriteString(w, fail.body)
		return false
	}
	return true
}

// Post is what a fake platform received.
type Post struct {
	ID      string
	Text    string
	Images  int
	Created time.Time
	Deleted bool
}

type recorder struct {
	mu    sync.Mutex
	posts []Post
	likes map[string]int
}

func (r *recorder) add(text string, images int) Post {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := Post{ID: strconv.Itoa(len(r.posts) + 1), Text: text, Images: images, Created: time.Now()}
	r.posts = append(r.posts, p)
	return p
}

// Posts are the posts received, deleted ones included.
func (r *recorder) Posts() []Post {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Post(nil), r.posts...)
}

// SetLikes sets a post's likes; unset, a post has (id*3)%11 of them.
func (r *recorder) SetLikes(id string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.likes == nil {
		r.likes = map[string]int{}
	}
	r.likes[id] = n
}

func (r *recorder) likesOf(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n, ok := r.likes[id]; ok {
		return n
	}
	n, _ := strconv.Atoi(strings.TrimLeft(id, "abcdefghijklmnopqrstuvwxyz"))
	return (n * 3) % 11
}

func (r *recorder) markDeleted(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.posts {
		if r.posts[i].ID == id && !r.posts[i].Deleted {
			r.posts[i].Deleted = true
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// Fedi is a Mastodon or a Pixelfed server (the same API as far as the
// companion goes). Like Mastodon, it answers a status with an
// Idempotency-Key it saw before with the status that key created.
type Fedi struct {
	Faults
	recorder
	Token string
	keyMu sync.Mutex
	keys  map[string]Post
	*httptest.Server
}

func NewFedi(token string) *Fedi {
	f := &Fedi{Token: token, keys: map[string]Post{}}
	mux := http.NewServeMux()
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if f.Expired() || r.Header.Get("Authorization") != "Bearer "+f.Token {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":"The access token is invalid"}`)
				return
			}
			h(w, r)
		}
	}
	media := 0
	var mediaMu sync.Mutex
	mux.HandleFunc("POST /api/v1/media", authed(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(8 << 20) // the whole request first, as a server does
		if !f.write(w, "media") {
			return
		}
		mediaMu.Lock()
		media++
		id := media
		mediaMu.Unlock()
		writeJSON(w, map[string]any{"id": fmt.Sprintf("m%d", id), "type": "image", "url": f.URL + "/media/" + strconv.Itoa(id)})
	}))
	mux.HandleFunc("POST /api/v1/statuses", authed(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		if !f.write(w, "status") {
			return
		}
		text := r.FormValue("status")
		if text == "" {
			text = r.FormValue("caption")
		}
		key := r.Header.Get("Idempotency-Key")
		f.keyMu.Lock()
		p, seen := f.keys[key]
		if !seen || key == "" {
			p = f.add(text, len(r.Form["media_ids[]"]))
			if key != "" {
				f.keys[key] = p
			}
		}
		f.keyMu.Unlock()
		writeJSON(w, map[string]any{"id": p.ID, "url": f.URL + "/@lna/" + p.ID, "content": "<p>" + text + "</p>", "created_at": p.Created.Format(time.RFC3339)})
	}))
	mux.HandleFunc("DELETE /api/v1/statuses/{id}", authed(func(w http.ResponseWriter, r *http.Request) {
		if !f.markDeleted(r.PathValue("id")) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, `{}`)
	}))
	mux.HandleFunc("GET /api/v1/statuses/{id}/favourited_by", authed(func(w http.ResponseWriter, r *http.Request) {
		n := f.likesOf(r.PathValue("id"))
		accs := make([]map[string]string, n)
		for i := range accs {
			accs[i] = map[string]string{"id": strconv.Itoa(i + 1), "acct": fmt.Sprintf("fan%d", i+1)}
		}
		writeJSON(w, accs)
	}))
	mux.HandleFunc("GET /api/v1/statuses/{id}/context", authed(func(w http.ResponseWriter, r *http.Request) {
		desc := []map[string]any{}
		if id := r.PathValue("id"); id == "1" {
			desc = append(desc, map[string]any{"id": "r1", "url": f.URL + "/@jane/r1", "content": "<p>Beautiful!</p>", "created_at": Base.Format(time.RFC3339),
				"account": map[string]string{"acct": "jane@other.example", "url": "https://other.example/@jane", "avatar": ""}})
		}
		writeJSON(w, map[string]any{"ancestors": []any{}, "descendants": desc})
	}))
	mux.HandleFunc("GET /api/v1/accounts/verify_credentials", authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"id": "1", "username": "lna", "acct": "lna"})
	}))
	mux.HandleFunc("GET /api/v1/accounts/{id}/statuses", authed(func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		if r.URL.Query().Get("max_id") == "" {
			for _, p := range f.Posts() {
				if !p.Deleted {
					out = append(out, map[string]any{"id": p.ID, "url": f.URL + "/@lna/" + p.ID, "content": p.Text, "created_at": p.Created.Format(time.RFC3339),
						"media_attachments": []map[string]string{}})
				}
			}
		}
		writeJSON(w, out)
	}))
	f.Server = httptest.NewServer(mux)
	return f
}

// PDS is a Bluesky server.
type PDS struct {
	Faults
	recorder
	Password string
	*httptest.Server
}

func newPDS(password string) *PDS {
	p := &PDS{Password: password}
	mux := http.NewServeMux()
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if p.Expired() || r.Header.Get("Authorization") != "Bearer jwt" {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"ExpiredToken","message":"Token has expired"}`)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("POST /xrpc/com.atproto.server.createSession", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Identifier, Password string }
		json.NewDecoder(r.Body).Decode(&body)
		if p.Expired() || body.Password != p.Password {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"AuthenticationRequired","message":"Invalid identifier or password"}`)
			return
		}
		io.WriteString(w, `{"accessJwt":"jwt","refreshJwt":"rjwt","did":"did:plc:lna","handle":"lna.example"}`)
	})
	blobs := 0
	var blobMu sync.Mutex
	mux.HandleFunc("POST /xrpc/com.atproto.repo.uploadBlob", authed(func(w http.ResponseWriter, r *http.Request) {
		if !p.write(w, "blob") {
			return
		}
		blobMu.Lock()
		blobs++
		n := blobs
		blobMu.Unlock()
		fmt.Fprintf(w, `{"blob":{"$type":"blob","ref":{"$link":"bafy%d"},"mimeType":"image/jpeg","size":4}}`, n)
	}))
	mux.HandleFunc("POST /xrpc/com.atproto.repo.createRecord", authed(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Record struct {
				Text  string `json:"text"`
				Embed struct {
					Images []any `json:"images"`
				} `json:"embed"`
			} `json:"record"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if !p.write(w, "record") {
			return
		}
		post := p.add(body.Record.Text, len(body.Record.Embed.Images))
		fmt.Fprintf(w, `{"uri":"at://did:plc:lna/app.bsky.feed.post/rk%s","cid":"cid%s"}`, post.ID, post.ID)
	}))
	mux.HandleFunc("POST /xrpc/com.atproto.repo.deleteRecord", authed(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Rkey string }
		json.NewDecoder(r.Body).Decode(&body)
		p.markDeleted(strings.TrimPrefix(body.Rkey, "rk"))
		io.WriteString(w, `{}`)
	}))
	mux.HandleFunc("GET /xrpc/app.bsky.feed.getLikes", authed(func(w http.ResponseWriter, r *http.Request) {
		uri := r.URL.Query().Get("uri")
		n := p.likesOf(uri[strings.LastIndex(uri, "/rk")+3:])
		likes := make([]map[string]any, n)
		for i := range likes {
			likes[i] = map[string]any{"actor": map[string]string{"did": fmt.Sprintf("did:plc:fan%d", i)}}
		}
		writeJSON(w, map[string]any{"uri": uri, "likes": likes})
	}))
	mux.HandleFunc("GET /xrpc/app.bsky.feed.getPostThread", authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"thread": map[string]any{"post": map[string]string{"uri": r.URL.Query().Get("uri")}, "replies": []any{}}})
	}))
	mux.HandleFunc("GET /xrpc/app.bsky.feed.getAuthorFeed", authed(func(w http.ResponseWriter, r *http.Request) {
		feed := []map[string]any{}
		for _, post := range p.Posts() {
			if !post.Deleted {
				feed = append(feed, map[string]any{"post": map[string]any{"uri": "at://did:plc:lna/app.bsky.feed.post/rk" + post.ID, "cid": "cid" + post.ID,
					"record": map[string]any{"text": post.Text, "createdAt": post.Created.Format(time.RFC3339)}}})
			}
		}
		writeJSON(w, map[string]any{"feed": feed})
	}))
	mux.HandleFunc("GET /xrpc/app.bsky.actor.getProfile", func(w http.ResponseWriter, r *http.Request) {
		if a := r.URL.Query().Get("actor"); a == "lna.example" || a == "did:plc:lna" {
			io.WriteString(w, `{"did":"did:plc:lna","handle":"lna.example"}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"InvalidRequest","message":"Profile not found"}`)
	})
	p.Server = httptest.NewServer(mux)
	return p
}

// Graph is Instagram's or Threads' Graph API: containers that are ready at
// once, publishing, likes, the account, and the token refresh.
type Graph struct {
	Faults
	recorder
	Token     string
	AccountID string
	threads   bool
	*httptest.Server
}

func newGraph(token, accountID string, threads bool) *Graph {
	g := &Graph{Token: token, AccountID: accountID, threads: threads}
	var mu sync.Mutex
	containers := map[string]struct {
		text   string
		images int
	}{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		token := r.Form.Get("access_token")
		if r.URL.Path == "/refresh" {
			if g.Expired() || token != g.Token {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":{"message":"Error validating access token: Session has expired","type":"OAuthException","code":190}}`)
				return
			}
			writeJSON(w, map[string]any{"access_token": g.Token, "token_type": "bearer", "expires_in": 5184000})
			return
		}
		if g.Expired() || token != g.Token {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Error validating access token: Session has expired","type":"OAuthException","code":190}}`)
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case parts[0] == "me":
			writeJSON(w, map[string]string{"id": g.AccountID, "user_id": g.AccountID, "username": "lna"})
		case len(parts) == 2 && parts[0] == g.AccountID && (parts[1] == "media" || parts[1] == "threads") && r.Method == http.MethodPost:
			if !g.write(w, "container") {
				return
			}
			text := r.Form.Get("caption")
			if g.threads {
				text = r.Form.Get("text")
			}
			images := 0
			if r.Form.Get("image_url") != "" {
				images = 1
			}
			mu.Lock()
			id := fmt.Sprintf("c%d", len(containers)+1)
			containers[id] = struct {
				text   string
				images int
			}{text, images}
			mu.Unlock()
			writeJSON(w, map[string]string{"id": id})
		case len(parts) == 2 && parts[0] == g.AccountID && (parts[1] == "media_publish" || parts[1] == "threads_publish"):
			if !g.write(w, "publish") {
				return
			}
			mu.Lock()
			c := containers[r.Form.Get("creation_id")]
			mu.Unlock()
			p := g.add(c.text, c.images)
			writeJSON(w, map[string]string{"id": "p" + p.ID})
		case len(parts) == 2 && parts[0] == g.AccountID && parts[1] == "media":
			data := []map[string]string{}
			for _, p := range g.Posts() {
				data = append(data, map[string]string{"id": "p" + p.ID, "caption": p.Text, "permalink": g.URL + "/p/" + p.ID, "timestamp": p.Created.Format("2006-01-02T15:04:05-0700")})
			}
			writeJSON(w, map[string]any{"data": data})
		case len(parts) == 1 && strings.HasPrefix(parts[0], "c"):
			if g.threads {
				writeJSON(w, map[string]string{"id": parts[0], "status": "FINISHED"})
			} else {
				writeJSON(w, map[string]string{"id": parts[0], "status_code": "FINISHED"})
			}
		case len(parts) == 1 && strings.HasPrefix(parts[0], "p") && r.Method == http.MethodDelete:
			if !g.markDeleted(strings.TrimPrefix(parts[0], "p")) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(w, map[string]bool{"success": true})
		case len(parts) == 1 && strings.HasPrefix(parts[0], "p"):
			id := strings.TrimPrefix(parts[0], "p")
			writeJSON(w, map[string]any{"id": parts[0], "permalink": g.URL + "/@lna/post/" + id, "like_count": g.likesOf(id)})
		case len(parts) == 2 && strings.HasPrefix(parts[0], "p") && parts[1] == "insights":
			writeJSON(w, map[string]any{"data": []any{map[string]any{"name": "likes", "values": []any{map[string]int{"value": g.likesOf(strings.TrimPrefix(parts[0], "p"))}}}}})
		case len(parts) == 2 && strings.HasPrefix(parts[0], "p") && parts[1] == "replies":
			writeJSON(w, map[string]any{"data": []any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"message":"Unknown path `+r.URL.Path+`","code":100}}`)
		}
	}))
	return g
}

// Push is a web push service: it takes every notification, or answers 410
// for a subscription that is gone.
type Push struct {
	mu       sync.Mutex
	received map[string]int
	gone     map[string]bool
	*httptest.Server
}

func newPush() *Push {
	p := &Push{received: map[string]int{}, gone: map[string]bool{}}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.gone[r.URL.Path] {
			w.WriteHeader(http.StatusGone)
			return
		}
		p.received[r.URL.Path]++
		w.WriteHeader(http.StatusCreated)
	}))
	return p
}

// Endpoint is a subscription's endpoint at the push service.
func (p *Push) Endpoint(name string) string { return p.URL + "/push/" + name }

// Received is how many notifications a subscription got.
func (p *Push) Received(name string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.received["/push/"+name]
}

// Unsubscribe makes the service answer 410 for a subscription.
func (p *Push) Unsubscribe(name string) { p.mu.Lock(); p.gone["/push/"+name] = true; p.mu.Unlock() }
