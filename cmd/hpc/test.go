package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/core"
	"gopkg.in/yaml.v3"
)

// testCmd runs the tests of one building block (§5.11, §6): a module's Go
// tests, which run the conformance suites of its capabilities, or, for a
// service in any language, the suites through the wire protocol against
// the service next to a test core.
func testCmd(args []string) error {
	fl := flag.NewFlagSet("test", flag.ContinueOnError)
	run := fl.String("run", "", "a service: the command that starts it (\"python3 services/lemmy/lemmy.py\")")
	config := fl.String("config", "", "a service: its block of companion.yaml (instances: …), or a companion.yaml with it under services:")
	name := fl.String("name", "", "a service: its block's name (default: the id of its manifest)")
	live := fl.Bool("live", false, "a module: also the tests against real accounts (-tags live)")
	verbose := fl.Bool("v", false, "show the core's and the service's log")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *run == "" {
		dir := "."
		if fl.NArg() > 0 {
			dir = fl.Arg(0)
		}
		return testModule(dir, *live, *verbose)
	}
	o := serviceTest{Name: *name, Out: os.Stdout, Command: *run}
	if *config != "" {
		b, err := os.ReadFile(*config)
		if err != nil {
			return err
		}
		o.Config = b
	}
	if *verbose {
		o.Logs = os.Stderr
	}
	failed, err := o.run(context.Background())
	if err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d failed", failed)
	}
	return nil
}

// testModule runs go vet and go test -race in a module's directory.
func testModule(dir string, live, verbose bool) error {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return fmt.Errorf("%s is not a module (no go.mod); a service is tested with --run", dir)
	}
	tags := []string{}
	if live {
		tags = []string{"-tags", "live"}
	}
	steps := [][]string{
		append(append([]string{"vet"}, tags...), "./..."),
		append(append([]string{"test", "-race", "-count=1"}, tags...), "./..."),
	}
	if verbose {
		steps[1] = append([]string{"test", "-v"}, steps[1][1:]...)
	}
	for _, s := range steps {
		cmd := exec.Command("go", s...)
		cmd.Dir, cmd.Stdout, cmd.Stderr = dir, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

// serviceTest is hpc test for a service.
type serviceTest struct {
	Name    string
	Config  []byte
	Command string
	Out     io.Writer
	Logs    io.Writer
	// Start starts the service with env instead of Command; Manifest
	// reads its manifest (tests).
	Start    func(env []string) (stop func(), err error)
	Manifest func(env []string) ([]byte, error)
	// Wait is how long its instances have to come up (60 s).
	Wait time.Duration
}

// suites are the conformance suites, by capability; image is the URL of a
// JPEG to post.
var suites = map[string]func(t companiontest.T, client any, image string){
	"social.publisher/v1": func(t companiontest.T, c any, image string) {
		companiontest.PublisherConformance(t, c.(social.Publisher), image)
	},
}

func (o *serviceTest) run(ctx context.Context) (failed int, err error) {
	if o.Logs == nil {
		o.Logs = io.Discard
	}
	if o.Wait == 0 {
		o.Wait = time.Minute
	}
	if o.Start == nil {
		o.Start, o.Manifest = o.command()
	}
	dir, err := os.MkdirTemp("", "hpc-test-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	data := filepath.Join(dir, "data")
	servicesAddr, err := freeLocalAddr()
	if err != nil {
		return 0, err
	}
	svcAddr, err := freeLocalAddr()
	if err != nil {
		return 0, err
	}
	_, port, _ := net.SplitHostPort(svcAddr)
	svcURL := "http://" + svcAddr
	env := []string{
		"PORT=" + port,
		"COMPANION_URL=http://" + servicesAddr,
		"COMPANION_DATA=" + filepath.Join(dir, "service-data"),
		"NO_PROXY=" + noProxy(), "no_proxy=" + noProxy(),
	}

	raw, err := o.Manifest(append(env, "COMPANION_MANIFEST=print"))
	if err != nil {
		return 0, fmt.Errorf("its manifest: %w", err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, fmt.Errorf("its manifest: %w\n%s", err, raw)
	}
	if o.Name == "" {
		o.Name = m.ID
	}
	if o.Name == "" || strings.ContainsAny(o.Name, "/: ") {
		return 0, fmt.Errorf("its name %q: give one with --name", o.Name)
	}
	env = append(env, "COMPANION_TOKEN_FILE="+filepath.Join(data, "services", o.Name, "run", "token"))

	block, err := o.block()
	if err != nil {
		return 0, err
	}
	block["url"] = svcURL
	instances, _ := block["instances"].(map[string]any)
	if len(instances) == 0 {
		instances = map[string]any{"test": map[string]any{}}
		block["instances"] = instances
	}
	names := make([]string, 0, len(instances))
	for n := range instances {
		names = append(names, n)
	}
	sort.Strings(names)

	// The image the suites post, from here.
	img := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(testJPEG())
	}))
	defer img.Close()

	// The service gets what it asks for.
	lock := wire.Lock{Services: map[string]wire.LockedService{o.Name: {
		From: "test", URL: svcURL, Manifest: m,
		Grants: wire.Grants{Egress: m.Requests.Egress, Events: m.Subscribes, Public: m.Requests.Public, Volume: m.Requests.Volume},
	}}}
	lockRaw, _ := json.Marshal(lock)
	cfg := map[string]any{
		"core": map[string]any{"domain": "example.com", "adminKey": companiontest.AdminKey, "publicUrl": "http://companion.test",
			"database": companiontest.SQLID, "blobs": companiontest.BlobID},
		"modules":  map[string]any{companiontest.SQLID: map[string]any{}, companiontest.BlobID: map[string]any{}},
		"services": map[string]any{o.Name: block},
	}
	cfgRaw, _ := yaml.Marshal(cfg)
	os.WriteFile(filepath.Join(dir, wire.LockFile), lockRaw, 0o600)
	os.WriteFile(filepath.Join(dir, "companion.yaml"), cfgRaw, 0o600)

	stop, err := o.Start(env)
	if err != nil {
		return 0, fmt.Errorf("start it: %w", err)
	}
	defer stop()
	companiontest.RegisterStorage()
	if os.Getenv("COMPANION_SECRET_KEY") == "" {
		os.Setenv("COMPANION_SECRET_KEY", "aHBjLXRlc3QtaGFzLWl0cy1vd24tc2VjcmV0LWtleSE=")
	}
	c := core.New(core.Options{ConfigPath: filepath.Join(dir, "companion.yaml"), DataDir: data, Logs: o.Logs, ServicesListen: servicesAddr})
	if err := c.Start(ctx); err != nil {
		return 0, fmt.Errorf("the test core: %w", err)
	}
	defer c.Stop(context.Background())

	fmt.Fprintf(o.Out, "%s next to a test core\n", o.Name)
	up := map[string]bool{}
	for _, n := range names {
		running, msg := waitInstance(c, o.Name+":"+n, o.Wait)
		up[n] = running
		if running {
			fmt.Fprintf(o.Out, "  %-24s up\n", n)
		} else {
			failed++
			fmt.Fprintf(o.Out, "  %-24s FAIL: did not come up: %s\n", n, msg)
		}
	}
	tok, _ := c.ServiceToken(o.Name)
	for _, offer := range m.Provides {
		suite := suites[offer.Capability]
		a, ok := wire.Lookup(offer.Capability)
		if suite == nil || !ok {
			fmt.Fprintf(o.Out, "  %-24s no conformance suite\n", offer.Capability)
			continue
		}
		for _, n := range names {
			if !up[n] {
				continue
			}
			h := &wire.HTTP{URL: svcURL + "/capabilities/" + offer.Capability, Prepare: func(r *http.Request, body []byte) {
				r.Header.Set(wire.InstanceHeader, n)
				wire.Sign(r, body, []byte(tok), time.Now())
			}}
			label := offer.Capability + " " + n
			if msg := runSuite(suite, a.Client(h, offer.Operations), img.URL+"/image.jpg"); msg != "" {
				failed++
				fmt.Fprintf(o.Out, "  %-24s FAIL: %s\n", label, msg)
			} else {
				fmt.Fprintf(o.Out, "  %-24s ok\n", label)
			}
		}
	}
	return failed, nil
}

// block is the service's block of the given config: the file is the block
// itself, or a companion.yaml with it under services.
func (o *serviceTest) block() (map[string]any, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(o.Config, &doc); err != nil {
		return nil, fmt.Errorf("its config: %w", err)
	}
	if doc == nil {
		return map[string]any{}, nil
	}
	if services, ok := doc["services"].(map[string]any); ok {
		b, ok := services[o.Name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("its config has no services.%s", o.Name)
		}
		return b, nil
	}
	return doc, nil
}

// command starts the service with o.Command, in a shell.
func (o *serviceTest) command() (start func([]string) (func(), error), manifest func([]string) ([]byte, error)) {
	shell := func(env []string) *exec.Cmd {
		cmd := exec.Command("sh", "-c", o.Command)
		cmd.Env = append(os.Environ(), env...)
		ownGroup(cmd)
		cmd.WaitDelay = 5 * time.Second
		return cmd
	}
	start = func(env []string) (func(), error) {
		cmd := shell(env)
		cmd.Stdout, cmd.Stderr = o.Logs, o.Logs
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
	manifest = func(env []string) ([]byte, error) {
		cmd := shell(env)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("%w\n%s", err, stderr.Bytes())
		}
		return out, nil
	}
	return start, manifest
}

// errFatal ends a suite at its first failure.
var errFatal = errors.New("fatal")

type reporter struct{ msg string }

func (r *reporter) Helper() {}
func (r *reporter) Fatal(args ...any) {
	r.msg = fmt.Sprint(args...)
	panic(errFatal)
}
func (r *reporter) Fatalf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)
	panic(errFatal)
}

// runSuite runs a suite; it returns why it failed, or "".
func runSuite(suite func(companiontest.T, any, string), client any, image string) (msg string) {
	r := &reporter{}
	defer func() {
		if v := recover(); v != nil {
			if v != errFatal {
				msg = fmt.Sprint("panic: ", v)
				return
			}
			msg = r.msg
		}
	}()
	suite(r, client, image)
	return ""
}

// waitInstance waits until the core runs the instance; it returns why not.
func waitInstance(c *core.Core, id string, wait time.Duration) (bool, string) {
	deadline := time.Now().Add(wait)
	for {
		req := httptest.NewRequest(http.MethodGet, "/api/core/instances/"+id, nil)
		req.Header.Set("Authorization", "ApiKey "+companiontest.AdminKey)
		rec := httptest.NewRecorder()
		c.Handler().ServeHTTP(rec, req)
		var v struct {
			Running bool   `json:"running"`
			Error   string `json:"error"`
		}
		json.Unmarshal(rec.Body.Bytes(), &v)
		if v.Running {
			return true, ""
		}
		if time.Now().After(deadline) {
			if v.Error == "" {
				v.Error = strings.TrimSpace(rec.Body.String())
			}
			return false, v.Error
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func freeLocalAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	return ln.Addr().String(), nil
}

// noProxy keeps the user's NO_PROXY and adds the local servers.
func noProxy() string {
	v := os.Getenv("NO_PROXY")
	if v == "" {
		v = os.Getenv("no_proxy")
	}
	if v != "" {
		v += ","
	}
	return v + "127.0.0.1,localhost"
}

// testJPEG is a small landscape.
func testJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 160, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 160; x++ {
			c := color.RGBA{120, 160, 200, 255}
			if y > 70 {
				c = color.RGBA{60, 90, 60, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	return buf.Bytes()
}
