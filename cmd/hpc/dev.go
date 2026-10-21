package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
	"gopkg.in/yaml.v3"
)

// devCmd runs the companion of build.yaml for development (§5.11): it is
// rebuilt and restarted when the core or a local module changes, its Go
// code or its UI, and the shell runs in Vite's dev server with hot reload
// in front of it. A service runs from its source outside Docker, at the
// url: of its block, and restarts when its source changes; its manifest
// goes into companion.lock as it changes.
func devCmd(args []string) error {
	fl := flag.NewFlagSet("dev", flag.ContinueOnError)
	file := fl.String("f", "build.yaml", "the build file")
	config := fl.String("c", "companion.yaml", "the companion's config")
	data := fl.String("d", "data", "the companion's data directory")
	noUI := fl.Bool("no-ui", false, "no Vite dev server for the shell")
	uiPort := fl.Int("ui-port", 5173, "the Vite dev server's port")
	interval := fl.Duration("interval", time.Second, "how often to look for changes")
	var services serviceFlags
	fl.Var(&services, "service", `a service to run from its source, name="command"; its block has a url: (repeatable)`)
	if err := fl.Parse(args); err != nil {
		return err
	}
	bf, mods, err := ReadBuildFile(*file)
	if err != nil {
		return err
	}
	work, err := filepath.Abs(filepath.Join(".hpc", "dev"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Service pages load the UI SDK from the companion; it is all of the
	// shell the dev companion embeds.
	shell := ""
	if isLocal(bf.Core) {
		if shell, err = buildUISDK(filepath.Join(bf.Core, "web"), work); err != nil {
			fmt.Fprintf(os.Stderr, "hpc dev: no UI SDK for service pages: %v\n", err)
		}
	}
	if !*noUI {
		if !isLocal(bf.Core) {
			return errors.New("the shell's dev server needs a local core (core: ./ in build.yaml); or pass --no-ui")
		}
		stop, err := startVite(filepath.Join(bf.Core, "web"), *uiPort, "http://"+localAddr(listenOf(*config)))
		if err != nil {
			return err
		}
		defer stop()
	}
	dataDir, err := filepath.Abs(*data)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	if len(services) > 0 {
		cfg, err := readApplyConfig(*config)
		if err != nil {
			return err
		}
		for _, sv := range services {
			name, command, _ := strings.Cut(sv, "=")
			d, err := devService(cfg, *config, dataDir, work, name, command)
			if err != nil {
				return err
			}
			d.interval = *interval
			wg.Add(1)
			go func() { defer wg.Done(); d.run(ctx) }()
		}
	}
	d := &devLoop{
		roots:    watchRoots(bf, mods),
		exclude:  []string{dataDir, work},
		skip:     skipDir,
		interval: *interval,
		log:      os.Stderr,
		build:    devBuild(*file, work, shell, os.Stderr),
		start: func(bin string) (func(), error) {
			return startProgram(bin, os.Stdout, os.Stderr, "-config", *config, "-data", *data, "serve")
		},
	}
	return d.run(ctx)
}

// serviceFlags are the --service flags.
type serviceFlags []string

func (s *serviceFlags) String() string { return strings.Join(*s, " ") }
func (s *serviceFlags) Set(v string) error {
	if name, command, ok := strings.Cut(v, "="); !ok || name == "" || command == "" {
		return errors.New(`want name="command"`)
	}
	*s = append(*s, v)
	return nil
}

// devService runs a service's command next to the dev companion, as the
// companion would reach it at its url:, and restarts it when its source
// (from: path:<dir>, else this directory) changes.
func devService(cfg *applyConfig, config, data, work, name, command string) (*devLoop, error) {
	s, ok := cfg.Services[name]
	if !ok || s.URL == "" {
		return nil, fmt.Errorf("--service %s: give services.%s a url: in %s, where the companion reaches it", name, name, config)
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Port() == "" {
		return nil, fmt.Errorf("services.%s.url %q: want http://localhost:<port>", name, s.URL)
	}
	root := "."
	if dir, ok := strings.CutPrefix(s.From, "path:"); ok {
		root = dir
	}
	if root, err = filepath.Abs(root); err != nil {
		return nil, err
	}
	svcData := filepath.Join(work, "services", name)
	if err := os.MkdirAll(svcData, 0o755); err != nil {
		return nil, err
	}
	env := []string{
		"PORT=" + u.Port(),
		"COMPANION_URL=http://" + localAddr(firstNonEmpty(cfg.Core.ServicesListen, ":3128")),
		"COMPANION_TOKEN_FILE=" + filepath.Join(data, "services", name, "run", "token"),
		"COMPANION_DATA=" + svcData,
	}
	lock := filepath.Join(filepath.Dir(config), wire.LockFile)
	log := prefixed{name + ": ", os.Stderr}
	return &devLoop{
		name:    name,
		roots:   []string{root},
		exclude: []string{data, work},
		skip:    skipCommon,
		log:     os.Stderr,
		build:   func(bool) (string, error) { return command, nil },
		start: func(command string) (func(), error) {
			stop, err := startShell(command, env, log)
			if err != nil {
				return nil, err
			}
			if err := syncLock(lock, name, s, os.Stderr); err != nil {
				fmt.Fprintf(os.Stderr, "hpc dev: %s: %v\n", name, err)
			}
			return stop, nil
		},
	}, nil
}

// syncLock records the service's manifest and grants in companion.lock
// once it answers, when they changed; the companion picks it up.
func syncLock(path, name string, s applyService, log io.Writer) error {
	deadline := time.Now().Add(30 * time.Second)
	var m *wire.Manifest
	for {
		var err error
		if m, err = fetchManifest(s.URL); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no manifest at %s: %w", s.URL, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := m.Check(); err != nil {
		return err
	}
	l, _, err := wire.ReadLock(path)
	if err != nil {
		return err
	}
	want := wire.LockedService{From: firstNonEmpty(s.From, "dev"), URL: s.URL, Manifest: *m, Grants: s.grants()}
	if old, ok := l.Services[name]; ok && sameJSON(old.Manifest, want.Manifest) && sameJSON(old.Grants, want.Grants) && old.URL == want.URL {
		return nil
	}
	want.AppliedAt = time.Now().UTC()
	l.Services[name] = want
	if err := l.Write(path); err != nil {
		return err
	}
	fmt.Fprintf(log, "hpc dev: %s: %s updated; it gets %s\n", name, path, rightsText(want.Grants))
	return nil
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// startShell runs a command line in a shell, with env added.
func startShell(command string, env []string, out io.Writer) (func(), error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 5 * time.Second
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	return func() {
		stopGroup(cmd, false)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			stopGroup(cmd, true)
			<-done
		}
	}, nil
}

// prefixed writes lines with a prefix.
type prefixed struct {
	prefix string
	w      io.Writer
}

func (p prefixed) Write(b []byte) (int, error) {
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if line != "" {
			io.WriteString(p.w, p.prefix+line)
		}
	}
	return len(b), nil
}

// devLoop rebuilds and restarts a program when its sources change.
type devLoop struct {
	name  string // in its log lines; "" for the companion
	roots []string
	// exclude are directories never looked at (the data); skip leaves out
	// directories by name.
	exclude  []string
	skip     func(root, p, name string) bool
	interval time.Duration
	// build builds the program, its UIs too when ui is set; it returns
	// the program's path.
	build func(ui bool) (string, error)
	start func(bin string) (stop func(), err error)
	log   io.Writer
}

func (d *devLoop) run(ctx context.Context) error {
	prefix := "hpc dev: "
	if d.name != "" {
		prefix += d.name + ": "
	}
	stop := func() {}
	defer func() { stop() }()
	code, ui := d.scan()
	needUI := true
	rebuild := func() {
		bin, err := d.build(needUI)
		if err != nil {
			fmt.Fprintf(d.log, "%sthe build failed; waiting for a change: %v\n", prefix, err)
			return
		}
		needUI = false
		stop()
		s, err := d.start(bin)
		if err != nil {
			fmt.Fprintf(d.log, "%sstart: %v\n", prefix, err)
			stop = func() {}
			return
		}
		stop = s
	}
	rebuild()
	fmt.Fprintf(d.log, "%swatching %s\n", prefix, strings.Join(d.roots, ", "))
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(d.interval):
		}
		c, u := d.scan()
		if c == code && u == ui {
			continue
		}
		// Wait for the editor to finish writing.
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(d.interval / 4):
			}
			c2, u2 := d.scan()
			if c2 == c && u2 == u {
				break
			}
			c, u = c2, u2
		}
		if u != ui {
			needUI = true
		}
		code, ui = c, u
		fmt.Fprintf(d.log, "%schanged, restarting\n", prefix)
		rebuild()
	}
}

// scan sums up the files under the roots: the module UIs' (under a ui
// directory) and the rest.
func (d *devLoop) scan() (code, ui uint64) {
	hc, hu := fnv.New64a(), fnv.New64a()
	for _, root := range d.roots {
		filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := e.Name()
			if e.IsDir() {
				if p != root && (d.skip(root, p, name) || slices.Contains(d.exclude, p)) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") || strings.HasSuffix(name, ".swp") {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return nil
			}
			h := hc
			if rel, _ := filepath.Rel(root, p); isUIPath(rel) {
				h = hu
			}
			fmt.Fprintf(h, "%s %d %d\n", p, info.Size(), info.ModTime().UnixNano())
			return nil
		})
	}
	return hc.Sum64(), hu.Sum64()
}

// skipCommon leaves out generated and installed files.
func skipCommon(_, _, name string) bool {
	switch name {
	case "node_modules", "dist", "build", "build-modules", ".svelte-kit", "data", "testdata", "__pycache__", "venv":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// skipDir leaves out what is not built into the companion: generated and
// installed files, and at the core's root what is not Go of the companion.
func skipDir(root, p, name string) bool {
	if skipCommon(root, p, name) {
		return true
	}
	if filepath.Dir(p) == root {
		switch name {
		case "web", "e2e", "docs", "bruno", "src", "sdk", "services", "scripts", "cmd":
			return true
		}
	}
	return false
}

func isUIPath(rel string) bool {
	for _, s := range strings.Split(filepath.ToSlash(rel), "/") {
		if s == "ui" {
			return true
		}
	}
	return false
}

// watchRoots are the local core and the local modules outside it.
func watchRoots(bf *BuildFile, mods []Module) []string {
	var roots []string
	if isLocal(bf.Core) {
		roots = append(roots, bf.Core)
	}
	for _, m := range mods {
		if m.Dir == "" {
			continue
		}
		inside := false
		for _, r := range roots {
			if rel, err := filepath.Rel(r, m.Dir); err == nil && !strings.HasPrefix(rel, "..") {
				inside = true
			}
		}
		if !inside {
			roots = append(roots, m.Dir)
		}
	}
	return roots
}

// devBuild builds into work, with the given shell or none (Vite serves
// it) and the module UIs of the last build unless they changed.
func devBuild(file, work, shell string, log io.Writer) func(ui bool) (string, error) {
	uiCache := filepath.Join(work, "ui")
	bin := filepath.Join(work, "companion")
	return func(ui bool) (string, error) {
		o := BuildOptions{File: file, Out: bin + ".next", Work: filepath.Join(work, "build"), Shell: shell, NoShell: shell == "", Log: log}
		if !ui {
			o.UI = uiCache
		}
		if err := build(o); err != nil {
			return "", err
		}
		if ui {
			os.RemoveAll(uiCache)
			if err := copyTree(filepath.Join(o.Work, "ui"), uiCache); err != nil {
				return "", err
			}
		}
		// A rename, as the running program's file may not be written.
		return bin, os.Rename(o.Out, bin)
	}
}

// startProgram starts a program; stop ends it as Docker would, SIGTERM
// and after 30 s SIGKILL.
func startProgram(bin string, stdout, stderr io.Writer, args ...string) (func(), error) {
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	return func() {
		stopGroup(cmd, false)
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			stopGroup(cmd, true)
			<-done
		}
	}, nil
}

// buildUISDK builds the UI SDK (web/vite.sdk.config.ts) into a shell
// directory of its own.
func buildUISDK(web, work string) (string, error) {
	if _, err := os.Stat(filepath.Join(web, "node_modules")); err != nil {
		if err := npmRun(web, os.Stderr, "ci"); err != nil {
			return "", err
		}
	}
	dir := filepath.Join(work, "shell")
	cmd := exec.Command("npx", "vite", "build", "-c", "vite.sdk.config.ts", "--outDir", filepath.Join(dir, "ui-sdk"), "--emptyOutDir", "--logLevel", "warn")
	cmd.Dir, cmd.Stdout, cmd.Stderr = web, os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return dir, nil
}

// startVite runs the shell's dev server in front of the companion.
func startVite(web string, port int, companion string) (func(), error) {
	if _, err := os.Stat(filepath.Join(web, "node_modules")); err != nil {
		if err := npmRun(web, os.Stderr, "ci"); err != nil {
			return nil, err
		}
	}
	cmd := exec.Command("npm", "run", "dev", "--", "--port", fmt.Sprint(port), "--strictPort")
	cmd.Dir, cmd.Stdout, cmd.Stderr = web, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "COMPANION_DEV_URL="+companion)
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "hpc dev: the admin UI, with hot reload, at http://localhost:%d (the companion at %s)\n", port, companion)
	return func() {
		stopGroup(cmd, false)
		cmd.Wait()
	}, nil
}

// listenOf is core.listen of a companion.yaml (":8080" by default).
func listenOf(config string) string {
	var f struct {
		Core struct {
			Listen string `yaml:"listen"`
		} `yaml:"core"`
	}
	if b, err := os.ReadFile(config); err == nil {
		yaml.Unmarshal(b, &f)
	}
	if f.Core.Listen == "" {
		return ":8080"
	}
	return f.Core.Listen
}

// localAddr is where a listen address is reached from this machine.
func localAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "localhost:8080"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}
