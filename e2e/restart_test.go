//go:build integration

package e2e_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/e2e/world"
)

type run struct {
	ID     int
	Result string
	ItemID string
	Error  string
}

// TestRestartInTheMiddleOfAJob stops the companion, as a container stop
// does, while a post hangs on its way to the platform.
func TestRestartInTheMiddleOfAJob(t *testing.T) {
	w := start(t, false)
	if err := w.Index(); err != nil {
		t.Fatal(err)
	}
	hanging, release := w.Mastodon.Hang("status")
	defer release()
	go w.Rotate("photos-mastodon")
	<-hanging

	stopped := make(chan error, 1)
	go func() { stopped <- w.Restart() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the restart waits for the hung platform call")
	}
	release() // the platform finishes the post the stopped run sent
	waitFor(t, func() bool { return len(w.Mastodon.Posts()) == 1 })

	// No run stays "running" for ever, and the post goes out once.
	var runs []run
	get(t, w, "/api/b/autoupload/runs?connection=photos-mastodon", &runs)
	if len(runs) != 1 || runs[0].Result != "interrupted" {
		t.Fatalf("runs after the restart %+v", runs)
	}
	if err := w.Rotate("photos-mastodon"); err != nil {
		t.Fatal(err)
	}
	assertOnce(t, w.Mastodon)
}

// TestKilledInTheMiddleOfAJob kills the companion process (kill -9, an OOM
// kill, a power cut) during "run now", and starts it again.
func TestKilledInTheMiddleOfAJob(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "companion")
	if out, err := exec.Command("go", "build", "-o", bin, "./cmd/companion").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	feed := world.NewFeed(world.Photos(5), nil)
	defer feed.Close()
	masto := world.NewFedi("masto-token")
	defer masto.Close()
	addr := freeAddr(t)
	cfg := filepath.Join(dir, "companion.yaml")
	os.WriteFile(cfg, []byte(fmt.Sprintf(`core:
  domain: example.com
  adminKey: %s
  listen: %q
  database: db.sqlite
  blobs: blob.fs
modules:
  db.sqlite: {}
  blob.fs: {}
  rss:
    instances:
      photos: { url: "%s/gallery.xml" }
  mastodon:
    instances:
      main: { instance: %q, token: masto-token }
  autoupload:
    instances:
      photos-mastodon: { source: rss:photos, target: mastodon:main }
`, world.AdminKey, addr, feed.URL, masto.URL)), 0o600)
	base := "http://" + addr
	var logs bytes.Buffer
	startProc := func() *exec.Cmd {
		cmd := exec.Command(bin, "serve")
		cmd.Env = append(os.Environ(), "COMPANION_CONFIG="+cfg, "COMPANION_DATA="+filepath.Join(dir, "data"),
			"COMPANION_SECRET_KEY=d29ybGQtc2VjcmV0LWtleS0zMi1ieXRlcy1sb25nISE=")
		cmd.Stdout, cmd.Stderr = &logs, &logs
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool {
			resp, err := http.Get(base + "/health")
			if err == nil {
				resp.Body.Close()
			}
			return err == nil && resp.StatusCode == 200
		})
		return cmd
	}
	admin := func(method, path string) string {
		req, _ := http.NewRequest(method, base+path, nil)
		req.Header.Set("Authorization", "ApiKey "+world.AdminKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	defer func() {
		if t.Failed() {
			// "companion serve" logs into data/logs/.
			files, _ := filepath.Glob(filepath.Join(dir, "data", "logs", "*", "*.log"))
			for _, f := range files {
				b, _ := os.ReadFile(f)
				t.Logf("%s:\n%s", f, b)
			}
			t.Logf("output:\n%s", logs.String())
		}
	}()

	proc := startProc()
	runJob := func(key string) {
		req, _ := http.NewRequest("POST", base+"/api/core/jobs/run", strings.NewReader(fmt.Sprintf(`{"key":%q,"wait":true}`, key)))
		req.Header.Set("Authorization", "ApiKey "+world.AdminKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	runJob("rss:photos/refresh")
	runJob("autoupload/index")

	hanging, release := masto.Hang("status")
	defer release()
	admin("POST", "/api/b/autoupload/connections/photos-mastodon/run")
	select {
	case <-hanging:
	case <-time.After(10 * time.Second):
		t.Fatal("the run never reached Mastodon")
	}
	proc.Process.Signal(syscall.SIGKILL)
	proc.Wait()
	release() // the platform finishes the post the dead process sent
	waitFor(t, func() bool { return len(masto.Posts()) == 1 })

	proc = startProc()
	defer func() { proc.Process.Signal(syscall.SIGTERM); proc.Wait() }()
	if out := admin("GET", "/api/b/autoupload/runs?connection=photos-mastodon"); !strings.Contains(out, `"result":"interrupted"`) || strings.Contains(out, `"result":"running"`) {
		t.Fatalf("runs after the kill: %s", out)
	}
	runJob("autoupload:photos-mastodon/publish")
	waitFor(t, func() bool {
		return strings.Contains(admin("GET", "/api/b/autoupload/runs?connection=photos-mastodon"), `"result":"published"`)
	})
	assertOnce(t, masto)

	// SIGTERM during a hung call: the process ends within Docker's ten
	// seconds instead of being killed.
	hanging, release2 := masto.Hang("status")
	defer release2()
	admin("POST", "/api/b/autoupload/connections/photos-mastodon/run")
	<-hanging
	began := time.Now()
	proc.Process.Signal(syscall.SIGTERM)
	exited := make(chan struct{})
	go func() { proc.Wait(); close(exited) }()
	select {
	case <-exited:
		if d := time.Since(began); d > 10*time.Second {
			t.Fatalf("SIGTERM took %s", d)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the companion did not stop on SIGTERM while a call hung")
	}
	proc = startProc()
}

// assertOnce fails when the platform received a post twice.
func assertOnce(t *testing.T, f *world.Fedi) {
	t.Helper()
	seen := map[string]int{}
	for _, p := range f.Posts() {
		seen[p.Text]++
		if seen[p.Text] > 1 {
			t.Fatalf("posted twice: %q (all: %+v)", p.Text, f.Posts())
		}
	}
	if len(f.Posts()) == 0 {
		t.Fatal("nothing posted")
	}
}

func freeAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}
