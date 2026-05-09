package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema"
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
	exec["prepare"] = []interface{}{
		map[string]interface{}{"command": "false"},
		map[string]interface{}{"command": "echo should-not-run"},
	}
	exec["command"] = "echo should-not-run-either"
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
