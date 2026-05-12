//go:build !error_autofiler

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
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "RSA_PRIVATE_KEY"},
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
	sensor := map[string]interface{}{"id": "run-x", "requires": []interface{}{map[string]interface{}{"kind": "env", "name": "FOO"}}}

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
		"id":       "run-project",
		"requires": []interface{}{map[string]interface{}{"kind": "sensor", "id": "setup-env"}},
	}
	depSensor := map[string]interface{}{
		"id": "setup-env",
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "RSA_PRIVATE_KEY"},
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

func TestHook_ContentArrayForm_StillParses(t *testing.T) {
	// Real Claude Code transcripts may serialize tool_result.content
	// as an array of {type, text} content blocks. The hook must still
	// extract the JSONL aggregate.
	dir := t.TempDir()

	failingSignal := map[string]interface{}{
		"sensor_id": "run-x",
		"verdict":   "error",
		"severity":  "high",
		"evidence": []interface{}{
			map[string]interface{}{"rationale": "Required environment variable FOO not set"},
		},
		"metadata": map[string]interface{}{"kind": "aggregate"},
	}
	sensor := map[string]interface{}{
		"id":       "run-x",
		"requires": []interface{}{map[string]interface{}{"kind": "env", "name": "FOO"}},
	}

	sigBytes, _ := json.Marshal(failingSignal)
	sensorBytes, _ := json.Marshal(sensor)
	sensorPath := filepath.Join(dir, "broken.json")
	os.WriteFile(sensorPath, sensorBytes, 0o644)

	// The user message also uses content-block form to ensure
	// findRunSensorTarget tolerates both shapes.
	transcript := []map[string]interface{}{
		{"type": "user", "content": []map[string]interface{}{{"type": "text", "text": "/run-sensor " + sensorPath}}},
		{"type": "tool_result", "content": []map[string]interface{}{{"type": "text", "text": string(sigBytes)}}},
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
	out := stdout.String()
	if !strings.Contains(out, "/heal-sensor") {
		t.Fatalf("expected injection from content-block form; got %q", out)
	}
	if !strings.Contains(out, "run-x") {
		t.Fatalf("expected sensor id in injection; got %q", out)
	}
}

func TestHook_NoTranscriptPath_Exit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader([]byte(`{}`)), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
}

// writeBareIDTranscript writes a transcript that invokes <command> with
// a BARE sensor id (the new contract after the blocking-sensors PR).
// The hook must resolve the id against cwd/.harness/sensors/<id>.json. Returns
// (transcriptPath, projectRoot).
func writeBareIDTranscript(t *testing.T, command, sensorID string, signal map[string]interface{}, sensorJSON map[string]interface{}) (string, string) {
	t.Helper()
	dir := t.TempDir()
	sensorsDir := filepath.Join(dir, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sensorJSON["id"] = sensorID
	sensorBytes, _ := json.Marshal(sensorJSON)
	if err := os.WriteFile(filepath.Join(sensorsDir, sensorID+".json"), sensorBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	sigBytes, _ := json.Marshal(signal)
	transcript := []map[string]interface{}{
		{"type": "user", "content": command + " " + sensorID},
		{"type": "tool_result", "content": string(sigBytes)},
	}
	path := filepath.Join(dir, "transcript.jsonl")
	f, _ := os.Create(path)
	for _, e := range transcript {
		b, _ := json.Marshal(e)
		f.Write(b)
		f.WriteString("\n")
	}
	f.Close()
	return path, dir
}

func TestHook_StartSensor_BareID_EmitsInjection(t *testing.T) {
	failingSignal := map[string]interface{}{
		"sensor_id": "watch-logs",
		"verdict":   "error",
		"severity":  "high",
		"evidence": []interface{}{
			map[string]interface{}{"rationale": "Required environment variable RSA_PRIVATE_KEY not set"},
		},
		"metadata": map[string]interface{}{"kind": "start_failed"},
	}
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "RSA_PRIVATE_KEY"},
		},
	}
	tPath, cwd := writeBareIDTranscript(t, "/start-sensor", "watch-logs", failingSignal, sensor)

	var stdout, stderr bytes.Buffer
	hookInput := []byte(`{"transcript_path":"` + tPath + `","cwd":"` + cwd + `"}`)
	code := run(bytes.NewReader(hookInput), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "/heal-sensor") {
		t.Fatalf("expected /heal-sensor injection; got %q", out)
	}
	if !strings.Contains(out, "/start-sensor") {
		t.Fatalf("expected injection to mention /start-sensor (the failing command); got %q", out)
	}
	if !strings.Contains(out, "watch-logs") {
		t.Fatalf("expected sensor id watch-logs in injection; got %q", out)
	}
	if !strings.Contains(out, filepath.Join(cwd, ".harness", "sensors", "watch-logs.json")) {
		t.Fatalf("expected resolved sensor path in injection; got %q", out)
	}
}

func TestHook_StopSensor_BareID_EmitsInjection(t *testing.T) {
	// /stop-sensor's aggregate, when the subprocess died from a missing-env
	// failure during its run, should also trigger heal.
	failingSignal := map[string]interface{}{
		"sensor_id": "run-project",
		"verdict":   "error",
		"severity":  "high",
		"evidence": []interface{}{
			map[string]interface{}{"rationale": "Required environment variable DATABASE_URL not set"},
		},
		"metadata": map[string]interface{}{"kind": "aggregate"},
	}
	sensor := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "DATABASE_URL"},
		},
	}
	tPath, cwd := writeBareIDTranscript(t, "/stop-sensor", "run-project", failingSignal, sensor)

	var stdout, stderr bytes.Buffer
	hookInput := []byte(`{"transcript_path":"` + tPath + `","cwd":"` + cwd + `"}`)
	code := run(bytes.NewReader(hookInput), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "/heal-sensor") {
		t.Fatalf("expected /heal-sensor injection; got %q", out)
	}
	if !strings.Contains(out, "/stop-sensor") {
		t.Fatalf("expected injection to mention /stop-sensor; got %q", out)
	}
	if !strings.Contains(out, "run-project") {
		t.Fatalf("expected sensor id run-project in injection; got %q", out)
	}
}

func TestHook_StopSensor_FlagBeforeID_ResolvesCorrectly(t *testing.T) {
	// /stop-sensor accepts --reap-dead-holders. Some users write the
	// flag BEFORE the id; the hook must still find the id. This guards
	// against the trivial regression of taking parts[j+1] verbatim.
	dir := t.TempDir()
	sensorsDir := filepath.Join(dir, ".harness", "sensors")
	os.MkdirAll(sensorsDir, 0o755)
	sensor := map[string]interface{}{
		"id":       "my-sensor",
		"requires": []interface{}{map[string]interface{}{"kind": "env", "name": "FOO"}},
	}
	sensorBytes, _ := json.Marshal(sensor)
	os.WriteFile(filepath.Join(sensorsDir, "my-sensor.json"), sensorBytes, 0o644)

	failingSignal := map[string]interface{}{
		"sensor_id": "my-sensor",
		"verdict":   "error", "severity": "high",
		"evidence": []interface{}{map[string]interface{}{"rationale": "Required environment variable FOO not set"}},
		"metadata": map[string]interface{}{"kind": "aggregate"},
	}
	sigBytes, _ := json.Marshal(failingSignal)

	transcript := []map[string]interface{}{
		{"type": "user", "content": "/stop-sensor --reap-dead-holders my-sensor"},
		{"type": "tool_result", "content": string(sigBytes)},
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
	hookInput := []byte(`{"transcript_path":"` + path + `","cwd":"` + dir + `"}`)
	code := run(bytes.NewReader(hookInput), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "my-sensor") {
		t.Fatalf("expected sensor id to be resolved past the --flag; got %q", out)
	}
}

func TestHook_StartSensor_AlreadyRejected_NotSetupShape(t *testing.T) {
	// /start-sensor's start_rejected (already running) is not
	// setup-shaped — no rule should match, no injection.
	rejectSignal := map[string]interface{}{
		"sensor_id": "watch-logs",
		"verdict":   "error",
		"severity":  "high",
		"evidence":  []interface{}{map[string]interface{}{"rationale": "sensor \"watch-logs\" already running with pid 12345"}},
		"metadata":  map[string]interface{}{"kind": "start_rejected"},
	}
	sensor := map[string]interface{}{}
	tPath, cwd := writeBareIDTranscript(t, "/start-sensor", "watch-logs", rejectSignal, sensor)

	var stdout, stderr bytes.Buffer
	hookInput := []byte(`{"transcript_path":"` + tPath + `","cwd":"` + cwd + `"}`)
	code := run(bytes.NewReader(hookInput), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no injection for start_rejected; got %q", stdout.String())
	}
}
