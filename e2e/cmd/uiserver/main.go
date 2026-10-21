// Command uiserver runs a companion with every module on a copy of the e2e
// fixture (testdata/legacy, migrated at start), the built admin shell and
// the built module UIs: for the UI tests, and for looking at the admin UI
// by hand. The admin key is the fixture's, "fixture-admin-key".
//
//	cd web && npm run build && npm run build:modules
//	cd ../e2e && go run ./cmd/uiserver -listen 127.0.0.1:18090
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
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18090", "address to serve on")
	fixture := flag.String("fixture", "testdata/legacy", "the old installation to migrate")
	shell := flag.String("shell", "../web/build", "the built admin shell")
	ui := flag.String("ui", "../web/build-modules", "the built module UIs")
	flag.Parse()

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

	entries, err := os.ReadDir(*ui)
	if err != nil {
		log.Printf("no module UIs in %s: %v", *ui, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			companion.AttachUI(strings.ReplaceAll(e.Name(), "__", "/"), os.DirFS(filepath.Join(*ui, e.Name())))
		}
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
