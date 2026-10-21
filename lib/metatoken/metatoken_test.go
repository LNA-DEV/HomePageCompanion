package metatoken_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/lib/metatoken"
)

// tokenUser is a building block holding a Meta token, like the instagram
// and threads modules.
type tokenUser struct {
	Token string `json:"token"`

	keeper *metatoken.Keeper
	asked  []string // tokens the refresher was given
	answer func(token string) (string, time.Duration, error)
}

func (u *tokenUser) Provision(ctx companion.Context) error {
	k, err := metatoken.New(ctx, u.Token, func(_ context.Context, token string) (string, time.Duration, error) {
		u.asked = append(u.asked, token)
		if u.answer != nil {
			return u.answer(token)
		}
		return token + "+", 60 * 24 * time.Hour, nil
	})
	u.keeper = k
	return err
}

func init() {
	companion.Register(companion.Info{ID: "metatoken-test", New: func() companion.Instance { return new(tokenUser) }})
}

func setup(t *testing.T, token string) (*companiontest.Harness, *tokenUser) {
	h := companiontest.New(t, "modules:\n  metatoken-test: { instances: { main: { token: "+token+" } } }\n")
	return h, companiontest.Instance[*tokenUser](h, "metatoken-test:main")
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestRefreshReplacesTheConfiguredToken(t *testing.T) {
	h, u := setup(t, "seed")
	c := &clock{time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	u.keeper.SetClock(c.now)

	if u.keeper.Token() != "seed" {
		t.Fatal("before the first refresh the configured token is used")
	}
	if s := u.keeper.Status(); s.Status != metatoken.StatusUnknown || s.Source != "config" {
		t.Fatalf("status %+v", s)
	}
	if err := u.keeper.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if u.keeper.Token() != "seed+" || len(u.asked) != 1 {
		t.Fatalf("token %q, asked %v", u.keeper.Token(), u.asked)
	}
	s := u.keeper.Status()
	if s.Status != metatoken.StatusOK || s.Source != "refreshed" || s.DaysLeft == nil || *s.DaysLeft != 60 {
		t.Fatalf("status %+v", s)
	}

	// Not due again within a week, due after it.
	c.t = c.t.Add(6 * 24 * time.Hour)
	u.keeper.RunDue(context.Background())
	if len(u.asked) != 1 {
		t.Fatal("refreshed again within a week")
	}
	c.t = c.t.Add(2 * 24 * time.Hour)
	u.keeper.RunDue(context.Background())
	if len(u.asked) != 2 || u.asked[1] != "seed+" || u.keeper.Token() != "seed++" {
		t.Fatalf("asked %v, token %q", u.asked, u.keeper.Token())
	}

	// The refreshed token survives a restart: the store is the source.
	if err := h.Rewrite("modules:\n  metatoken-test: { instances: { main: { token: seed } } }\n# reload\n"); err != nil {
		t.Fatal(err)
	}
	u = companiontest.Instance[*tokenUser](h, "metatoken-test:main")
	if u.keeper.Token() != "seed++" {
		t.Fatalf("after reload %q", u.keeper.Token())
	}

	// A new token in the config replaces the stored one.
	if err := h.Rewrite("modules:\n  metatoken-test: { instances: { main: { token: fresh } } }\n"); err != nil {
		t.Fatal(err)
	}
	u = companiontest.Instance[*tokenUser](h, "metatoken-test:main")
	if u.keeper.Token() != "fresh" {
		t.Fatalf("new config token not used: %q", u.keeper.Token())
	}
	if s := u.keeper.Status(); s.Source != "config" {
		t.Fatalf("status after a new token %+v", s)
	}
}

func TestFailedRefreshKeepsTheTokenAndReportsIt(t *testing.T) {
	_, u := setup(t, "seed")
	c := &clock{time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	u.keeper.SetClock(c.now)
	u.keeper.RefreshNow(context.Background())

	// Close to the expiry it counts as expiring.
	c.t = c.t.Add(50 * 24 * time.Hour)
	if s := u.keeper.Status(); s.Status != metatoken.StatusExpiring || *s.DaysLeft != 10 {
		t.Fatalf("status %+v", s)
	}

	u.answer = func(string) (string, time.Duration, error) {
		return "", 0, errors.New(`{"error":{"message":"Error validating access token: Session has expired","code":190}}` + strings.Repeat("x", 600))
	}
	err := u.keeper.RefreshNow(context.Background())
	if err == nil || u.keeper.Token() != "seed+" {
		t.Fatalf("err %v, token %q", err, u.keeper.Token())
	}
	s := u.keeper.Status()
	if s.Status != metatoken.StatusExpired || s.LastAttemptAt == nil || len([]rune(s.LastError)) > 501 {
		t.Fatalf("status %+v", s)
	}

	// Past the expiry date it is expired whatever the last error said.
	u.answer = func(string) (string, time.Duration, error) { return "", 0, errors.New("network down") }
	u.keeper.RefreshNow(context.Background())
	c.t = c.t.Add(11 * 24 * time.Hour)
	if s := u.keeper.Status(); s.Status != metatoken.StatusExpired {
		t.Fatalf("status %+v", s)
	}
}

func TestNoTokenConfigured(t *testing.T) {
	_, u := setup(t, `""`)
	if u.keeper.Token() != "" {
		t.Fatal("token without a configured one")
	}
	if err := u.keeper.RefreshNow(context.Background()); err == nil {
		t.Fatal("refreshing nothing should fail")
	}
	if err := u.keeper.RunDue(context.Background()); err != nil || len(u.asked) != 0 {
		t.Fatalf("RunDue without a token: %v, asked %v", err, u.asked)
	}
}

func TestConnectedAccount(t *testing.T) {
	// Connected through OAuth without a token in the config.
	h, u := setup(t, `""`)
	c := &clock{time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	u.keeper.SetClock(c.now)
	expires := c.t.Add(60 * 24 * time.Hour)
	if err := u.keeper.Connect("long-lived", expires, "@lna", "17841"); err != nil {
		t.Fatal(err)
	}
	if u.keeper.Token() != "long-lived" {
		t.Fatalf("token %q", u.keeper.Token())
	}
	s := u.keeper.Status()
	if s.Source != "connected" || s.Account != "@lna" || s.AccountID != "17841" || s.Status != metatoken.StatusOK || !s.ExpiresAt.Equal(expires) {
		t.Fatalf("status %+v", s)
	}
	// It is refreshed like any other, once a week, and stays connected.
	c.t = c.t.Add(8 * 24 * time.Hour)
	if err := u.keeper.RunDue(context.Background()); err != nil || u.keeper.Token() != "long-lived+" || u.asked[0] != "long-lived" {
		t.Fatalf("refresh %v %q %v", err, u.keeper.Token(), u.asked)
	}
	if s := u.keeper.Status(); s.Source != "connected" || s.Account != "@lna" {
		t.Fatalf("after refresh %+v", s)
	}
	// A token put into the config later replaces the connection, as any
	// new configured token replaces the stored one.
	if err := h.Rewrite("modules:\n  metatoken-test: { instances: { main: { token: fresh } } }\n"); err != nil {
		t.Fatal(err)
	}
	u2 := companiontest.Instance[*tokenUser](h, "metatoken-test:main")
	if u2.keeper.Token() != "fresh" || u2.keeper.Status().Source != "config" {
		t.Fatalf("new config token: %q %+v", u2.keeper.Token(), u2.keeper.Status())
	}

	// Disconnecting falls back to the configured token.
	u2.keeper.Connect("again", expires, "@lna", "17841")
	if err := u2.keeper.Disconnect(); err != nil || u2.keeper.Token() != "fresh" || u2.keeper.Status().Source != "config" {
		t.Fatalf("disconnect %v %q", err, u2.keeper.Token())
	}
}

func TestLooksExpired(t *testing.T) {
	for msg, want := range map[string]bool{
		`{"error":{"code":190}}`:            true,
		"Session has EXPIRED":               true,
		"connection reset":                  false,
		`{"error":{"code":1900}}`:           false,
		`{"error":{"code":4,"message":""}}`: false,
	} {
		if metatoken.LooksExpired(msg) != want {
			t.Errorf("LooksExpired(%q) != %v", msg, want)
		}
	}
}

var legacyKey = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

func TestUnsealLegacy(t *testing.T) {
	sealed, err := metatoken.SealLegacy(legacyKey, "insta", "IGQ-token")
	if err != nil {
		t.Fatal(err)
	}
	if tok, err := metatoken.UnsealLegacy(legacyKey, "insta", sealed); err != nil || tok != "IGQ-token" {
		t.Fatalf("%q %v", tok, err)
	}
	other := base64.StdEncoding.EncodeToString([]byte("fedcba9876543210fedcba9876543210"))
	for name, call := range map[string]func() (string, error){
		"wrong key":         func() (string, error) { return metatoken.UnsealLegacy(other, "insta", sealed) },
		"wrong target":      func() (string, error) { return metatoken.UnsealLegacy(legacyKey, "threads", sealed) },
		"short key":         func() (string, error) { return metatoken.UnsealLegacy("c2hvcnQ=", "insta", sealed) },
		"key not base64":    func() (string, error) { return metatoken.UnsealLegacy("%%%", "insta", sealed) },
		"truncated":         func() (string, error) { return metatoken.UnsealLegacy(legacyKey, "insta", "AAAA") },
		"stored not base64": func() (string, error) { return metatoken.UnsealLegacy(legacyKey, "insta", "%%%") },
	} {
		if _, err := call(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestImportLegacy(t *testing.T) {
	h, u := setup(t, "seed")
	sealed, _ := metatoken.SealLegacy(legacyKey, "main", "refreshed-token")
	ghost, _ := metatoken.SealLegacy(legacyKey, "gone", "x")
	old := companiontest.LegacyDB(t,
		`CREATE TABLE platform_tokens (platform text, target_name text, access_token text, seed_hash text, refreshed_at datetime, expires_at datetime)`,
		`INSERT INTO platform_tokens VALUES ('instagram', 'main', '`+sealed+`', '', '2026-09-01 00:00:00', '2026-10-31 00:00:00')`,
		`INSERT INTO platform_tokens VALUES ('instagram', 'gone', '`+ghost+`', '', '2026-09-01 00:00:00', '2026-10-31 00:00:00')`,
		`INSERT INTO platform_tokens VALUES ('instagram', 'never', 'x', '', NULL, NULL)`,
		`INSERT INTO platform_tokens VALUES ('threads', 'main', 'x', '', '2026-09-01 00:00:00', NULL)`)
	keeper := func(companion.InstanceRef) *metatoken.Keeper { return u.keeper }
	mc := h.Module("metatoken-test")

	// Without the old key nothing moves.
	t.Setenv("TOKEN_ENCRYPTION_KEY", "")
	if err := metatoken.ImportLegacy(mc, old, "instagram", keeper); err != nil {
		t.Fatal(err)
	}
	if u.keeper.Token() != "seed" {
		t.Fatal("imported without the key")
	}

	t.Setenv("TOKEN_ENCRYPTION_KEY", legacyKey)
	old.Check = true
	metatoken.ImportLegacy(mc, old, "instagram", keeper)
	if u.keeper.Token() != "seed" {
		t.Fatal("--check changed the token")
	}
	old.Check = false
	if err := metatoken.ImportLegacy(mc, old, "instagram", keeper); err != nil {
		t.Fatal(err)
	}
	if u.keeper.Token() != "refreshed-token" {
		t.Fatalf("token after import %q", u.keeper.Token())
	}
	if s := u.keeper.Status(); s.Source != "refreshed" || s.ExpiresAt == nil || s.ExpiresAt.Day() != 31 {
		t.Fatalf("status %+v", s)
	}

	// No platform_tokens table: nothing to do.
	if err := metatoken.ImportLegacy(mc, companiontest.LegacyDB(t), "instagram", keeper); err != nil {
		t.Fatal(err)
	}
}
