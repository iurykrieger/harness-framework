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

	"sigs.k8s.io/yaml"
)

// repoRoot returns the absolute path to the repo root, derived from this
// test file's location. catalog-sensors_test.go lives at
// skills/create-sensors/scripts/, three levels deep.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}

// withProjectRoot creates a fresh project root with a .harness/sensors/
// child directory, sets HARNESS_REGISTRY_ROOT to it, and returns the
// absolute path to .harness/sensors/ (where copyCanonical writes).
func withProjectRoot(t *testing.T, mkSensorsDir bool) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	if mkSensorsDir {
		if err := os.MkdirAll(filepath.Join(root, ".harness", "sensors"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prev, had := os.LookupEnv("HARNESS_REGISTRY_ROOT")
	os.Setenv("HARNESS_REGISTRY_ROOT", root)
	t.Cleanup(func() {
		if had {
			os.Setenv("HARNESS_REGISTRY_ROOT", prev)
		} else {
			os.Unsetenv("HARNESS_REGISTRY_ROOT")
		}
	})
	return filepath.Join(root, ".harness", "sensors")
}

// copyCanonical writes a copy of the canonical-computational sensor under
// dstDir with the id field rewritten to newID. Reads the canonical YAML,
// converts to JSON for the id rewrite, then re-emits YAML so the
// catalog (which globs *.yaml) finds it.
func copyCanonical(t *testing.T, dstDir, newID string) string {
	t.Helper()
	src := filepath.Join(repoRoot(t), "lib", "sensor", "testdata", "canonical-computational.yaml")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	jsonBody, err := yaml.YAMLToJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := strings.Replace(string(jsonBody), `"smoke-comp"`, `"`+newID+`"`, 1)
	yamlBody, err := yaml.JSONToYAML([]byte(rewritten))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dstDir, newID+".yaml")
	if err := os.WriteFile(dst, yamlBody, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
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

func TestRun_EmptyDir(t *testing.T) {
	withProjectRoot(t, true)
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("expected empty stdout, got %q", stdout.String())
	}
}

func TestRun_MissingDir(t *testing.T) {
	withProjectRoot(t, false) // .harness/ exists, .harness/sensors/ does not
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("expected empty stdout, got %q", stdout.String())
	}
}

func TestRun_OneSensor(t *testing.T) {
	dir := withProjectRoot(t, true)
	copyCanonical(t, dir, "alpha")

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
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
	if !strings.Contains(lines[0], `"path":".harness/sensors/alpha.yaml"`) {
		t.Fatalf("line missing expected path: %q", lines[0])
	}
}

func TestRun_MultipleSensors_SortedByID(t *testing.T) {
	dir := withProjectRoot(t, true)
	copyCanonical(t, dir, "charlie")
	copyCanonical(t, dir, "alpha")
	copyCanonical(t, dir, "bravo")

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
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

func TestRun_RecursiveWalk_SubdirSensor(t *testing.T) {
	dir := withProjectRoot(t, true)
	copyCanonical(t, dir, "root-sensor")
	// Create a per-usecase subfolder sensor
	subDir := filepath.Join(dir, "my-usecase")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	copyCanonical(t, subDir, "usecase-sensor")

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	lines := splitNonEmpty(stdout.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (root + subdir sensor), got %d: %q", len(lines), stdout.String())
	}
	found := map[string]bool{}
	for _, l := range lines {
		if strings.Contains(l, `"id":"root-sensor"`) {
			found["root-sensor"] = true
		}
		if strings.Contains(l, `"id":"usecase-sensor"`) {
			found["usecase-sensor"] = true
		}
	}
	if !found["root-sensor"] || !found["usecase-sensor"] {
		t.Fatalf("did not find both sensors in output: %q", stdout.String())
	}
}

func TestRun_MalformedYAML_EmitsWarn(t *testing.T) {
	dir := withProjectRoot(t, true)
	copyCanonical(t, dir, "ok-sensor")
	// Top-level YAML array — converts to a JSON array, which fails
	// json.Unmarshal into map[string]interface{}.
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("- not-a-map\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
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
	dir := withProjectRoot(t, true)
	copyCanonical(t, dir, "valid")

	// Build a schema-invalid sensor: canonical with "regulation" deleted.
	src := filepath.Join(repoRoot(t), "lib", "sensor", "testdata", "canonical-computational.yaml")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	jsonBody, err := yaml.YAMLToJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(jsonBody, &m); err != nil {
		t.Fatal(err)
	}
	m["id"] = "broken-schema"
	delete(m, "regulation")
	bad, _ := json.Marshal(m)
	badYAML, err := yaml.JSONToYAML(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken-schema.yaml"), badYAML, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
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
	dir := withProjectRoot(t, true)

	// Build a schema-valid blocking + stream sensor.
	src := filepath.Join(repoRoot(t), "lib", "sensor", "testdata", "canonical-computational.yaml")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	jsonBody, err := yaml.YAMLToJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(jsonBody, &m); err != nil {
		t.Fatal(err)
	}
	m["id"] = "blocking-one"
	m["output"] = "stream"
	exec := m["execution"].(map[string]interface{})
	exec["blocking"] = true
	exec["graceful_timeout_ms"] = float64(5000)
	exec["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{
			map[string]interface{}{
				"regex":    "ready",
				"verdict":  "pass",
				"severity": "info",
			},
		},
	}
	delete(m["cost"].(map[string]interface{})["latency"].(map[string]interface{}), "timeout_ms")
	out, _ := json.Marshal(m)
	outYAML, err := yaml.JSONToYAML(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blocking-one.yaml"), outYAML, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"blocking":true`) {
		t.Fatalf("expected blocking=true in digest, got %q", stdout.String())
	}
}
