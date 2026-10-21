package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeBuild(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "build.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadBuildFile(t *testing.T) {
	p := writeBuild(t, `core: v1.4.0
modules:
  - github.com/LNA-DEV/HomePageCompanion/modules/db-sqlite
  - github.com/LNA-DEV/HomePageCompanion/modules/mastodon@v1.3.0
  - github.com/someone/hpc-pixelfed-stories@v0.2.0
  - example.com/trips => ./modules/trips
`)
	bf, mods, err := ReadBuildFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if bf.Core != "v1.4.0" || len(mods) != 4 {
		t.Fatalf("%+v %+v", bf, mods)
	}
	if mods[1].Version != "v1.3.0" || mods[3].Path != "example.com/trips" || mods[3].Dir != filepath.Join(filepath.Dir(p), "modules/trips") {
		t.Fatalf("mods %+v", mods)
	}
	for name, body := range map[string]string{
		"no core":    "modules: [a]\n",
		"no modules": "core: v1\n",
		"twice":      "core: v1\nmodules: [a, a@v2]\n",
		"bad local":  "core: v1\nmodules: [\"a =>\"]\n",
		"space":      "core: v1\nmodules: [\"a b\"]\n",
	} {
		if _, _, err := ReadBuildFile(writeBuild(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPlanForARemoteCore(t *testing.T) {
	_, mods, _ := ReadBuildFile(writeBuild(t, `core: v1.4.0
modules:
  - github.com/LNA-DEV/HomePageCompanion/modules/db-sqlite
  - github.com/LNA-DEV/HomePageCompanion/modules/mastodon@v1.3.0
  - github.com/someone/hpc-pixelfed-stories
  - example.com/trips => ./trips
`))
	p := MakePlan(&BuildFile{Core: "v1.4.0"}, mods, "1.26.0")
	want := []string{corePath + "@v1.4.0", corePath + "/modules/db-sqlite@v1.4.0", corePath + "/modules/mastodon@v1.3.0", "github.com/someone/hpc-pixelfed-stories@latest"}
	if strings.Join(p.Get, " ") != strings.Join(want, " ") {
		t.Fatalf("get %v", p.Get)
	}
	gm := string(p.GoMod)
	if !strings.Contains(gm, "example.com/trips => ") || strings.Contains(gm, corePath+" => ") || !strings.Contains(gm, "go 1.26.0") {
		t.Fatalf("go.mod\n%s", gm)
	}
	mg := string(p.MainGo)
	for _, imp := range []string{`_ "example.com/trips"`, `_ "github.com/someone/hpc-pixelfed-stories"`, "core.Main()", "//go:embed all:shell"} {
		if !strings.Contains(mg, imp) {
			t.Errorf("main.go has no %s\n%s", imp, mg)
		}
	}
}

func TestPlanForALocalCore(t *testing.T) {
	_, mods, _ := ReadBuildFile(writeBuild(t, "core: /src\nmodules:\n  - github.com/LNA-DEV/HomePageCompanion/modules/rss\n  - github.com/x/y@v1.0.0\n"))
	p := MakePlan(&BuildFile{Core: "/src"}, mods, "1.26.0")
	gm := string(p.GoMod)
	for _, want := range []string{corePath + " => /src", corePath + "/modules/rss => /src/modules/rss"} {
		if !strings.Contains(gm, want) {
			t.Errorf("go.mod has no %q\n%s", want, gm)
		}
	}
	if len(p.Get) != 1 || p.Get[0] != "github.com/x/y@v1.0.0" {
		t.Fatalf("get %v", p.Get)
	}
}

// TestBuildsAndRunsACompanion builds a small companion from this
// repository and starts it.
func TestBuildsAndRunsACompanion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bp := filepath.Join(dir, "build.yaml")
	os.WriteFile(bp, []byte("core: "+root+"\nmodules:\n  - github.com/LNA-DEV/HomePageCompanion/modules/db-sqlite\n  - github.com/LNA-DEV/HomePageCompanion/modules/blob-fs\n  - github.com/LNA-DEV/HomePageCompanion/modules/webmention\n"), 0o644)
	shell := filepath.Join(dir, "shell")
	os.MkdirAll(shell, 0o755)
	os.WriteFile(filepath.Join(shell, "index.html"), []byte("<!doctype html><title>shell</title>"), 0o644)
	bin := filepath.Join(dir, "companion")
	var log bytes.Buffer
	if err := build(BuildOptions{File: bp, Out: bin, Shell: shell, Version: "9.9.9", Log: &log}); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}

	out, err := exec.Command(bin, "modules").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "webmention") || !strings.Contains(string(out), "db.sqlite") {
		t.Fatalf("modules: %v\n%s", err, out)
	}
	if out, _ := exec.Command(bin, "version").CombinedOutput(); !strings.Contains(string(out), "9.9.9") {
		t.Fatalf("version: %s", out)
	}

	cfg := filepath.Join(dir, "companion.yaml")
	os.WriteFile(cfg, []byte("core: { adminKey: k, listen: \"127.0.0.1:18089\" }\nmodules:\n  webmention: {}\n"), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-config", cfg, "-data", filepath.Join(dir, "data"))
	var run bytes.Buffer
	cmd.Stdout, cmd.Stderr = &run, &run
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		cmd.Wait()
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get("http://127.0.0.1:18089/")
		if err == nil {
			var b bytes.Buffer
			b.ReadFrom(resp.Body)
			resp.Body.Close()
			if !strings.Contains(b.String(), "<title>shell</title>") {
				t.Fatalf("the shell is not served: %s", b.String())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not up: %v\n%s", err, run.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	resp, err := http.Post("http://127.0.0.1:18089/api/webmention", "application/x-www-form-urlencoded",
		strings.NewReader("source=https://a.example/x&target=https://b.example/"))
	if err != nil || resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webmention: %v %v", err, resp)
	}
}
