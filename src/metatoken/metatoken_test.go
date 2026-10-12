package metatoken

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/instagramapi"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/LNA-DEV/HomePageCompanion/threadsapi"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	igSeed = "IGAA-seed-token-0001"
	thSeed = "THAA-seed-token-0001"
)

func newKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

// useKey installs a fresh key for the test and disables refresh afterwards.
func useKey(t *testing.T) {
	t.Helper()
	if err := setKey(newKey(t)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = setKey("") })
}

// useDB gives the test its own SQLite database.
func useDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.PlatformToken{}); err != nil {
		t.Fatal(err)
	}
	prev := database.Db
	database.Db = db
	t.Cleanup(func() { database.Db = prev })
	return db
}

// useConfig writes a config.yaml with one Instagram, one Threads and one
// Mastodon target into a scratch working directory, takes their tokens from
// IG_TOKEN / TH_TOKEN, and wires Apply as the real startup does.
func useConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := `targets:
  - name: main-instagram
    platform: instagram
    accessToken: ${IG_TOKEN}
    accountId: "1"
  - name: main-threads
    platform: threads
    accessToken: ${TH_TOKEN}
    accountId: "2"
  - name: main-mastodon
    platform: mastodon
    accessToken: not-a-meta-token
`
	if err := os.WriteFile(filepath.Join("data", "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IG_TOKEN", igSeed)
	t.Setenv("TH_TOKEN", thSeed)

	prevData, prevHook := config.Data, config.AfterLoad
	config.AfterLoad = Apply
	t.Cleanup(func() { config.Data, config.AfterLoad = prevData, prevHook })
	config.LoadConfig()
}

// fakeMeta stands in for both refresh endpoints. It answers each token with
// that token plus "-r", or fails with status when set.
type fakeMeta struct {
	mu     sync.Mutex
	calls  []string // grant_type:access_token
	status int
}

func (f *fakeMeta) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	f.calls = append(f.calls, q.Get("grant_type")+":"+q.Get("access_token"))
	if f.status != 0 {
		w.WriteHeader(f.status)
		io.WriteString(w, `{"error":{"message":"Error validating access token: Session has expired","type":"OAuthException","code":190}}`)
		return
	}
	fmt.Fprintf(w, `{"access_token":%q,"token_type":"bearer","expires_in":5184000}`, q.Get("access_token")+"-r")
}

func (f *fakeMeta) takeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

func useFakeMeta(t *testing.T) *fakeMeta {
	t.Helper()
	f := &fakeMeta{}
	srv := httptest.NewServer(f)
	prevIG, prevTH := instagramapi.RefreshURL, threadsapi.RefreshURL
	instagramapi.RefreshURL = srv.URL + "/ig"
	threadsapi.RefreshURL = srv.URL + "/th"
	t.Cleanup(func() {
		instagramapi.RefreshURL, threadsapi.RefreshURL = prevIG, prevTH
		srv.Close()
	})
	return f
}

func tokenOf(name string) string {
	for _, t := range config.Data.Targets {
		if t.Name == name {
			return t.AccessToken
		}
	}
	return ""
}

func rowOf(t *testing.T, name string) models.PlatformToken {
	t.Helper()
	var row models.PlatformToken
	if database.Db.Where("target_name = ?", name).Limit(1).Find(&row).RowsAffected == 0 {
		t.Fatalf("no row for %s", name)
	}
	return row
}

func countRows(t *testing.T) int64 {
	t.Helper()
	var n int64
	database.Db.Model(&models.PlatformToken{}).Count(&n)
	return n
}

// --- encryption --------------------------------------------------------------

func TestSealRoundTrip(t *testing.T) {
	useKey(t)
	sealed, err := seal("main-instagram", igSeed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, igSeed) {
		t.Fatal("sealed value contains the token")
	}
	got, err := unseal("main-instagram", sealed)
	if err != nil || got != igSeed {
		t.Fatalf("unseal = %q, %v", got, err)
	}
}

func TestSealFreshNonce(t *testing.T) {
	useKey(t)
	a, _ := seal("x", igSeed)
	b, _ := seal("x", igSeed)
	if a == b {
		t.Fatal("two seals of the same token must differ")
	}
}

func TestUnsealWrongKey(t *testing.T) {
	useKey(t)
	sealed, _ := seal("x", igSeed)
	useKey(t)
	if _, err := unseal("x", sealed); err == nil {
		t.Fatal("unseal with another key must fail")
	}
}

func TestUnsealOtherTarget(t *testing.T) {
	useKey(t)
	sealed, _ := seal("main-instagram", igSeed)
	if _, err := unseal("main-threads", sealed); err == nil {
		t.Fatal("a value moved to another target must not decrypt")
	}
}

func TestSetKeyRejects(t *testing.T) {
	t.Cleanup(func() { _ = setKey("") })
	cases := map[string]string{
		"not base64": "%%%",
		"16 bytes":   base64.StdEncoding.EncodeToString(make([]byte, 16)),
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			if err := setKey(key); err == nil {
				t.Fatal("expected an error")
			}
			if Enabled() || disabledReason == "" {
				t.Fatalf("refresh must be off with a reason, got enabled=%v reason=%q", Enabled(), disabledReason)
			}
		})
	}
	if err := setKey(""); err != nil || Enabled() {
		t.Fatal("an empty key disables refresh without an error")
	}
}

// --- overlay (concept §5.2) --------------------------------------------------

func TestApplySeeds(t *testing.T) {
	useKey(t)
	useDB(t)
	useConfig(t)

	if tokenOf("main-instagram") != igSeed || tokenOf("main-threads") != thSeed {
		t.Fatal("a fresh seed must keep the .env tokens")
	}
	row := rowOf(t, "main-instagram")
	if row.SeedHash != seedHash(igSeed) || row.RefreshedAt != nil || row.ExpiresAt != nil || row.Platform != "instagram" {
		t.Fatalf("unexpected seed row %+v", row)
	}
	if strings.Contains(row.AccessToken, igSeed) {
		t.Fatal("token stored in plain text")
	}
	if countRows(t) != 2 {
		t.Fatalf("want rows for the two Meta targets only, got %d", countRows(t))
	}
	if tokenOf("main-mastodon") != "not-a-meta-token" {
		t.Fatal("non-Meta targets must be untouched")
	}
}

func TestApplyUsesStoredToken(t *testing.T) {
	useKey(t)
	useDB(t)
	useConfig(t)

	sealed, _ := seal("main-instagram", "IGAA-refreshed")
	database.Db.Model(&models.PlatformToken{}).Where("target_name = ?", "main-instagram").Update("access_token", sealed)

	config.LoadConfig()
	if got := tokenOf("main-instagram"); got != "IGAA-refreshed" {
		t.Fatalf("token = %q, want the stored one", got)
	}
}

func TestApplyNewEnvTokenWins(t *testing.T) {
	useKey(t)
	useDB(t)
	useConfig(t)

	now := time.Now()
	sealed, _ := seal("main-instagram", "IGAA-refreshed")
	database.Db.Model(&models.PlatformToken{}).Where("target_name = ?", "main-instagram").
		Updates(map[string]any{"access_token": sealed, "refreshed_at": now, "expires_at": now.Add(time.Hour), "last_error": "x"})

	t.Setenv("IG_TOKEN", "IGAA-pasted-by-hand")
	config.LoadConfig()

	if got := tokenOf("main-instagram"); got != "IGAA-pasted-by-hand" {
		t.Fatalf("token = %q, want the new .env one", got)
	}
	row := rowOf(t, "main-instagram")
	if row.SeedHash != seedHash("IGAA-pasted-by-hand") || row.RefreshedAt != nil || row.ExpiresAt != nil || row.LastError != "" {
		t.Fatalf("row not re-seeded: %+v", row)
	}
}

func TestApplyReseedsUndecryptableRow(t *testing.T) {
	useKey(t)
	useDB(t)
	useConfig(t)

	sealed, _ := seal("main-instagram", "IGAA-refreshed")
	database.Db.Model(&models.PlatformToken{}).Where("target_name = ?", "main-instagram").Update("access_token", sealed)
	useKey(t) // the key changed

	config.LoadConfig()
	if got := tokenOf("main-instagram"); got != igSeed {
		t.Fatalf("token = %q, want the .env one", got)
	}
	if got, err := unseal("main-instagram", rowOf(t, "main-instagram").AccessToken); err != nil || got != igSeed {
		t.Fatalf("row not re-seeded with the new key: %q, %v", got, err)
	}
}

func TestApplyEmptyEnvToken(t *testing.T) {
	useKey(t)
	useDB(t)
	useConfig(t)
	database.Db.Where("1 = 1").Delete(&models.PlatformToken{})
	t.Setenv("IG_TOKEN", "")
	config.LoadConfig()

	var n int64
	database.Db.Model(&models.PlatformToken{}).Where("target_name = ?", "main-instagram").Count(&n)
	if n != 0 || tokenOf("main-instagram") != "" {
		t.Fatal("an empty .env token must be left alone")
	}
}

func TestApplyWithoutKey(t *testing.T) {
	_ = setKey("")
	useDB(t)
	useConfig(t)

	if countRows(t) != 0 {
		t.Fatal("nothing may be stored without a key")
	}
	if tokenOf("main-instagram") != igSeed {
		t.Fatal("without a key the .env token is used")
	}
}

// --- refresh job (concept §5.3, §5.4) ----------------------------------------

func TestRunDueRefreshesAndSwapsConfig(t *testing.T) {
	useKey(t)
	useDB(t)
	meta := useFakeMeta(t)
	useConfig(t)

	before := time.Now()
	RunDue()

	calls := meta.takeCalls()
	want := []string{"ig_refresh_token:" + igSeed, "th_refresh_token:" + thSeed}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	if tokenOf("main-instagram") != igSeed+"-r" || tokenOf("main-threads") != thSeed+"-r" {
		t.Fatalf("config not swapped: %q, %q", tokenOf("main-instagram"), tokenOf("main-threads"))
	}
	row := rowOf(t, "main-instagram")
	if row.RefreshedAt == nil || row.ExpiresAt == nil || row.LastAttemptAt == nil || row.LastError != "" {
		t.Fatalf("row not updated: %+v", row)
	}
	if exp := before.Add(60 * 24 * time.Hour); row.ExpiresAt.Before(exp.Add(-time.Minute)) || row.ExpiresAt.After(exp.Add(time.Minute)) {
		t.Fatalf("expiresAt = %v, want about %v", row.ExpiresAt, exp)
	}
	if row.SeedHash != seedHash(igSeed) {
		t.Fatal("a refresh must keep the seed hash, or the next load would re-seed")
	}

	// The 5-minute reload keeps the refreshed token.
	config.LoadConfig()
	if tokenOf("main-instagram") != igSeed+"-r" {
		t.Fatal("reload brought the .env token back")
	}

	// Not due again right away.
	RunDue()
	if calls := meta.takeCalls(); len(calls) != 0 {
		t.Fatalf("refreshed again too early: %v", calls)
	}
}

func TestRunDueInterval(t *testing.T) {
	useKey(t)
	useDB(t)
	meta := useFakeMeta(t)
	useConfig(t)
	RunDue()
	meta.takeCalls()

	for _, tc := range []struct {
		age  time.Duration
		want int
	}{
		{6 * 24 * time.Hour, 0},
		{7 * 24 * time.Hour, 2},
	} {
		database.Db.Model(&models.PlatformToken{}).Where("1 = 1").Update("refreshed_at", time.Now().Add(-tc.age))
		RunDue()
		if got := len(meta.takeCalls()); got != tc.want {
			t.Fatalf("age %v: %d refreshes, want %d", tc.age, got, tc.want)
		}
	}
}

func TestRunDueFailureKeepsToken(t *testing.T) {
	useKey(t)
	useDB(t)
	meta := useFakeMeta(t)
	useConfig(t)
	meta.status = http.StatusBadRequest

	RunDue()
	if tokenOf("main-instagram") != igSeed {
		t.Fatal("a failed refresh must keep the old token")
	}
	row := rowOf(t, "main-instagram")
	if row.LastAttemptAt == nil || !strings.Contains(row.LastError, `"code":190`) || row.RefreshedAt != nil {
		t.Fatalf("failure not recorded: %+v", row)
	}
	if s, _ := StatusOf("main-instagram"); s.Status != StatusExpired {
		t.Fatalf("status = %q, want expired", s.Status)
	}

	// A failed seed is still due and retried by the next run.
	meta.takeCalls()
	meta.status = 0
	RunDue()
	if len(meta.takeCalls()) != 2 || tokenOf("main-instagram") != igSeed+"-r" {
		t.Fatal("the next run must retry and succeed")
	}
	if rowOf(t, "main-instagram").LastError != "" {
		t.Fatal("a success clears the error")
	}
}

func TestRunDueSkipsUnconfiguredTargets(t *testing.T) {
	useKey(t)
	useDB(t)
	meta := useFakeMeta(t)
	useConfig(t)

	sealed, _ := seal("gone", "IGAA-gone")
	database.Db.Create(&models.PlatformToken{TargetName: "gone", Platform: "instagram", AccessToken: sealed, SeedHash: seedHash("IGAA-gone")})

	RunDue()
	for _, c := range meta.takeCalls() {
		if strings.Contains(c, "IGAA-gone") {
			t.Fatal("a target no longer in the config must not be refreshed")
		}
	}
}

func TestRunDueWithoutKey(t *testing.T) {
	useKey(t)
	useDB(t)
	meta := useFakeMeta(t)
	useConfig(t)
	_ = setKey("")

	RunDue()
	if calls := meta.takeCalls(); len(calls) != 0 {
		t.Fatalf("no request may be made without a key, got %v", calls)
	}
}

// TestRefreshDoesNotOverwriteReseed: a refresh that finishes after the row
// was re-seeded from .env must not write the old lineage back.
func TestRefreshDoesNotOverwriteReseed(t *testing.T) {
	useKey(t)
	useDB(t)
	useFakeMeta(t)
	useConfig(t)

	stale := rowOf(t, "main-instagram")
	t.Setenv("IG_TOKEN", "IGAA-pasted-by-hand")
	config.LoadConfig()

	if refreshOne(stale, time.Now()) {
		t.Fatal("refreshOne must report no change for a re-seeded row")
	}
	if got, _ := unseal("main-instagram", rowOf(t, "main-instagram").AccessToken); got != "IGAA-pasted-by-hand" {
		t.Fatalf("stored token = %q, want the new seed", got)
	}
}

// --- what is stored and served -----------------------------------------------

func TestDatabaseHoldsNoPlaintext(t *testing.T) {
	useKey(t)
	useDB(t)
	useFakeMeta(t)
	useConfig(t)
	RunDue()

	var rows []map[string]any
	database.Db.Raw("SELECT * FROM platform_tokens").Scan(&rows)
	dump := fmt.Sprint(rows)
	for _, tok := range []string{igSeed, thSeed, igSeed + "-r", thSeed + "-r"} {
		if strings.Contains(dump, tok) {
			t.Fatalf("plain token %q in the database", tok)
		}
	}
}

func adminRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterRoutes(r.Group("/api"), func(c *gin.Context) { c.Next() })
	return r
}

func serve(r *gin.Engine, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestAdminNeverReturnsToken(t *testing.T) {
	useKey(t)
	useDB(t)
	meta := useFakeMeta(t)
	useConfig(t)
	r := adminRouter()

	w := serve(r, http.MethodPost, "/api/admin/tokens/main-instagram/refresh")
	if w.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", w.Code, w.Body)
	}
	var s Status
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.Status != StatusOK || s.Source != "refreshed" || s.DaysLeft == nil || *s.DaysLeft != 59 {
		t.Fatalf("unexpected status %+v", s)
	}
	if calls := meta.takeCalls(); len(calls) != 1 {
		t.Fatalf("refresh now must touch one target, got %v", calls)
	}

	w = serve(r, http.MethodGet, "/api/admin/tokens")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	for _, body := range []string{w.Body.String(), serve(r, http.MethodPost, "/api/admin/tokens/main-threads/refresh").Body.String()} {
		for _, tok := range []string{"IGAA", "THAA"} {
			if strings.Contains(body, tok) {
				t.Fatalf("response contains a token: %s", body)
			}
		}
	}
}

func TestAdminRefreshErrors(t *testing.T) {
	useKey(t)
	useDB(t)
	useFakeMeta(t)
	useConfig(t)
	r := adminRouter()

	if w := serve(r, http.MethodPost, "/api/admin/tokens/main-mastodon/refresh"); w.Code != http.StatusNotFound {
		t.Fatalf("non-Meta target: %d", w.Code)
	}
	if w := serve(r, http.MethodPost, "/api/admin/tokens/nope/refresh"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown target: %d", w.Code)
	}

	_ = setKey("")
	if w := serve(r, http.MethodPost, "/api/admin/tokens/main-instagram/refresh"); w.Code != http.StatusConflict {
		t.Fatalf("without a key: %d", w.Code)
	}
	var list []Status
	_ = json.Unmarshal(serve(r, http.MethodGet, "/api/admin/tokens").Body.Bytes(), &list)
	if len(list) != 2 || list[0].Status != StatusDisabled || !strings.Contains(list[0].DisabledReason, keyEnv) {
		t.Fatalf("without a key both targets show disabled: %+v", list)
	}
}

func TestStatusExpiring(t *testing.T) {
	useKey(t)
	useDB(t)
	useConfig(t)

	now := time.Now()
	database.Db.Model(&models.PlatformToken{}).Where("target_name = ?", "main-instagram").
		Updates(map[string]any{"refreshed_at": now.Add(-50 * 24 * time.Hour), "expires_at": now.Add(10 * 24 * time.Hour)})
	if s, _ := StatusOf("main-instagram"); s.Status != StatusExpiring {
		t.Fatalf("status = %q, want expiring", s.Status)
	}
	if s, _ := StatusOf("main-threads"); s.Status != StatusUnknown || s.Source != "config" {
		t.Fatalf("an unrefreshed seed is unknown: %+v", s)
	}
}
