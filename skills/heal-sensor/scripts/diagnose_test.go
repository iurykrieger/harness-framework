//go:build heal_diagnose

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	os.WriteFile(p, []byte(content), 0o644)
	return p
}

func TestDiagnose_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "# Project\n\nRun `cp .env.example .env` to set up.")
	writeFile(t, dir, ".env.example", "RSA_PRIVATE_KEY=YOUR_KEY_HERE\n")
	signal := writeFile(t, dir, "signal.json", `{"sensor_id":"x","verdict":"error","evidence":[{"rationale":"Required environment variable RSA_PRIVATE_KEY not set"}]}`)
	sensor := writeFile(t, dir, "sensor.json", `{"id":"x","requires":{"env":[{"name":"RSA_PRIVATE_KEY","description":"PEM contents for JWT signing"}]}}`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--signal", signal, "--sensor", sensor, "--root", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout.String())
	}
	if _, ok := out["failed_sensor"]; !ok {
		t.Errorf("missing failed_sensor")
	}
	if _, ok := out["signal"]; !ok {
		t.Errorf("missing signal")
	}
	docs, _ := out["documents"].(map[string]interface{})
	if docs == nil || docs["README.md"] == nil {
		t.Errorf("expected README.md in documents map")
	}
	tmpls, _ := out["templates"].([]interface{})
	if len(tmpls) == 0 {
		t.Errorf("expected at least one .env.example in templates")
	}
}

func TestDiagnose_MissingArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
}

func TestDiagnose_SignalUnreadable(t *testing.T) {
	dir := t.TempDir()
	sensor := writeFile(t, dir, "s.json", `{"id":"x"}`)
	var stdout, stderr bytes.Buffer
	code := run([]string{"--signal", "/nonexistent.json", "--sensor", sensor, "--root", dir}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
}

// TestDiagnose_FailingStepFromMetadataSteps verifies that when the
// aggregate signal carries metadata.steps[], the diagnostic output
// surfaces a failing_step entry pointing at the last fail/error step.
// Fail-fast guarantees the LAST entry is the one that decided the
// verdict.
func TestDiagnose_FailingStepFromMetadataSteps(t *testing.T) {
	dir := t.TempDir()
	signal := writeFile(t, dir, "signal.json", `{
		"sensor_id":"x",
		"verdict":"fail",
		"metadata":{
			"kind":"aggregate",
			"steps":[
				{"id":"create","type":"http","verdict":"pass"},
				{"id":"gate","type":"assert","verdict":"fail","stderr_excerpt":"value missing"}
			]
		}
	}`)
	sensor := writeFile(t, dir, "sensor.json", `{"id":"x"}`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--signal", signal, "--sensor", sensor, "--root", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not JSON: %v", err)
	}
	fs, ok := out["failing_step"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected failing_step in output; got %v", out)
	}
	if fs["id"] != "gate" {
		t.Errorf("failing_step.id = %v (want gate)", fs["id"])
	}
	if fs["verdict"] != "fail" {
		t.Errorf("failing_step.verdict = %v (want fail)", fs["verdict"])
	}
	if fs["type"] != "assert" {
		t.Errorf("failing_step.type = %v (want assert)", fs["type"])
	}
}

// TestDiagnose_FailingStepPicksLastFailOrError ensures the LAST
// fail/error entry wins when multiple are present (defensive — in
// practice fail-fast stops at the first).
func TestDiagnose_FailingStepPicksLastFailOrError(t *testing.T) {
	dir := t.TempDir()
	signal := writeFile(t, dir, "signal.json", `{
		"sensor_id":"x",
		"verdict":"error",
		"metadata":{
			"kind":"aggregate",
			"steps":[
				{"id":"a","type":"shell","verdict":"fail"},
				{"id":"b","type":"shell","verdict":"error"}
			]
		}
	}`)
	sensor := writeFile(t, dir, "sensor.json", `{"id":"x"}`)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--signal", signal, "--sensor", sensor, "--root", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var out map[string]interface{}
	_ = json.Unmarshal(stdout.Bytes(), &out)
	fs, _ := out["failing_step"].(map[string]interface{})
	if fs == nil || fs["id"] != "b" {
		t.Errorf("expected failing_step.id=b, got %v", fs)
	}
}

// TestDiagnose_NoFailingStepWhenLegacyShape verifies that a
// command:-shape aggregate (no metadata.steps[]) produces no
// failing_step entry — legacy behavior unchanged.
func TestDiagnose_NoFailingStepWhenLegacyShape(t *testing.T) {
	dir := t.TempDir()
	signal := writeFile(t, dir, "signal.json", `{"sensor_id":"x","verdict":"fail","metadata":{"kind":"aggregate"}}`)
	sensor := writeFile(t, dir, "sensor.json", `{"id":"x"}`)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--signal", signal, "--sensor", sensor, "--root", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var out map[string]interface{}
	_ = json.Unmarshal(stdout.Bytes(), &out)
	if _, ok := out["failing_step"]; ok {
		t.Errorf("legacy-shape signal should not produce failing_step; got %v", out["failing_step"])
	}
}

// TestDiagnose_NoFailingStepWhenAllPass verifies that when every step
// passes (or warns), no failing_step is emitted — only fail/error
// entries qualify.
func TestDiagnose_NoFailingStepWhenAllPass(t *testing.T) {
	dir := t.TempDir()
	signal := writeFile(t, dir, "signal.json", `{
		"sensor_id":"x",
		"verdict":"warn",
		"metadata":{
			"kind":"aggregate",
			"steps":[
				{"id":"a","type":"shell","verdict":"pass"},
				{"id":"b","type":"shell","verdict":"warn"}
			]
		}
	}`)
	sensor := writeFile(t, dir, "sensor.json", `{"id":"x"}`)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--signal", signal, "--sensor", sensor, "--root", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var out map[string]interface{}
	_ = json.Unmarshal(stdout.Bytes(), &out)
	if _, ok := out["failing_step"]; ok {
		t.Errorf("warn-only signal should not produce failing_step; got %v", out["failing_step"])
	}
}
