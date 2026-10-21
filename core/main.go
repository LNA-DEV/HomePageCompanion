package core

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/core/internal/conf"
	"github.com/LNA-DEV/HomePageCompanion/core/internal/logger"
)

// shell is the admin shell a generated main attaches.
var shell fs.FS

// SetShell attaches the built admin shell; a hpc build calls it.
func SetShell(files fs.FS) { shell = files }

const usage = `usage: companion [flags] [command]

commands:
  serve                     run the companion (default)
  backup [-o file]          write the whole state to one file
         [--include-env] [--include-blobs] [--module id]
  restore <file>            bring a backup back (stop the companion first)
         [--module id] [--config]
  migrate [--check]         move a pre-module installation over
         [--from data] [--legacy-config data/config.yaml]
  secrets rotate            re-encrypt the secret store
         --new KEY (the old key is COMPANION_SECRET_KEY)
  config check              read companion.yaml and check every connection
  modules                   list the compiled-in building blocks
  health                    exit 0 when the running companion answers /health
  version

flags (or COMPANION_CONFIG, COMPANION_DATA):
  -config companion.yaml
  -data   data
`

// Main is the companion's entry point; a build's main package calls it.
func Main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fl := flag.NewFlagSet("companion", flag.ContinueOnError)
	fl.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	cfg := fl.String("config", envOr("COMPANION_CONFIG", "companion.yaml"), "")
	data := fl.String("data", envOr("COMPANION_DATA", "data"), "")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	args = fl.Args()
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	opts := Options{ConfigPath: *cfg, DataDir: *data, Shell: shell}
	ctx := context.Background()
	var err error
	switch cmd {
	case "serve":
		err = serve(ctx, opts)
	case "backup":
		err = backupCmd(ctx, opts, args)
	case "restore":
		err = restoreCmd(ctx, opts, args)
	case "migrate":
		err = migrateCmd(ctx, opts, args)
	case "secrets":
		err = secretsCmd(ctx, opts, args)
	case "config":
		err = configCmd(opts, args)
	case "modules":
		for _, i := range companion.Registered() {
			var caps []string
			for _, p := range i.Provides {
				caps = append(caps, p.String())
			}
			fmt.Printf("%-14s %s\n", i.ID, strings.Join(caps, ", "))
		}
	case "version":
		fmt.Println(buildVersion())
	case "health":
		err = healthCmd(opts)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "companion:", err)
		return 1
	}
	return 0
}

// healthCmd asks the companion on this machine for /health: the container's
// health check, which has no curl.
func healthCmd(opts Options) error {
	listen := opts.Listen
	if listen == "" {
		if f, _, err := conf.Load(opts.ConfigPath); err == nil {
			listen = f.Core.Listen
		}
	}
	if listen == "" {
		listen = ":8080"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/health")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/health: HTTP %d", resp.StatusCode)
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func serve(ctx context.Context, opts Options) error {
	// Tee log output into daily files (app-*.log, access-*.log) as well as
	// the original stdout/stderr.
	if err := logger.Init(filepath.Join(opts.DataDir, "logs")); err != nil {
		log.Printf("logger init failed: %v", err)
	}
	log.Print("Started companion")
	c := New(opts)
	if err := c.Start(ctx); err != nil {
		return err
	}
	listen := opts.Listen
	if listen == "" {
		listen = c.cfg().Core.Listen
	}
	if listen == "" {
		listen = ":8080"
	}
	srv := &http.Server{Addr: listen, Handler: c, ReadHeaderTimeout: 30 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	c.log.Printf("listening on %s", listen)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	select {
	case err := <-errc:
		c.Stop(context.Background())
		return err
	case s := <-sig:
		c.log.Printf("%s: stopping", s)
	}
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	c.Stop(sctx)
	return nil
}

func backupCmd(ctx context.Context, opts Options, args []string) error {
	fl := flag.NewFlagSet("backup", flag.ContinueOnError)
	out := fl.String("o", "", "output file (default backup-<time>.tar.gz)")
	env := fl.Bool("include-env", false, "")
	blobs := fl.Bool("include-blobs", false, "")
	module := fl.String("module", "", "")
	if err := fl.Parse(args); err != nil {
		return err
	}
	c := New(opts)
	if err := c.OpenStorage(ctx); err != nil {
		return err
	}
	defer c.Stop(ctx)
	name := *out
	if name == "" {
		name = "backup-" + time.Now().UTC().Format("20060102-150405") + ".tar.gz"
	}
	var w io.Writer
	if name == "-" {
		w = os.Stdout
	} else {
		f, err := os.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	m, err := c.Backup(ctx, w, BackupOptions{IncludeEnv: *env, IncludeBlobs: *blobs, Module: *module})
	if err != nil {
		return err
	}
	if name != "-" {
		fmt.Fprintf(os.Stderr, "wrote %s: %d database area(s), blobs: %v, config: %s\n", name, len(m.Areas), m.Blobs, strings.Join(m.Config, ", "))
	}
	return nil
}

func restoreCmd(ctx context.Context, opts Options, args []string) error {
	fl := flag.NewFlagSet("restore", flag.ContinueOnError)
	module := fl.String("module", "", "")
	withConfig := fl.Bool("config", false, "")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fl.NArg() != 1 {
		return errors.New("restore: name the backup file")
	}
	f, err := os.Open(fl.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()
	c := New(opts)
	if err := c.OpenStorage(ctx); err != nil {
		return err
	}
	defer c.Stop(ctx)
	return c.Restore(ctx, f, RestoreOptions{Module: *module, Config: *withConfig})
}

func secretsCmd(ctx context.Context, opts Options, args []string) error {
	if len(args) == 0 || args[0] != "rotate" {
		return errors.New("secrets: only \"rotate\" is supported")
	}
	fl := flag.NewFlagSet("rotate", flag.ContinueOnError)
	newKey := fl.String("new", "", "the new key, base64 of 32 bytes")
	oldKey := fl.String("old", "", "the old key (default COMPANION_SECRET_KEY, else data/secret.key)")
	if err := fl.Parse(args[1:]); err != nil {
		return err
	}
	if *newKey == "" {
		return errors.New("secrets rotate: --new is required (openssl rand -base64 32)")
	}
	old := *oldKey
	if old == "" {
		old = os.Getenv(SecretKeyEnv)
	}
	if old == "" {
		b, err := os.ReadFile(filepath.Join(opts.DataDir, secretKeyFile))
		if err != nil {
			return errors.New("secrets rotate: no old key (set --old or COMPANION_SECRET_KEY)")
		}
		old = string(b)
	}
	c := New(opts)
	if err := c.OpenStorage(ctx); err != nil {
		return err
	}
	defer c.Stop(ctx)
	n, err := c.secrets.rotate(old, *newKey)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "re-encrypted %d secret(s). Set %s to the new key now.\n", n, SecretKeyEnv)
	return nil
}

func configCmd(opts Options, args []string) error {
	if len(args) == 0 || args[0] != "check" {
		return errors.New("config: only \"check\" is supported")
	}
	f, _, err := conf.Load(opts.ConfigPath, filepath.Join(opts.DataDir, ".env"), ".env")
	if err != nil {
		return err
	}
	c := New(opts)
	lock, _, err := c.services.readLock()
	if err != nil {
		return err
	}
	ps, err := c.plan(f, lock)
	if err != nil {
		return err
	}
	var ids []string
	for _, p := range ps {
		ids = append(ids, p.id)
	}
	sort.Strings(ids)
	fmt.Printf("%d instance(s): %s\n", len(ids), strings.Join(ids, ", "))
	return nil
}
