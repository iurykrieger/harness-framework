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
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
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

func TestRunOne_SimpleNoLifecycle(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	s := Sensor{ID: "smoke-comp", JSON: roundTripJSON(t, testfixtures.ValidSensorComputational())}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, schemasDir, v, &out, &errBuf)
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
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := roundTripJSON(t, testfixtures.ValidSensorComputational())
	exec := js["execution"].(map[string]interface{})
	exec["command"] = "echo should-not-run-either"
	js["requires"] = []interface{}{
		map[string]interface{}{"kind": "step", "command": "false"},
		map[string]interface{}{"kind": "step", "command": "echo should-not-run"},
	}
	s := Sensor{ID: js["id"].(string), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, schemasDir, v, &out, &errBuf)
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
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := roundTripJSON(t, testfixtures.ValidSensorComputational())
	exec := js["execution"].(map[string]interface{})
	exec["command"] = "true"
	exec["teardown"] = []interface{}{
		map[string]interface{}{"command": "false"}, // first fails
		map[string]interface{}{"command": "true"},  // second still runs
	}
	s := Sensor{ID: js["id"].(string), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, schemasDir, v, &out, &errBuf)
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
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := roundTripJSON(t, testfixtures.ValidSensorComputational())
	exec := js["execution"].(map[string]interface{})
	exec["command"] = "false"
	exec["teardown"] = []interface{}{
		map[string]interface{}{"command": "true"},
	}
	s := Sensor{ID: js["id"].(string), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, schemasDir, v, &out, &errBuf)
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
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := roundTripJSON(t, testfixtures.ValidSensorComputational())
	exec := js["execution"].(map[string]interface{})
	// Single-mode failure whose stderr matches the env-file-absent
	// curated heal pattern. The orchestrator must surface
	// metadata.heal_hint = "env-file-absent:<excerpt>" so the heal
	// classifier's fast path can fire.
	exec["command"] = "echo 'open .env: ENOENT no such file or directory' >&2; exit 1"
	s := Sensor{ID: js["id"].(string), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, schemasDir, v, &out, &errBuf)
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
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := roundTripJSON(t, testfixtures.ValidSensorComputational())
	exec := js["execution"].(map[string]interface{})
	// Single-mode failure with stderr that does NOT match any curated
	// heal pattern. metadata.heal_hint MUST be absent — emitting it on
	// generic failures would poison the classifier.
	exec["command"] = "echo 'some unrelated error message' >&2; exit 1"
	s := Sensor{ID: js["id"].(string), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, schemasDir, v, &out, &errBuf)
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
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := roundTripJSON(t, testfixtures.ValidSensorComputational())
	exec := js["execution"].(map[string]interface{})
	// Even when stderr would match a heal pattern, a passing command
	// must NOT emit heal_hint — there is nothing to heal.
	exec["command"] = "echo 'open .env: ENOENT no such file' >&2; exit 0"
	s := Sensor{ID: js["id"].(string), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, schemasDir, v, &out, &errBuf)
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
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := roundTripJSON(t, testfixtures.ValidSensorComputational())
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

	s := Sensor{ID: js["id"].(string), JSON: js}
	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, schemasDir, v, &out, &errBuf)
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

// TestRunOne_WithRoot_CreatesAndRemovesEntry verifies the persistence
// contract of RunOneWithRoot: a <run-id>/ directory and a registry
// entry are created around the spawned subprocess, the entry is removed
// on successful exit, and the aggregate Signal is written to BOTH
// stdout and <run-id>/signals.log.
func TestRunOne_WithRoot_CreatesAndRemovesEntry(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	sensorPath := filepath.Join(proj, "sensors", "echo.json")
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
	sig, code := RunOneWithRoot(context.Background(), s, "", nil, &root, &stdout, &stderr)
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
	if err := os.MkdirAll(filepath.Join(proj, "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	sensorPath := filepath.Join(proj, "sensors", "echo.json")
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
	sig, code := RunOneWithRoot(context.Background(), s, "", nil, &root, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%s", code, stderr.String())
	}
	// Signal must still be emitted (degraded, but valid JSON).
	if sig == nil {
		t.Fatal("expected non-nil signal even on persistence failure")
	}

	// The <run-id>/ directory under .runtime/sensors/echo/ must NOT exist
	// after cleanup. Walk the sensor log dir and confirm no child dirs.
	sensorLogDir := filepath.Join(proj, ".runtime", "sensors", "echo")
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

// The aggregate Signal emitted on stdout is valid JSON and the LAST line.
func TestRunOne_OutputIsValidJSON(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	s := Sensor{ID: "smoke-comp", JSON: roundTripJSON(t, testfixtures.ValidSensorComputational())}

	var out, errBuf bytes.Buffer
	if _, code := RunOne(context.Background(), s, schemasDir, v, &out, &errBuf); code != 0 {
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
