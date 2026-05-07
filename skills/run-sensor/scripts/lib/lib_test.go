package lib

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// repoSchemasDir resolves the repo schemas/ directory from this test file's
// own location (independent of the test working directory).
func repoSchemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// .../skills/run-sensor/scripts/lib/lib_test.go → 4 levels up to repo root.
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", ".."))
	dir := filepath.Join(repoRoot, "schemas")
	if _, err := os.Stat(filepath.Join(dir, "sensor.json")); err != nil {
		t.Fatalf("schemas dir not where expected (%s): %v", dir, err)
	}
	return dir
}

func freezeClock(t *testing.T) func() {
	t.Helper()
	origNow, origID := NowFn, NewRunIDFn
	frozen := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	NowFn = func() time.Time { return frozen }
	NewRunIDFn = func() string { return "00000000-0000-4000-8000-000000000000" }
	return func() { NowFn, NewRunIDFn = origNow, origID }
}

func validSensorComputational() map[string]interface{} {
	return map[string]interface{}{
		"id": "smoke-comp", "version": "0.1.0",
		"name": "smoke", "description": "fixture",
		"type": "computational", "regulation": "maintainability",
		"phase": "on-demand", "determinism": "high",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 100, "timeout_ms": 5000},
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 64},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command": "true",
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
				map[string]interface{}{"exit_code": 1, "verdict": "fail", "severity": "high"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "x", "expected_verdict": "pass", "expected_severity": "info"}},
		},
	}
}

func validSensorInferential() map[string]interface{} {
	return map[string]interface{}{
		"id": "smoke-inf", "version": "0.1.0",
		"name": "smoke inf", "description": "fixture",
		"type": "inferential", "regulation": "maintainability",
		"phase": "post-integration", "determinism": "low",
		"cost": map[string]interface{}{
			"class":   "expensive",
			"latency": map[string]interface{}{"p50_ms": 6000, "p95_ms": 15000, "timeout_ms": 60000},
			"tokens":  map[string]interface{}{"model": "anthropic/claude-sonnet-4-6", "input_avg": 4000, "output_avg": 400, "max_output": 1024},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "pull-request"}},
		"execution": map[string]interface{}{
			"model":                "anthropic/claude-sonnet-4-6",
			"system_prompt":        "You are a similarity judge. Output JSON only.",
			"user_prompt_template": "Compare {{a}} to {{b}}.",
			"decoding":             map[string]interface{}{"temperature": 0.0, "max_tokens": 1024},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "f", "expected_verdict": "warn", "expected_severity": "medium"}},
		},
		"calibration": map[string]interface{}{
			"confidence_threshold": 0.7,
			"calibration_set":      "tests/cal.jsonl",
			"calibration_size":     120,
			"calibration_date":     "2026-04-15",
		},
	}
}

func writeTempJSON(t *testing.T, v interface{}) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.json")
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ──────────────────────────────────────────────────────────────────────
// Pure helpers
// ──────────────────────────────────────────────────────────────────────

func TestResolveSensorPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sensors", "x.json")
	_ = os.MkdirAll(filepath.Dir(target), 0o755)
	_ = os.WriteFile(target, []byte("{}"), 0o644)

	cases := []struct {
		name string
		arg  string
		want string
	}{
		{"@-prefix relative", "@sensors/x.json", target},
		{"relative", "sensors/x.json", target},
		{"absolute", target, target},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveSensorPath(tc.arg, dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
	t.Run("not found", func(t *testing.T) {
		if _, err := ResolveSensorPath("@sensors/missing.json", dir); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("empty after trimming", func(t *testing.T) {
		if _, err := ResolveSensorPath("@", dir); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestBuildEnvelope(t *testing.T) {
	defer freezeClock(t)()
	env, err := BuildEnvelope(validSensorComputational())
	if err != nil {
		t.Fatal(err)
	}
	if env.SensorID != "smoke-comp" || env.Version != "0.1.0" || env.SensorType != "computational" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
	if env.RunID != "00000000-0000-4000-8000-000000000000" {
		t.Fatalf("run_id not from frozen NewRunIDFn: %s", env.RunID)
	}
	if env.StartedAt != "2026-05-06T12:00:00Z" {
		t.Fatalf("started_at not from frozen NowFn: %s", env.StartedAt)
	}
}

func TestBuildEnvelope_MissingFields(t *testing.T) {
	for _, missing := range []string{"id", "version", "type"} {
		t.Run(missing, func(t *testing.T) {
			s := validSensorComputational()
			delete(s, missing)
			if _, err := BuildEnvelope(s); err == nil {
				t.Fatalf("expected error when %q missing", missing)
			}
		})
	}
}

func TestMapExitCode(t *testing.T) {
	ecMap := []interface{}{
		map[string]interface{}{"exit_code": 0.0, "verdict": "pass", "severity": "info"},
		map[string]interface{}{"exit_code": 1.0, "verdict": "fail", "severity": "high"},
		map[string]interface{}{"exit_code": "*", "verdict": "error", "severity": "medium"},
	}
	cases := []struct {
		code    int
		verdict string
	}{
		{0, "pass"}, {1, "fail"}, {99, "error"},
	}
	for _, c := range cases {
		v, _ := MapExitCode(c.code, ecMap)
		if v != c.verdict {
			t.Errorf("MapExitCode(%d)=%q want %q", c.code, v, c.verdict)
		}
	}

	// Without wildcard fallback
	noWild := []interface{}{
		map[string]interface{}{"exit_code": 0.0, "verdict": "pass", "severity": "info"},
	}
	v, s := MapExitCode(99, noWild)
	if v != "error" || s != "high" {
		t.Errorf("expected error/high default, got %s/%s", v, s)
	}
}

func TestRenderTemplate(t *testing.T) {
	cases := []struct {
		name        string
		tmpl        string
		bindings    map[string]string
		want        string
		wantMissing []string
	}{
		{"simple", "Hi {{n}}!", map[string]string{"n": "Iury"}, "Hi Iury!", nil},
		{"repeated slot", "{{a}} + {{a}} = {{b}}", map[string]string{"a": "1", "b": "2"}, "1 + 1 = 2", nil},
		{"missing once", "{{a}} {{b}} {{a}}", map[string]string{"b": "B"}, "{{a}} B {{a}}", []string{"a"}},
		{"whitespace", "{{ name  }}", map[string]string{"name": "x"}, "x", nil},
		{"no slots", "plain", map[string]string{}, "plain", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, missing := RenderTemplate(tc.tmpl, tc.bindings)
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
			if len(missing) != len(tc.wantMissing) {
				t.Errorf("missing=%v want %v", missing, tc.wantMissing)
			}
		})
	}
}

func TestFindSchemasDir(t *testing.T) {
	expected := repoSchemasDir(t)
	_, thisFile, _, _ := runtime.Caller(0)
	got, err := FindSchemasDir(filepath.Dir(thisFile))
	if err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatalf("got %s, want %s", got, expected)
	}
}

func TestFindSchemasDir_Missing(t *testing.T) {
	if _, err := FindSchemasDir(t.TempDir()); err == nil {
		t.Fatal("expected error when no schemas dir exists")
	}
}

// ──────────────────────────────────────────────────────────────────────
// Validator (covers cross-file $ref to signal.json#/$defs/{Verdict,Severity})
// ──────────────────────────────────────────────────────────────────────

func TestValidator_AcceptsValidSensors(t *testing.T) {
	v, err := NewValidator(repoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]map[string]interface{}{
		"computational": validSensorComputational(),
		"inferential":   validSensorInferential(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := v.Validate(TargetSensor, s); err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
		})
	}
}

func TestValidator_RejectsMutations(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	clone := func(m map[string]interface{}) map[string]interface{} {
		raw, _ := json.Marshal(m)
		var c map[string]interface{}
		_ = json.Unmarshal(raw, &c)
		return c
	}
	cases := []struct {
		name    string
		mutator func() map[string]interface{}
		wantSub string
	}{
		{
			name: "computational + tokens (mutual exclusion)",
			mutator: func() map[string]interface{} {
				s := clone(validSensorComputational())
				s["cost"].(map[string]interface{})["tokens"] = map[string]interface{}{
					"model": "x", "input_avg": 1, "output_avg": 1, "max_output": 1,
				}
				return s
			},
			wantSub: "not failed",
		},
		{
			name: "$ref to signal.json#/$defs/Verdict bites in exit_code_map",
			mutator: func() map[string]interface{} {
				s := clone(validSensorComputational())
				ec := s["execution"].(map[string]interface{})["exit_code_map"].([]interface{})
				ec[0].(map[string]interface{})["verdict"] = "broken"
				return s
			},
			wantSub: "$defs/Verdict",
		},
		{
			name: "inferential missing calibration",
			mutator: func() map[string]interface{} {
				s := clone(validSensorInferential())
				delete(s, "calibration")
				return s
			},
			wantSub: "calibration",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Validate(TargetSensor, tc.mutator())
			if err == nil {
				t.Fatal("expected error")
			}
			rendered := flattenValidationError(err)
			if !strings.Contains(rendered, tc.wantSub) {
				t.Fatalf("missing %q: %s", tc.wantSub, rendered)
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────────────
// ExecuteComputational
// ──────────────────────────────────────────────────────────────────────

func TestExecuteComputational_Pass(t *testing.T) {
	defer freezeClock(t)()
	schemasDir := repoSchemasDir(t)
	sensor := validSensorComputational()
	sensor["execution"].(map[string]interface{})["command"] = "true"
	path := writeTempJSON(t, sensor)

	var stdout, stderr bytes.Buffer
	if code := ExecuteComputational(path, schemasDir, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var sig map[string]interface{}
	_ = json.Unmarshal(stdout.Bytes(), &sig)
	if sig["verdict"] != "pass" || sig["confidence"] != 1.0 {
		t.Fatalf("verdict=%v confidence=%v", sig["verdict"], sig["confidence"])
	}
}

func TestExecuteComputational_Fail(t *testing.T) {
	defer freezeClock(t)()
	schemasDir := repoSchemasDir(t)
	sensor := validSensorComputational()
	sensor["execution"].(map[string]interface{})["command"] = "false"
	path := writeTempJSON(t, sensor)

	var stdout, stderr bytes.Buffer
	if code := ExecuteComputational(path, schemasDir, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var sig map[string]interface{}
	_ = json.Unmarshal(stdout.Bytes(), &sig)
	if sig["verdict"] != "fail" || sig["severity"] != "high" {
		t.Fatalf("verdict=%v severity=%v", sig["verdict"], sig["severity"])
	}
}

func TestExecuteComputational_Timeout(t *testing.T) {
	defer freezeClock(t)()
	schemasDir := repoSchemasDir(t)
	sensor := validSensorComputational()
	sensor["execution"].(map[string]interface{})["command"] = "sleep 10"
	sensor["cost"].(map[string]interface{})["latency"].(map[string]interface{})["timeout_ms"] = 200
	path := writeTempJSON(t, sensor)

	var stdout, stderr bytes.Buffer
	if code := ExecuteComputational(path, schemasDir, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var sig map[string]interface{}
	_ = json.Unmarshal(stdout.Bytes(), &sig)
	if sig["verdict"] != "error" {
		t.Fatalf("expected verdict=error after timeout, got %v", sig["verdict"])
	}
}

func TestExecuteComputational_RejectsInferential(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeTempJSON(t, validSensorInferential())
	var stdout, stderr bytes.Buffer
	if code := ExecuteComputational(path, schemasDir, &stdout, &stderr); code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
}

// ──────────────────────────────────────────────────────────────────────
// ExecuteInferential (HTTP mocked via httptest.Server)
// ──────────────────────────────────────────────────────────────────────

// startMockAnthropic launches an httptest.Server that responds to POST
// /v1/messages with a single text block containing replyJSON. The server's
// URL should be passed as apiBase to ExecuteInferential.
func startMockAnthropic(t *testing.T, replyJSON string, inputTokens, outputTokens int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		if r.Header.Get("x-api-key") == "" {
			http.Error(w, "missing api key", http.StatusUnauthorized)
			return
		}
		resp := map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": replyJSON}},
			"usage":   map[string]interface{}{"input_tokens": inputTokens, "output_tokens": outputTokens},
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestExecuteInferential_Pass(t *testing.T) {
	defer freezeClock(t)()
	schemasDir := repoSchemasDir(t)
	sensorPath := writeTempJSON(t, validSensorInferential())

	llmReply := `{
		"verdict": "pass",
		"severity": "info",
		"confidence": 0.92,
		"evidence": [{"rationale": "no duplicate found"}],
		"remediation": {"instructions": "no action needed"}
	}`
	srv := startMockAnthropic(t, llmReply, 1234, 56)

	slots := map[string]string{"a": "foo()", "b": "bar()"}
	var stdout, stderr bytes.Buffer
	if code := ExecuteInferential(sensorPath, schemasDir, slots, srv.URL, "test-key", &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var sig map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &sig); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sig["verdict"] != "pass" {
		t.Fatalf("verdict=%v", sig["verdict"])
	}
	if sig["sensor_id"] != "smoke-inf" {
		t.Fatalf("sensor_id=%v", sig["sensor_id"])
	}
	cost := sig["cost_actual"].(map[string]interface{})
	if cost["input_tokens"] != float64(1234) || cost["output_tokens"] != float64(56) {
		t.Fatalf("cost_actual.tokens=%v", cost)
	}
	if cost["model"] != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("cost_actual.model=%v", cost["model"])
	}
}

func TestExecuteInferential_Downgrade(t *testing.T) {
	defer freezeClock(t)()
	schemasDir := repoSchemasDir(t)
	sensorPath := writeTempJSON(t, validSensorInferential())

	llmReply := `{
		"verdict": "fail",
		"severity": "medium",
		"confidence": 0.5,
		"evidence": [{"rationale": "uncertain duplicate"}]
	}`
	srv := startMockAnthropic(t, llmReply, 100, 50)

	slots := map[string]string{"a": "x", "b": "y"}
	var stdout, stderr bytes.Buffer
	if code := ExecuteInferential(sensorPath, schemasDir, slots, srv.URL, "k", &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var sig map[string]interface{}
	_ = json.Unmarshal(stdout.Bytes(), &sig)
	if sig["verdict"] != "warn" {
		t.Fatalf("expected fail→warn calibration downgrade, got %v", sig["verdict"])
	}
	meta, ok := sig["metadata"].(map[string]interface{})
	if !ok || meta["calibration_downgrade"] != true {
		t.Fatalf("expected metadata.calibration_downgrade=true, got %v", sig["metadata"])
	}
}

func TestExecuteInferential_UnboundSlot(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	sensorPath := writeTempJSON(t, validSensorInferential())
	srv := startMockAnthropic(t, `{}`, 0, 0)
	var stdout, stderr bytes.Buffer
	code := ExecuteInferential(sensorPath, schemasDir, map[string]string{"a": "x"}, srv.URL, "k", &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for unbound slot, got %d", code)
	}
	if !strings.Contains(stderr.String(), "b") {
		t.Fatalf("stderr should name unbound slot 'b': %s", stderr.String())
	}
}

func TestExecuteInferential_MalformedLLMOutput(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	sensorPath := writeTempJSON(t, validSensorInferential())
	srv := startMockAnthropic(t, `not valid json`, 0, 0)
	var stdout, stderr bytes.Buffer
	code := ExecuteInferential(sensorPath, schemasDir, map[string]string{"a": "x", "b": "y"}, srv.URL, "k", &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for malformed LLM output, got %d", code)
	}
}

func TestExecuteInferential_RejectsComputational(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	sensorPath := writeTempJSON(t, validSensorComputational())
	var stdout, stderr bytes.Buffer
	if code := ExecuteInferential(sensorPath, schemasDir, nil, "http://unused", "k", &stdout, &stderr); code != 2 {
		t.Fatalf("expected exit 2 for type mismatch, got %d", code)
	}
}

func TestExecuteInferential_NonAnthropicModelRejected(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	s := validSensorInferential()
	s["execution"].(map[string]interface{})["model"] = "openai/gpt-4"
	s["cost"].(map[string]interface{})["tokens"].(map[string]interface{})["model"] = "openai/gpt-4"
	sensorPath := writeTempJSON(t, s)
	var stdout, stderr bytes.Buffer
	if code := ExecuteInferential(sensorPath, schemasDir, map[string]string{"a": "x", "b": "y"}, "http://unused", "k", &stdout, &stderr); code != 2 {
		t.Fatalf("expected exit 2 for non-anthropic provider, got %d (stderr=%s)", code, stderr.String())
	}
}

// ──────────────────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────────────────

func flattenValidationError(err error) string {
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		var b strings.Builder
		PrintValidationError(&b, ve, "")
		return b.String()
	}
	return err.Error()
}
