package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAsJSON_yamlFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.yaml")
	if err := os.WriteFile(path, []byte("id: my-sensor\nversion: 1.0.0\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	got, err := ReadAsJSON(path)
	if err != nil {
		t.Fatalf("ReadAsJSON: %v", err)
	}
	want := `{"id":"my-sensor","version":"1.0.0"}`
	if strings.TrimSpace(string(got)) != want {
		t.Fatalf("got %q want %q", string(got), want)
	}
}

func TestReadAsJSON_ymlFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.yml")
	if err := os.WriteFile(path, []byte("id: my-sensor\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := ReadAsJSON(path); err != nil {
		t.Fatalf("ReadAsJSON: %v", err)
	}
}

func TestReadAsJSON_jsonFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	if err := os.WriteFile(path, []byte(`{"id":"my-sensor"}`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	got, err := ReadAsJSON(path)
	if err != nil {
		t.Fatalf("ReadAsJSON: %v", err)
	}
	if string(got) != `{"id":"my-sensor"}` {
		t.Fatalf("got %q", string(got))
	}
}

func TestReadAsJSON_malformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("foo: [unclosed\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	_, err := ReadAsJSON(path)
	if err == nil {
		t.Fatalf("expected error for malformed YAML")
	}
	if !strings.Contains(err.Error(), "parse YAML") {
		t.Fatalf("error %q does not mention parse YAML", err)
	}
}

func TestReadAsJSON_missingFile(t *testing.T) {
	_, err := ReadAsJSON("/nonexistent/path.yaml")
	if err == nil {
		t.Fatalf("expected error for missing file")
	}
}
