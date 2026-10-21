package main

import (
	"context"
	"errors"
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

	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
)

// fakeDev records the dev loop's builds and starts.
type fakeDev struct {
	mu      sync.Mutex
	builds  []bool // ui, per build
	running []string
	stopped []string
	fail    bool
}

func (f *fakeDev) build(ui bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds = append(f.builds, ui)
	if f.fail {
		return "", errors.New("does not compile")
	}
	return fmt.Sprintf("bin-%d", len(f.builds)), nil
}

func (f *fakeDev) start(bin string) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = append(f.running, bin)
	return func() { f.mu.Lock(); f.stopped = append(f.stopped, bin); f.mu.Unlock() }, nil
}

func (f *fakeDev) state() (builds []bool, running, stopped []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.builds...), append([]string(nil), f.running...), append([]string(nil), f.stopped...)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDevLoop(t *testing.T) {
	root := t.TempDir()
	write := func(name, s string) {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	write("mod.go", "package m")
	write("ui/src/entry.ts", "export {}")
	write("ui/node_modules/x/index.js", "")
	f := &fakeDev{}
	write("data/companion.db", "x")
	d := &devLoop{roots: []string{root}, exclude: []string{filepath.Join(root, "var")}, skip: skipDir, interval: 20 * time.Millisecond, build: f.build, start: f.start, log: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- d.run(ctx) }()

	// The first build has the UIs.
	waitFor(t, "the first start", func() bool { _, r, _ := f.state(); return len(r) == 1 })
	if b, _, _ := f.state(); !b[0] {
		t.Fatal("the first build is without the UIs")
	}
	// Go code: a build without the UIs, and a restart.
	write("mod.go", "package m // changed")
	waitFor(t, "the restart", func() bool { _, r, _ := f.state(); return len(r) == 2 })
	if b, _, s := f.state(); b[1] || len(s) != 1 || s[0] != "bin-1" {
		t.Fatalf("after a Go change: builds %v, stopped %v", b, s)
	}
	// A UI: with the UIs.
	write("ui/src/entry.ts", "export const x = 1")
	waitFor(t, "the UI rebuild", func() bool { _, r, _ := f.state(); return len(r) == 3 })
	if b, _, _ := f.state(); !b[2] {
		t.Fatal("a UI change built without the UIs")
	}
	// Installed files and the data are not watched.
	write("ui/node_modules/x/index.js", "changed")
	write("data/companion.db", "changed")
	write("var/log.txt", "changed")
	time.Sleep(100 * time.Millisecond)
	if b, _, _ := f.state(); len(b) != 3 {
		t.Fatalf("node_modules caused a build: %v", b)
	}
	// A failed build keeps the running program.
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	write("mod.go", "package m // broken")
	waitFor(t, "the failed build", func() bool { b, _, _ := f.state(); return len(b) == 4 })
	if _, r, s := f.state(); len(r) != 3 || len(s) != 2 {
		t.Fatalf("a failed build stopped the program: running %v, stopped %v", r, s)
	}
	// The next good one replaces it.
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	write("mod.go", "package m // fixed")
	waitFor(t, "the fixed build", func() bool { _, r, _ := f.state(); return len(r) == 4 })

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, _, s := f.state(); len(s) != 4 || s[3] != "bin-5" {
		t.Fatalf("not stopped at the end: %v", s)
	}
}

func TestDevWatchRoots(t *testing.T) {
	core := t.TempDir()
	other := t.TempDir()
	bf := &BuildFile{Core: core}
	roots := watchRoots(bf, []Module{
		{Path: corePath + "/modules/rss"},
		{Path: "example.com/inside", Dir: filepath.Join(core, "modules", "x")},
		{Path: "example.com/other", Dir: other},
		{Path: "example.com/remote", Version: "v1.0.0"},
	})
	if len(roots) != 2 || roots[0] != core || roots[1] != other {
		t.Fatalf("roots %v", roots)
	}
	if roots := watchRoots(&BuildFile{Core: "v1.2.0"}, nil); len(roots) != 0 {
		t.Fatalf("a remote core is watched: %v", roots)
	}
}

func TestDevSkips(t *testing.T) {
	root := "/repo"
	for _, c := range []struct {
		path string
		skip bool
	}{
		{"/repo/web", true},
		{"/repo/e2e", true},
		{"/repo/cmd", true},
		{"/repo/core", false},
		{"/repo/modules/rss", false},
		{"/repo/modules/rss/web", false},
		{"/repo/modules/rss/ui/node_modules", true},
		{"/repo/.git", true},
		{"/repo/modules/rss/testdata", true},
	} {
		if got := skipDir(root, c.path, filepath.Base(c.path)); got != c.skip {
			t.Errorf("%s: skip %v, want %v", c.path, got, c.skip)
		}
	}
	if !isUIPath("modules/rss/ui/src/entry.ts") || isUIPath("core/serviceui.go") {
		t.Error("isUIPath")
	}
}

func TestDevListen(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "companion.yaml")
	if l := listenOf(cfg); l != ":8080" {
		t.Fatalf("without a config: %s", l)
	}
	os.WriteFile(cfg, []byte("core:\n  listen: 127.0.0.1:9090\n"), 0o644)
	if l := listenOf(cfg); l != "127.0.0.1:9090" {
		t.Fatalf("listen %s", l)
	}
	for in, want := range map[string]string{":8080": "localhost:8080", "0.0.0.0:80": "localhost:80", "127.0.0.1:9090": "127.0.0.1:9090", "bad": "localhost:8080"} {
		if got := localAddr(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

// startProgram stops the program with SIGTERM.
func TestDevStartProgram(t *testing.T) {
	sh := filepath.Join(t.TempDir(), "prog")
	os.WriteFile(sh, []byte("#!/bin/sh\ntrap 'echo stopped; exit 0' TERM\necho started\nwhile true; do sleep 0.05; done\n"), 0o755)
	var out syncBuffer
	stop, err := startProgram(sh, &out, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start", func() bool { return out.String() == "started\n" })
	stop()
	if out.String() != "started\nstopped\n" {
		t.Fatalf("output %q", out.String())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = append(s.b, p...)
	return len(p), nil
}

func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return string(s.b) }

func TestDevServiceNeedsURL(t *testing.T) {
	cfg := &applyConfig{Services: map[string]applyService{"lemmy": {From: "path:."}}}
	if _, err := devService(cfg, "companion.yaml", t.TempDir(), t.TempDir(), "lemmy", "python3 lemmy.py"); err == nil || !strings.Contains(err.Error(), "give services.lemmy a url:") {
		t.Fatalf("%v", err)
	}
	if _, err := devService(cfg, "companion.yaml", t.TempDir(), t.TempDir(), "other", "x"); err == nil {
		t.Fatal("a service without a block")
	}
	var s serviceFlags
	if s.Set("lemmy") == nil || s.Set("=x") == nil || s.Set(`lemmy=python3 lemmy.py`) != nil || len(s) != 1 {
		t.Fatalf("flags %v", s)
	}
}

// The service's manifest goes into companion.lock when it changes.
func TestDevServiceLock(t *testing.T) {
	var mu sync.Mutex
	manifest := `{"id":"lemmy","provides":[{"capability":"social.publisher/v1"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		io.WriteString(w, manifest)
	}))
	defer srv.Close()
	lock := filepath.Join(t.TempDir(), "companion.lock")
	var s applyService
	s.URL = srv.URL
	s.Grants.Egress = []string{"lemmy.world"}
	var log strings.Builder
	if err := syncLock(lock, "lemmy", s, &log); err != nil {
		t.Fatal(err)
	}
	l, _, _ := wire.ReadLock(lock)
	got := l.Services["lemmy"]
	if got.URL != srv.URL || got.From != "dev" || len(got.Manifest.Provides) != 1 || !got.Grants.Volume || got.Grants.Egress[0] != "lemmy.world" {
		t.Fatalf("lock %+v", got)
	}
	if !strings.Contains(log.String(), "it gets egress lemmy.world · volume") {
		t.Fatalf("log %q", log.String())
	}
	// Unchanged: not written again.
	log.Reset()
	syncLock(lock, "lemmy", s, &log)
	if log.Len() != 0 {
		t.Fatalf("written again: %q", log.String())
	}
	mu.Lock()
	manifest = `{"id":"lemmy","provides":[{"capability":"social.publisher/v1"},{"capability":"social.interactions/v1"}]}`
	mu.Unlock()
	syncLock(lock, "lemmy", s, &log)
	if l, _, _ = wire.ReadLock(lock); len(l.Services["lemmy"].Manifest.Provides) != 2 {
		t.Fatalf("not updated: %+v", l.Services["lemmy"])
	}
}

func TestPrefixed(t *testing.T) {
	var b strings.Builder
	p := prefixed{"lemmy: ", &b}
	p.Write([]byte("one\ntwo\n"))
	if b.String() != "lemmy: one\nlemmy: two\n" {
		t.Fatalf("%q", b.String())
	}
}
