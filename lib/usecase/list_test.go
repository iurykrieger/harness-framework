package usecase

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListByJourney_Empty(t *testing.T) {
	dir := t.TempDir()
	got, err := ListByJourney(filepath.Join(dir, "usecases"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty map, got %v", got)
	}
}

func TestListByJourney_Absent(t *testing.T) {
	got, err := ListByJourney("/this/path/does/not/exist")
	if err != nil {
		t.Fatalf("absent dir must be tolerated, got: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty map, got %v", got)
	}
}

func TestListByJourney_GroupsByJourney(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "user-registration", "create-user-with-email.yaml"), "x")
	writeFile(t, filepath.Join(dir, "user-registration", "create-user-duplicate.yaml"), "x")
	writeFile(t, filepath.Join(dir, "user-login", "login-happy-path.yaml"), "x")

	got, err := ListByJourney(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"user-registration": {"create-user-duplicate", "create-user-with-email"},
		"user-login":        {"login-happy-path"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestListByJourney_SkipsNonYAML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "j", "valid.yaml"), "x")
	writeFile(t, filepath.Join(dir, "j", "ignore.txt"), "x")
	writeFile(t, filepath.Join(dir, "j", "also.json"), "x")
	writeFile(t, filepath.Join(dir, "j", "no-extension"), "x")

	got, err := ListByJourney(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"j": {"valid"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestListByJourney_SkipsDotfilesAndFiles(t *testing.T) {
	dir := t.TempDir()
	// dotfile in journey dir
	writeFile(t, filepath.Join(dir, "j", ".hidden.yaml"), "x")
	writeFile(t, filepath.Join(dir, "j", "real.yaml"), "x")
	// stray file at root usecases dir (no journey wrapper)
	writeFile(t, filepath.Join(dir, "stray.yaml"), "x")

	got, err := ListByJourney(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"j": {"real"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestListByJourney_SortedDeterministic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "j", "c.yaml"), "x")
	writeFile(t, filepath.Join(dir, "j", "a.yaml"), "x")
	writeFile(t, filepath.Join(dir, "j", "b.yaml"), "x")

	got, err := ListByJourney(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got["j"], want) {
		t.Fatalf("got %v, want %v", got["j"], want)
	}
}
