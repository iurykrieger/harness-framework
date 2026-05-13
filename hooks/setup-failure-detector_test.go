//go:build !error_autofiler

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/transcript/transcripttest"
)

// withSensor writes a sensor JSON at <cwd>/.harness/sensors/<id>.json so
// loadFailedSensorView can find it. Returns the cwd to feed into the
// hook input.
func withSensor(t *testing.T, id string, requires []map[string]interface{}) string {
	t.Helper()
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]interface{}{
		"id":       id,
		"requires": requires,
	})
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return cwd
}

func TestHook_RunSensorError_EmitsHealInjection(t *testing.T) {
	cwd := withSensor(t, "watch-logs", []map[string]interface{}{
		{"kind": "env", "name": "RSA_PRIVATE_KEY"},
	})
	transcriptPath := transcripttest.Path(t, "run-sensor-error.jsonl")
	hookIn, _ := json.Marshal(map[string]string{
		"transcript_path": transcriptPath,
		"cwd":             cwd,
	})

	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader(hookIn), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "/heal-sensor") {
		t.Fatalf("expected injection mentioning /heal-sensor; got %q", out)
	}
	if !strings.Contains(out, "watch-logs") {
		t.Fatalf("expected sensor id in injection; got %q", out)
	}
}

func TestHook_RunSensorPass_NoInjection(t *testing.T) {
	cwd := withSensor(t, "lint-gofmt", nil)
	transcriptPath := transcripttest.Path(t, "run-sensor-pass.jsonl")
	hookIn, _ := json.Marshal(map[string]string{"transcript_path": transcriptPath, "cwd": cwd})

	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader(hookIn), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout; got %q", stdout.String())
	}
}

func TestHook_AlreadyHealed_NoLoop(t *testing.T) {
	cwd := withSensor(t, "watch-logs", []map[string]interface{}{
		{"kind": "env", "name": "RSA_PRIVATE_KEY"},
	})
	transcriptPath := transcripttest.Path(t, "already-healed.jsonl")
	hookIn, _ := json.Marshal(map[string]string{"transcript_path": transcriptPath, "cwd": cwd})

	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader(hookIn), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no-op when /heal-sensor already in transcript; got %q", stdout.String())
	}
}

func TestHook_CascadeFromFailedDep_TargetsDep(t *testing.T) {
	// run-project depends on setup-env; setup-env failed due to missing env.
	// The cascade aggregate is the LAST JSONL line on the tool_result.
	// Hook must walk back to setup-env's real aggregate, classify on that,
	// and emit an injection targeting setup-env.
	cwd := withSensor(t, "run-project", []map[string]interface{}{
		{"kind": "sensor", "id": "setup-env"},
	})
	// Also write setup-env so loadFailedSensorView can find it.
	depDir := filepath.Join(cwd, ".harness", "sensors")
	depBody, _ := json.Marshal(map[string]interface{}{
		"id":       "setup-env",
		"requires": []map[string]interface{}{{"kind": "env", "name": "RSA_PRIVATE_KEY"}},
	})
	if err := os.WriteFile(filepath.Join(depDir, "setup-env.json"), depBody, 0o644); err != nil {
		t.Fatal(err)
	}
	transcriptPath := transcripttest.Path(t, "cascade-failed-dep.jsonl")
	hookIn, _ := json.Marshal(map[string]string{"transcript_path": transcriptPath, "cwd": cwd})

	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader(hookIn), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "setup-env") {
		t.Fatalf("expected injection to target the failing dep setup-env; got %q", out)
	}
	if !strings.Contains(out, "run-project") {
		t.Fatalf("expected injection to mention requested sensor run-project; got %q", out)
	}
}

func TestHook_MalformedStdin_Exit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader([]byte("not json")), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d want 2", code)
	}
}

func TestHook_NoTranscriptPath_Exit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader([]byte(`{}`)), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d want 2", code)
	}
}
