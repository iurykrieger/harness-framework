//go:build write_sensor

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}

// withProjectRoot creates a fresh project root with a .harness/ marker,
// .harness/sensors/, and .harness/sensors/fixtures/. Returns absolute root.
func withProjectRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, sub := range []string{".harness", ".harness/sensors", ".harness/sensors/fixtures"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
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
	return root
}

// writeDraftWithFixture builds a canonical-computational sensor with id=newID,
// optionally writes the fixture file referenced by golden_cases[0], and returns
// the draft path.
func writeDraftWithFixture(t *testing.T, root, newID string, writeFixture bool) string {
	t.Helper()
	s := sensortest.LoadComputational(t)
	s.ID = newID
	fixtureRel := ".harness/sensors/fixtures/" + newID + "/pass.txt"
	s.Verification.GoldenCases = []sensor.GoldenCase{
		{
			Fixture:          fixtureRel,
			ExpectedVerdict:  "pass",
			ExpectedSeverity: "info",
		},
	}
	if writeFixture {
		full := filepath.Join(root, fixtureRel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("ok"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	draftDir := t.TempDir()
	draftPath := filepath.Join(draftDir, "draft.json")
	body, err := json.Marshal(s.AsMap())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(draftPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return draftPath
}

func TestRun_HappyPath(t *testing.T) {
	root := withProjectRoot(t)
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := filepath.Join(root, ".harness", "sensors")
	draft := writeDraftWithFixture(t, root, "alpha", true)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", outDir, "--schemas-dir", schemasDir, draft}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "alpha.json")); err != nil {
		t.Fatalf("alpha.json not written: %v", err)
	}
	if !strings.Contains(stdout.String(), `"verdict":"pass"`) {
		t.Fatalf("missing pass signal: %s", stdout.String())
	}
}

func TestRun_MissingFixture(t *testing.T) {
	root := withProjectRoot(t)
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := filepath.Join(root, ".harness", "sensors")
	draft := writeDraftWithFixture(t, root, "beta", false) // fixture NOT written

	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", outDir, "--schemas-dir", schemasDir, draft}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d (want 2) stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "missing_fixture") {
		t.Fatalf("missing_fixture not in stdout: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "beta.json")); !os.IsNotExist(err) {
		t.Fatalf("beta.json should not exist, err=%v", err)
	}
}

func TestRun_SensorAlreadyExists(t *testing.T) {
	root := withProjectRoot(t)
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := filepath.Join(root, ".harness", "sensors")

	// Pre-existing target file with stale content.
	target := filepath.Join(outDir, "gamma.json")
	if err := os.WriteFile(target, []byte(`{"id":"gamma"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	draft := writeDraftWithFixture(t, root, "gamma", true)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", outDir, "--schemas-dir", schemasDir, draft}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d (want 2) stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "sensor_already_exists") {
		t.Fatalf("expected sensor_already_exists; got %s", stdout.String())
	}
	// Verify the stale content survived.
	body, _ := os.ReadFile(target)
	if !strings.Contains(string(body), `"id":"gamma"`) || strings.Contains(string(body), `"smoke-comp"`) {
		t.Fatalf("stale content should be untouched; got %s", body)
	}
}

func TestRun_SchemaInvalid(t *testing.T) {
	root := withProjectRoot(t)
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := filepath.Join(root, ".harness", "sensors")

	// Build a draft missing the required "regulation" field.
	s := sensortest.LoadComputational(t).AsMap()
	s["id"] = "delta"
	delete(s, "regulation")
	// Even bad drafts need a fixture for pre-checks; we ensure the
	// schema check is what trips, not the missing-fixture check.
	fixtureRel := ".harness/sensors/fixtures/delta/pass.txt"
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(fixtureRel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, fixtureRel), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	s["verification"] = map[string]interface{}{
		"golden_cases": []interface{}{
			map[string]interface{}{
				"fixture":           fixtureRel,
				"expected_verdict":  "pass",
				"expected_severity": "info",
			},
		},
	}
	draftDir := t.TempDir()
	draftPath := filepath.Join(draftDir, "draft.json")
	body, _ := json.Marshal(s)
	if err := os.WriteFile(draftPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", outDir, "--schemas-dir", schemasDir, draftPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit=%d (want 1) stdout=%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "schema_invalid") {
		t.Fatalf("missing schema_invalid in stdout: %s", stdout.String())
	}
}

func TestRun_MissingOut(t *testing.T) {
	withProjectRoot(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"some.json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit=%d (want 2)", code)
	}
}

func TestRun_DraftMissing(t *testing.T) {
	root := withProjectRoot(t)
	schemasDir := schematest.RepoSchemasDir(t)
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"--out", filepath.Join(root, ".harness", "sensors"), "--schemas-dir", schemasDir, "/nonexistent/x.json"},
		&stdout, &stderr,
	)
	if code != 2 {
		t.Fatalf("exit=%d (want 2)", code)
	}
	if !strings.Contains(stdout.String(), "read_draft") {
		t.Fatalf("expected read_draft kind: %s", stdout.String())
	}
}
