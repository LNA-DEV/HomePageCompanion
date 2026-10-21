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
	probe := filepath.Join(dir, "probe")
	os.MkdirAll(probe, 0o755)
	build := exec.Command("go", "build", "-o", filepath.Join(probe, "probe"), "./cmd/probe")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, out)
	}
	os.WriteFile(filepath.Join(probe, "Dockerfile"), []byte("FROM gcr.io/distroless/static-debian12:nonroot\nCOPY probe /probe\nENTRYPOINT [\"/probe\"]\n"), 0o644)
	run(t, probe, "docker", "build", "--quiet", "-t", "companion-probe:test", ".")
	hpc := filepath.Join(bin, "hpc")
	if out, err := exec.Command("go", "build", "-o", hpc, "github.com/LNA-DEV/HomePageCompanion/cmd/hpc").CombinedOutput(); err != nil {
		t.Fatalf("build hpc: %v\n%s", err, out)
	}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
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
	out := run(t, dir, hpc, "apply", "--yes")
	t.Logf("hpc apply:\n%s", out)
	for _, want := range []string{"asks for  volume", "gets      egress allowed:8080 · volume", "new service"} {
		if !strings.Contains(out, want) {
			t.Errorf("hpc apply did not show %q", want)
		}
	}

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	admin := func(method, path string) (int, []byte) {
		req, _ := http.NewRequest(method, base+path, nil)
		req.Header.Set("Authorization", "ApiKey compose-admin-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, []byte(err.Error())
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	waitReady := func() {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for {
			code, b := admin("GET", "/api/core/instances/probe:main")
			var v struct {
				Running bool   `json:"running"`
				Error   string `json:"error"`
			}
			json.Unmarshal(b, &v)
			if code == 200 && v.Running {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("probe not ready: %d %s", code, b)
			}
			time.Sleep(time.Second)
		}
	}
	waitReady()
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
