package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

// roundTripJSON normalises a sensor fixture so that numeric literals
// become float64 (as they would be after json.Unmarshal of the on-disk
// instance), matching what production loaders see.
func roundTripJSON(t *testing.T, in map[string]interface{}) map[string]interface{} {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// makeSensorPath creates <tmp>/sensors/<id>.json so that RunOne's
// projectRoot derivation (filepath.Dir(filepath.Dir(s.Path))) resolves to
// tmp — a t.TempDir() — preventing .runtime from leaking into the repo tree.
func makeSensorPath(t *testing.T, id string) string {
	t.Helper()
	tmp := t.TempDir()
	sensorsDir := filepath.Join(tmp, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(sensorsDir, id+".json")
}

func TestRunOne_SimpleNoLifecycle(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	s := Sensor{ID: "smoke-comp", Path: makeSensorPath(t, "smoke-comp"), JSON: roundTripJSON(t, sensortest.LoadComputational(t).AsMap())}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, filepath.Dir(filepath.Dir(s.Path)), schemasDir, v, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	if sig["verdict"] != "pass" {
		t.Fatalf("verdict=%v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if _, ok := md["lifecycle"]; ok {
		t.Fatal("metadata.lifecycle should be absent when prepare/teardown both empty")
	}
}

func TestRunOne_PrepareFailFast(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := roundTripJSON(t, sensortest.LoadComputational(t).AsMap())
	exec := js["execution"].(map[string]interface{})
	exec["command"] = "echo should-not-run-either"
	js["requires"] = []interface{}{
		map[string]interface{}{"kind": "step", "command": "false"},
		map[string]interface{}{"kind": "step", "command": "echo should-not-run"},
	}
	s := Sensor{ID: js["id"].(string), Path: makeSensorPath(t, js["id"].(string)), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, filepath.Dir(filepath.Dir(s.Path)), schemasDir, v, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if sig["verdict"] != "error" {
		t.Fatalf("expected verdict=error, got %v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	lc := md["lifecycle"].(map[string]interface{})
	prep := lc["prepare"].([]interface{})
	if len(prep) != 1 {
		t.Fatalf("expected 1 prepare step (fail-fast), got %d", len(prep))
	}
	first := prep[0].(map[string]interface{})
	if first["verdict"] != "fail" {
		t.Errorf("first prepare verdict = %v", first["verdict"])
	}
	if !strings.Contains(out.String(), `"sensor_id":"smoke-comp"`) {
		t.Error("expected aggregate Signal on stdout")
	}
}

func TestRunOne_TeardownBestEffort(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := roundTripJSON(t, sensortest.LoadComputational(t).AsMap())
	exec := js["execution"].(map[string]interface{})
	exec["command"] = "true"
	exec["teardown"] = []interface{}{
		map[string]interface{}{"command": "false"}, // first fails
		map[string]interface{}{"command": "true"},  // second still runs
	}
	s := Sensor{ID: js["id"].(string), Path: makeSensorPath(t, js["id"].(string)), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, filepath.Dir(filepath.Dir(s.Path)), schemasDir, v, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	// Teardown failure does NOT downgrade aggregate verdict.
	if sig["verdict"] != "pass" {
		t.Fatalf("aggregate verdict=%v, want pass (teardown failures are warn evidence only)", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	lc := md["lifecycle"].(map[string]interface{})
	td := lc["teardown"].([]interface{})
	if len(td) != 2 {
		t.Fatalf("expected 2 teardown steps, got %d (best-effort means all run)", len(td))
	}
	first := td[0].(map[string]interface{})
	second := td[1].(map[string]interface{})
	if first["verdict"] != "warn" {
		t.Errorf("first teardown verdict = %v, want warn", first["verdict"])
	}
	if second["verdict"] != "pass" {
		t.Errorf("second teardown verdict = %v, want pass", second["verdict"])
	}
}

func TestRunOne_TeardownRunsAfterCommandFail(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := roundTripJSON(t, sensortest.LoadComputational(t).AsMap())
	exec := js["execution"].(map[string]interface{})
	exec["command"] = "false"
	exec["teardown"] = []interface{}{
		map[string]interface{}{"command": "true"},
	}
	s := Sensor{ID: js["id"].(string), Path: makeSensorPath(t, js["id"].(string)), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, filepath.Dir(filepath.Dir(s.Path)), schemasDir, v, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	md := sig["metadata"].(map[string]interface{})
	lc, ok := md["lifecycle"].(map[string]interface{})
	if !ok {
		t.Fatal("expected metadata.lifecycle to exist")
	}
	td := lc["teardown"].([]interface{})
	if len(td) != 1 || td[0].(map[string]interface{})["verdict"] != "pass" {
		t.Fatalf("teardown should still run after command fail; got %v", td)
	}
}

func TestRunOne_HealHintEmittedOnStderrPattern(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := roundTripJSON(t, sensortest.LoadComputational(t).AsMap())
	exec := js["execution"].(map[string]interface{})
	// Single-mode failure whose stderr matches the env-file-absent
	// curated heal pattern. The orchestrator must surface
	// metadata.heal_hint = "env-file-absent:<excerpt>" so the heal
	// classifier's fast path can fire.
	exec["command"] = "echo 'open .env: ENOENT no such file or directory' >&2; exit 1"
	s := Sensor{ID: js["id"].(string), Path: makeSensorPath(t, js["id"].(string)), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, filepath.Dir(filepath.Dir(s.Path)), schemasDir, v, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	if sig["verdict"] != "fail" {
		t.Fatalf("expected verdict=fail, got %v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	hint, ok := md["heal_hint"].(string)
	if !ok {
		t.Fatalf("expected metadata.heal_hint to be set; metadata=%v", md)
	}
	if !strings.HasPrefix(hint, "env-file-absent:") {
		t.Fatalf("heal_hint = %q, want prefix %q", hint, "env-file-absent:")
	}
	if len(hint) > 120+len("env-file-absent:")+1 {
		t.Fatalf("heal_hint excerpt should be truncated to ~120 chars, got %d: %q", len(hint), hint)
	}
}

func TestRunOne_HealHintAbsentOnBenignFailure(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := roundTripJSON(t, sensortest.LoadComputational(t).AsMap())
	exec := js["execution"].(map[string]interface{})
	// Single-mode failure with stderr that does NOT match any curated
	// heal pattern. metadata.heal_hint MUST be absent — emitting it on
	// generic failures would poison the classifier.
	exec["command"] = "echo 'some unrelated error message' >&2; exit 1"
	s := Sensor{ID: js["id"].(string), Path: makeSensorPath(t, js["id"].(string)), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, filepath.Dir(filepath.Dir(s.Path)), schemasDir, v, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	if sig["verdict"] != "fail" {
		t.Fatalf("expected verdict=fail, got %v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if _, ok := md["heal_hint"]; ok {
		t.Fatalf("metadata.heal_hint should be absent on benign stderr; metadata=%v", md)
	}
}

func TestRunOne_HealHintAbsentOnPassingCommand(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := roundTripJSON(t, sensortest.LoadComputational(t).AsMap())
	exec := js["execution"].(map[string]interface{})
	// Even when stderr would match a heal pattern, a passing command
	// must NOT emit heal_hint — there is nothing to heal.
	exec["command"] = "echo 'open .env: ENOENT no such file' >&2; exit 0"
	s := Sensor{ID: js["id"].(string), Path: makeSensorPath(t, js["id"].(string)), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, filepath.Dir(filepath.Dir(s.Path)), schemasDir, v, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	if sig["verdict"] != "pass" {
		t.Fatalf("expected verdict=pass, got %v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if _, ok := md["heal_hint"]; ok {
		t.Fatalf("metadata.heal_hint should be absent on pass; metadata=%v", md)
	}
}

// TestRunOne_AbortsOnMissingRequiresEnv verifies the orchestrator
// enforces sensor.requires.env BEFORE running prepare/command/teardown.
// When a non-optional env var is missing, RunOne emits a single
// verdict=error/severity=high aggregate Signal whose evidence rationale
// matches the per-var format from rule_missing_env, and never spawns
// the subprocess.
func TestRunOne_AbortsOnMissingRequiresEnv(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := roundTripJSON(t, sensortest.LoadComputational(t).AsMap())
	js["requires"] = []interface{}{
		map[string]interface{}{
			"kind":        "env",
			"name":        "HARNESS_TEST_NEVER_SET",
			"description": "intentionally unset for this test",
		},
	}
	exec := js["execution"].(map[string]interface{})
	// Use a tripwire command — if the orchestrator forgets to abort,
	// this writes a file we can detect to surface the regression.
	exec["command"] = "echo SHOULD-NOT-RUN; exit 0"

	// Hard-guarantee the env var is unset for both os.LookupEnv and the
	// LookupEnvFn hook (some tests in other packages override it).
	_ = os.Unsetenv("HARNESS_TEST_NEVER_SET")
	prev := sensor.LookupEnvFn
	sensor.LookupEnvFn = func(name string) (string, bool) {
		if name == "HARNESS_TEST_NEVER_SET" {
			return "", false
		}
		return os.LookupEnv(name)
	}
	t.Cleanup(func() { sensor.LookupEnvFn = prev })

	s := Sensor{ID: js["id"].(string), Path: makeSensorPath(t, js["id"].(string)), JSON: js}
	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, filepath.Dir(filepath.Dir(s.Path)), schemasDir, v, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	if sig["verdict"] != "error" || sig["severity"] != "high" {
		t.Fatalf("verdict/severity = %v/%v, want error/high", sig["verdict"], sig["severity"])
	}
	if strings.Contains(out.String(), "SHOULD-NOT-RUN") {
		t.Fatal("subprocess was spawned despite missing required env var")
	}
	ev, _ := sig["evidence"].([]interface{})
	if len(ev) != 1 {
		t.Fatalf("expected exactly 1 evidence entry, got %d", len(ev))
	}
	first, _ := ev[0].(map[string]interface{})
	rat, _ := first["rationale"].(string)
	want := "Required environment variable HARNESS_TEST_NEVER_SET is not set: intentionally unset for this test"
	if rat != want {
		t.Fatalf("rationale = %q\nwant     = %q", rat, want)
	}
	rem, _ := sig["remediation"].(map[string]interface{})
	if rem == nil || !strings.Contains(rem["instructions"].(string), "HARNESS_TEST_NEVER_SET") {
		t.Fatalf("remediation should name missing var: %+v", rem)
	}
}

func TestRunOne_GateFailure_Tool(t *testing.T) {
	// Stub LookupEnvFn to neutralize any env requirements (none in this fixture, but be defensive).
	prevLookup := sensor.LookupEnvFn
	sensor.LookupEnvFn = func(string) (string, bool) { return "", true }
	t.Cleanup(func() { sensor.LookupEnvFn = prevLookup })

	tmp := t.TempDir()
	sensorsDir := filepath.Join(tmp, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := Sensor{
		ID:   "needs-docker",
		Path: filepath.Join(sensorsDir, "needs-docker.json"),
		JSON: map[string]interface{}{
			"id":      "needs-docker",
			"version": "0.1.0",
			"type":    "computational",
			"output":  "stream",
			"requires": []interface{}{
				map[string]interface{}{"kind": "tool", "name": "definitely-not-on-PATH-xyz-1234"},
			},
			"execution": map[string]interface{}{
				"command":       "echo should-not-run",
				"exit_code_map": []interface{}{},
				"output_parsing": map[string]interface{}{
					"patterns": []interface{}{
						map[string]interface{}{"regex": "x", "verdict": "pass", "severity": "info"},
					},
				},
			},
		},
	}

	var stdout, stderr bytes.Buffer
	sig, code := RunOne(context.Background(), s, filepath.Dir(filepath.Dir(s.Path)), "", nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if sig["verdict"] != "error" {
		t.Fatalf("verdict = %v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if hh, _ := md["heal_hint"].(string); !strings.HasPrefix(hh, "binary-not-found:") {
		t.Errorf("heal_hint = %v, want binary-not-found:* prefix", md["heal_hint"])
	}
	if strings.Count(stdout.String(), "\n") != 1 {
		t.Errorf("expected 1 stdout line, got %d (%s)", strings.Count(stdout.String(), "\n"), stdout.String())
	}
}

func TestRunOne_GateFailure_Context(t *testing.T) {
	tmp := t.TempDir()
	sensorsDir := filepath.Join(tmp, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := Sensor{
		ID:   "needs-context",
		Path: filepath.Join(sensorsDir, "needs-context.json"),
		JSON: map[string]interface{}{
			"id":      "needs-context",
			"version": "0.1.0",
			"type":    "computational",
			"output":  "stream",
			"requires": []interface{}{
				map[string]interface{}{"kind": "context", "path": "/this/path/does/not/exist/12345"},
			},
			"execution": map[string]interface{}{
				"command":       "echo should-not-run",
				"exit_code_map": []interface{}{},
				"output_parsing": map[string]interface{}{
					"patterns": []interface{}{
						map[string]interface{}{"regex": "x", "verdict": "pass", "severity": "info"},
					},
				},
			},
		},
	}
	var stdout, stderr bytes.Buffer
	sig, _ := RunOne(context.Background(), s, filepath.Dir(filepath.Dir(s.Path)), "", nil, &stdout, &stderr)
	if sig["verdict"] != "error" {
		t.Fatalf("verdict = %v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if hh, _ := md["heal_hint"].(string); !strings.HasPrefix(hh, "missing-context:") {
		t.Errorf("heal_hint = %v", md["heal_hint"])
	}
}

func TestRunOne_GateFailure_Env(t *testing.T) {
	prev := sensor.LookupEnvFn
	sensor.LookupEnvFn = func(string) (string, bool) { return "", false }
	t.Cleanup(func() { sensor.LookupEnvFn = prev })

	tmp := t.TempDir()
	sensorsDir := filepath.Join(tmp, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := Sensor{
		ID:   "needs-env",
		Path: filepath.Join(sensorsDir, "needs-env.json"),
		JSON: map[string]interface{}{
			"id":      "needs-env",
			"version": "0.1.0",
			"type":    "computational",
			"output":  "stream",
			"requires": []interface{}{
				map[string]interface{}{"kind": "env", "name": "DEFINITELY_UNSET_VAR_XYZ"},
			},
			"execution": map[string]interface{}{
				"command":       "echo should-not-run",
				"exit_code_map": []interface{}{},
				"output_parsing": map[string]interface{}{
					"patterns": []interface{}{
						map[string]interface{}{"regex": "x", "verdict": "pass", "severity": "info"},
					},
				},
			},
		},
	}
	var stdout, stderr bytes.Buffer
	sig, _ := RunOne(context.Background(), s, filepath.Dir(filepath.Dir(s.Path)), "", nil, &stdout, &stderr)
	if sig["verdict"] != "error" {
		t.Fatalf("verdict = %v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if hh, _ := md["heal_hint"].(string); !strings.HasPrefix(hh, "missing-env:") {
		t.Errorf("heal_hint = %v", md["heal_hint"])
	}
}

// TestRunOne_WithRoot_CreatesAndRemovesEntry verifies the persistence
// contract of RunOneWithRoot: a <run-id>/ directory and a registry
// entry are created around the spawned subprocess, the entry is removed
// on successful exit, and the aggregate Signal is written to BOTH
// stdout and <run-id>/signals.log.
func TestRunOne_WithRoot_CreatesAndRemovesEntry(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".harness", "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	sensorPath := filepath.Join(proj, ".harness", "sensors", "echo.json")
	if err := os.WriteFile(sensorPath, []byte(`{
      "id": "echo", "version": "0.0.0", "kind": "observation",
      "type": "computational", "output": "single",
      "cost": {"compute": "low"},
      "execution": {"command": "echo hi", "exit_code_map": [{"exit_code": 0, "verdict": "pass", "severity": "info"}]}
    }`), 0o644); err != nil {
		t.Fatal(err)
	}
	root := registry.NewRoot(proj)
	s, err := loadSensorForTest(sensorPath)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	sig, code := RunOneWithRoot(context.Background(), s, proj, "", nil, &root, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%s", code, stderr.String())
	}
	if sig["verdict"] != "pass" {
		t.Errorf("verdict=%v", sig["verdict"])
	}

	rs, _ := registry.Load(root)
	if len(rs.Entries) != 0 {
		t.Errorf("entry not removed: %+v", rs.Entries)
	}
	// The <run-id>/ directory must exist and contain signals.log with the aggregate.
	runID, _ := sig["run_id"].(string)
	if runID == "" {
		t.Fatal("aggregate signal missing run_id")
	}
	sigsPath := root.SignalsLogRun("echo", runID)
	if _, err := os.Stat(sigsPath); err != nil {
		t.Fatalf("signals.log missing at %s: %v", sigsPath, err)
	}
}

// TestRunOne_WithRoot_RegistryInsertFailureCleansUpDir verifies that when the
// registry insert fails after os.MkdirAll succeeds, the just-created <run-id>/
// directory is removed (no orphan), the aggregate Signal does not claim a
// run_id that has a corresponding on-disk artifact, and the function still
// returns a valid (non-zero-code) signal.
//
// Cleanup contract:
//   - persistOK = false due to registry insert failure → os.RemoveAll(runDir) is called
//   - runDir is cleared to "" → signals.log append is skipped
//   - envelope.RunID is NOT updated to the pid-composite → aggregate run_id is
//     the pre-spawn plain UUID from sensor.BuildEnvelope
func TestRunOne_WithRoot_RegistryInsertFailureCleansUpDir(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".harness", "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	sensorPath := filepath.Join(proj, ".harness", "sensors", "echo.json")
	if err := os.WriteFile(sensorPath, []byte(`{
      "id": "echo", "version": "0.0.0", "kind": "observation",
      "type": "computational", "output": "single",
      "cost": {"compute": "low"},
      "execution": {"command": "echo hi", "exit_code_map": [{"exit_code": 0, "verdict": "pass", "severity": "info"}]}
    }`), 0o644); err != nil {
		t.Fatal(err)
	}
	root := registry.NewRoot(proj)
	s, err := loadSensorForTest(sensorPath)
	if err != nil {
		t.Fatal(err)
	}

	// Sabotage: pre-create a DIRECTORY at the path where registry.Save
	// would write running_sensors.json. The lock file is a sibling, so the
	// lock still works, but the Save's atomic rename onto a directory
	// returns EISDIR (or equivalent), failing the registry insert.
	regFilePath := root.RegistryFile()
	if err := os.MkdirAll(regFilePath, 0o755); err != nil {
		t.Fatal("sabotage mkdir:", err)
	}

	var stdout, stderr bytes.Buffer
	sig, code := RunOneWithRoot(context.Background(), s, proj, "", nil, &root, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%s", code, stderr.String())
	}
	// Signal must still be emitted (degraded, but valid JSON).
	if sig == nil {
		t.Fatal("expected non-nil signal even on persistence failure")
	}

	// The <run-id>/ directory under .harness/runtime/echo/ must NOT exist
	// after cleanup. Walk the sensor log dir and confirm no child dirs.
	sensorLogDir := filepath.Join(proj, ".harness", "runtime", "echo")
	entries, readErr := os.ReadDir(sensorLogDir)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("unexpected error reading sensor log dir: %v", readErr)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("orphan run dir left on disk: %s", filepath.Join(sensorLogDir, e.Name()))
		}
	}

	// The aggregate run_id must NOT correspond to a pid-composite directory
	// that was created and then removed — it should be a plain UUID (the
	// pre-spawn envelope value), not the pid-prefixed composite.
	runID, _ := sig["run_id"].(string)
	if runID == "" {
		t.Fatal("aggregate signal missing run_id")
	}
	// A pid-composite looks like "<integer>-<8hex>" so its first segment
	// is a decimal number. A plain UUID starts with a hex group that can
	// overlap, but the key invariant is: no corresponding on-disk dir.
	sigsPath := root.SignalsLogRun("echo", runID)
	if _, statErr := os.Stat(sigsPath); statErr == nil {
		t.Errorf("signals.log should NOT exist when persistence failed, found: %s", sigsPath)
	}
}

// loadSensorForTest is a tiny helper to load a Sensor struct as RunOne expects.
func loadSensorForTest(path string) (Sensor, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Sensor{}, err
	}
	var j map[string]interface{}
	if err := json.Unmarshal(b, &j); err != nil {
		return Sensor{}, err
	}
	id, _ := j["id"].(string)
	return Sensor{ID: id, Path: path, JSON: j}, nil
}

// TestRunOne_SingleOutputFailure_PopulatesEvidenceFromStderr verifies
// that a single-output sensor exiting non-zero with stderr matching a
// curated heal pattern has at least one evidence[].excerpt carrying
// that stderr text. Enables the subprocess-failed rule to fire for
// standalone failing sensors.
func TestRunOne_SingleOutputFailure_PopulatesEvidenceFromStderr(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
"id": "build-fails",
"version": "1.0.0",
"name": "Build fails",
"description": "Standalone sensor that emits a 'failed to solve' line on stderr and exits 1.",
"determinism": "high",
"kind": "assertion",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "on-demand",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50,"timeout_ms":2000}
},
"execution": {
  "command": "echo 'failed to solve: process did not complete successfully: exit code: 1' 1>&2; exit 1",
  "exit_code_map": [{"exit_code":0,"verdict":"pass","severity":"info"},{"exit_code":"*","verdict":"fail","severity":"high"}]
}
}`)
	if err := os.WriteFile(filepath.Join(dir, "build-fails.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	sensorPath := filepath.Join(dir, "build-fails.json")
	s, err := loadSensorForTest(sensorPath)
	if err != nil {
		t.Fatal(err)
	}
	schemasDir := schematest.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	var stdout, stderr bytes.Buffer
	sig, code := RunOne(context.Background(), s, root, schemasDir, v, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("RunOne exit code = %d, want 0 (aggregate is fail; RunOne itself succeeds)", code)
	}
	if got, _ := sig["verdict"].(string); got != "fail" {
		t.Fatalf("verdict = %q, want fail", got)
	}

	ev, _ := sig["evidence"].([]interface{})
	foundFailedToSolve := false
	for _, raw := range ev {
		e, _ := raw.(map[string]interface{})
		if e == nil {
			continue
		}
		excerpt, _ := e["excerpt"].(string)
		if strings.Contains(excerpt, "failed to solve:") {
			foundFailedToSolve = true
			break
		}
	}
	if !foundFailedToSolve {
		t.Fatalf("evidence does not contain stderr tail; got %+v", ev)
	}
}

// The aggregate Signal emitted on stdout is valid JSON and the LAST line.
func TestRunOne_OutputIsValidJSON(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	s := Sensor{ID: "smoke-comp", Path: makeSensorPath(t, "smoke-comp"), JSON: roundTripJSON(t, sensortest.LoadComputational(t).AsMap())}

	var out, errBuf bytes.Buffer
	if _, code := RunOne(context.Background(), s, filepath.Dir(filepath.Dir(s.Path)), schemasDir, v, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(last), &got); err != nil {
		t.Fatalf("last line is not valid JSON: %v", err)
	}
	md := got["metadata"].(map[string]interface{})
	if md["kind"] != "aggregate" {
		t.Errorf("last line metadata.kind = %v, want aggregate", md["kind"])
	}
}
