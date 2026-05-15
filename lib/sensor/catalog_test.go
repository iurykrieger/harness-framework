package sensor_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
	"sigs.k8s.io/yaml"
)

func newValidator(t *testing.T) *schema.Validator {
	t.Helper()
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func writeSensorWithID(t *testing.T, dir, newID string) {
	t.Helper()
	body, _ := json.Marshal(sensortest.LoadComputational(t).AsMap())
	rewritten := strings.Replace(string(body), `"smoke-comp"`, `"`+newID+`"`, 1)
	yamlBody, err := yaml.JSONToYAML([]byte(rewritten))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, newID+".yaml"), yamlBody, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCatalog_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	out, warns, err := sensor.Catalog(dir, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 || len(warns) != 0 {
		t.Fatalf("expected empty result, got %d entries / %d warns", len(out), len(warns))
	}
}

func TestCatalog_MissingDir_ReturnsEmpty(t *testing.T) {
	parent := t.TempDir()
	out, warns, err := sensor.Catalog(filepath.Join(parent, "does-not-exist"), newValidator(t))
	if err != nil {
		t.Fatalf("expected nil error for missing dir, got %v", err)
	}
	if len(out) != 0 || len(warns) != 0 {
		t.Fatalf("expected empty result, got %d entries / %d warns", len(out), len(warns))
	}
}

func TestCatalog_OneSensor(t *testing.T) {
	dir := t.TempDir()
	writeSensorWithID(t, dir, "alpha")
	out, warns, err := sensor.Catalog(dir, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("unexpected warns: %v", warns)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 sensor, got %d", len(out))
	}
	if out[0].ID != "alpha" {
		t.Fatalf("ID=%q", out[0].ID)
	}
	if out[0].Kind != sensor.KindAssertion {
		t.Fatalf("Kind=%q", out[0].Kind)
	}
	if out[0].Execution.Blocking {
		t.Fatalf("Blocking should be false for canonical sensor")
	}
}

func TestCatalog_MultipleSorted(t *testing.T) {
	dir := t.TempDir()
	writeSensorWithID(t, dir, "charlie")
	writeSensorWithID(t, dir, "alpha")
	writeSensorWithID(t, dir, "bravo")
	out, _, err := sensor.Catalog(dir, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3, got %d", len(out))
	}
	for i, want := range []string{"alpha", "bravo", "charlie"} {
		if out[i].ID != want {
			t.Fatalf("position %d: got %q want %q", i, out[i].ID, want)
		}
	}
}

func TestCatalog_MalformedYAML_Warns(t *testing.T) {
	dir := t.TempDir()
	writeSensorWithID(t, dir, "ok")
	// A scalar string is valid YAML syntactically but cannot decode into
	// an object map, so the catalog should warn during parse.
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("not-an-object"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, warns, err := sensor.Catalog(dir, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != "ok" {
		t.Fatalf("expected one valid 'ok' sensor; got %d", len(out))
	}
	if len(warns) != 1 || warns[0].File != "broken.yaml" {
		t.Fatalf("expected one warn for broken.yaml; got %v", warns)
	}
}

func TestCatalog_SchemaInvalid_Warns(t *testing.T) {
	dir := t.TempDir()
	writeSensorWithID(t, dir, "valid")
	bad := sensortest.LoadComputational(t).AsMap()
	bad["id"] = "broken-schema"
	delete(bad, "regulation")
	body, _ := json.Marshal(bad)
	yamlBody, err := yaml.JSONToYAML(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken-schema.yaml"), yamlBody, 0o644); err != nil {
		t.Fatal(err)
	}
	out, warns, err := sensor.Catalog(dir, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != "valid" {
		t.Fatalf("expected only 'valid' to survive; got %d entries", len(out))
	}
	if len(warns) != 1 || warns[0].File != "broken-schema.yaml" {
		t.Fatalf("expected warn for broken-schema.yaml; got %v", warns)
	}
}

func TestCatalog_NilValidator_Errors(t *testing.T) {
	_, _, err := sensor.Catalog(t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected error when validator is nil")
	}
}

func TestCatalog_BlockingExposedOnExecution(t *testing.T) {
	dir := t.TempDir()
	// Construct a schema-valid blocking + stream sensor.
	m := sensortest.LoadComputational(t).AsMap()
	m["id"] = "blocking-one"
	m["output"] = "stream"
	exec := m["execution"].(map[string]interface{})
	exec["blocking"] = true
	exec["graceful_timeout_ms"] = float64(5000)
	exec["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{
			map[string]interface{}{"regex": "ready", "verdict": "pass", "severity": "info"},
		},
	}
	delete(m["cost"].(map[string]interface{})["latency"].(map[string]interface{}), "timeout_ms")
	body, _ := json.Marshal(m)
	yamlBody, err := yaml.JSONToYAML(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blocking-one.yaml"), yamlBody, 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := sensor.Catalog(dir, newValidator(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !out[0].Execution.Blocking {
		t.Fatalf("expected blocking-one with Execution.Blocking=true; got %#v", out)
	}
}
