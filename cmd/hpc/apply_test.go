package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
	"gopkg.in/yaml.v3"
)

const fakeManifest = `{"id":"lemmy","name":"Lemmy","version":"1.0.0","provides":[{"capability":"social.publisher/v1"}],
"slots":[{"name":"media","needs":"media.image/v1","optional":true}],"requests":{"egress":["lemmy.world"],"volume":true}}`

// fakeDocker is a docker program that records its calls and answers
// what hpc apply asks.
func fakeDocker(t *testing.T, dir, user string) (bin, log string) {
	t.Helper()
	log = filepath.Join(dir, "docker.log")
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(fakeManifest), 0o644)
	script := `#!/bin/sh
echo "$@" >> ` + log + `
case "$*" in
  "image inspect --format {{json .RepoDigests}}"*) echo '["ghcr.io/x/lemmy@sha256:feed"]' ;;
  "image inspect --format {{.Id}}"*) echo 'sha256:0123456789abcdef0123' ;;
  "image inspect --format {{.Config.User}}"*) echo '` + user + `' ;;
  run*) cat ` + filepath.Join(dir, "manifest.json") + ` ;;
esac
exit 0
`
	bin = filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

type applySetup struct {
	dir, config, log string
	o                applyOptions
	out              *bytes.Buffer
}

func setupApply(t *testing.T, config, user string) *applySetup {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	cfg := filepath.Join(dir, "data", "companion.yaml")
	os.WriteFile(cfg, []byte(config), 0o600)
	os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services:\n  companion:\n    image: ghcr.io/lna-dev/homepagecompanion:latest\n"), 0o644)
	bin, log := fakeDocker(t, dir, user)
	out := &bytes.Buffer{}
	return &applySetup{dir: dir, config: cfg, log: log, out: out, o: applyOptions{
		config: cfg, compose: filepath.Join(dir, "docker-compose.yml"), companion: "companion", docker: bin, stdout: out, stdin: strings.NewReader(""),
	}}
}

func (s *applySetup) lock(t *testing.T) *wire.Lock {
	t.Helper()
	l, _, err := wire.ReadLock(filepath.Join(s.dir, "data", wire.LockFile))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

const lemmyConfig = `core: { servicesListen: ":3128" }
services:
  lemmy:
    from: oci://ghcr.io/x/lemmy:1
    grants: { egress: [lemmy.world] }
    limits: { memoryMB: 128 }
    env: { LEMMY_COMMUNITY: "photo$graphy" }
    instances:
      main: { server: https://lemmy.world }
`

func TestApplyWritesComposeAndLock(t *testing.T) {
	s := setupApply(t, lemmyConfig, "")
	s.o.yes = true
	if err := apply(s.o); err != nil {
		t.Fatalf("%v\n%s", err, s.out)
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, "compose.generated.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Services map[string]struct {
			Extends     map[string]string `yaml:"extends"`
			Image       string            `yaml:"image"`
			User        string            `yaml:"user"`
			Networks    yaml.Node         `yaml:"networks"`
			ReadOnly    bool              `yaml:"read_only"`
			CapDrop     []string          `yaml:"cap_drop"`
			SecurityOpt []string          `yaml:"security_opt"`
			MemLimit    string            `yaml:"mem_limit"`
			PidsLimit   int               `yaml:"pids_limit"`
			Environment map[string]string `yaml:"environment"`
			Volumes     []string          `yaml:"volumes"`
			Ports       []string          `yaml:"ports"`
		} `yaml:"services"`
		Networks map[string]map[string]any `yaml:"networks"`
	}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	if comp := c.Services["companion"]; comp.Image != "" || len(comp.Extends) != 0 {
		t.Fatalf("the companion is the operator's, the overlay only adds networks: %+v\n%s", comp, raw)
	}
	if !strings.Contains(string(raw), "svc-lemmy:\n        aliases:\n          - companion") {
		t.Fatalf("companion not in the service's network under its name:\n%s", raw)
	}
	l := c.Services["lemmy"]
	if l.Image != "ghcr.io/x/lemmy@sha256:feed" || l.User != nonRoot || !l.ReadOnly || l.CapDrop[0] != "ALL" ||
		l.SecurityOpt[0] != "no-new-privileges:true" || l.MemLimit != "128m" || l.PidsLimit != defaultPIDs || len(l.Ports) != 0 {
		t.Fatalf("lemmy container: %+v\n%s", l, raw)
	}
	if l.Environment["HTTPS_PROXY"] != "http://companion:3128" || l.Environment["COMPANION_URL"] != "http://companion:3128" ||
		l.Environment["NO_PROXY"] != "companion" || l.Environment["LEMMY_COMMUNITY"] != "photo$$graphy" {
		t.Fatalf("environment %v", l.Environment)
	}
	if strings.Join(l.Volumes, " ") != "./data/services/lemmy/run:/run/companion:ro ./data/services/lemmy/volume:/data" {
		t.Fatalf("volumes %v", l.Volumes)
	}
	if c.Networks["svc-lemmy"]["internal"] != true {
		t.Fatalf("the service's network is not internal: %v", c.Networks)
	}
	lock := s.lock(t)
	ls := lock.Services["lemmy"]
	if ls.Image != "ghcr.io/x/lemmy@sha256:feed" || ls.Manifest.Name != "Lemmy" || ls.Grants.Egress[0] != "lemmy.world" || !ls.Grants.Volume || ls.User != nonRoot {
		t.Fatalf("lock %+v", ls)
	}
	log, _ := os.ReadFile(s.log)
	for _, want := range []string{"pull ghcr.io/x/lemmy:1", "run --rm --network none -e COMPANION_MANIFEST=print ghcr.io/x/lemmy@sha256:feed", "compose -f " + filepath.Join(s.dir, "docker-compose.yml") + " -f " + filepath.Join(s.dir, "compose.generated.yml") + " up -d --remove-orphans"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("docker was not asked to %q:\n%s", want, log)
		}
	}
	for _, want := range []string{"offers    social.publisher/v1", "asks for  egress lemmy.world · volume", "gets      egress lemmy.world · volume", "new service"} {
		if !strings.Contains(s.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, s.out)
		}
	}
}

// Rights the lock has not confirmed need a yes; the same rights again do
// not.
func TestApplyConfirmsNewRights(t *testing.T) {
	s := setupApply(t, lemmyConfig, "10001")
	s.o.yes = true
	if err := apply(s.o); err != nil {
		t.Fatal(err)
	}
	if u := s.lock(t).Services["lemmy"].User; u != "10001" {
		t.Fatalf("the image's own user was replaced: %q", u)
	}
	s.o.yes = false
	if err := apply(s.o); err != nil {
		t.Fatalf("unchanged rights asked again: %v", err)
	}

	os.WriteFile(s.config, []byte(strings.Replace(lemmyConfig, "egress: [lemmy.world]", "egress: [lemmy.world, pictrs.example.com]", 1)), 0o600)
	if err := apply(s.o); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("new rights applied without a terminal: %v", err)
	}
	s.o.interactive, s.o.stdin = true, strings.NewReader("n\n")
	if err := apply(s.o); err == nil {
		t.Fatal("applied after no")
	}
	if g := s.lock(t).Services["lemmy"].Grants.Egress; len(g) != 1 {
		t.Fatalf("the lock changed after no: %v", g)
	}
	if !strings.Contains(s.out.String(), "new       egress to pictrs.example.com") {
		t.Fatalf("the new right is not shown:\n%s", s.out)
	}
	s.o.stdin = strings.NewReader("y\n")
	if err := apply(s.o); err != nil {
		t.Fatal(err)
	}
	if g := s.lock(t).Services["lemmy"].Grants.Egress; len(g) != 2 {
		t.Fatalf("lock after yes: %v", g)
	}
}

func TestApplyBuildsLocalSources(t *testing.T) {
	s := setupApply(t, `services:
  probe:
    from: path:services/probe
    dockerfile: build/Dockerfile
    grants: { volume: false }
    instances: { main: {} }
`, "")
	s.o.yes, s.o.noUp = true, true
	if err := apply(s.o); err != nil {
		t.Fatalf("%v\n%s", err, s.out)
	}
	log, _ := os.ReadFile(s.log)
	ctx := filepath.Join(s.dir, "services", "probe")
	if !strings.Contains(string(log), "build --quiet -t companion-svc-probe:build -f "+filepath.Join(ctx, "build/Dockerfile")+" "+ctx) {
		t.Fatalf("build:\n%s", log)
	}
	if strings.Contains(string(log), "compose") {
		t.Fatal("--no-up ran compose")
	}
	ls := s.lock(t).Services["probe"]
	if ls.Image != "companion-svc-probe:0123456789ab" || ls.Grants.Volume {
		t.Fatalf("lock %+v", ls)
	}
	raw, _ := os.ReadFile(filepath.Join(s.dir, "compose.generated.yml"))
	if strings.Contains(string(raw), ":/data") {
		t.Fatalf("a volume without the grant:\n%s", raw)
	}
}

func TestApplyServiceRunByHand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(wire.Manifest{ID: "dev", Provides: []wire.Offer{{Capability: "social.publisher/v1"}}})
	}))
	defer srv.Close()
	s := setupApply(t, "services:\n  dev:\n    url: "+srv.URL+"\n    instances: { main: {} }\n", "")
	s.o.yes = true
	if err := apply(s.o); err != nil {
		t.Fatal(err)
	}
	if ls := s.lock(t).Services["dev"]; ls.URL != srv.URL || ls.Manifest.ID != "dev" {
		t.Fatalf("lock %+v", ls)
	}
	raw, _ := os.ReadFile(filepath.Join(s.dir, "compose.generated.yml"))
	if strings.Contains(string(raw), "\n  dev:") {
		t.Fatalf("a container for a service run by hand:\n%s", raw)
	}
}

func TestApplyDryRun(t *testing.T) {
	s := setupApply(t, lemmyConfig, "")
	s.o.dryRun = true
	if err := apply(s.o); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"compose.generated.yml", "data/" + wire.LockFile} {
		if _, err := os.Stat(filepath.Join(s.dir, f)); err == nil {
			t.Errorf("dry run wrote %s", f)
		}
	}
}

func TestParseGitSource(t *testing.T) {
	repo, ref, sub, err := parseGitSource("git+https://github.com/LNA-DEV/HomePageCompanion@services/lemmy/v1.0.0#services/lemmy")
	if err != nil || repo != "https://github.com/LNA-DEV/HomePageCompanion" || ref != "services/lemmy/v1.0.0" || sub != "services/lemmy" {
		t.Fatalf("%q %q %q %v", repo, ref, sub, err)
	}
	if _, _, _, err := parseGitSource("git+https://github.com/x/y"); err == nil {
		t.Fatal("a source without a ref")
	}
	if _, _, _, err := parseGitSource("git+https://user@github.com/x/y"); err == nil {
		t.Fatal("the userinfo taken for a ref")
	}
}
