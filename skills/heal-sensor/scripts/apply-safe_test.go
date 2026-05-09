//go:build heal_apply_safe

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestApplySafe_HappyPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".env.example")
	dst := filepath.Join(dir, ".env")
	os.WriteFile(src, []byte("FOO=bar\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644)

	plan := map[string]interface{}{
		"diagnosis":  map[string]interface{}{"failed_sensor_id": "x", "shape": "missing-env"},
		"auto_apply": []interface{}{map[string]interface{}{"kind": "copy-template", "src": src, "dst": dst}},
	}
	sensor := map[string]interface{}{"id": "x", "requires": map[string]interface{}{"context": []string{dir}}}

	planPath := filepath.Join(dir, "plan.json")
	sensorPath := filepath.Join(dir, "s.json")
	planB, _ := json.Marshal(plan)
	sensorB, _ := json.Marshal(sensor)
	os.WriteFile(planPath, planB, 0o644)
	os.WriteFile(sensorPath, sensorB, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--plan", planPath, "--sensor", sensorPath, "--root", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf(".env not created: %v", err)
	}
}

func TestApplySafe_NeedsInput_PromptsCaller(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	os.WriteFile(envFile, []byte(""), 0o600)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644)

	plan := map[string]interface{}{
		"diagnosis":  map[string]interface{}{"failed_sensor_id": "x", "shape": "missing-env"},
		"auto_apply": []interface{}{map[string]interface{}{"kind": "set-env-in-file", "file": envFile, "name": "FOO", "value_source": "ask-user"}},
	}
	sensor := map[string]interface{}{"id": "x", "requires": map[string]interface{}{"env": []interface{}{map[string]interface{}{"name": "FOO"}}, "context": []string{dir}}}
	planPath := filepath.Join(dir, "plan.json")
	sensorPath := filepath.Join(dir, "s.json")
	pb, _ := json.Marshal(plan)
	sb, _ := json.Marshal(sensor)
	os.WriteFile(planPath, pb, 0o644)
	os.WriteFile(sensorPath, sb, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--plan", planPath, "--sensor", sensorPath, "--root", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	var out struct {
		Results []struct {
			Applied    bool `json:"applied"`
			NeedsInput bool `json:"needs_input"`
		} `json:"results"`
	}
	json.Unmarshal(stdout.Bytes(), &out)
	if len(out.Results) != 1 {
		t.Fatalf("results len = %d", len(out.Results))
	}
	if out.Results[0].Applied || !out.Results[0].NeedsInput {
		t.Fatalf("expected NeedsInput=true; got %#v", out.Results[0])
	}
}
