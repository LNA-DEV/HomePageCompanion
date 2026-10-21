//go:build compose

// The service tests (docs/concepts/modular-architecture.md §6): a service
// in its own container next to the companion, started by hpc apply, and
// what it may and may not do: its slot works, an unconnected one does not;
// the host it was granted is reachable, others are not, neither through the
// egress proxy nor around it; its container is locked down.
//
//	COMPANION_IMAGE=<image> go test -tags compose -run Compose -v .
package e2e_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type probeResult struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
	Error  string `json:"error"`
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestComposeServices(t *testing.T) {
	image := os.Getenv("COMPANION_IMAGE")
	if image == "" {
		t.Skip("COMPANION_IMAGE names the companion image to test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("no Docker")
	}
	dir, err := os.MkdirTemp("", "companion-compose-")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()

	// The probe service and the stand-ins for two hosts on the internet.
	buildProbe(t, dir)
	hpc := buildHPC(t, bin)
	port := freePort(t)
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(fmt.Sprintf(`services:
  companion:
    image: %s
    ports: ["127.0.0.1:%d:8080"]
    volumes: ["./data:/app/data"]
    environment:
      COMPANION_SECRET_KEY: Y29tcG9zZS10ZXN0LWtleS0zMi1ieXRlcy1sb25nISE=
  allowed:
    image: companion-probe:test
    environment: { PROBE_MODE: web, PROBE_NAME: allowed }
  denied:
    image: companion-probe:test
    environment: { PROBE_MODE: web, PROBE_NAME: denied }
`, image, port)), 0o644)
	os.WriteFile(filepath.Join(dir, "data", "companion.yaml"), []byte(fmt.Sprintf(`core:
  domain: example.com
  adminKey: compose-admin-key
  publicUrl: http://127.0.0.1:%d
modules:
  db.sqlite: {}
  blob.fs: {}
  webpush:
    config: { subscriber: "mailto:admin@example.com" }
services:
  probe:
    from: path:probe
    grants: { egress: ["allowed:8080"] }
    instances:
      main: { notify: webpush }
`, port)), 0o644)

	cleanup(t, dir)
	out := run(t, dir, hpc, "apply", "--yes")
	t.Logf("hpc apply:\n%s", out)
	for _, want := range []string{"asks for  volume", "gets      egress allowed:8080 · volume", "new service"} {
		if !strings.Contains(out, want) {
			t.Errorf("hpc apply did not show %q", want)
		}
	}

	admin := adminClient(port)
	waitReady(t, admin, "probe:main")
	probeCall := func(method, path string) probeResult {
		t.Helper()
		code, b := admin(method, "/api/b/probe/"+path)
		var r probeResult
		if code != 200 || json.Unmarshal(b, &r) != nil {
			t.Fatalf("%s %s: %d %s", method, path, code, b)
		}
		return r
	}

	if r := probeCall("GET", "fetch?url=http://allowed:8080/x"); r.Status != 200 || !strings.Contains(r.Body, `"hello":"allowed"`) {
		t.Errorf("granted host through the proxy: %+v", r)
	}
	if r := probeCall("GET", "fetch?url=http://denied:8080/"); r.Status != http.StatusForbidden || !strings.Contains(r.Body, "not granted") {
		t.Errorf("host not granted, through the proxy: %+v", r)
	}
	if r := probeCall("GET", "fetch?url=https://example.com/"); r.Error == "" || r.Status == 200 {
		t.Errorf("HTTPS to a host not granted: %+v", r)
	}
	if r := probeCall("GET", "direct?url=http://denied:8080/"); r.Error == "" {
		t.Errorf("around the proxy to the other host: %+v", r)
	}
	if r := probeCall("GET", "direct?url=http://1.1.1.1/"); r.Error == "" {
		t.Errorf("around the proxy to the internet: %+v", r)
	}
	if r := probeCall("GET", "core?method=GET&url=http://companion:8080/api/core/instances"); r.Status != http.StatusUnauthorized {
		t.Errorf("the admin API with the service's token: %+v", r)
	}
	if r := probeCall("GET", "core?url=http://companion:3128/core/v1/slots/notify/broadcast&target=probe:main"); r.Status != http.StatusForbidden {
		t.Errorf("a slot target not connected: %+v", r)
	}
	if r := probeCall("POST", "notify"); r.Status != 200 {
		t.Errorf("its connected slot: %+v", r)
	}
	if r := probeCall("POST", "write?path=/data/probe.txt"); r.Status != 200 {
		t.Errorf("writing its volume: %+v", r)
	}
	if r := probeCall("POST", "write?path=/probe.txt"); !strings.Contains(r.Error, "read-only") {
		t.Errorf("writing its root file system: %+v", r)
	}

	// Its container, as hpc apply wrote it.
	id := run(t, dir, "docker", "compose", "-f", "docker-compose.yml", "-f", "compose.generated.yml", "ps", "-q", "probe")
	inspect := run(t, dir, "docker", "inspect", "--format",
		"{{.HostConfig.ReadonlyRootfs}} {{.HostConfig.CapDrop}} {{.HostConfig.SecurityOpt}} {{.Config.User}} {{.HostConfig.Memory}} {{.HostConfig.PidsLimit}}", id)
	if inspect != "true [ALL] [no-new-privileges:true] 65532 268435456 128" {
		t.Errorf("container settings: %s", inspect)
	}
	nets := run(t, dir, "docker", "inspect", "--format", "{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}", id)
	netName := strings.TrimSpace(nets)
	if strings.Contains(netName, " ") || !strings.HasSuffix(netName, "_svc-probe") {
		t.Errorf("networks: %q", nets)
	}
	if internal := run(t, dir, "docker", "network", "inspect", "--format", "{{.Internal}}", netName); internal != "true" {
		t.Errorf("its network is not internal: %s", internal)
	}

	// A restart of the service: the core notices, at the latest with its
	// next health check, and configures it again.
	run(t, dir, "docker", "restart", id)
	deadline := time.Now().Add(30 * time.Second)
	for {
		code, b := admin("POST", "/api/b/probe/notify")
		var r probeResult
		if code == 200 && json.Unmarshal(b, &r) == nil && r.Status == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the service was not configured again after its restart")
		}
		time.Sleep(time.Second)
	}
}

// buildProbe builds the probe and its image, companion-probe:test.
func buildProbe(t *testing.T, dir string) {
	t.Helper()
	probe := filepath.Join(dir, "probe")
	os.MkdirAll(probe, 0o755)
	build := exec.Command("go", "build", "-o", filepath.Join(probe, "probe"), "./cmd/probe")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, out)
	}
	os.WriteFile(filepath.Join(probe, "Dockerfile"), []byte("FROM gcr.io/distroless/static-debian12:nonroot\nCOPY probe /probe\nENTRYPOINT [\"/probe\"]\n"), 0o644)
	run(t, probe, "docker", "build", "--quiet", "-t", "companion-probe:test", ".")
}

func buildHPC(t *testing.T, bin string) string {
	t.Helper()
	hpc := filepath.Join(bin, "hpc")
	if out, err := exec.Command("go", "build", "-o", hpc, "github.com/LNA-DEV/HomePageCompanion/cmd/hpc").CombinedOutput(); err != nil {
		t.Fatalf("build hpc: %v\n%s", err, out)
	}
	return hpc
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// cleanup takes the project down after the test, with its logs on failure.
func cleanup(t *testing.T, dir string) {
	t.Cleanup(func() {
		if t.Failed() {
			cmd := exec.Command("docker", "compose", "-f", "docker-compose.yml", "-f", "compose.generated.yml", "logs", "--no-color", "--tail", "80")
			cmd.Dir = dir
			out, _ := cmd.CombinedOutput()
			t.Logf("logs:\n%s", out)
		}
		down := exec.Command("docker", "compose", "-f", "docker-compose.yml", "-f", "compose.generated.yml", "down", "-v", "--remove-orphans")
		down.Dir = dir
		down.Run()
		// The companion wrote data/ as root.
		exec.Command("docker", "run", "--rm", "-v", dir+":/d", "alpine:3.20", "rm", "-rf", "/d/data").Run()
		os.RemoveAll(dir)
	})
}

type admin func(method, path string, body ...string) (int, []byte)

func adminClient(port int) admin {
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	return func(method, path string, body ...string) (int, []byte) {
		var r io.Reader
		if len(body) > 0 {
			r = strings.NewReader(body[0])
		}
		req, _ := http.NewRequest(method, base+path, r)
		req.Header.Set("Authorization", "ApiKey compose-admin-key")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, []byte(err.Error())
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
}

func waitReady(t *testing.T, call admin, id string) {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for {
		code, b := call("GET", "/api/core/instances/"+id)
		var v struct {
			Running bool   `json:"running"`
			Error   string `json:"error"`
		}
		json.Unmarshal(b, &v)
		if code == 200 && v.Running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s not ready: %d %s", id, code, b)
		}
		time.Sleep(time.Second)
	}
}

// The gate of Stage 3 in containers: the Lemmy service, Python, built and
// started by hpc apply; autoupload posts through it to a fake Lemmy, which
// the service reaches through the egress proxy, as it reaches the feed's
// images.
func TestComposeLemmy(t *testing.T) {
	image := os.Getenv("COMPANION_IMAGE")
	if image == "" {
		t.Skip("COMPANION_IMAGE names the companion image to test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("no Docker")
	}
	root, _ := filepath.Abs("..")
	dir, err := os.MkdirTemp("", "companion-lemmy-")
	if err != nil {
		t.Fatal(err)
	}
	buildProbe(t, dir)
	hpc := buildHPC(t, t.TempDir())
	port, lemmyPort := freePort(t), freePort(t)
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(fmt.Sprintf(`services:
  companion:
    image: %s
    ports: ["127.0.0.1:%d:8080"]
    volumes: ["./data:/app/data"]
    environment:
      COMPANION_SECRET_KEY: Y29tcG9zZS10ZXN0LWtleS0zMi1ieXRlcy1sb25nISE=
  lemmy-fake:
    image: companion-probe:test
    ports: ["127.0.0.1:%d:8080"]
    environment: { PROBE_MODE: lemmy, PROBE_BASE: "http://lemmy-fake:8080" }
  feed:
    image: companion-probe:test
    environment: { PROBE_MODE: feed, PROBE_BASE: "http://feed:8080" }
`, image, port, lemmyPort)), 0o644)
	os.WriteFile(filepath.Join(dir, "data", "companion.yaml"), []byte(fmt.Sprintf(`core:
  domain: example.com
  adminKey: compose-admin-key
  publicUrl: http://127.0.0.1:%d
modules:
  db.sqlite: {}
  blob.fs: {}
  media: {}
  rss:
    instances:
      photos: { url: "http://feed:8080/gallery.xml" }
  autoupload:
    instances:
      photos-lemmy: { source: rss:photos, target: lemmy:main, media: media, cron: "0 0 18 * * *" }
services:
  lemmy:
    from: path:%s
    dockerfile: services/lemmy/Dockerfile
    grants: { egress: ["lemmy-fake:8080", "feed:8080"] }
    instances:
      main: { server: "http://lemmy-fake:8080", username: bot, password: bot-password, community: photography }
`, port, root)), 0o644)
	cleanup(t, dir)
	out := run(t, dir, hpc, "apply", "--yes")
	t.Logf("hpc apply:\n%s", out)
	if !strings.Contains(out, "offers    social.publisher/v1") || !strings.Contains(out, "gets      egress lemmy-fake:8080 · egress feed:8080") {
		t.Errorf("hpc apply did not show the rights:\n%s", out)
	}
	call := adminClient(port)
	waitReady(t, call, "lemmy:main")

	code, body := call("POST", "/api/core/jobs/run", `{"key":"autoupload:photos-lemmy/publish","wait":true}`)
	if code != http.StatusAccepted || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("publish run: %d %s", code, body)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/_posts", lemmyPort))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var posts []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	json.NewDecoder(resp.Body).Decode(&posts)
	if len(posts) != 1 || !strings.HasPrefix(posts[0].Name, "Lake at dawn") || !strings.HasPrefix(posts[0].URL, "http://lemmy-fake:8080/pictrs/image/") {
		t.Fatalf("the fake Lemmy got %+v", posts)
	}
	_, items := call("GET", "/api/b/autoupload/items")
	if !strings.Contains(string(items), "http://lemmy-fake:8080/post/1") {
		t.Fatalf("autoupload did not record the post: %s", items)
	}
	// Its UI, through the shell's token.
	_, tok := call("POST", "/api/core/services/lemmy/ui-token")
	var ui struct{ Base string }
	json.Unmarshal(tok, &ui)
	code, page := call("GET", ui.Base+"index.html")
	if code != 200 || !strings.Contains(string(page), "/ui/sdk/companion-ui.js") {
		t.Fatalf("its UI: %d %.200s", code, page)
	}
	if code, status := call("GET", "/api/b/lemmy/status"); code != 200 || !strings.Contains(string(status), "bot@lemmy-fake:8080") {
		t.Fatalf("its API: %d %s", code, status)
	}
}
