//go:build replay_fixture

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

// uniqueSensorID returns a schema-valid id ([a-z][a-z0-9-]*) that no
// previous test run could have created — used to assert per-run runtime
// directories independently from any other test.
func uniqueSensorID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// writeProjectSensor materializes a computational sensor YAML at the
// canonical <root>/.harness/sensors/<id>.yaml location and returns its
// absolute path. The orchestrator persists runtime under
// <root>/.harness/runtime/<id>/<run-id>/ when invoked against this path.
func writeProjectSensor(t *testing.T, root, id string) string {
	t.Helper()
	s := sensortest.LoadComputational(t).AsMap()
	s["id"] = id
	jsonBody, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal sensor: %v", err)
	}
	body, err := yaml.JSONToYAML(jsonBody)
	if err != nil {
		t.Fatalf("convert sensor to YAML: %v", err)
	}
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sensors: %v", err)
	}
	path := filepath.Join(dir, id+".yaml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write sensor: %v", err)
	}
	return path
}

// writeTempFixture writes content to a tempfile and returns its absolute path.
func writeTempFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// TestReplayFixture_PersistsRunUnderCanonicalRuntime drives the script
// against a freshly-created project tree. It asserts:
//   - the aggregate Signal preserves sensor.id verbatim
//   - the run materializes a <projectRoot>/.harness/runtime/<id>/<run-id>/
//     directory, i.e. the same shape any other valid /run-sensor run
//     would produce.
func TestReplayFixture_PersistsRunUnderCanonicalRuntime(t *testing.T) {
	proj := t.TempDir()
	id := uniqueSensorID("replay-canonical")
	sensorPath := writeProjectSensor(t, proj, id)
	fixturePath := writeTempFixture(t, "")

	// Point HARNESS_REGISTRY_ROOT at the test project so resolveProjectRoot
	// picks it up regardless of where `go test` was invoked from.
	t.Setenv("HARNESS_REGISTRY_ROOT", proj)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--sensor", sensorPath,
		"--fixture", fixturePath,
		"--schemas-dir", schematest.RepoSchemasDir(t),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}

	// The aggregate Signal is the last JSONL line on stdout.
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		t.Fatalf("no stdout; stderr=%s", stderr.String())
	}
	lines := strings.Split(out, "\n")
	var agg map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &agg); err != nil {
		t.Fatalf("decode aggregate: %v; raw=%q", err, lines[len(lines)-1])
	}
	if got, _ := agg["sensor_id"].(string); got != id {
		t.Fatalf("aggregate.sensor_id = %q, want %q (sensor.id preserved verbatim)", got, id)
	}

	// The orchestrator wrote a <run-id>/ directory under the canonical
	// runtime tree. Its name is <pid>-<short>, so just assert the parent
	// directory exists and contains exactly one entry.
	sensorRuntimeDir := filepath.Join(proj, ".harness", "runtime", id)
	entries, err := os.ReadDir(sensorRuntimeDir)
	if err != nil {
		t.Fatalf("read runtime dir %s: %v", sensorRuntimeDir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("runtime entries: got %d, want 1 (single <run-id>/)", len(entries))
	}
	runID := entries[0].Name()
	runDir := filepath.Join(sensorRuntimeDir, runID)
	for _, name := range []string{"raw.log", "signals.log"} {
		if _, err := os.Stat(filepath.Join(runDir, name)); err != nil {
			t.Errorf("expected %s in %s: %v", name, runDir, err)
		}
	}
}

func TestReplayFixture_MissingFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"both missing", []string{}},
		{"only sensor", []string{"--sensor", "/tmp/x.json"}},
		{"only fixture", []string{"--fixture", "/tmp/y.txt"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tc.args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("exit=%d, want 2; stderr=%s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "usage:") {
				t.Fatalf("stderr lacks usage hint: %s", stderr.String())
			}
		})
	}
}

func TestReplayFixture_SensorNotJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("this is not json"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	fixturePath := writeTempFixture(t, "")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensor", path, "--fixture", fixturePath}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "parse sensor") {
		t.Fatalf("stderr lacks parse error: %s", stderr.String())
	}
}

func TestReplayFixture_SensorMissingExecution(t *testing.T) {
	bad := map[string]interface{}{
		"id":   "no-execution-block",
		"type": "computational",
		// no "execution" field
	}
	body, _ := json.Marshal(bad)
	sensorPath := filepath.Join(t.TempDir(), "no-exec.json")
	if err := os.WriteFile(sensorPath, body, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	fixturePath := writeTempFixture(t, "")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensor", sensorPath, "--fixture", fixturePath}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "execution") {
		t.Fatalf("stderr does not name 'execution': %s", stderr.String())
	}
}

func TestReplayFixture_SensorFileMissing(t *testing.T) {
	fixturePath := writeTempFixture(t, "")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensor", "/nonexistent/sensor.json", "--fixture", fixturePath}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "read sensor") {
		t.Fatalf("stderr lacks read error: %s", stderr.String())
	}
}
