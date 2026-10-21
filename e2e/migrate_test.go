package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/core"
)

// fixture copies the old installation into a fresh directory.
func fixture(t *testing.T) (dir, data string) {
	t.Helper()
	dir = t.TempDir()
	data = filepath.Join(dir, "data")
	if err := copyDir("testdata/legacy", data); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FIXTURE_BUCKET", filepath.Join(data, "bucket"))
	t.Setenv("FIXTURE_GBIF", "http://127.0.0.1:9/density")
	t.Setenv("TRUSTED_PROXIES", "")
	return dir, data
}

func migrate(t *testing.T, dir, data string, mo core.MigrateOptions) (string, error) {
	t.Helper()
	var log bytes.Buffer
	mo.From, mo.Out = data, &log
	err := core.Migrate(context.Background(), core.Options{ConfigPath: filepath.Join(dir, "companion.yaml"), DataDir: data}, mo)
	t.Logf("migrate:\n%s", log.String())
	return log.String(), err
}

func TestMigrateCheckChangesNothing(t *testing.T) {
	dir, data := fixture(t)
	before := listing(t, dir)
	out, err := migrate(t, dir, data, core.MigrateOptions{Check: true})
	if err != nil {
		t.Fatal(err)
	}
	if after := listing(t, dir); after != before {
		t.Fatalf("--check changed files:\nbefore\n%s\nafter\n%s", before, after)
	}
	for _, want := range []string{"--- ", "token: ${MASTODON_PAT}", "(check, nothing is written)", "microblog: 2 post(s)", "instagram: main-instagram: refreshed token"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in the report", want)
		}
	}
}

func TestMigrateOnceThenRefuses(t *testing.T) {
	dir, data := fixture(t)
	if _, err := migrate(t, dir, data, core.MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"companion.yaml", "build.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	backups, _ := filepath.Glob(filepath.Join(data, "backups", "legacy-*.tar.gz"))
	if len(backups) != 1 {
		t.Fatalf("legacy backups %v", backups)
	}
	// The old installation is untouched.
	old, _ := os.ReadFile(filepath.Join(data, "companion.db"))
	orig, _ := os.ReadFile("testdata/legacy/companion.db")
	if !bytes.Equal(old, orig) {
		t.Fatal("the old companion.db changed")
	}

	_, err := migrate(t, dir, data, core.MigrateOptions{})
	if err == nil || !strings.Contains(err.Error(), "migrated on") {
		t.Fatalf("second run: %v", err)
	}
	// A refused run writes nothing, not even a backup.
	all, _ := filepath.Glob(filepath.Join(data, "backups", "*"))
	if len(all) != 1 {
		t.Fatalf("backups after the refused run %v", all)
	}
}

func TestMigratedCompanionKeepsItsState(t *testing.T) {
	dir, data := fixture(t)
	if _, err := migrate(t, dir, data, core.MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "companion.yaml")
	c := core.New(core.Options{ConfigPath: cfg, DataDir: data, Logs: io.Discard})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop(context.Background())
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()
	admin := func(path string, into any) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("Authorization", "ApiKey fixture-admin-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, b)
		}
		if err := json.Unmarshal(b, into); err != nil {
			t.Fatalf("%s: %v %s", path, err, b)
		}
	}

	// The refreshed Instagram token came over and is what is used.
	var tokens []struct {
		Instance, Status, Source string
	}
	admin("/api/b/instagram/tokens", &tokens)
	if len(tokens) != 1 || tokens[0].Instance != "main-instagram" || tokens[0].Source != "refreshed" {
		t.Fatalf("tokens %+v", tokens)
	}

	// The posts with their publications, the history and the failure that
	// a later success resolved.
	var items struct {
		Items []struct {
			ItemID string `json:"id"`
			Cells  map[string]struct{ State string }
		}
	}
	admin("/api/b/autoupload/items", &items)
	found := false
	for _, it := range items.Items {
		if it.ItemID == "watzmann" {
			found = true
			if it.Cells["mastodon:main-mastodon"].State != "published" || it.Cells["pixelfed:main-pixelfed"].State != "published" {
				t.Fatalf("watzmann cells %+v", it.Cells)
			}
		}
	}
	if !found {
		t.Fatalf("items %+v", items)
	}
	var problems struct{ Total int }
	admin("/api/b/autoupload/problems", &problems)
	if problems.Total != 0 {
		t.Fatalf("a resolved failure is a problem: %+v", problems)
	}
}

// listing is every file under dir with its size.
func listing(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			fmt.Fprintf(&b, "%s %d %s\n", rel, fi.Size(), fi.ModTime())
		}
		return nil
	})
	return b.String()
}

// An operator who wrote companion.yaml (or ran the new companion) before
// migrating keeps that config, and its state is backed up first.
func TestMigrateIntoAPreparedCompanion(t *testing.T) {
	first, firstData := fixture(t)
	if _, err := migrate(t, first, firstData, core.MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	prepared, _ := os.ReadFile(filepath.Join(first, "companion.yaml"))
	prepared = append([]byte("# edited by the operator\n"), prepared...)

	dir, data := fixture(t)
	cfg := filepath.Join(dir, "companion.yaml")
	if err := os.WriteFile(cfg, prepared, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := migrate(t, dir, data, core.MigrateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(cfg); !bytes.Equal(got, prepared) {
		t.Fatal("the prepared companion.yaml was replaced")
	}
	pre, _ := filepath.Glob(filepath.Join(data, "backups", "pre-migrate-*.tar.gz"))
	if len(pre) != 1 || !strings.Contains(out, "is used as it is") {
		t.Fatalf("pre-migrate backups %v", pre)
	}
}

// companion.yaml.example loads with every module compiled in: each block
// and slot in it exists and every instance accepts its config.
func TestExampleConfigIsValid(t *testing.T) {
	raw, err := os.ReadFile("../companion.yaml.example")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "companion.yaml")
	example := strings.Replace(string(raw), "s3://<bucket>", "s3://example-bucket", 1)
	if err := os.WriteFile(cfg, []byte(example), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"ADMIN_API_KEY", "MASTODON_INSTANCE", "MASTODON_PAT", "PIXELFED_PAT", "BLUESKY_USERNAME", "BLUESKY_PAT",
		"INSTAGRAM_ACCOUNT_ID", "INSTAGRAM_ACCESS_TOKEN", "THREADS_USER_ID", "THREADS_ACCESS_TOKEN", "IP_HASH_SALT"} {
		t.Setenv(v, "x-"+strings.ToLower(v))
	}
	t.Setenv("MASTODON_INSTANCE", "https://mastodon.example")
	c := core.New(core.Options{ConfigPath: cfg, DataDir: filepath.Join(dir, "data"), Logs: io.Discard})
	if err := c.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop(context.Background())
	for _, id := range []string{"autoupload:photos-threads", "basemap:default", "trips:default", "interactions:default"} {
		if _, ok := c.Instance(id); !ok {
			t.Errorf("no %s", id)
		}
	}
}
