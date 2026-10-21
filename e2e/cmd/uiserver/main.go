// Command uiserver runs a companion with every module, the built admin
// shell and the built module UIs: for the UI tests, and for looking at the
// admin UI by hand. By default it serves a copy of the e2e fixture
// (testdata/legacy, migrated at start; admin key "fixture-admin-key"); with
// -world, the seeded integration setup with its fake platforms (package
// world; admin key "world-admin-key"), which the Playwright tests use.
//
//	cd web && npm run build && npm run build:modules
//	cd ../e2e && go run ./cmd/uiserver -listen 127.0.0.1:18090 [-world]
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/core"
	_ "github.com/LNA-DEV/HomePageCompanion/e2e"
	"github.com/LNA-DEV/HomePageCompanion/e2e/world"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18090", "address to serve on")
	fixture := flag.String("fixture", "testdata/legacy", "the old installation to migrate")
	shell := flag.String("shell", "../web/build", "the built admin shell")
	ui := flag.String("ui", "../web/build-modules", "the built module UIs")
	useWorld := flag.Bool("world", false, "serve the seeded integration setup instead of the fixture")
	flag.Parse()
	attachUI(*ui)
	if *useWorld {
		serveWorld(*listen, *shell)
		return
	}

	dir, err := os.MkdirTemp("", "companion-ui-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	data := filepath.Join(dir, "data")
	if err := os.CopyFS(data, os.DirFS(*fixture)); err != nil {
		log.Fatal(err)
	}
	os.Setenv("FIXTURE_BUCKET", filepath.Join(data, "bucket"))
	os.Setenv("FIXTURE_GBIF", "http://127.0.0.1:9/density")
	cfg := filepath.Join(dir, "companion.yaml")
	opts := core.Options{ConfigPath: cfg, DataDir: data}
	if err := core.Migrate(context.Background(), opts, core.MigrateOptions{From: data, Out: os.Stderr}); err != nil {
		log.Fatal(err)
	}

	if _, err := fs.Stat(os.DirFS(*shell), "index.html"); err == nil {
		opts.Shell = os.DirFS(*shell)
	} else {
		log.Printf("no shell in %s, serving the placeholder", *shell)
	}

	c := core.New(opts)
	if err := c.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: *listen, Handler: c.Handler()}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		srv.Shutdown(context.Background())
	}()
	fmt.Printf("serving the admin UI on http://%s (key fixture-admin-key)\n", *listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	c.Stop(context.Background())
}

func attachUI(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("no module UIs in %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			companion.AttachUI(strings.ReplaceAll(e.Name(), "__", "/"), os.DirFS(filepath.Join(dir, e.Name())))
		}
	}
}

// serveWorld runs the integration setup until SIGTERM.
func serveWorld(listen, shell string) {
	w, err := world.Start(world.Options{Listen: listen, Seed: true, Logs: os.Stderr, Core: func(o *core.Options) {
		if _, err := fs.Stat(os.DirFS(shell), "index.html"); err == nil {
			o.Shell = os.DirFS(shell)
		} else {
			log.Printf("no shell in %s, serving the placeholder", shell)
		}
	}})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("serving the integration setup on %s (key %s)\n", w.URL, world.AdminKey)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	w.Close()
}
