//go:build heal_apply_sensors

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

func TestApplySensors_NewSetupSensor(t *testing.T) {
	dir := t.TempDir()
	plan := map[string]interface{}{
		"diagnosis":         map[string]interface{}{"failed_sensor_id": "x", "shape": "missing-env"},
		"new_setup_sensors": []interface{}{map[string]interface{}{"id": "smoke-setup", "json": sensortest.LoadSetup(t).AsMap()}},
	}
	planPath := filepath.Join(dir, "plan.json")
	pb, _ := json.Marshal(plan)
	os.WriteFile(planPath, pb, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--plan", planPath, "--out", dir, "--schemas-dir", schematest.RepoSchemasDir(t)}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "smoke-setup.yaml")); err != nil {
		t.Fatal("setup sensor not persisted")
	}
}

func TestApplySensors_PatchBumpsPatchVersion(t *testing.T) {
	dir := t.TempDir()
	patched := sensortest.LoadComputational(t).AsMap()
	patched["version"] = "0.1.0"
	patched["description"] = "patched by heal"

	plan := map[string]interface{}{
		"diagnosis":      map[string]interface{}{"failed_sensor_id": "smoke-comp", "shape": "missing-env"},
		"sensor_patches": []interface{}{map[string]interface{}{"id": "smoke-comp", "patch": patched}},
	}
	planPath := filepath.Join(dir, "plan.json")
	pb, _ := json.Marshal(plan)
	os.WriteFile(planPath, pb, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--plan", planPath, "--out", dir, "--schemas-dir", schematest.RepoSchemasDir(t)}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out, _ := os.ReadFile(filepath.Join(dir, "smoke-comp.yaml"))
	jsonOut, err := yaml.YAMLToJSON(out)
	if err != nil {
		t.Fatalf("written YAML invalid: %v", err)
	}
	var got map[string]interface{}
	json.Unmarshal(jsonOut, &got)
	if got["version"] != "0.1.1" {
		t.Fatalf("expected patched version 0.1.1; got %v", got["version"])
	}
}

func TestApplySensors_InvalidSensorRejected(t *testing.T) {
	dir := t.TempDir()
	bad := sensortest.LoadComputational(t).AsMap()
	delete(bad, "regulation")

	plan := map[string]interface{}{
		"diagnosis":         map[string]interface{}{"failed_sensor_id": "x", "shape": "missing-env"},
		"new_setup_sensors": []interface{}{map[string]interface{}{"id": "smoke-comp", "json": bad}},
	}
	planPath := filepath.Join(dir, "plan.json")
	pb, _ := json.Marshal(plan)
	os.WriteFile(planPath, pb, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--plan", planPath, "--out", dir, "--schemas-dir", schematest.RepoSchemasDir(t)}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected 1 (validation), got %d (stderr=%s)", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "smoke-comp.yaml")); err == nil {
		t.Fatal("invalid sensor must NOT be persisted")
	}
}
