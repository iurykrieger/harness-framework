package lib

import (
	"os"
	"path/filepath"
	"testing"
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
			got, err := ResolveSensorPath(tc.arg, dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
	t.Run("not found", func(t *testing.T) {
		if _, err := ResolveSensorPath("@sensors/missing.json", dir); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("empty after trimming", func(t *testing.T) {
		if _, err := ResolveSensorPath("@", dir); err == nil {
			t.Fatal("expected error")
		}
	})
}
