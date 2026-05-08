package sensor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindSensorByID_Found(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "start-postgres.json")
	if err := os.WriteFile(target, []byte(`{"id":"start-postgres"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := FindSensorByID("start-postgres", root)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("got %q want %q", got, target)
	}
}

func TestFindSensorByID_Missing(t *testing.T) {
	root := t.TempDir()
	if _, err := FindSensorByID("nope", root); err == nil {
		t.Fatal("expected error when sensor file missing")
	}
}

func TestFindSensorByID_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := FindSensorByID("../escape", root); err == nil {
		t.Fatal("expected error when id contains path separators")
	}
}
