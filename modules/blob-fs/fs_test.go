package blobfs

import (
	"context"
	"strings"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/companiontest"
)

func TestConformance(t *testing.T) {
	companiontest.BlobConformance(t, NewDir(t.TempDir(), "home-page-companion/test"), 0)
}

// Keys never leave the bucket's folder.
func TestRejectsEscapingKeys(t *testing.T) {
	d := NewDir(t.TempDir(), "p/area")
	for _, k := range []string{"../x", "a/../../x", "", "a//b"} {
		if err := d.Put(context.Background(), k, strings.NewReader("x"), 1, ""); err == nil {
			t.Errorf("%q accepted", k)
		}
	}
}

// Two areas of one store do not see each other.
func TestAreasAreSeparate(t *testing.T) {
	s := &Store{root: t.TempDir(), Prefix: DefaultPrefix}
	a, b := s.Bucket("trips"), s.Bucket("microblog")
	a.Put(context.Background(), "x.jpg", strings.NewReader("x"), 1, "")
	if objs, _ := b.List(context.Background(), ""); len(objs) != 0 {
		t.Fatalf("microblog sees %v", objs)
	}
	if a.Prefix() != "home-page-companion/trips" {
		t.Fatal(a.Prefix())
	}
}
