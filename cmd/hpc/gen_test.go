package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The generated files in the repository match what hpc gen writes now: a
// capability changed without hpc gen fails here, not in a service.
func TestGeneratedFilesAreUpToDate(t *testing.T) {
	pkgs, err := loadCapabilities([]string{"../../capability/..."})
	if err != nil {
		t.Fatal(err)
	}
	root := "../.."
	n := 0
	for _, p := range pkgs {
		if len(p.Caps) == 0 {
			continue
		}
		src, err := genGo(p)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string][]byte{filepath.Join(p.Dir, genFile): src, filepath.Join(root, "web/src/sdk/capabilities", p.Name+".ts"): genTS(p)}
		for _, c := range p.Caps {
			b, err := genOpenAPI(p, c)
			if err != nil {
				t.Fatal(err)
			}
			want[filepath.Join(root, "docs/services/openapi", c.FullName()+".json")] = b
		}
		for path, b := range want {
			old, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(old, b) {
				t.Errorf("%s is not up to date: run hpc gen", path)
			}
			n++
		}
	}
	if n < 10 {
		t.Fatalf("only %d files checked", n)
	}
}

func TestCapabilityModel(t *testing.T) {
	pkgs, err := loadCapabilities([]string{"../../capability/social", "../../capability/media", "../../capability/storage"})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]*genPkg{}
	for _, p := range pkgs {
		by[p.Name] = p
	}
	if len(by["storage"].Caps) != 0 {
		t.Fatal("storage is Go only (hpc:local) but was generated")
	}
	social := by["social"]
	var pub *genCap
	for _, c := range social.Caps {
		if c.Wire() == "social.publisher/v1" {
			pub = c
		}
	}
	if pub == nil || len(pub.Required) != 4 || len(pub.Optional) != 2 || pub.Required[0].Name != "describe" || pub.Required[3].Name != "resolveMention" {
		t.Fatalf("publisher %+v", pub)
	}
	if !pub.Required[2].Itself || pub.Required[3].Itself || pub.Required[3].Params[0].JSON != "profileUrl" {
		t.Fatalf("argument encoding: %+v %+v", pub.Required[2], pub.Required[3].Params)
	}
	resize := by["media"].Caps[0].Required[0]
	if resize.Name != "resize" || resize.Raw != 0 || !resize.RawResult || resize.Itself {
		t.Fatalf("resize %+v", resize)
	}
}

func TestRefusesBinaryInsideJSON(t *testing.T) {
	_, err := loadCapabilities([]string{"./testdata/badcap"})
	if err == nil || !strings.Contains(err.Error(), "[]byte inside JSON") {
		t.Fatalf("binary field accepted: %v", err)
	}
}

func TestNames(t *testing.T) {
	for in, want := range map[string]string{"profileURL": "profileUrl", "to": "to", "message": "message", "imageID": "imageId", "HTTPStatus": "httpStatus"} {
		if got := jsonKey(in); got != want {
			t.Errorf("jsonKey(%s) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{"NotAProfile": "not_a_profile", "RateLimited": "rate_limited", "Self": "self", "IdentityUnknown": "identity_unknown"} {
		if got := snake(in); got != want {
			t.Errorf("snake(%s) = %s, want %s", in, got, want)
		}
	}
}
