package webpush_test

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/capability/notify"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/modules/webpush"
	wp "github.com/SherClockHolmes/webpush-go"
)

type recorder struct {
	mu   sync.Mutex
	sent []string
	gone map[string]bool
}

func (r *recorder) send(p []byte, s *wp.Subscription, o *wp.Options) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, s.Endpoint+" "+string(p))
	code := 201
	if r.gone[s.Endpoint] {
		code = 410
	}
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}, nil
}

const config = "modules:\n  webpush:\n    instances:\n      default: { subscriber: \"mailto:me@example.com\" }\n      me: {}\n"

func TestSubscribeAndBroadcast(t *testing.T) {
	h := companiontest.New(t, config)
	rec := &recorder{gone: map[string]bool{"https://push.example/gone": true}}
	for _, n := range []string{"default", "me"} {
		companiontest.Instance[*webpush.Audience](h, "webpush:"+n).SetSender(rec.send)
	}
	key := h.Public("GET", "/api/webpush/vapidkey", nil)
	if key.Code != 200 || len(key.Body.String()) < 40 || key.Header().Get("Content-Type") != "application/text" {
		t.Fatalf("vapid key %d %q", key.Code, key.Body)
	}
	if again := h.Public("GET", "/api/webpush/vapidkey", nil).Body.String(); again != key.Body.String() {
		t.Fatal("the key changed")
	}
	for _, ep := range []string{"https://push.example/a", "https://push.example/gone"} {
		w := h.Public("POST", "/api/webpush/subscribe", []byte(`{"endpoint":"`+ep+`","keys":{"p256dh":"p","auth":"a"}}`))
		if w.Code != 200 {
			t.Fatalf("subscribe %d %s", w.Code, w.Body)
		}
	}
	if w := h.Public("POST", "/api/webpush/subscribe", []byte(`{"endpoint":"x"}`)); w.Code != 400 {
		t.Fatalf("bad subscription %d", w.Code)
	}
	if w := h.Public("POST", "/api/webpush/broadcast", []byte(`{"title":"t","body":"b"}`)); w.Code != 401 {
		t.Fatalf("broadcast without key %d", w.Code)
	}
	if w := h.Admin("POST", "/api/webpush/broadcast", []byte(`{"title":"Hello","body":"World"}`)); w.Code != 200 {
		t.Fatalf("broadcast %d", w.Code)
	}
	if len(rec.sent) != 2 || !strings.Contains(rec.sent[0], `"title":"Hello"`) {
		t.Fatalf("sent %v", rec.sent)
	}
	// The gone subscription was removed.
	subs := h.Admin("GET", "/api/b/webpush/subscribers", nil).Body.String()
	if strings.Contains(subs, "gone") || strings.Contains(subs, `"auth"`) {
		t.Fatalf("subscribers %s", subs)
	}
	// The "me" audience is separate.
	h.Admin("POST", "/api/b/webpush/audiences/me/subscribe", []byte(`{"endpoint":"https://push.example/phone","keys":{"p256dh":"p","auth":"a"}}`))
	rec.sent = nil
	me := companiontest.Instance[*webpush.Audience](h, "webpush:me")
	me.Broadcast(t.Context(), notifyMsg("Token", "expires"))
	if len(rec.sent) != 1 || !strings.Contains(rec.sent[0], "phone") {
		t.Fatalf("me audience %v", rec.sent)
	}
	// The admin UI's broadcast says how many devices it reached.
	rec.gone["https://push.example/phone"] = true
	h.Admin("POST", "/api/b/webpush/audiences/me/subscribe", []byte(`{"endpoint":"https://push.example/tablet","keys":{"p256dh":"p","auth":"a"}}`))
	if w := h.Admin("POST", "/api/b/webpush/audiences/me/broadcast", []byte(`{"title":"Hi","body":"there"}`)); w.Code != 200 || !strings.Contains(w.Body.String(), `"sent":1`) || !strings.Contains(w.Body.String(), `"failed":1`) {
		t.Fatalf("admin broadcast %d %s", w.Code, w.Body)
	}
	var priv []string
	h.Core.DB().Raw("SELECT value FROM core_secrets WHERE module = 'webpush'").Scan(&priv)
	if len(priv) != 1 {
		t.Fatalf("private key not in the secret store: %v", priv)
	}
}

func TestImportMovesThePrivateKey(t *testing.T) {
	h := companiontest.New(t, config)
	privKey, pubKey, _ := wp.GenerateVAPIDKeys()
	old := companiontest.LegacyDB(t,
		`CREATE TABLE vapid_keys (id integer primary key, public_key text, private_key text, created_at datetime)`,
		`INSERT INTO vapid_keys (public_key, private_key) VALUES ('`+pubKey+`', '`+privKey+`')`,
		`CREATE TABLE notification_subscriptions (id integer primary key, created_at datetime, updated_at datetime, deleted_at datetime, endpoint text, expiration_time integer, auth text, p256dh text)`,
		`INSERT INTO notification_subscriptions (endpoint, auth, p256dh, created_at) VALUES ('https://push.example/old', 'a', 'p', '2026-01-01')`)
	info, _ := companion.Lookup("webpush")
	if err := info.Import(h.Module("webpush"), old); err != nil {
		t.Fatal(err)
	}
	if got := h.Public("GET", "/api/webpush/vapidkey", nil).Body.String(); got != pubKey {
		t.Fatalf("public key after import %q", got)
	}
	if !strings.Contains(h.Admin("GET", "/api/b/webpush/subscribers", nil).Body.String(), "push.example/old") {
		t.Fatal("subscriber not imported")
	}
}

func notifyMsg(title, body string) notify.Message { return notify.Message{Title: title, Body: body} }
