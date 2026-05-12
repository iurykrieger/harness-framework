package sensor_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

func TestValidateAndPersist_ValidComputational(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	body, _ := json.Marshal(sensortest.LoadComputational(t).AsMap())

	path, err := sensor.ValidateAndPersist(body, outDir, schemasDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(outDir, "smoke-comp.json")
	abs, _ := filepath.Abs(want)
	if path != abs {
		t.Fatalf("path = %q, want %q", path, abs)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("file missing: %v", err)
	}
}

func TestValidateAndPersist_InvalidJSON(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	_, err := sensor.ValidateAndPersist([]byte("not-json"), t.TempDir(), schemasDir)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestValidateAndPersist_SchemaViolation(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	bad := sensortest.LoadComputational(t).AsMap()
	delete(bad, "regulation")
	body, _ := json.Marshal(bad)

	outDir := t.TempDir()
	_, err := sensor.ValidateAndPersist(body, outDir, schemasDir)
	if err == nil {
		t.Fatal("expected schema error, got nil")
	}
	entries, _ := os.ReadDir(outDir)
	if len(entries) != 0 {
		t.Fatalf("expected outDir empty, found %d entries", len(entries))
	}
}

func TestValidateAndPersist_Idempotent(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	body, _ := json.Marshal(sensortest.LoadComputational(t).AsMap())

	p1, err := sensor.ValidateAndPersist(body, outDir, schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := sensor.ValidateAndPersist(body, outDir, schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatalf("paths differ: %q vs %q", p1, p2)
	}
	a, _ := os.ReadFile(p1)
	b, _ := os.ReadFile(p2)
	if string(a) != string(b) {
		t.Fatal("repeat write produced different content")
	}
}

func TestValidateAndPersist_OverwritesStale(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	stale := filepath.Join(outDir, "smoke-comp.json")
	if err := os.WriteFile(stale, []byte("STALE"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(sensortest.LoadComputational(t).AsMap())

	if _, err := sensor.ValidateAndPersist(body, outDir, schemasDir); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(stale)
	if strings.Contains(string(out), "STALE") {
		t.Fatal("stale content not overwritten")
	}
}

func TestValidateAndPersist_CreatesNestedOutDir(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	parent := t.TempDir()
	out := filepath.Join(parent, "deep", ".harness", "sensors")
	body, _ := json.Marshal(sensortest.LoadComputational(t).AsMap())

	if _, err := sensor.ValidateAndPersist(body, out, schemasDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "smoke-comp.json")); err != nil {
		t.Fatalf("nested out dir not created: %v", err)
	}
}
