package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAsJSON_HappyPath(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		input    []byte
		wantJSON string
	}{
		{
			name:     "YAML",
			filename: "input.yaml",
			input:    []byte("id: my-sensor\nversion: 1.0.0\n"),
			wantJSON: `{"id":"my-sensor","version":"1.0.0"}`,
		},
		{
			name:     "YML",
			filename: "input.yml",
			input:    []byte("id: my-sensor\nversion: 1.0.0\n"),
			wantJSON: `{"id":"my-sensor","version":"1.0.0"}`,
		},
		{
			name:     "JSON",
			filename: "input.json",
			input:    []byte(`{"id":"my-sensor"}`),
			wantJSON: `{"id":"my-sensor"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.filename)
			if err := os.WriteFile(path, tc.input, 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			got, err := ReadAsJSON(path)
			if err != nil {
				t.Fatalf("ReadAsJSON: %v", err)
			}
			if strings.TrimSpace(string(got)) != tc.wantJSON {
				t.Fatalf("got %q want %q", string(got), tc.wantJSON)
			}
		})
	}
}

func TestReadAsJSON_MalformedYAML(t *testing.T) {
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

func TestReadAsJSON_MissingFile(t *testing.T) {
	_, err := ReadAsJSON("/nonexistent/path.yaml")
	if err == nil {
		t.Fatalf("expected error for missing file")
	}
}
