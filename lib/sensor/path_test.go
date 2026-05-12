package sensor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestResolve_ByID(t *testing.T) {
	dir := t.TempDir()
	sensorsDir := filepath.Join(dir, "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(sensorsDir, "watch-logs.json")
	if err := os.WriteFile(want, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := sensor.Resolve("watch-logs", dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolve_ByPath(t *testing.T) {
	dir := t.TempDir()
	sensorsDir := filepath.Join(dir, "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sensorsDir, "x.json")
	if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		arg  string
	}{
		{"@-prefix relative", "@sensors/x.json"},
		{"relative", "sensors/x.json"},
		{"absolute", target},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sensor.Resolve(tc.arg, dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != target {
				t.Fatalf("got %s, want %s", got, target)
			}
		})
	}
}

func TestResolve_Empty(t *testing.T) {
	if _, err := sensor.Resolve("", "/tmp"); err == nil {
		t.Fatal("expected error on empty")
	}
}

func TestResolve_BadID(t *testing.T) {
	if _, err := sensor.Resolve("Bad_ID", "/tmp"); err == nil {
		t.Fatal("expected error on uppercase id")
	}
}

func TestResolve_MissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := sensor.Resolve("nope", dir); err == nil {
		t.Fatal("expected error when file missing")
	}
}

func TestResolve_PathTraversal(t *testing.T) {
	if _, err := sensor.Resolve("../etc/passwd", "/tmp"); err == nil {
		t.Fatal("expected error on path-like id")
	}
}
