package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/service"
)

// noID publishes without returning the post's ID, which the suite catches.
type noID struct{ companiontest.FakePublisher }

func (n *noID) Publish(ctx context.Context, p social.Post) (social.Result, error) {
	n.FakePublisher.Publish(ctx, p)
	return social.Result{URL: "https://example.com/p"}, nil
}

func init() {
	companion.Register(companion.Info{ID: "test.noid", New: func() companion.Instance { return new(noID) },
		Provides: []companion.Capability{social.PublisherV1}})
}

// inProcess runs a building block as a service in this process, as
// service.Serve would with the environment hpc test gives it.
func inProcess(t *testing.T, id string) (func([]string) (func(), error), func([]string) ([]byte, error)) {
	info, ok := companion.Lookup(id)
	if !ok {
		t.Fatalf("%s is not registered", id)
	}
	opts := func(env []string) service.Options {
		o := service.Options{Logs: testLogs{t}}
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "PORT":
				o.Listen = "127.0.0.1:" + v
			case "COMPANION_URL":
				o.CoreURL = v
			case "COMPANION_TOKEN_FILE":
				o.TokenFile = v
			case "COMPANION_DATA":
				o.DataDir = v
			}
		}
		return o
	}
	start := func(env []string) (func(), error) {
		o := opts(env)
		svc, err := service.New(o, info)
		if err != nil {
			return nil, err
		}
		srv := &http.Server{Addr: o.Listen, Handler: svc}
		go srv.ListenAndServe()
		return func() { srv.Close(); svc.Close(context.Background()) }, nil
	}
	manifest := func(env []string) ([]byte, error) {
		svc, err := service.New(opts(env), info)
		if err != nil {
			return nil, err
		}
		return json.Marshal(svc.Manifest())
	}
	return start, manifest
}

type testLogs struct{ t *testing.T }

func (l testLogs) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func TestServiceConformance(t *testing.T) {
	start, manifest := inProcess(t, "fake.publisher")
	var out bytes.Buffer
	o := serviceTest{Name: "fake", Config: []byte("instances:\n  main: { platform: fake }\n  other: {}\n"), Out: &out, Logs: testLogs{t},
		Start: start, Manifest: manifest, Wait: 20 * time.Second}
	failed, err := o.run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", out.String())
	if failed != 0 {
		t.Fatalf("%d failed", failed)
	}
	for _, want := range []string{"fake next to a test core", "main                     up", "social.publisher/v1 main ok", "social.publisher/v1 other ok", "social.interactions/v1   no conformance suite"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q", want)
		}
	}
}

func TestServiceConformanceFails(t *testing.T) {
	start, manifest := inProcess(t, "test.noid")
	var out bytes.Buffer
	// The config is a companion.yaml with the block under services.
	o := serviceTest{Name: "noid", Config: []byte("services:\n  noid:\n    instances:\n      main: {}\n"), Out: &out, Logs: testLogs{t},
		Start: start, Manifest: manifest, Wait: 20 * time.Second}
	failed, err := o.run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if failed != 1 || !strings.Contains(out.String(), "social.publisher/v1 main FAIL: publish returned no ID") {
		t.Fatalf("%d failed:\n%s", failed, out.String())
	}
}

func TestServiceConformanceNoServicesBlock(t *testing.T) {
	o := serviceTest{Name: "x", Config: []byte("services:\n  y: {}\n")}
	if _, err := o.block(); err == nil || !strings.Contains(err.Error(), "no services.x") {
		t.Fatalf("%v", err)
	}
}

func TestTestModuleNeedsGoMod(t *testing.T) {
	if err := testModule(t.TempDir(), false, false); err == nil || !strings.Contains(err.Error(), "no go.mod") {
		t.Fatalf("%v", err)
	}
}
