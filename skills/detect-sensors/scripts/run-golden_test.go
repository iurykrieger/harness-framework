//go:build run_golden

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// pluginRootForTest returns the plugin checkout's absolute path. The
// test must point CLAUDE_PLUGIN_ROOT at it so run-golden's go-run
// invocation of the standard runner resolves the right module.
// skills/detect-sensors/scripts is three directory levels below the
// plugin root.
func pluginRootForTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(wd)))
}

// writeProjectSensor materializes a sensor YAML at the canonical
// <root>/.harness/sensors/<id>.yaml location and returns its absolute
// path. The runner derives the project root by walking up two levels
// from this path, so the canonical layout is load-bearing.
func writeProjectSensor(t *testing.T, root, id, body string) string {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sensors: %v", err)
	}
	path := filepath.Join(dir, id+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write sensor: %v", err)
	}
	return path
}

// TestRunGolden_PassesOnHappyPath exercises a trivial computational
// sensor whose command exits 0 and whose single golden case expects
// verdict=pass / severity=info. runGolden must invoke the runner,
// parse the aggregate Signal, find the verdict matches the expectation,
// and return 0.
func TestRunGolden_PassesOnHappyPath(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))

	proj := t.TempDir()
	body := `id: trivial-pass
version: 0.1.0
name: trivial-pass
description: trivial pass
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
verification:
  golden_cases:
    - fixture: x
      expected_verdict: pass
      expected_severity: info
execution:
  command: "true"
  exit_code_map:
    - exit_code: 0
      verdict: pass
      severity: info
    - exit_code: "*"
      verdict: fail
      severity: high
`
	sensorPath := writeProjectSensor(t, proj, "trivial-pass", body)
	rc := runGolden(sensorPath)
	if rc != 0 {
		t.Fatalf("runGolden rc=%d, want 0", rc)
	}
}

// TestRunGolden_FailsOnVerdictMismatch exercises a sensor whose command
// exits 1 (mapped to fail/high) but whose golden case wrongly expects
// verdict=pass. runGolden must spot the mismatch on the aggregate
// Signal and return non-zero.
func TestRunGolden_FailsOnVerdictMismatch(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))

	proj := t.TempDir()
	body := `id: bad-verdict
version: 0.1.0
name: bad-verdict
description: bad verdict
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
verification:
  golden_cases:
    - fixture: x
      expected_verdict: pass
      expected_severity: info
execution:
  command: "exit 1"
  exit_code_map:
    - exit_code: 0
      verdict: pass
      severity: info
    - exit_code: "*"
      verdict: fail
      severity: high
`
	sensorPath := writeProjectSensor(t, proj, "bad-verdict", body)
	rc := runGolden(sensorPath)
	if rc == 0 {
		t.Fatalf("runGolden rc=0, want non-zero (verdict mismatch)")
	}
}
