package logger

import (
	"strings"
	"testing"
)

func useDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	mu.Lock()
	prev := dir
	dir = d
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); dir = prev; mu.Unlock() })
	return d
}

func TestDailyWriterAndTail(t *testing.T) {
	d := useDir(t)
	w := &dailyWriter{dir: d, prefix: "app"}
	for _, l := range []string{"one module=rss\n", "two module=trips\n", "three module=rss\n"} {
		if _, err := w.Write([]byte(l)); err != nil {
			t.Fatal(err)
		}
	}
	files, err := ListFiles()
	if err != nil || len(files) != 1 || !strings.HasPrefix(files[0], "app-") {
		t.Fatalf("files %v %v", files, err)
	}
	lines, err := TailLines(files[0], 10, "module=rss")
	if err != nil || len(lines) != 2 || lines[1] != "three module=rss" {
		t.Fatalf("tail: %v %v", lines, err)
	}
	if lines, _ := TailLines(files[0], 1, ""); len(lines) != 1 || lines[0] != "three module=rss" {
		t.Fatalf("tail 1: %v", lines)
	}
	for _, bad := range []string{"../escape.log", "sub/x.log", "app.txt", `a\b.log`} {
		if _, err := TailLines(bad, 10, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestClientIDs(t *testing.T) {
	for id, ok := range map[string]bool{"abc_DEF-123": true, "": false, "../x": false, strings.Repeat("a", 65): false} {
		if (ValidateClientID(id) == nil) != ok {
			t.Errorf("%q", id)
		}
	}
}
