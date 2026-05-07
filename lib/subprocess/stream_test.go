package subprocess_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/subprocess"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func mustCompilePatterns(t *testing.T, raw []interface{}) []signal.Pattern {
	t.Helper()
	pats, err := signal.CompilePatterns(raw)
	if err != nil {
		t.Fatal(err)
	}
	return pats
}

func envelopeFor(t *testing.T) sensor.Envelope {
	t.Helper()
	return sensor.Envelope{
		SensorID: "smoke", Version: "0.1.0",
		RunID:      "00000000-0000-4000-8000-000000000000",
		StartedAt:  "2026-05-06T12:00:00Z",
		SensorType: "computational",
	}
}

func decodeJSONL(t *testing.T, s string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestStreamSubprocess_EmitsJSONLPerMatch(t *testing.T) {
	defer testfixtures.FreezeClock(t)()
	v, err := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	patterns := mustCompilePatterns(t, []interface{}{
		map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
		map[string]interface{}{"regex": "^PASS", "verdict": "pass", "severity": "info"},
	})

	var stdout, stderr bytes.Buffer
	res, err := subprocess.StreamSubprocess(context.Background(), subprocess.StreamConfig{
		Command:   `printf 'PASS a\nFAIL b\nignored line\n'; exit 1`,
		Patterns:  patterns,
		Envelope:  envelopeFor(t),
		Validator: v,
		Stdout:    &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("stream: %v stderr=%s", err, stderr.String())
	}
	if res.ExitCode != 1 {
		t.Fatalf("exit=%d", res.ExitCode)
	}
	if res.TimedOut {
		t.Fatal("unexpected timeout")
	}
	lines := decodeJSONL(t, stdout.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 individuals (PASS + FAIL), got %d: %s", len(lines), stdout.String())
	}
	if lines[0]["verdict"] != "pass" || lines[1]["verdict"] != "fail" {
		t.Fatalf("verdicts: %v / %v", lines[0]["verdict"], lines[1]["verdict"])
	}
	md := lines[0]["metadata"].(map[string]interface{})
	if md["kind"] != "individual" || md["line"] != "PASS a" {
		t.Fatalf("metadata: %v", md)
	}
}

func TestStreamSubprocess_ShellFeatures(t *testing.T) {
	defer testfixtures.FreezeClock(t)()
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	patterns := mustCompilePatterns(t, []interface{}{
		map[string]interface{}{"regex": "^WARN", "verdict": "warn", "severity": "low"},
	})
	var stdout, stderr bytes.Buffer
	// pipe + 2>&1 + glob: things strings.Fields would mangle
	res, _ := subprocess.StreamSubprocess(context.Background(), subprocess.StreamConfig{
		Command:   `printf 'WARN x\nINFO y\n' | grep -E '^(WARN|INFO)'`,
		Patterns:  patterns,
		Envelope:  envelopeFor(t),
		Validator: v,
		Stdout:    &stdout, Stderr: &stderr,
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", res.ExitCode, stderr.String())
	}
	lines := decodeJSONL(t, stdout.String())
	if len(lines) != 1 || lines[0]["verdict"] != "warn" {
		t.Fatalf("expected 1 warn, got %v", lines)
	}
}

func TestStreamSubprocess_Timeout(t *testing.T) {
	defer testfixtures.FreezeClock(t)()
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	var stdout, stderr bytes.Buffer
	res, _ := subprocess.StreamSubprocess(context.Background(), subprocess.StreamConfig{
		Command:   `sleep 10`,
		Patterns:  nil,
		Envelope:  envelopeFor(t),
		Validator: v,
		Stdout:    &stdout, Stderr: &stderr,
		TimeoutMS: 200,
	})
	if !res.TimedOut {
		t.Fatal("expected timed_out=true")
	}
}

func TestStreamSubprocess_BinaryNotFound(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	var stdout, stderr bytes.Buffer
	// sh exits non-zero with "command not found"; ExitCode is non-zero, no individuals.
	res, err := subprocess.StreamSubprocess(context.Background(), subprocess.StreamConfig{
		Command:   "this-binary-definitely-does-not-exist-zzz arg1 arg2",
		Patterns:  nil,
		Envelope:  envelopeFor(t),
		Validator: v,
		Stdout:    &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if res.ExitCode == 0 {
		t.Fatalf("expected non-zero exit, got %d", res.ExitCode)
	}
}

func TestStreamSubprocess_NoPatternsNoIndividuals(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	var stdout, stderr bytes.Buffer
	res, _ := subprocess.StreamSubprocess(context.Background(), subprocess.StreamConfig{
		Command:   `printf 'whatever\n'`,
		Patterns:  nil,
		Envelope:  envelopeFor(t),
		Validator: v,
		Stdout:    &stdout, Stderr: &stderr,
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d", res.ExitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no JSONL output, got: %s", stdout.String())
	}
	if len(res.Individuals) != 0 {
		t.Fatalf("expected zero individuals, got %d", len(res.Individuals))
	}
}
