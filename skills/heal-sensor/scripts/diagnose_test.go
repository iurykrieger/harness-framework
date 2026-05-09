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
