package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
	"github.com/LNA-DEV/HomePageCompanion/lib/envfile"
	"gopkg.in/yaml.v3"
)

// hpc apply starts the services of companion.yaml next to the companion
// (docs/concepts/modular-architecture.md §5.6, §5.8): it builds or pulls
// each service's image, reads its manifest, shows the rights it asks for
// and gets, and, once the operator confirmed rights that are new, writes
// compose.generated.yml and companion.lock and runs docker compose. The
// core is not rebuilt for a service.

// applyConfig is what hpc apply reads of companion.yaml.
type applyConfig struct {
	Core struct {
		ServicesListen string `yaml:"servicesListen"`
	} `yaml:"core"`
	Services map[string]applyService `yaml:"services"`
}

type applyService struct {
	From       string `yaml:"from"`
	Dockerfile string `yaml:"dockerfile"`
	URL        string `yaml:"url"`
	Grants     struct {
		Egress []string `yaml:"egress"`
		Events []string `yaml:"events"`
		Public bool     `yaml:"public"`
		Volume *bool    `yaml:"volume"`
	} `yaml:"grants"`
	Limits struct {
		MemoryMB int     `yaml:"memoryMB"`
		PIDs     int     `yaml:"pids"`
		CPUs     float64 `yaml:"cpus"`
	} `yaml:"limits"`
	Env map[string]string `yaml:"env"`
}

func (s applyService) grants() wire.Grants {
	return wire.Grants{Egress: s.Grants.Egress, Events: s.Grants.Events, Public: s.Grants.Public, Volume: s.Grants.Volume == nil || *s.Grants.Volume}
}

// Defaults of a service container (§5.8): memory and PID limits always.
const (
	defaultMemoryMB = 256
	defaultPIDs     = 128
	defaultCPUs     = 1.0
	// nonRoot is the user a service runs as when its image says root.
	nonRoot = "65532:65532"
)

type applyOptions struct {
	config, compose, out, companion, docker string
	yes, dryRun, noUp                       bool
	stdin                                   io.Reader
	stdout                                  io.Writer
	interactive                             bool
}

func applyCmd(args []string) error {
	fl := flag.NewFlagSet("apply", flag.ContinueOnError)
	o := applyOptions{stdin: os.Stdin, stdout: os.Stdout}
	fl.StringVar(&o.config, "c", "", "companion.yaml (default: data/companion.yaml, else companion.yaml)")
	fl.StringVar(&o.compose, "f", "", "the compose file with the companion (default: docker-compose.yml or compose.yaml)")
	fl.StringVar(&o.out, "o", "", "the file to write (default: compose.generated.yml next to it)")
	fl.StringVar(&o.companion, "companion", "companion", "the companion's service in that file")
	fl.StringVar(&o.docker, "docker", "docker", "the docker program")
	fl.BoolVar(&o.yes, "yes", false, "confirm new rights without asking")
	fl.BoolVar(&o.dryRun, "dry-run", false, "show what would happen, write and start nothing (images are still built)")
	fl.BoolVar(&o.noUp, "no-up", false, "write the files, do not run docker compose")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		o.interactive = true
	}
	return apply(o)
}

// resolvedService is a service as hpc apply found it.
type resolvedService struct {
	id     string
	conf   applyService
	locked wire.LockedService
	old    *wire.LockedService
	build  bool // hpc apply builds it (git or path)
}

func apply(o applyOptions) error {
	if o.config == "" {
		o.config = "companion.yaml"
		if _, err := os.Stat(filepath.Join("data", "companion.yaml")); err == nil {
			o.config = filepath.Join("data", "companion.yaml")
		}
	}
	if o.compose == "" {
		for _, f := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yaml", "compose.yml"} {
			if _, err := os.Stat(f); err == nil {
				o.compose = f
				break
			}
		}
		if o.compose == "" {
			return errors.New("no compose file with the companion here (docker-compose.yml); name one with -f")
		}
	}
	if o.out == "" {
		o.out = filepath.Join(filepath.Dir(o.compose), "compose.generated.yml")
	}
	cfg, err := readApplyConfig(o.config)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(filepath.Dir(o.config), wire.LockFile)
	lock, _, err := wire.ReadLock(lockPath)
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(o.compose)
	dataDir := filepath.Dir(o.config)

	var ids []string
	for id := range cfg.Services {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var services []*resolvedService
	for _, id := range ids {
		if !validServiceID.MatchString(id) {
			return fmt.Errorf("service %q: names are lower case a-z, 0-9, '.', '_' and '-'", id)
		}
		rs := &resolvedService{id: id, conf: cfg.Services[id]}
		if old, ok := lock.Services[id]; ok {
			rs.old = &old
		}
		fmt.Fprintf(o.stdout, "%s: %s\n", id, describeSource(rs.conf))
		if err := o.resolve(rs, projectDir); err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		services = append(services, rs)
	}
	for id := range lock.Services {
		if _, ok := cfg.Services[id]; !ok {
			fmt.Fprintf(o.stdout, "%s: no longer configured, it is removed\n", id)
		}
	}

	needsConfirmation := false
	for _, rs := range services {
		if printRights(o.stdout, rs) {
			needsConfirmation = true
		}
	}
	if o.dryRun {
		fmt.Fprintln(o.stdout, "dry run: nothing written, nothing started")
		return nil
	}
	if needsConfirmation && !o.yes {
		if !o.interactive {
			return errors.New("new rights need a confirmation: run hpc apply in a terminal, or with --yes")
		}
		fmt.Fprint(o.stdout, "Grant the new rights? [y/N] ")
		line, _ := bufio.NewReader(o.stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("not applied")
		}
	}

	next := wire.Lock{Version: 1, Services: map[string]wire.LockedService{}}
	for _, rs := range services {
		rs.locked.Grants = rs.conf.grants()
		rs.locked.AppliedAt = time.Now().UTC().Truncate(time.Second)
		next.Services[rs.id] = rs.locked
	}
	compose, err := composeFile(o, cfg, services, projectDir, dataDir)
	if err != nil {
		return err
	}
	if err := os.WriteFile(o.out, compose, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(o.stdout, "wrote %s\n", o.out)
	for _, rs := range services {
		if rs.conf.URL != "" {
			continue
		}
		// The core writes the token here when it starts the service's
		// instances; Docker would create a missing directory as root.
		for _, d := range []string{"run", "volume"} {
			os.MkdirAll(filepath.Join(dataDir, "services", rs.id, d), 0o755)
		}
	}
	// The lock before the containers: a companion that compose recreates
	// starts with it, a running one picks it up within a minute.
	if err := next.Write(lockPath); err != nil {
		return err
	}
	fmt.Fprintf(o.stdout, "wrote %s\n", lockPath)
	if o.noUp {
		return nil
	}
	return o.run(o.stdout, o.docker, composeArgs(o, "up", "-d", "--remove-orphans")...)
}

// composeArgs runs docker compose on the operator's file with the
// generated one on top.
func composeArgs(o applyOptions, args ...string) []string {
	return append([]string{"compose", "-f", o.compose, "-f", o.out}, args...)
}

var validServiceID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func readApplyConfig(path string) (*applyConfig, error) {
	envfile.Load(filepath.Join(filepath.Dir(path), ".env"))
	envfile.Load(".env")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg applyConfig
	if err := yaml.Unmarshal(envfile.Expand(raw), &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

func describeSource(s applyService) string {
	if s.URL != "" {
		return "runs elsewhere, at " + s.URL
	}
	return s.From
}

// resolve gets the service's image and manifest.
func (o applyOptions) resolve(rs *resolvedService, projectDir string) error {
	c := rs.conf
	rs.locked.From = c.From
	if c.URL != "" {
		// Run by hand (development): only the manifest, no container.
		rs.locked.URL = c.URL
		m, err := fetchManifest(c.URL)
		if err != nil {
			return err
		}
		rs.locked.Manifest = *m
		return nil
	}
	switch {
	case strings.HasPrefix(c.From, "oci://"):
		ref := strings.TrimPrefix(c.From, "oci://")
		if err := o.run(io.Discard, o.docker, "pull", ref); err != nil {
			// An image built here, or no registry in reach: the local one.
			if _, ierr := o.output(o.docker, "image", "inspect", "--format", "{{.Id}}", ref); ierr != nil {
				return fmt.Errorf("pull %s: %w", ref, err)
			}
			fmt.Fprintf(o.stdout, "  pull failed, using the local image %s\n", ref)
		}
		image := ref
		if out, err := o.output(o.docker, "image", "inspect", "--format", "{{json .RepoDigests}}", ref); err == nil {
			var digests []string
			json.Unmarshal(out, &digests)
			if len(digests) > 0 {
				image = digests[0]
			}
		}
		rs.locked.Image = image
	case strings.HasPrefix(c.From, "git+"):
		repo, ref, sub, err := parseGitSource(c.From)
		if err != nil {
			return err
		}
		dir := filepath.Join(projectDir, ".hpc", "src", rs.id)
		os.RemoveAll(dir)
		os.MkdirAll(filepath.Dir(dir), 0o755)
		if err := o.run(io.Discard, "git", "clone", "--quiet", "--depth", "1", "--branch", ref, repo, dir); err != nil {
			return fmt.Errorf("clone %s@%s: %w", repo, ref, err)
		}
		commit, err := o.output("git", "-C", dir, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		rs.locked.Commit = strings.TrimSpace(string(commit))
		if err := o.buildImage(rs, filepath.Join(dir, sub)); err != nil {
			return err
		}
	case strings.HasPrefix(c.From, "path:"):
		dir := strings.TrimPrefix(c.From, "path:")
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(projectDir, dir)
		}
		if err := o.buildImage(rs, dir); err != nil {
			return err
		}
	default:
		return fmt.Errorf("from %q: want oci://<image>, git+https://<repo>@<ref> or path:<dir>", c.From)
	}

	// The manifest, from the image itself: no network, nothing kept.
	out, err := o.output(o.docker, "run", "--rm", "--network", "none", "-e", "COMPANION_MANIFEST=print", rs.locked.Image)
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(out, &m); err != nil {
		return fmt.Errorf("manifest: the image printed no JSON manifest with COMPANION_MANIFEST=print: %w", err)
	}
	rs.locked.Manifest = m

	user, err := o.output(o.docker, "image", "inspect", "--format", "{{.Config.User}}", rs.locked.Image)
	if err != nil {
		return err
	}
	rs.locked.User = containerUser(strings.TrimSpace(string(user)))
	return nil
}

// containerUser is who the container runs as: the image's numeric user,
// or nonRoot instead of root.
func containerUser(u string) string {
	switch u {
	case "", "root", "0", "0:0", "root:root":
		return nonRoot
	}
	return u
}

func (o applyOptions) buildImage(rs *resolvedService, dir string) error {
	tag := "companion-svc-" + rs.id
	args := []string{"build", "--quiet", "-t", tag + ":build"}
	if rs.conf.Dockerfile != "" {
		df := rs.conf.Dockerfile
		if !filepath.IsAbs(df) {
			df = filepath.Join(dir, df)
		}
		args = append(args, "-f", df)
	}
	if err := o.run(io.Discard, o.docker, append(args, dir)...); err != nil {
		return fmt.Errorf("build %s: %w", dir, err)
	}
	id, err := o.output(o.docker, "image", "inspect", "--format", "{{.Id}}", tag+":build")
	if err != nil {
		return err
	}
	short := strings.TrimPrefix(strings.TrimSpace(string(id)), "sha256:")
	if len(short) > 12 {
		short = short[:12]
	}
	rs.locked.Image = tag + ":" + short
	rs.build = true
	return o.run(io.Discard, o.docker, "tag", tag+":build", rs.locked.Image)
}

// parseGitSource splits git+https://host/repo@ref#subdir.
func parseGitSource(from string) (repo, ref, sub string, err error) {
	s := strings.TrimPrefix(from, "git+")
	s, sub, _ = strings.Cut(s, "#")
	// The ref's @ comes after the host, not in it (user@host).
	hostEnd := len(s)
	if j := strings.Index(s, "://"); j >= 0 {
		if k := strings.IndexByte(s[j+3:], '/'); k >= 0 {
			hostEnd = j + 3 + k
		}
	}
	i := strings.LastIndex(s, "@")
	if i < hostEnd {
		return "", "", "", fmt.Errorf("from %q: name a tag or branch, git+https://…@v1.0.0", from)
	}
	return s[:i], s[i+1:], sub, nil
}

func fetchManifest(base string) (*wire.Manifest, error) {
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get(strings.TrimSuffix(base, "/") + "/manifest")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /manifest: %s", resp.Status)
	}
	var m wire.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return &m, nil
}

// printRights shows what a service offers, asks for and gets; it reports
// whether it gets something the lock has not confirmed yet.
func printRights(w io.Writer, rs *resolvedService) bool {
	m := rs.locked.Manifest
	if rs.locked.Image != "" {
		fmt.Fprintf(w, "  image     %s\n", rs.locked.Image)
	}
	if rs.old != nil && rs.old.Image != "" && rs.old.Image != rs.locked.Image {
		fmt.Fprintf(w, "  was       %s\n", rs.old.Image)
	}
	for _, p := range m.Provides {
		fmt.Fprintf(w, "  offers    %s\n", p.Capability)
	}
	for _, s := range m.Slots {
		opt := ""
		if s.Optional {
			opt = " (optional)"
		}
		fmt.Fprintf(w, "  slot      %s: %s%s\n", s.Name, s.Needs, opt)
	}
	asked := wire.Grants{Egress: m.Requests.Egress, Events: m.Subscribes, Public: m.Requests.Public, Volume: m.Requests.Volume}
	given := rs.conf.grants()
	fmt.Fprintf(w, "  asks for  %s\n", rightsText(asked))
	fmt.Fprintf(w, "  gets      %s\n", rightsText(given))
	if missing := asked.Beyond(given); len(missing) > 0 {
		fmt.Fprintf(w, "  not given %s (the service may not work fully)\n", strings.Join(missing, ", "))
	}
	if rs.conf.URL != "" {
		fmt.Fprintf(w, "  runs at   %s, outside Docker: the egress proxy needs Proxy-Authorization there\n", rs.conf.URL)
	}
	confirmed := wire.Grants{}
	if rs.old != nil {
		confirmed = rs.old.Grants
	}
	added := given.Beyond(confirmed)
	if rs.old == nil {
		fmt.Fprintln(w, "  new service")
		return true
	}
	if len(added) > 0 {
		fmt.Fprintf(w, "  new       %s\n", strings.Join(added, ", "))
		return true
	}
	return false
}

func rightsText(g wire.Grants) string {
	var parts []string
	for _, h := range g.Egress {
		parts = append(parts, "egress "+h)
	}
	for _, e := range g.Events {
		parts = append(parts, "event "+e)
	}
	if g.Public {
		parts = append(parts, "public routes")
	}
	if g.Volume {
		parts = append(parts, "volume")
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, " · ")
}

// composeFile is compose.generated.yml, on top of the operator's compose
// file: the companion joins every service's network, and every service sits
// in a network of its own that has no way out (internal: true).
func composeFile(o applyOptions, cfg *applyConfig, services []*resolvedService, projectDir, dataDir string) ([]byte, error) {
	servicesPort := "3128"
	if l := cfg.Core.ServicesListen; l != "" {
		if _, p, err := net.SplitHostPort(l); err == nil {
			servicesPort = p
		}
	}
	rel := func(p string) (string, error) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		pd, _ := filepath.Abs(projectDir)
		r, err := filepath.Rel(pd, abs)
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(r, ".") {
			r = "./" + r
		}
		return filepath.ToSlash(r), nil
	}
	base, err := rel(o.compose)
	if err != nil {
		return nil, err
	}
	companionNets := yaml.Node{Kind: yaml.MappingNode}
	addKey(&companionNets, "default", &yaml.Node{Kind: yaml.MappingNode})
	networks := yaml.Node{Kind: yaml.MappingNode}
	addKey(&networks, "default", &yaml.Node{Kind: yaml.MappingNode})

	svcs := yaml.Node{Kind: yaml.MappingNode}
	companion := yaml.Node{Kind: yaml.MappingNode}
	addKey(&companion, "networks", &companionNets)
	addKey(&svcs, o.companion, &companion)

	for _, rs := range services {
		if rs.conf.URL != "" {
			continue
		}
		network := "svc-" + rs.id
		alias := yaml.Node{Kind: yaml.MappingNode}
		addKey(&alias, "aliases", seq("companion"))
		addKey(&companionNets, network, &alias)
		internal := yaml.Node{Kind: yaml.MappingNode}
		addScalar(&internal, "internal", "true")
		networks.Content = append(networks.Content, scalar(network), &internal)

		c := rs.conf
		mem, pids, cpus := c.Limits.MemoryMB, c.Limits.PIDs, c.Limits.CPUs
		if mem <= 0 {
			mem = defaultMemoryMB
		}
		if pids <= 0 {
			pids = defaultPIDs
		}
		if cpus <= 0 {
			cpus = defaultCPUs
		}
		s := yaml.Node{Kind: yaml.MappingNode}
		addScalar(&s, "image", rs.locked.Image)
		addQuoted(&s, "user", rs.locked.User)
		addKey(&s, "networks", seq(network))
		addScalar(&s, "read_only", "true")
		addKey(&s, "tmpfs", seq("/tmp"))
		addKey(&s, "cap_drop", seq("ALL"))
		addKey(&s, "security_opt", seq("no-new-privileges:true"))
		addScalar(&s, "mem_limit", fmt.Sprintf("%dm", mem))
		addScalar(&s, "pids_limit", strconv.Itoa(pids))
		addScalar(&s, "cpus", strconv.FormatFloat(cpus, 'f', -1, 64))
		proxy := "http://companion:" + servicesPort
		env := map[string]string{
			"COMPANION_URL":        proxy,
			"COMPANION_TOKEN_FILE": "/run/companion/token",
			"COMPANION_DATA":       "/data",
			"HTTP_PROXY":           proxy,
			"HTTPS_PROXY":          proxy,
			"http_proxy":           proxy,
			"https_proxy":          proxy,
			"NO_PROXY":             "companion",
			"no_proxy":             "companion",
		}
		if p := rs.locked.Manifest.Port; p != 0 {
			env["PORT"] = strconv.Itoa(p)
		}
		for k, v := range c.Env {
			if _, reserved := env[k]; reserved {
				return nil, fmt.Errorf("%s: env %s is set by hpc apply", rs.id, k)
			}
			env[k] = v
		}
		envNode := yaml.Node{Kind: yaml.MappingNode}
		var keys []string
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			// Compose would expand $VAR itself.
			addQuoted(&envNode, k, strings.ReplaceAll(env[k], "$", "$$"))
		}
		addKey(&s, "environment", &envNode)
		run, err := rel(filepath.Join(dataDir, "services", rs.id, "run"))
		if err != nil {
			return nil, err
		}
		vols := []string{run + ":/run/companion:ro"}
		if rs.conf.grants().Volume {
			vol, err := rel(filepath.Join(dataDir, "services", rs.id, "volume"))
			if err != nil {
				return nil, err
			}
			vols = append(vols, vol+":/data")
		}
		addKey(&s, "volumes", seq(vols...))
		addScalar(&s, "restart", "unless-stopped")
		addKey(&svcs, rs.id, &s)
	}

	doc := yaml.Node{Kind: yaml.MappingNode}
	addKey(&doc, "services", &svcs)
	addKey(&doc, "networks", &networks)
	var b bytes.Buffer
	b.WriteString("# Written by hpc apply from " + filepath.ToSlash(o.config) + "; do not edit, run hpc apply again.\n")
	b.WriteString("# It adds the services to " + base + ", everything there stays as it is:\n")
	b.WriteString("#   docker compose -f " + base + " -f " + filepath.Base(o.out) + " up -d\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }

func addKey(m *yaml.Node, k string, v *yaml.Node) { m.Content = append(m.Content, scalar(k), v) }

func addScalar(m *yaml.Node, k, v string) { addKey(m, k, scalar(v)) }

func addQuoted(m *yaml.Node, k, v string) {
	addKey(m, k, &yaml.Node{Kind: yaml.ScalarNode, Value: v, Style: yaml.DoubleQuotedStyle})
}

func seq(vs ...string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode}
	for _, v := range vs {
		n.Content = append(n.Content, scalar(v))
	}
	return n
}

// run and output run a program; run shows its output on w.
func (o applyOptions) run(w io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, io.MultiWriter(w, &stderr)
	if w == io.Discard {
		cmd.Stderr = &stderr
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %v\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (o applyOptions) output(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %v\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
