// hooks/setup-failure-detector_test.go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTranscript writes a fake Claude Code transcript with one tool
// result containing the given Signal as its last JSONL line, then
// returns the transcript path.
func writeTranscript(t *testing.T, signal map[string]interface{}, sensorJSON map[string]interface{}) string {
	t.Helper()
	dir := t.TempDir()

	// The /run-sensor tool result emits JSONL on stdout; the LAST line
	// is the aggregate Signal.
	sigBytes, _ := json.Marshal(signal)
	toolOutput := string(sigBytes)

	sensorBytes, _ := json.Marshal(sensorJSON)
	sensorPath := filepath.Join(dir, "broken.json")
	os.WriteFile(sensorPath, sensorBytes, 0o644)

	transcript := []map[string]interface{}{
		{"type": "user", "content": "/run-sensor " + sensorPath},
		{"type": "tool_use", "name": "Bash", "input": map[string]interface{}{"command": "go run ./skills/run-sensor/scripts " + sensorPath}},
		{"type": "tool_result", "content": toolOutput},
	}
	path := filepath.Join(dir, "transcript.jsonl")
	f, _ := os.Create(path)
	for _, e := range transcript {
		b, _ := json.Marshal(e)
		f.Write(b)
		f.WriteString("\n")
	}
	f.Close()
	return path
}

func TestHook_SetupShape_EmitsInjection(t *testing.T) {
	failingSignal := map[string]interface{}{
		"sensor_id": "run-x",
		"verdict":   "error",
		"severity":  "high",
		"evidence": []interface{}{
			map[string]interface{}{"rationale": "Required environment variable RSA_PRIVATE_KEY not set"},
		},
		"metadata": map[string]interface{}{"kind": "aggregate"},
	}
	sensor := map[string]interface{}{
		"id": "run-x",
		"requires": map[string]interface{}{
			"env": []interface{}{
				map[string]interface{}{"name": "RSA_PRIVATE_KEY"},
			},
		},
	}
	path := writeTranscript(t, failingSignal, sensor)

	var stdout, stderr bytes.Buffer
	hookInput := []byte(`{"transcript_path":"` + path + `"}`)
	code := run(bytes.NewReader(hookInput), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "/heal-sensor") {
		t.Fatalf("expected injection mentioning /heal-sensor; got %q", out)
	}
	if !strings.Contains(out, "run-x") {
		t.Fatalf("expected sensor id in injection; got %q", out)
	}
}

func TestHook_PassingSignal_EmitsNothing(t *testing.T) {
	passingSignal := map[string]interface{}{
		"sensor_id": "run-x",
		"verdict":   "pass",
		"severity":  "info",
		"metadata":  map[string]interface{}{"kind": "aggregate"},
	}
	sensor := map[string]interface{}{"id": "run-x"}
	path := writeTranscript(t, passingSignal, sensor)

	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader([]byte(`{"transcript_path":"`+path+`"}`)), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout; got %q", stdout.String())
	}
}

func TestHook_AlreadyHealed_NoLoop(t *testing.T) {
	// Transcript shows /run-sensor failure FOLLOWED by /heal-sensor — must not re-trigger.
	dir := t.TempDir()
	failingSignal := map[string]interface{}{
		"sensor_id": "run-x", "verdict": "error", "severity": "high",
		"evidence": []interface{}{map[string]interface{}{"rationale": "Required environment variable FOO not set"}},
		"metadata": map[string]interface{}{"kind": "aggregate"},
	}
	sensor := map[string]interface{}{"id": "run-x", "requires": map[string]interface{}{"env": []interface{}{map[string]interface{}{"name": "FOO"}}}}

	sigBytes, _ := json.Marshal(failingSignal)
	sensorBytes, _ := json.Marshal(sensor)
	sensorPath := filepath.Join(dir, "broken.json")
	os.WriteFile(sensorPath, sensorBytes, 0o644)

	transcript := []map[string]interface{}{
		{"type": "user", "content": "/run-sensor " + sensorPath},
		{"type": "tool_result", "content": string(sigBytes)},
		{"type": "user", "content": "/heal-sensor invoked"},
		{"type": "assistant", "content": "heal applied"},
	}
	path := filepath.Join(dir, "transcript.jsonl")
	f, _ := os.Create(path)
	for _, e := range transcript {
		b, _ := json.Marshal(e)
		f.Write(b)
		f.WriteString("\n")
	}
	f.Close()

	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader([]byte(`{"transcript_path":"`+path+`"}`)), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no-op when /heal-sensor already in transcript; got %q", stdout.String())
	}
}

func TestHook_CascadeFromFailedDep_TargetsDep(t *testing.T) {
	// When the LAST aggregate is a cascade signal pointing at a failed dep,
	// the hook must walk back to the dep's real aggregate, classify on
	// THAT, and emit an injection targeting the dep's sensor file.
	dir := t.TempDir()

	// Dep aggregate (real failure: setup-shape, env missing).
	depSignal := map[string]interface{}{
		"sensor_id": "setup-env",
		"verdict":   "error",
		"severity":  "high",
		"evidence": []interface{}{
			map[string]interface{}{"rationale": "Required environment variable RSA_PRIVATE_KEY not set"},
		},
		"metadata": map[string]interface{}{"kind": "aggregate"},
	}
	// Cascade aggregate for the requested sensor (run-project) pointing at setup-env.
	cascadeSignal := map[string]interface{}{
		"sensor_id": "run-project",
		"verdict":   "error",
		"severity":  "high",
		"evidence":  []interface{}{},
		"metadata": map[string]interface{}{
			"kind":           "cascade",
			"failed_dep_id":  "setup-env",
		},
	}

	depBytes, _ := json.Marshal(depSignal)
	cascadeBytes, _ := json.Marshal(cascadeSignal)
	toolOutput := string(depBytes) + "\n" + string(cascadeBytes)

	// Sensor files: requested + dep (resolved via filepath.Dir(originalSensorPath)).
	requestedSensor := map[string]interface{}{
		"id":         "run-project",
		"depends_on": []string{"setup-env"},
	}
	depSensor := map[string]interface{}{
		"id": "setup-env",
		"requires": map[string]interface{}{
			"env": []interface{}{
				map[string]interface{}{"name": "RSA_PRIVATE_KEY"},
			},
		},
	}
	requestedBytes, _ := json.Marshal(requestedSensor)
	depBytesSensor, _ := json.Marshal(depSensor)
	requestedPath := filepath.Join(dir, "run-project.json")
	depPath := filepath.Join(dir, "setup-env.json")
	os.WriteFile(requestedPath, requestedBytes, 0o644)
	os.WriteFile(depPath, depBytesSensor, 0o644)

	transcript := []map[string]interface{}{
		{"type": "user", "content": "/run-sensor " + requestedPath},
		{"type": "tool_use", "name": "Bash", "input": map[string]interface{}{"command": "go run ./skills/run-sensor/scripts " + requestedPath}},
		{"type": "tool_result", "content": toolOutput},
	}
	tPath := filepath.Join(dir, "transcript.jsonl")
	f, _ := os.Create(tPath)
	for _, e := range transcript {
		b, _ := json.Marshal(e)
		f.Write(b)
		f.WriteString("\n")
	}
	f.Close()

	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader([]byte(`{"transcript_path":"`+tPath+`"}`)), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "/heal-sensor") {
		t.Fatalf("expected injection mentioning /heal-sensor; got %q", out)
	}
	if !strings.Contains(out, "setup-env") {
		t.Fatalf("expected injection to target failed dep setup-env; got %q", out)
	}
	if strings.Contains(out, "\"run-project\"") {
		t.Fatalf("injection should target the dep, not the requested sensor; got %q", out)
	}
	if !strings.Contains(out, depPath) {
		t.Fatalf("expected injection to point at dep sensor file %q; got %q", depPath, out)
	}
}

func TestHook_NoTranscriptPath_Exit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader([]byte(`{}`)), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
}
