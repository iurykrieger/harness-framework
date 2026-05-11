package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// makeSensorPath creates <tmp>/sensors/<id>.json so that RunOne's
// projectRoot derivation (filepath.Dir(filepath.Dir(s.Path))) resolves to
// tmp — a t.TempDir() — preventing .runtime from leaking into the repo tree.
func makeSensorPath(t *testing.T, id string) string {
	t.Helper()
	tmp := t.TempDir()
	sensorsDir := filepath.Join(tmp, "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(sensorsDir, id+".json")
}

func TestRunOne_SimpleNoLifecycle(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	s := Sensor{ID: "smoke-comp", Path: makeSensorPath(t, "smoke-comp"), JSON: roundTripJSON(t, testfixtures.ValidSensorComputational())}

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
	s := Sensor{ID: js["id"].(string), Path: makeSensorPath(t, js["id"].(string)), JSON: js}

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
	s := Sensor{ID: js["id"].(string), Path: makeSensorPath(t, js["id"].(string)), JSON: js}

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
	s := Sensor{ID: js["id"].(string), Path: makeSensorPath(t, js["id"].(string)), JSON: js}

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
	s := Sensor{ID: js["id"].(string), Path: makeSensorPath(t, js["id"].(string)), JSON: js}

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
	s := Sensor{ID: js["id"].(string), Path: makeSensorPath(t, js["id"].(string)), JSON: js}

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
	s := Sensor{ID: js["id"].(string), Path: makeSensorPath(t, js["id"].(string)), JSON: js}

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

	s := Sensor{ID: js["id"].(string), Path: makeSensorPath(t, js["id"].(string)), JSON: js}
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

func TestRunOne_GateFailure_Tool(t *testing.T) {
	// Stub LookupEnvFn to neutralize any env requirements (none in this fixture, but be defensive).
	prevLookup := sensor.LookupEnvFn
	sensor.LookupEnvFn = func(string) (string, bool) { return "", true }
	t.Cleanup(func() { sensor.LookupEnvFn = prevLookup })

	tmp := t.TempDir()
	sensorsDir := filepath.Join(tmp, "sensors")
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
	sig, code := RunOne(context.Background(), s, "", nil, &stdout, &stderr)
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
	sensorsDir := filepath.Join(tmp, "sensors")
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
	sig, _ := RunOne(context.Background(), s, "", nil, &stdout, &stderr)
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
	sensorsDir := filepath.Join(tmp, "sensors")
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
	sig, _ := RunOne(context.Background(), s, "", nil, &stdout, &stderr)
	if sig["verdict"] != "error" {
		t.Fatalf("verdict = %v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if hh, _ := md["heal_hint"].(string); !strings.HasPrefix(hh, "missing-env:") {
		t.Errorf("heal_hint = %v", md["heal_hint"])
	}
}

func TestRunOne_PersistsAggregateAndStdoutMatchesSignalsLog(t *testing.T) {
	tmp := t.TempDir()
	sensorsDir := filepath.Join(tmp, "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sensorPath := filepath.Join(sensorsDir, "echo-stream.json")
	s := Sensor{
		ID:   "echo-stream",
		Path: sensorPath,
		JSON: map[string]interface{}{
			"id":      "echo-stream",
			"version": "0.0.1",
			"type":    "computational",
			"output":  "stream",
			"execution": map[string]interface{}{
				"command": `printf "PASS line\n"`,
				"exit_code_map": []interface{}{
					map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
				},
				"output_parsing": map[string]interface{}{
					"patterns": []interface{}{
						map[string]interface{}{"regex": "PASS", "verdict": "pass", "severity": "info"},
					},
				},
			},
			"cost": map[string]interface{}{
				"class":   "cheap",
				"latency": map[string]interface{}{"p50_ms": 1, "p95_ms": 5, "timeout_ms": 5000},
				"compute": map[string]interface{}{"cpu": "low", "memory_mb": 1},
			},
		},
	}
	var stdout, stderr bytes.Buffer
	_, code := RunOne(context.Background(), s, "", nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	parent := filepath.Join(tmp, ".runtime", "sensors", "echo-stream")
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no .runtime entry: err=%v entries=%v", err, entries)
	}
	sigLog := filepath.Join(parent, entries[0].Name(), "signals.log")
	fileBytes, err := os.ReadFile(sigLog)
	if err != nil {
		t.Fatalf("read signals.log: %v", err)
	}
	if !bytes.Equal(stdout.Bytes(), fileBytes) {
		t.Errorf("stdout != signals.log\nstdout=%q\nfile=%q", stdout.String(), string(fileBytes))
	}
	rawLog := filepath.Join(parent, entries[0].Name(), "raw.log")
	rawBytes, _ := os.ReadFile(rawLog)
	if !strings.Contains(string(rawBytes), "PASS line") {
		t.Errorf("raw.log missing subprocess output: %q", string(rawBytes))
	}
}

// The aggregate Signal emitted on stdout is valid JSON and the LAST line.
func TestRunOne_OutputIsValidJSON(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	s := Sensor{ID: "smoke-comp", Path: makeSensorPath(t, "smoke-comp"), JSON: roundTripJSON(t, testfixtures.ValidSensorComputational())}

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
