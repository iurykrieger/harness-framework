//go:build catalog_sensors

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot returns the absolute path to the repo root, derived from this
// test file's location. catalog-sensors_test.go lives at
// skills/create-sensor/scripts/, three levels deep.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}

func TestRun_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensors-dir", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("expected empty stdout, got %q", stdout.String())
	}
}

func TestRun_MissingDir(t *testing.T) {
	parent := t.TempDir()
	missing := filepath.Join(parent, "does-not-exist")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensors-dir", missing}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("expected empty stdout, got %q", stdout.String())
	}
}

// helper: copy a sensor JSON from lib/sensor/testdata into a temp dir, with id rewritten.
func copyCanonical(t *testing.T, dstDir, newID string) string {
	t.Helper()
	src := filepath.Join(repoRoot(t), "lib", "sensor", "testdata", "canonical-computational.json")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := strings.Replace(string(body), `"smoke-comp"`, `"`+newID+`"`, 1)
	dst := filepath.Join(dstDir, newID+".json")
	if err := os.WriteFile(dst, []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestRun_OneSensor(t *testing.T) {
	dir := t.TempDir()
	copyCanonical(t, dir, "alpha")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensors-dir", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := splitNonEmpty(stdout.String())
	if len(lines) != 1 {
		t.Fatalf("expected 1 JSONL line, got %d: %q", len(lines), stdout.String())
	}
	if !strings.Contains(lines[0], `"id":"alpha"`) {
		t.Fatalf("line missing id=alpha: %q", lines[0])
	}
	if !strings.Contains(lines[0], `"kind":"assertion"`) {
		t.Fatalf("line missing kind=assertion: %q", lines[0])
	}
	if !strings.Contains(lines[0], `"blocking":false`) {
		t.Fatalf("line missing blocking=false: %q", lines[0])
	}
}

func TestRun_MultipleSensors_SortedByID(t *testing.T) {
	dir := t.TempDir()
	copyCanonical(t, dir, "charlie")
	copyCanonical(t, dir, "alpha")
	copyCanonical(t, dir, "bravo")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensors-dir", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := splitNonEmpty(stdout.String())
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	for i, want := range []string{"alpha", "bravo", "charlie"} {
		needle := `"id":"` + want + `"`
		if !strings.Contains(lines[i], needle) {
			t.Fatalf("line %d expected %s, got %q", i, needle, lines[i])
		}
	}
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func TestRun_MalformedJSON_EmitsWarn(t *testing.T) {
	dir := t.TempDir()
	copyCanonical(t, dir, "ok-sensor")
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensors-dir", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := splitNonEmpty(stdout.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (1 valid + 1 warn), got %d: %q", len(lines), stdout.String())
	}
	hasWarn, hasValid := false, false
	for _, line := range lines {
		if strings.Contains(line, `"verdict":"warn"`) {
			hasWarn = true
		}
		if strings.Contains(line, `"id":"ok-sensor"`) {
			hasValid = true
		}
	}
	if !hasWarn || !hasValid {
		t.Fatalf("expected one warn and one valid digest; got %q", stdout.String())
	}
}

func TestRun_SchemaInvalid_EmitsWarn(t *testing.T) {
	dir := t.TempDir()
	copyCanonical(t, dir, "valid")
	// Build a schema-invalid sensor: canonical with "regulation" deleted.
	src := filepath.Join(repoRoot(t), "lib", "sensor", "testdata", "canonical-computational.json")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	m["id"] = "broken-schema"
	delete(m, "regulation")
	bad, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "broken-schema.json"), bad, 0o644); err != nil {
		t.Fatal(err)
	}

	schemasDir := filepath.Join(repoRoot(t), "schemas")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensors-dir", dir, "--schemas-dir", schemasDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := splitNonEmpty(stdout.String())
	hasWarn, hasValid := false, false
	for _, line := range lines {
		if strings.Contains(line, `"verdict":"warn"`) && strings.Contains(line, "broken-schema") {
			hasWarn = true
		}
		if strings.Contains(line, `"id":"valid"`) {
			hasValid = true
		}
	}
	if !hasWarn || !hasValid {
		t.Fatalf("expected warn for broken-schema and valid digest for 'valid'; got %q", stdout.String())
	}
}

func TestRun_BlockingDerivation(t *testing.T) {
	dir := t.TempDir()
	// Load canonical and set execution.blocking = true.
	src := filepath.Join(repoRoot(t), "lib", "sensor", "testdata", "canonical-computational.json")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	m["id"] = "blocking-one"
	exec := m["execution"].(map[string]interface{})
	exec["blocking"] = true
	exec["graceful_timeout_ms"] = float64(5000)
	delete(m["cost"].(map[string]interface{})["latency"].(map[string]interface{}), "timeout_ms")
	out, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "blocking-one.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensors-dir", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"blocking":true`) {
		t.Fatalf("expected blocking=true in digest, got %q", stdout.String())
	}
}
