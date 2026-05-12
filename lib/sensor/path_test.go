package sensor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestResolveSensorPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sensors", "x.json")
	_ = os.MkdirAll(filepath.Dir(target), 0o755)
	_ = os.WriteFile(target, []byte("{}"), 0o644)

	cases := []struct {
		name string
		arg  string
		want string
	}{
		{"@-prefix relative", "@sensors/x.json", target},
		{"relative", "sensors/x.json", target},
		{"absolute", target, target},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sensor.ResolveSensorPath(tc.arg, dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
	t.Run("not found", func(t *testing.T) {
		if _, err := sensor.ResolveSensorPath("@sensors/missing.json", dir); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("empty after trimming", func(t *testing.T) {
		if _, err := sensor.ResolveSensorPath("@", dir); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestResolveByID(t *testing.T) {
	dir := t.TempDir()
	sensorsDir := filepath.Join(dir, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(sensorsDir, "watch-logs.json")
	if err := os.WriteFile(want, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := sensor.ResolveByID("watch-logs", dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveByID_RejectsEmpty(t *testing.T) {
	if _, err := sensor.ResolveByID("", "/tmp"); err == nil {
		t.Fatal("expected error on empty id")
	}
}

func TestResolveByID_RejectsBadShape(t *testing.T) {
	if _, err := sensor.ResolveByID("../etc/passwd", "/tmp"); err == nil {
		t.Fatal("expected error on path-like id")
	}
}

func TestResolveByID_MissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := sensor.ResolveByID("nope", dir); err == nil {
		t.Fatal("expected error when file missing")
	}
}
