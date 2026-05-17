package sensor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// TestLoad_StepsShape exercises the steps[] execution shape end-to-end:
// load + schema-validate a sensor declared with execution.steps[] and
// assert the typed Steps slice carries the decoded entries.
func TestLoad_StepsShape(t *testing.T) {
	dir := t.TempDir()
	sensorDir := filepath.Join(dir, ".harness", "sensors")
	if err := os.MkdirAll(sensorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`
id: example-steps-load
version: 0.1.0
name: example
description: x
kind: assertion
type: computational
regulation: maintainability
phase: on-demand
determinism: high
output: single
cost:
  class: cheap
  compute:
    cpu: low
    memory_mb: 64
  latency:
    p50_ms: 10
    p95_ms: 100
    timeout_ms: 5000
triggers:
  - "on": manual
use_cases:
  - fake-uc
execution:
  steps:
    - id: ping
      type: shell
      run: echo hi
      exit_code_map:
        "0": pass
`)
	path := filepath.Join(sensorDir, "example-steps-load.yaml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := sensor.Load(path, newValidator(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := len(s.Execution.Steps); got != 1 {
		t.Fatalf("Execution.Steps len = %d, want 1", got)
	}
	if got := s.Execution.Steps[0].Type; got != "shell" {
		t.Fatalf("Type = %q, want shell", got)
	}
	if got := s.Execution.Steps[0].ID; got != "ping" {
		t.Fatalf("ID = %q, want ping", got)
	}
	if got := s.Execution.Steps[0].Run; got != "echo hi" {
		t.Fatalf("Run = %q, want %q", got, "echo hi")
	}
	if got := s.Execution.Steps[0].ExitCodeMap["0"]; got != "pass" {
		t.Fatalf("ExitCodeMap[0] = %q, want pass", got)
	}
}

// TestLoad_CommandShape exercises the legacy command shortcut: load
// normalizes it into a single shell step in memory while preserving
// Command on the Execution struct (round-trip leaves the on-disk YAML
// untouched; this test asserts the normalization).
func TestLoad_CommandShape(t *testing.T) {
	dir := t.TempDir()
	sensorDir := filepath.Join(dir, ".harness", "sensors")
	if err := os.MkdirAll(sensorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`
id: example-command-load
version: 0.1.0
name: example
description: x
kind: assertion
type: computational
regulation: maintainability
phase: on-demand
determinism: high
output: single
cost:
  class: cheap
  compute:
    cpu: low
    memory_mb: 64
  latency:
    p50_ms: 10
    p95_ms: 100
    timeout_ms: 5000
triggers:
  - "on": manual
use_cases:
  - fake-uc
execution:
  command: "true"
  exit_code_map:
    - exit_code: 0
      verdict: pass
      severity: info
    - exit_code: 1
      verdict: fail
      severity: high
`)
	path := filepath.Join(sensorDir, "example-command-load.yaml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := sensor.Load(path, newValidator(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := s.Execution.Command; got != "true" {
		t.Fatalf("Execution.Command = %q, want %q", got, "true")
	}
	if got := len(s.Execution.Steps); got != 1 {
		t.Fatalf("normalized Execution.Steps len = %d, want 1", got)
	}
	step := s.Execution.Steps[0]
	if step.ID != "main" {
		t.Fatalf("normalized step ID = %q, want main", step.ID)
	}
	if step.Type != "shell" {
		t.Fatalf("normalized step Type = %q, want shell", step.Type)
	}
	if step.Run != "true" {
		t.Fatalf("normalized step Run = %q, want %q", step.Run, "true")
	}
	if got := step.ExitCodeMap["0"]; got != "pass" {
		t.Fatalf("normalized step ExitCodeMap[0] = %q, want pass", got)
	}
	if got := step.ExitCodeMap["1"]; got != "fail" {
		t.Fatalf("normalized step ExitCodeMap[1] = %q, want fail", got)
	}
}

// loadFromString is a small helper for the use_cases coverage tests: it
// writes body into a temp sensor file and runs sensor.Load against it.
func loadFromString(t *testing.T, body []byte) (*sensor.Sensor, error) {
	t.Helper()
	dir := t.TempDir()
	sensorDir := filepath.Join(dir, ".harness", "sensors")
	if err := os.MkdirAll(sensorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sensorDir, "x.yaml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return sensor.Load(path, newValidator(t))
}

func TestLoadAcceptsUseCases(t *testing.T) {
	yamlBody := []byte(`
id: x
version: 1.0.0
name: x
description: x
kind: assertion
type: computational
regulation: maintainability
phase: on-demand
determinism: high
output: single
cost:
  class: cheap
  compute: { cpu: low, memory_mb: 64 }
  latency: { p50_ms: 10, p95_ms: 100, timeout_ms: 5000 }
triggers:
  - "on": manual
execution:
  command: "true"
  exit_code_map:
    - { exit_code: 0, verdict: pass, severity: info }
use_cases:
  - sample-uc
`)
	s, err := loadFromString(t, yamlBody)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.UseCases) != 1 || s.UseCases[0] != "sample-uc" {
		t.Fatalf("UseCases not preserved: %#v", s.UseCases)
	}
}

func TestLoadRejectsEmptyUseCases(t *testing.T) {
	yamlBody := []byte(`
id: x
version: 1.0.0
name: x
description: x
kind: assertion
type: computational
regulation: maintainability
phase: on-demand
determinism: high
output: single
cost:
  class: cheap
  compute: { cpu: low, memory_mb: 64 }
  latency: { p50_ms: 10, p95_ms: 100, timeout_ms: 5000 }
triggers:
  - "on": manual
execution:
  command: "true"
  exit_code_map:
    - { exit_code: 0, verdict: pass, severity: info }
use_cases: []
`)
	if _, err := loadFromString(t, yamlBody); err == nil {
		t.Fatal("expected schema error for empty use_cases, got nil")
	}
}

func TestLoadRejectsLegacyVerificationField(t *testing.T) {
	yamlBody := []byte(`
id: x
version: 1.0.0
name: x
description: x
kind: assertion
type: computational
regulation: maintainability
phase: on-demand
determinism: high
output: single
cost:
  class: cheap
  compute: { cpu: low, memory_mb: 64 }
  latency: { p50_ms: 10, p95_ms: 100, timeout_ms: 5000 }
triggers:
  - "on": manual
execution:
  command: "true"
  exit_code_map:
    - { exit_code: 0, verdict: pass, severity: info }
use_cases: [foo]
verification:
  golden_cases:
    - { fixture: x, expected_verdict: pass, expected_severity: info }
`)
	if _, err := loadFromString(t, yamlBody); err == nil {
		t.Fatal("expected schema error for legacy verification field, got nil")
	}
}
