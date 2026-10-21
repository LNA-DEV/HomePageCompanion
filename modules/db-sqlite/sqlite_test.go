package dbsqlite

import (
	"context"
	"path/filepath"
	"testing"

	"gorm.io/gorm"
)

type note struct {
	ID   uint
	Text string
}

func newDB(t *testing.T) *DB {
	t.Helper()
	d := &DB{dir: t.TempDir(), open: map[string]*gorm.DB{}}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestAreasBackupRestore(t *testing.T) {
	d := newDB(t)
	a, err := d.Open("autoupload")
	if err != nil {
		t.Fatal(err)
	}
	a.AutoMigrate(&note{})
	a.Create(&note{Text: "before"})
	if _, err := d.Open("trips"); err != nil {
		t.Fatal(err)
	}
	areas, _ := d.Areas()
	if len(areas) != 2 || areas[0] != "autoupload" {
		t.Fatalf("areas %v", areas)
	}
	dst := filepath.Join(t.TempDir(), "autoupload.db")
	if err := d.Backup(context.Background(), "autoupload", dst); err != nil {
		t.Fatal(err)
	}
	a.Model(&note{}).Where("1 = 1").Update("text", "after")
	if err := d.Restore(context.Background(), "autoupload", dst); err != nil {
		t.Fatal(err)
	}
	b, _ := d.Open("autoupload")
	var n note
	b.First(&n)
	if n.Text != "before" {
		t.Fatalf("restored %q", n.Text)
	}
	if err := d.Drop("trips"); err != nil {
		t.Fatal(err)
	}
	if areas, _ := d.Areas(); len(areas) != 1 {
		t.Fatalf("after drop: %v", areas)
	}
}

func TestRejectsBadAreaNames(t *testing.T) {
	d := newDB(t)
	for _, a := range []string{"../x", "", "A", "a/b"} {
		if _, err := d.Open(a); err == nil {
			t.Errorf("%q accepted", a)
		}
	}
}
