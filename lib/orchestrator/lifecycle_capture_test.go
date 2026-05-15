package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
)

// TestRunOneWithRootCapture_DoesNotEmitAggregate verifies the new
// capture variant returns the aggregate Signal without writing it to
// stdout. Individual Signals (none in single-mode) are unaffected.
func TestRunOneWithRootCapture_DoesNotEmitAggregate(t *testing.T) {
	proj := t.TempDir()
	sensorsDir := filepath.Join(proj, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(sensorsDir, "echo.yaml"), []byte(`{
		"id": "echo", "version": "0.0.0", "kind": "observation",
		"type": "computational", "output": "single",
		"cost": {"compute": "low", "latency": {"timeout_ms": 5000}},
		"execution": {"command": "echo hi", "exit_code_map": [{"exit_code": 0, "verdict": "pass", "severity": "info"}]}
	}`), 0o644)

	schemasDir := schematest.RepoSchemasDir(t)
	v, code := schema.LoadValidator(schemasDir, os.Stderr)
	if code != 0 {
		t.Fatalf("validator init: code=%d", code)
	}
	rt := registry.NewRoot(proj)

	// Build orchestrator.Sensor directly from the file the test wrote.
	sensorJSON := mustLoadJSON(t, filepath.Join(sensorsDir, "echo.yaml"))
	s := orchestrator.Sensor{ID: "echo", JSON: sensorJSON}

	var stdout, stderr bytes.Buffer
	sig, exit := orchestrator.RunOneWithRootCapture(context.Background(), s, proj, schemasDir, v, &rt, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if sig == nil {
		t.Fatal("expected non-nil aggregate Signal")
	}
	if sig["verdict"] != "pass" {
		t.Errorf("verdict = %v, want pass", sig["verdict"])
	}

	// stdout MUST NOT contain the aggregate. Single-mode emits nothing
	// else, so stdout should be empty (or whitespace).
	if strings.TrimSpace(stdout.String()) != "" {
		t.Errorf("RunOneWithRootCapture emitted to stdout (must not):\n%s", stdout.String())
	}
}

func mustLoadJSON(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
