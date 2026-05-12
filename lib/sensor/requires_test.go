package sensor

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// stableNow is a frozen clock used by tests that assert on signal timestamps.
func stableNow() time.Time {
	return time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
}

// withFakeEnv replaces LookupEnvFn with a map-backed lookup for the duration
// of the test, restoring the previous hook on cleanup. Tests rely on this to
// drive checkEnv without mutating the process environment.
func withFakeEnv(t *testing.T, env map[string]string) {
	t.Helper()
	prev := LookupEnvFn
	LookupEnvFn = func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
	t.Cleanup(func() { LookupEnvFn = prev })
}

// ---------------------------------------------------------------------------
// 2.1 – Gate / Failure types
// ---------------------------------------------------------------------------

func TestGate_FailedZeroValue(t *testing.T) {
	var g Gate
	if g.Failed() {
		t.Fatal("zero-value Gate should not be Failed")
	}
}

func TestGate_FailedWhenNonEmpty(t *testing.T) {
	g := Gate{Failures: []Failure{{Kind: "tool"}}}
	if !g.Failed() {
		t.Fatal("non-empty Failures should make Failed() true")
	}
}

func TestFailureFields(t *testing.T) {
	f := Failure{Kind: "tool", Identifier: "docker",
		Rationale: `Required tool "docker" is not on PATH`,
		HealShape: "binary-not-found"}
	v := reflect.ValueOf(f)
	if v.NumField() != 4 {
		t.Fatalf("Failure should have 4 fields, got %d", v.NumField())
	}
}

// ---------------------------------------------------------------------------
// 2.2 – checkTool
// ---------------------------------------------------------------------------

func TestCheckTool_MissingTool(t *testing.T) {
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "tool", "name": "docker"},
		},
	}
	opts := GateOpts{
		LookPath: func(name string) (string, error) {
			return "", errors.New("not found")
		},
	}
	g := CheckRequiresGate(sensor, opts)
	if len(g.Failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(g.Failures))
	}
	f := g.Failures[0]
	if f.Kind != "tool" {
		t.Errorf("Kind = %q, want %q", f.Kind, "tool")
	}
	if f.Identifier != "docker" {
		t.Errorf("Identifier = %q, want %q", f.Identifier, "docker")
	}
	want := `Required tool "docker" is not on PATH`
	if f.Rationale != want {
		t.Errorf("Rationale = %q, want %q", f.Rationale, want)
	}
	if f.HealShape != "binary-not-found" {
		t.Errorf("HealShape = %q, want %q", f.HealShape, "binary-not-found")
	}
}

func TestCheckTool_PresentTool(t *testing.T) {
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "tool", "name": "git"},
		},
	}
	opts := GateOpts{
		LookPath: func(name string) (string, error) {
			return "/usr/bin/git", nil
		},
	}
	g := CheckRequiresGate(sensor, opts)
	if len(g.Failures) != 0 {
		t.Fatalf("expected no failures, got %d: %+v", len(g.Failures), g.Failures)
	}
}

func TestCheckTool_NonErrNotFoundStillRegistersFailure(t *testing.T) {
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "tool", "name": "docker"},
		},
	}
	opts := GateOpts{
		LookPath: func(name string) (string, error) {
			return "", errors.New("permission denied")
		},
	}
	g := CheckRequiresGate(sensor, opts)
	if len(g.Failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(g.Failures))
	}
	if g.Failures[0].Kind != "tool" {
		t.Errorf("Kind = %q, want %q", g.Failures[0].Kind, "tool")
	}
	want := `Required tool "docker" is not on PATH`
	if g.Failures[0].Rationale != want {
		t.Errorf("Rationale = %q, want %q", g.Failures[0].Rationale, want)
	}
}

func TestCheckTool_MalformedEntriesIgnored(t *testing.T) {
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "tool"},             // no name
			map[string]interface{}{"kind": "tool", "name": ""}, // empty name
		},
	}
	opts := GateOpts{
		LookPath: func(name string) (string, error) {
			return "", errors.New("not found")
		},
	}
	g := CheckRequiresGate(sensor, opts)
	if len(g.Failures) != 0 {
		t.Fatalf("expected 0 failures, got %d", len(g.Failures))
	}
}

// ---------------------------------------------------------------------------
// 2.3 – checkContext
// ---------------------------------------------------------------------------

func TestCheckContext_MissingPath(t *testing.T) {
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "context", "path": "/nonexistent/path"},
		},
	}
	opts := GateOpts{
		Stat: func(path string) error {
			return &os.PathError{Op: "stat", Path: path, Err: os.ErrNotExist}
		},
	}
	g := CheckRequiresGate(sensor, opts)
	if len(g.Failures) != 1 {
		t.Fatalf("expected 1 failure, got %d: %+v", len(g.Failures), g.Failures)
	}
	f := g.Failures[0]
	if f.Kind != "context" {
		t.Errorf("Kind = %q, want %q", f.Kind, "context")
	}
	if f.Identifier != "/nonexistent/path" {
		t.Errorf("Identifier = %q", f.Identifier)
	}
	want := `Required context path "/nonexistent/path" does not exist`
	if f.Rationale != want {
		t.Errorf("Rationale = %q, want %q", f.Rationale, want)
	}
	if f.HealShape != "missing-context" {
		t.Errorf("HealShape = %q, want %q", f.HealShape, "missing-context")
	}
}

func TestCheckContext_Exists(t *testing.T) {
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "context", "path": "/tmp"},
		},
	}
	opts := GateOpts{
		Stat: func(path string) error { return nil },
	}
	g := CheckRequiresGate(sensor, opts)
	if len(g.Failures) != 0 {
		t.Fatalf("expected no failures, got %d", len(g.Failures))
	}
}

func TestCheckContext_NonNotExistError_CannotStatRationale(t *testing.T) {
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "context", "path": "/some/path"},
		},
	}
	opts := GateOpts{
		Stat: func(path string) error {
			return errors.New("permission denied")
		},
	}
	g := CheckRequiresGate(sensor, opts)
	if len(g.Failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(g.Failures))
	}
	f := g.Failures[0]
	if !strings.Contains(f.Rationale, "cannot stat") {
		t.Errorf("rationale should contain 'cannot stat', got %q", f.Rationale)
	}
	if f.HealShape != "missing-context" {
		t.Errorf("HealShape = %q, want %q", f.HealShape, "missing-context")
	}
}

// ---------------------------------------------------------------------------
// 2.4 – checkEnv
// ---------------------------------------------------------------------------

func TestCheckEnv_MissingNonOptionalWithDescription(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "GH_TOKEN", "description": "PAT for GitHub API"},
		},
	}
	g := CheckRequiresGate(sensor, GateOpts{})
	if len(g.Failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(g.Failures))
	}
	f := g.Failures[0]
	if f.Kind != "env" {
		t.Errorf("Kind = %q, want %q", f.Kind, "env")
	}
	if f.Identifier != "GH_TOKEN" {
		t.Errorf("Identifier = %q, want %q", f.Identifier, "GH_TOKEN")
	}
	if !strings.Contains(f.Rationale, "GH_TOKEN") || !strings.Contains(f.Rationale, "PAT for GitHub API") {
		t.Errorf("Rationale = %q", f.Rationale)
	}
	if f.HealShape != "missing-env" {
		t.Errorf("HealShape = %q, want %q", f.HealShape, "missing-env")
	}
}

func TestCheckEnv_MissingWithoutDescription(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "REGION"},
		},
	}
	g := CheckRequiresGate(sensor, GateOpts{})
	if len(g.Failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(g.Failures))
	}
	f := g.Failures[0]
	if !strings.Contains(f.Rationale, "REGION") {
		t.Errorf("Rationale = %q", f.Rationale)
	}
	if strings.Contains(f.Rationale, ": ") {
		t.Errorf("Rationale should not contain ': ' when no description, got %q", f.Rationale)
	}
}

func TestCheckEnv_OptionalMissingIsSkipped(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "DEBUG", "optional": true},
			map[string]interface{}{"kind": "env", "name": "REGION"},
		},
	}
	g := CheckRequiresGate(sensor, GateOpts{})
	if len(g.Failures) != 1 {
		t.Fatalf("expected only REGION to fail, got %d: %+v", len(g.Failures), g.Failures)
	}
	if g.Failures[0].Identifier != "REGION" {
		t.Errorf("Identifier = %q, want %q", g.Failures[0].Identifier, "REGION")
	}
}

func TestCheckEnv_MalformedEntriesIgnored(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "env"},                          // no name
			map[string]interface{}{"kind": "env", "name": ""},              // empty name
			map[string]interface{}{"kind": "env", "name": "REAL_ONE"},      // counted
			map[string]interface{}{"kind": "env", "description": "orphan"}, // missing name
		},
	}
	g := CheckRequiresGate(sensor, GateOpts{})
	if len(g.Failures) != 1 || g.Failures[0].Identifier != "REAL_ONE" {
		t.Fatalf("expected only REAL_ONE to fail, got %+v", g.Failures)
	}
}

// ---------------------------------------------------------------------------
// 2.5 – Cross-kind ordering, ignored kinds, edge cases
// ---------------------------------------------------------------------------

func TestCheckRequiresGate_OrderingToolContextEnv(t *testing.T) {
	// 6 entries: 2 tool, 2 context, 2 env — in shuffled order
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "ENV_A"},
			map[string]interface{}{"kind": "tool", "name": "tool-a"},
			map[string]interface{}{"kind": "context", "path": "/ctx/b"},
			map[string]interface{}{"kind": "tool", "name": "tool-b"},
			map[string]interface{}{"kind": "env", "name": "ENV_B"},
			map[string]interface{}{"kind": "context", "path": "/ctx/a"},
		},
	}
	opts := GateOpts{
		LookupEnv: func(name string) (string, bool) { return "", false },
		LookPath:  func(name string) (string, error) { return "", errors.New("not found") },
		Stat: func(path string) error {
			return &os.PathError{Op: "stat", Path: path, Err: os.ErrNotExist}
		},
	}
	g := CheckRequiresGate(sensor, opts)
	if len(g.Failures) != 6 {
		t.Fatalf("expected 6 failures, got %d: %+v", len(g.Failures), g.Failures)
	}
	wantIds := []string{"tool-a", "tool-b", "/ctx/b", "/ctx/a", "ENV_A", "ENV_B"}
	for i, want := range wantIds {
		if g.Failures[i].Identifier != want {
			t.Errorf("Failures[%d].Identifier = %q, want %q", i, g.Failures[i].Identifier, want)
		}
	}
}

func TestCheckRequiresGate_PermissionIgnored(t *testing.T) {
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "permission", "name": "Bash"},
			map[string]interface{}{"kind": "sensor", "id": "other-sensor"},
			map[string]interface{}{"kind": "step", "name": "setup"},
		},
	}
	g := CheckRequiresGate(sensor, GateOpts{})
	if len(g.Failures) != 0 {
		t.Fatalf("expected no failures for ignored kinds, got %d: %+v", len(g.Failures), g.Failures)
	}
}

func TestCheckRequiresGate_NoRequiresField(t *testing.T) {
	g := CheckRequiresGate(map[string]interface{}{}, GateOpts{})
	if g.Failed() {
		t.Fatal("expected no failure when no requires field")
	}
}

func TestCheckRequiresGate_EmptyRequiresArray(t *testing.T) {
	sensor := map[string]interface{}{
		"requires": []interface{}{},
	}
	g := CheckRequiresGate(sensor, GateOpts{})
	if g.Failed() {
		t.Fatal("expected no failure on empty requires array")
	}
}

// ---------------------------------------------------------------------------
// 2.6 – BuildRequiresGateSignal
// ---------------------------------------------------------------------------

func TestBuildRequiresGateSignal_Shape(t *testing.T) {
	env := Envelope{SensorID: "demo", Version: "1.2.3", RunID: "r-1", StartedAt: "2026-05-12T00:00:00Z"}
	gate := Gate{Failures: []Failure{
		{Kind: "env", Identifier: "FOO", Rationale: "Required environment variable FOO is not set", HealShape: "missing-env"},
		{Kind: "tool", Identifier: "redis-cli", Rationale: `Required tool "redis-cli" is not on PATH`, HealShape: "binary-not-found"},
	}}

	sig := BuildRequiresGateSignal(env, "stream", gate)

	if sig["sensor_id"] != "demo" {
		t.Errorf("sensor_id = %v, want demo", sig["sensor_id"])
	}
	if sig["verdict"] != "error" {
		t.Errorf("verdict = %v, want error", sig["verdict"])
	}
	if sig["severity"] != "high" {
		t.Errorf("severity = %v, want high", sig["severity"])
	}

	md, ok := sig["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata is not a map: %T", sig["metadata"])
	}
	if md["kind"] != "failed" {
		t.Errorf("metadata.kind = %v, want failed", md["kind"])
	}
	if md["cause"] != "preflight_failed" {
		t.Errorf("metadata.cause = %v, want preflight_failed", md["cause"])
	}
	if md["output_mode"] != "stream" {
		t.Errorf("metadata.output_mode = %v, want stream", md["output_mode"])
	}
	if md["heal_hint"] != "missing-env:FOO" {
		t.Errorf("metadata.heal_hint = %v, want missing-env:FOO", md["heal_hint"])
	}

	envs, ok := md["missing_envs"].([]interface{})
	if !ok || len(envs) != 1 || envs[0] != "FOO" {
		t.Errorf("metadata.missing_envs = %v, want [FOO]", md["missing_envs"])
	}
	tools, ok := md["missing_tools"].([]interface{})
	if !ok || len(tools) != 1 || tools[0] != "redis-cli" {
		t.Errorf("metadata.missing_tools = %v, want [redis-cli]", md["missing_tools"])
	}
	if _, present := md["missing_contexts"]; present {
		t.Errorf("metadata.missing_contexts should be omitted when empty, got %v", md["missing_contexts"])
	}

	ev, ok := sig["evidence"].([]interface{})
	if !ok || len(ev) != 2 {
		t.Fatalf("evidence: got %v (%T), want length 2", sig["evidence"], sig["evidence"])
	}
}

func TestBuildRequiresGateSignal_EmptyGate(t *testing.T) {
	prev := NowFn
	defer func() { NowFn = prev }()
	NowFn = stableNow

	env := Envelope{SensorID: "s1", Version: "1.0.0", RunID: "r1", StartedAt: "2026-05-08T00:00:00Z"}
	gate := Gate{}

	sig := BuildRequiresGateSignal(env, "single", gate)

	if sig["verdict"] != "error" {
		t.Errorf("verdict = %v, want error", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "failed" {
		t.Errorf("metadata.kind = %v, want failed", md["kind"])
	}
	if md["cause"] != "preflight_failed" {
		t.Errorf("metadata.cause = %v, want preflight_failed", md["cause"])
	}
	if _, hasHint := md["heal_hint"]; hasHint {
		t.Errorf("heal_hint should not be present for empty gate")
	}
	if _, present := md["missing_envs"]; present {
		t.Errorf("metadata.missing_envs should be absent for empty gate, got %v", md["missing_envs"])
	}
	if _, present := md["missing_tools"]; present {
		t.Errorf("metadata.missing_tools should be absent for empty gate, got %v", md["missing_tools"])
	}
	if _, present := md["missing_contexts"]; present {
		t.Errorf("metadata.missing_contexts should be absent for empty gate, got %v", md["missing_contexts"])
	}
	if _, hasRem := sig["remediation"]; hasRem {
		t.Errorf("remediation should be absent for empty gate")
	}
}

func TestBuildRequiresGateSignal_UnknownKindsProduceNoRemediation(t *testing.T) {
	prev := NowFn
	defer func() { NowFn = prev }()
	NowFn = stableNow

	env := Envelope{SensorID: "x", Version: "0.1.0", RunID: "r", StartedAt: "2026-05-08T00:00:00Z"}
	gate := Gate{Failures: []Failure{
		{Kind: "permission", Identifier: "Bash(rm)", Rationale: "ignored", HealShape: "n/a"},
	}}
	sig := BuildRequiresGateSignal(env, "single", gate)
	if _, has := sig["remediation"]; has {
		t.Errorf("expected no remediation key when no known-kind failures, got %v", sig["remediation"])
	}
	// metadata.heal_hint should still be present (it's derived from gate.Failures[0] regardless of Kind).
	md := sig["metadata"].(map[string]interface{})
	if md["heal_hint"] != "n/a:Bash(rm)" {
		t.Errorf("heal_hint = %v", md["heal_hint"])
	}
}

func TestBuildRequiresGateSignal_EnvOnly_OmitsToolAndContextLists(t *testing.T) {
	env := Envelope{SensorID: "demo", Version: "1.0.0", RunID: "r-1", StartedAt: "2026-05-12T00:00:00Z"}
	gate := Gate{Failures: []Failure{
		{Kind: "env", Identifier: "ONLY_ENV", Rationale: "Required environment variable ONLY_ENV is not set", HealShape: "missing-env"},
	}}

	sig := BuildRequiresGateSignal(env, "single", gate)
	md := sig["metadata"].(map[string]interface{})

	if _, present := md["missing_tools"]; present {
		t.Errorf("metadata.missing_tools should be omitted when empty, got %v", md["missing_tools"])
	}
	if _, present := md["missing_contexts"]; present {
		t.Errorf("metadata.missing_contexts should be omitted when empty, got %v", md["missing_contexts"])
	}
	if envs := md["missing_envs"].([]interface{}); len(envs) != 1 {
		t.Errorf("metadata.missing_envs = %v, want [ONLY_ENV]", envs)
	}
}

func TestBuildRequiresGateSignal_AllKinds_PopulatesAllMissingLists(t *testing.T) {
	env := Envelope{SensorID: "demo", Version: "1.0.0", RunID: "r-1", StartedAt: "2026-05-12T00:00:00Z"}
	gate := Gate{Failures: []Failure{
		{Kind: "env", Identifier: "E1", Rationale: "Required environment variable E1 is not set", HealShape: "missing-env"},
		{Kind: "tool", Identifier: "T1", Rationale: `Required tool "T1" is not on PATH`, HealShape: "binary-not-found"},
		{Kind: "context", Identifier: "/missing/path", Rationale: `Required context path "/missing/path" does not exist`, HealShape: "missing-context"},
	}}

	sig := BuildRequiresGateSignal(env, "stream", gate)
	md := sig["metadata"].(map[string]interface{})

	if envs := md["missing_envs"].([]interface{}); len(envs) != 1 || envs[0] != "E1" {
		t.Errorf("metadata.missing_envs = %v, want [E1]", envs)
	}
	if tools := md["missing_tools"].([]interface{}); len(tools) != 1 || tools[0] != "T1" {
		t.Errorf("metadata.missing_tools = %v, want [T1]", tools)
	}
	if ctxs := md["missing_contexts"].([]interface{}); len(ctxs) != 1 || ctxs[0] != "/missing/path" {
		t.Errorf("metadata.missing_contexts = %v, want [/missing/path]", ctxs)
	}
}
