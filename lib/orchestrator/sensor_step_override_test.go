package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// TestRunViaEngine_FixtureOverrideMergedIntoPool verifies the fixture
// override map is merged into the sub-run's fixture pool by runViaEngine.
// The override entry names a fixture under a name that auto-discovery
// would NOT surface (the file lives outside .harness/fixtures/), so the
// child shell step's ${{ fixtures.parent-only.json }} accessor resolves
// only when the override threading is in place.
func TestRunViaEngine_FixtureOverrideMergedIntoPool(t *testing.T) {
	proj := t.TempDir()
	schemasDir := schematest.RepoSchemasDir(t)
	v, _ := schema.LoadValidator(schemasDir, os.Stderr)

	// Fixture file lives outside .harness/fixtures/ so fixture.Discover
	// cannot surface it. Only the explicit override entry can put it in
	// the sub-run's pool.
	fxPath := filepath.Join(proj, "out-of-tree.json")
	if err := os.WriteFile(fxPath, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	typed := &sensor.Sensor{
		ID:      "child",
		Version: "0.0.0",
		Output:  sensor.OutputSingle,
		Execution: sensor.Execution{
			Steps: []sensor.StepConfig{
				{
					ID:   "render-fixture",
					Type: "shell",
					Run:  "cat ${{ fixtures.parent-only.json }}",
				},
			},
		},
	}

	var stdout bytes.Buffer
	res := runViaEngine(
		context.Background(),
		typed,
		proj, "", v, nil, &stdout,
		map[string]string{"parent-only.json": fxPath},
		nil,
	)
	if res.EngineError != nil {
		t.Fatalf("engine error: %v", res.EngineError)
	}
	if res.Verdict != "pass" {
		t.Fatalf("verdict = %q (want pass) -- fixture override not merged into pool; stdout=%s", res.Verdict, stdout.String())
	}
}

// TestRunViaEngine_EnvOverrideMergedIntoSealedSnapshot verifies the env
// override map is merged into the sealed env snapshot. The override
// declares an env var (HARNESS_TEST_OVERRIDE_VAR) the orchestrator process
// itself is guaranteed not to inherit; the child shell step references it
// via ${{ env.<name> }} and would error if the override were dropped.
func TestRunViaEngine_EnvOverrideMergedIntoSealedSnapshot(t *testing.T) {
	proj := t.TempDir()
	schemasDir := schematest.RepoSchemasDir(t)
	v, _ := schema.LoadValidator(schemasDir, os.Stderr)

	const overrideKey = "HARNESS_TEST_OVERRIDE_VAR"
	const overrideVal = "value-from-parent"
	// Defensive: ensure the test environment hasn't leaked the same var
	// from a prior run.
	if os.Getenv(overrideKey) != "" {
		t.Fatalf("test prerequisite: %s must not be set", overrideKey)
	}

	typed := &sensor.Sensor{
		ID:      "child",
		Version: "0.0.0",
		Output:  sensor.OutputSingle,
		Execution: sensor.Execution{
			Steps: []sensor.StepConfig{
				{
					ID:   "render-env",
					Type: "shell",
					// echo the override; non-zero only if rendering fails.
					Run: "echo ${{ env." + overrideKey + " }}",
				},
			},
		},
	}

	var stdout bytes.Buffer
	res := runViaEngine(
		context.Background(),
		typed,
		proj, "", v, nil, &stdout,
		nil,
		map[string]string{overrideKey: overrideVal},
	)
	if res.EngineError != nil {
		t.Fatalf("engine error: %v", res.EngineError)
	}
	if res.Verdict != "pass" {
		t.Fatalf("verdict = %q (want pass) -- env override not merged into sealed snapshot; stdout=%s", res.Verdict, stdout.String())
	}
}

// TestRunOneWithRootCaptureOverride_EndToEnd drives the full orchestrator
// path with overrides supplied at the public entry point — the same path
// the engine's newSubrunFunc uses for type: sensor sub-runs. The child
// sensor uses ${{ env.<override-key> }} in its shell step; the aggregate
// verdict is pass only when the override threads through every layer.
func TestRunOneWithRootCaptureOverride_EndToEnd(t *testing.T) {
	proj := t.TempDir()
	schemasDir := schematest.RepoSchemasDir(t)
	v, _ := schema.LoadValidator(schemasDir, os.Stderr)

	const overrideKey = "HARNESS_TEST_OVERRIDE_E2E"
	const overrideVal = "from-parent-override"
	if os.Getenv(overrideKey) != "" {
		t.Fatalf("test prerequisite: %s must not be set", overrideKey)
	}

	sensorsDir := filepath.Join(proj, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
      "id": "child",
      "version": "0.0.0",
      "name": "child",
      "description": "uses env override threaded from parent step",
      "kind": "observation",
      "type": "computational",
      "regulation": "maintainability",
      "phase": "on-demand",
      "determinism": "high",
      "output": "single",
      "triggers": [{"on": "manual"}],
      "cost": {
        "class": "cheap",
        "compute": {"cpu": "low", "memory_mb": 32},
        "latency": {"p50_ms": 10, "p95_ms": 50, "timeout_ms": 5000}
      },
      "execution": {
        "steps": [
          {"id": "echo-override", "type": "shell", "run": "echo ${{ env.` + overrideKey + ` }}"}
        ]
      },
      "verification": {
        "golden_cases": [
          {"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}
        ]
      }
    }`)
	path := filepath.Join(sensorsDir, "child.yaml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	var asMap map[string]interface{}
	if err := json.Unmarshal(body, &asMap); err != nil {
		t.Fatal(err)
	}
	child := Sensor{ID: "child", Path: path, JSON: asMap}

	var stdout, stderr bytes.Buffer
	sig, code := RunOneWithRootCaptureOverride(
		context.Background(), child, proj, schemasDir, v, nil,
		&stdout, &stderr,
		nil,
		map[string]string{overrideKey: overrideVal},
	)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if sig == nil {
		t.Fatal("nil aggregate signal")
	}
	verdict, _ := sig["verdict"].(string)
	if verdict != "pass" {
		t.Fatalf("aggregate verdict = %q (want pass); stderr=%s", verdict, stderr.String())
	}
}

// TestRunOneWithRootCaptureOverride_FixtureOverride proves the override
// fixture map reaches the sub-run's pool through the public entry point.
// The fixture lives outside .harness/fixtures/ so auto-discovery cannot
// surface it; only the override map can put it in the child's pool.
func TestRunOneWithRootCaptureOverride_FixtureOverride(t *testing.T) {
	proj := t.TempDir()
	schemasDir := schematest.RepoSchemasDir(t)
	v, _ := schema.LoadValidator(schemasDir, os.Stderr)

	// Fixture under a sibling tree of the project. Not discoverable.
	external := t.TempDir()
	fxPath := filepath.Join(external, "parent-only.json")
	if err := os.WriteFile(fxPath, []byte(`{"y":2}`), 0o644); err != nil {
		t.Fatal(err)
	}

	sensorsDir := filepath.Join(proj, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
      "id": "child",
      "version": "0.0.0",
      "name": "child",
      "description": "uses fixture override threaded from parent step",
      "kind": "observation",
      "type": "computational",
      "regulation": "maintainability",
      "phase": "on-demand",
      "determinism": "high",
      "output": "single",
      "triggers": [{"on": "manual"}],
      "cost": {
        "class": "cheap",
        "compute": {"cpu": "low", "memory_mb": 32},
        "latency": {"p50_ms": 10, "p95_ms": 50, "timeout_ms": 5000}
      },
      "execution": {
        "steps": [
          {"id": "render", "type": "shell", "run": "cat ${{ fixtures.parent-only.json }}"}
        ]
      },
      "verification": {
        "golden_cases": [
          {"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}
        ]
      }
    }`)
	path := filepath.Join(sensorsDir, "child.yaml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	var asMap map[string]interface{}
	if err := json.Unmarshal(body, &asMap); err != nil {
		t.Fatal(err)
	}
	child := Sensor{ID: "child", Path: path, JSON: asMap}

	var stdout, stderr bytes.Buffer
	sig, code := RunOneWithRootCaptureOverride(
		context.Background(), child, proj, schemasDir, v, nil,
		&stdout, &stderr,
		map[string]string{"parent-only.json": fxPath},
		nil,
	)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if sig == nil {
		t.Fatal("nil aggregate signal")
	}
	verdict, _ := sig["verdict"].(string)
	if verdict != "pass" {
		// Surface the engine error and step stderr to make the failure
		// mode obvious if the fixture pool merge regresses.
		t.Fatalf("aggregate verdict = %q (want pass); stdout=%s stderr=%s",
			verdict, stdout.String(), stderr.String())
	}
}

