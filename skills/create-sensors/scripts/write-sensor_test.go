//go:build write_sensor

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

// withProjectRoot creates a fresh project root with a .harness/ marker,
// .harness/sensors/, and .harness/usecases/. Returns absolute root.
func withProjectRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, sub := range []string{".harness", ".harness/sensors", ".harness/usecases", ".harness/usecases/framework"} {
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

// writeUseCaseFile writes a minimal usecase YAML at
// <root>/.harness/usecases/framework/<id>.yaml so RequireUseCaseFilesOnDisk
// resolves the id during persistence.
func writeUseCaseFile(t *testing.T, root, id string) {
	t.Helper()
	path := filepath.Join(root, ".harness", "usecases", "framework", id+".yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("id: "+id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeDraftWithUseCase builds a canonical-computational sensor with id=newID
// and use_cases=[ucID], optionally writes the matching usecase YAML on disk,
// and returns the draft path.
func writeDraftWithUseCase(t *testing.T, root, newID, ucID string, writeUC bool) string {
	t.Helper()
	s := sensortest.LoadComputational(t)
	s.ID = newID
	s.UseCases = []string{ucID}
	if writeUC {
		writeUseCaseFile(t, root, ucID)
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
	draft := writeDraftWithUseCase(t, root, "alpha", "alpha-uc", true)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", outDir, "--schemas-dir", schemasDir, draft}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "alpha.yaml")); err != nil {
		t.Fatalf("alpha.yaml not written: %v", err)
	}
	if !strings.Contains(stdout.String(), `"verdict":"pass"`) {
		t.Fatalf("missing pass signal: %s", stdout.String())
	}
	// Verify the absolute path is emitted on a second stdout line for backward compat with detect-sensors.
	expectedPath := filepath.Join(outDir, "alpha.yaml")
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 stdout lines (signal + path), got %d: %q", len(lines), stdout.String())
	}
	if !strings.Contains(lines[len(lines)-1], "alpha.yaml") {
		t.Fatalf("last stdout line should be the sensor path; got %q", lines[len(lines)-1])
	}
	// And cross-reference: the path on the second line must point to the file that was written.
	abs, _ := filepath.Abs(expectedPath)
	if lines[len(lines)-1] != abs {
		t.Fatalf("path mismatch: stdout=%q want %q", lines[len(lines)-1], abs)
	}
}

func TestRun_MissingUseCase(t *testing.T) {
	root := withProjectRoot(t)
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := filepath.Join(root, ".harness", "sensors")
	draft := writeDraftWithUseCase(t, root, "beta", "nonexistent-uc", false) // usecase NOT written

	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", outDir, "--schemas-dir", schemasDir, draft}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d (want 2) stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "usecase_not_found") {
		t.Fatalf("usecase_not_found not in stdout: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "beta.yaml")); !os.IsNotExist(err) {
		t.Fatalf("beta.yaml should not exist, err=%v", err)
	}
}

func TestRun_SensorAlreadyExists(t *testing.T) {
	root := withProjectRoot(t)
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := filepath.Join(root, ".harness", "sensors")

	// Pre-existing target file with stale content.
	target := filepath.Join(outDir, "gamma.yaml")
	if err := os.WriteFile(target, []byte(`id: gamma`), 0o644); err != nil {
		t.Fatal(err)
	}
	draft := writeDraftWithUseCase(t, root, "gamma", "gamma-uc", true)

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
	if !strings.Contains(string(body), `id: gamma`) || strings.Contains(string(body), `smoke-comp`) {
		t.Fatalf("stale content should be untouched; got %s", body)
	}
}

func TestRun_SchemaInvalid(t *testing.T) {
	root := withProjectRoot(t)
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := filepath.Join(root, ".harness", "sensors")

	// Build a draft missing the required "regulation" field. The usecase
	// file is present so the schema check is what trips, not the
	// usecase-not-found pre-check.
	writeUseCaseFile(t, root, "delta-uc")
	s := sensortest.LoadComputational(t).AsMap()
	s["id"] = "delta"
	delete(s, "regulation")
	s["use_cases"] = []interface{}{"delta-uc"}
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
