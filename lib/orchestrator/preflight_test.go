package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func TestRunPreparePhase_NoSteps(t *testing.T) {
	target := orchestrator.Sensor{
		ID: "no-prep",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{
				"command": "true",
			},
		},
	}
	results, failed := orchestrator.RunPreparePhase(context.Background(), target, "", 1000)
	if failed {
		t.Errorf("failed: got true, want false (no prepare steps)")
	}
	if len(results) != 0 {
		t.Errorf("results: got %d entries, want 0", len(results))
	}
}

func TestRunPreparePhase_AllPass(t *testing.T) {
	target := orchestrator.Sensor{
		ID: "all-pass",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{
				"command": "true",
			},
			"requires": []interface{}{
				map[string]interface{}{"kind": "step", "command": "true"},
				map[string]interface{}{"kind": "step", "command": "true"},
			},
		},
	}
	results, failed := orchestrator.RunPreparePhase(context.Background(), target, "", 1000)
	if failed {
		t.Errorf("failed: got true, want false (all steps pass)")
	}
	if len(results) != 2 {
		t.Fatalf("results length: got %d, want 2", len(results))
	}
	for i, raw := range results {
		step, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("results[%d] not a map", i)
		}
		if step["verdict"] != "pass" {
			t.Errorf("results[%d].verdict = %v, want pass", i, step["verdict"])
		}
	}
}

func TestRunPreparePhase_NoExecution(t *testing.T) {
	target := orchestrator.Sensor{
		ID: "no-execution",
		JSON: map[string]interface{}{
			"id": "no-execution",
		},
	}
	results, failed := orchestrator.RunPreparePhase(context.Background(), target, "", 1000)
	if failed {
		t.Errorf("failed: got true, want false (no execution map)")
	}
	if results != nil {
		t.Errorf("results: got %v, want nil", results)
	}
}

func TestRunPreparePhase_FirstFails_FailFast(t *testing.T) {
	target := orchestrator.Sensor{
		ID: "first-fails",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{
				"command": "true",
			},
			"requires": []interface{}{
				map[string]interface{}{"kind": "step", "command": "false"},
				map[string]interface{}{"kind": "step", "command": "true"},
			},
		},
	}
	results, failed := orchestrator.RunPreparePhase(context.Background(), target, "", 1000)
	if !failed {
		t.Errorf("failed: got false, want true (first step is `false`)")
	}
	if len(results) != 1 {
		t.Fatalf("results length: got %d, want 1 (fail-fast should abort)", len(results))
	}
	step := results[0].(map[string]interface{})
	if step["verdict"] != "fail" {
		t.Errorf("results[0].verdict = %v, want fail", step["verdict"])
	}
}

// ---- RunDeps helpers ----

func writeSensorJSON(t *testing.T, root, id string, body map[string]interface{}) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body["id"] = id
	data, _ := json.MarshalIndent(body, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeNonBlockingDep(t *testing.T, root, id string, depsOn []string, command string) {
	t.Helper()
	s := sensortest.LoadComputational(t).AsMap()
	if len(depsOn) > 0 {
		reqs := []interface{}{}
		for _, d := range depsOn {
			reqs = append(reqs, map[string]interface{}{"kind": "sensor", "id": d})
		}
		s["requires"] = reqs
	}
	exec := s["execution"].(map[string]interface{})
	exec["command"] = command
	writeSensorJSON(t, root, id, s)
}

func writeBlockingDepFixture(t *testing.T, root, id string, depsOn []string) {
	t.Helper()
	body := map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Blocking " + id,
		"description": "blocking fixture for " + id,
		"determinism": "high",
		"kind":        "setup",
		"type":        "computational",
		"output":      "stream",
		"regulation":  "behaviour",
		"phase":       "continuous",
		"triggers":    []interface{}{map[string]interface{}{"on": "manual"}},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"},
			},
		},
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50},
		},
		"execution": map[string]interface{}{
			"command":             "while true; do echo TICK; sleep 0.1; done",
			"blocking":            true,
			"graceful_timeout_ms": 200,
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": "*", "verdict": "pass", "severity": "info"},
			},
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^TICK$", "verdict": "pass", "severity": "info"},
				},
			},
		},
	}
	if len(depsOn) > 0 {
		reqs := []interface{}{}
		for _, d := range depsOn {
			reqs = append(reqs, map[string]interface{}{"kind": "sensor", "id": d})
		}
		body["requires"] = reqs
	}
	writeSensorJSON(t, root, id, body)
}

func runDepsForTest(t *testing.T, root, targetID, holderID string, holderPID int) (*orchestrator.RunDepsResult, string, string) {
	t.Helper()
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, code := schema.LoadValidator(schemasDir, io.Discard)
	if code != 0 {
		t.Fatalf("schema validator init: code=%d", code)
	}
	var out, errBuf bytes.Buffer
	res := orchestrator.RunDeps(context.Background(), targetID, root, schemasDir, holderID, holderPID, v, &out, &errBuf)
	return res, out.String(), errBuf.String()
}

// ---- RunDeps tests ----

func TestRunDeps_NoDeps(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingDep(t, root, "lone", nil, "true")

	res, _, _ := runDepsForTest(t, root, "lone", "lone", os.Getpid())
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode: got %d, want 0", res.ExitCode)
	}
	if len(res.Order) != 1 || res.Order[0].ID != "lone" {
		t.Errorf("Order: got %v, want [lone]", res.Order)
	}
	if len(res.LiveStack) != 0 {
		t.Errorf("LiveStack: got %v, want []", res.LiveStack)
	}
	if res.CascadeSig != nil {
		t.Errorf("CascadeSig: got non-nil, want nil")
	}
}

func TestRunDeps_SetupDepPASS(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingDep(t, root, "setup", nil, "true")
	writeNonBlockingDep(t, root, "target", []string{"setup"}, "true")

	res, stdout, _ := runDepsForTest(t, root, "target", "target", os.Getpid())
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode: got %d, want 0", res.ExitCode)
	}
	if res.CascadeSig != nil {
		t.Fatalf("CascadeSig: got non-nil, want nil")
	}
	depSig, ok := res.Signals["setup"]
	if !ok {
		t.Fatal("Signals[setup] missing")
	}
	if depSig["verdict"] != "pass" {
		t.Errorf("setup verdict: got %v, want pass", depSig["verdict"])
	}
	if !strings.Contains(stdout, `"sensor_id":"setup"`) {
		t.Errorf("stdout should contain setup signal; got: %s", stdout)
	}
}

func TestRunDeps_SetupDepFAIL_RootCascadesViaCascadeSig(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingDep(t, root, "bad-setup", nil, "false")
	writeNonBlockingDep(t, root, "target", []string{"bad-setup"}, "true")

	res, stdout, _ := runDepsForTest(t, root, "target", "target", os.Getpid())
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode: got %d, want 0", res.ExitCode)
	}
	if res.CascadeSig == nil {
		t.Fatal("CascadeSig: got nil, want non-nil (root would cascade)")
	}
	md := res.CascadeSig["metadata"].(map[string]interface{})
	if md["kind"] != "cascade" {
		t.Errorf("CascadeSig.metadata.kind: got %v, want cascade", md["kind"])
	}
	if md["failed_dep_id"] != "bad-setup" {
		t.Errorf("CascadeSig.metadata.failed_dep_id: got %v, want bad-setup", md["failed_dep_id"])
	}
	// CascadeSig should NOT have been emitted to stdout.
	if strings.Contains(stdout, `"kind":"cascade"`) {
		t.Errorf("root cascade should NOT be on stdout, but found: %s", stdout)
	}
}

func TestRunDeps_BlockingDepStartFresh(t *testing.T) {
	root := t.TempDir()
	writeBlockingDepFixture(t, root, "blocking-tick", nil)
	writeNonBlockingDep(t, root, "target", []string{"blocking-tick"}, "true")

	const customHolderPID = 12321
	res, _, _ := runDepsForTest(t, root, "target", "target", customHolderPID)
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode: got %d, want 0", res.ExitCode)
	}
	if got := res.LiveStack; len(got) != 1 || got[0].ID != "blocking-tick" {
		t.Errorf("LiveStack: got %v, want [blocking-tick]", got)
	}

	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	entry := rs.FindEntry("blocking-tick")
	if entry == nil {
		t.Fatal("expected entry in registry, got nil")
	}
	if len(entry.HeldBy) != 1 || entry.HeldBy[0].PID != customHolderPID {
		t.Errorf("HeldBy: got %+v, want one (id=target, pid=%d)", entry.HeldBy, customHolderPID)
	}

	// Cleanup: detach all live deps in reverse.
	v, _ := schema.LoadValidator(testfixtures.RepoSchemasDir(t), io.Discard)
	for i := len(res.LiveStack) - 1; i >= 0; i-- {
		orchestrator.DetachLiveDep(res.LiveStack[i], root, "target", v, io.Discard, io.Discard)
	}
}

func TestRunDeps_DAGCycle(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingDep(t, root, "a", []string{"b"}, "true")
	writeNonBlockingDep(t, root, "b", []string{"a"}, "true")

	res, _, errBuf := runDepsForTest(t, root, "a", "a", os.Getpid())
	if res.ExitCode != 1 {
		t.Errorf("ExitCode: got %d, want 1", res.ExitCode)
	}
	if !strings.Contains(errBuf, "cycle") {
		t.Errorf("stderr should mention cycle; got: %s", errBuf)
	}
}

func TestRunDeps_DepFileMissing(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingDep(t, root, "target", []string{"ghost"}, "true")

	res, _, _ := runDepsForTest(t, root, "target", "target", os.Getpid())
	if res.ExitCode != 1 {
		t.Errorf("ExitCode: got %d, want 1", res.ExitCode)
	}
}

func TestRunDeps_TransitiveCascade(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingDep(t, root, "leaf-fail", nil, "false")
	writeNonBlockingDep(t, root, "middle", []string{"leaf-fail"}, "true")
	writeNonBlockingDep(t, root, "target", []string{"middle"}, "true")

	res, stdout, _ := runDepsForTest(t, root, "target", "target", os.Getpid())
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode: got %d, want 0", res.ExitCode)
	}
	// middle should have been cascade-skipped and emitted to stdout.
	if !strings.Contains(stdout, `"sensor_id":"middle"`) || !strings.Contains(stdout, `"kind":"cascade"`) {
		t.Errorf("expected middle's cascade signal on stdout; got: %s", stdout)
	}
	// target's cascade is in CascadeSig (not emitted).
	if res.CascadeSig == nil {
		t.Fatal("target should have CascadeSig (its dep middle cascaded)")
	}
}

