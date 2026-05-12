package subprocess_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
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
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
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
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
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
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
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
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
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
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
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

func TestStreamSubprocess_TeesRawLogWhenRunDirSet(t *testing.T) {
	_, runID, runDir := testfixtures.WithRunDir(t, "alpha", "")
	_ = runID

	var stdout, stderr bytes.Buffer
	cfg := subprocess.StreamConfig{
		Command:  `printf 'line one\nline two\n'`,
		Envelope: sensor.Envelope{SensorID: "alpha", Version: "0.0.0", RunID: runID, StartedAt: "2026-05-11T00:00:00Z"},
		Stdout:   &stdout,
		Stderr:   &stderr,
		RunDir:   runDir,
	}
	res, err := subprocess.StreamSubprocess(context.Background(), cfg)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d", res.ExitCode)
	}

	raw, err := os.ReadFile(filepath.Join(runDir, "raw.log"))
	if err != nil {
		t.Fatalf("read raw.log: %v", err)
	}
	if want := "line one\nline two\n"; string(raw) != want {
		t.Errorf("raw.log = %q, want %q", string(raw), want)
	}
}

func TestStreamSubprocess_NoTeeWhenRunDirEmpty(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cfg := subprocess.StreamConfig{
		Command:  `echo hello`,
		Envelope: sensor.Envelope{SensorID: "alpha", Version: "0.0.0", RunID: "1-x", StartedAt: "2026-05-11T00:00:00Z"},
		Stdout:   &stdout,
		Stderr:   &stderr,
		// RunDir intentionally empty — legacy behavior
	}
	if _, err := subprocess.StreamSubprocess(context.Background(), cfg); err != nil {
		t.Fatalf("stream: %v", err)
	}
	// Nothing to assert about disk; the absence of a panic / RunDir-related error is the check.
}

func TestStreamSubprocess_RespectsDir(t *testing.T) {
	tmp := t.TempDir()
	outFile := filepath.Join(tmp, "pwd.out")

	var stdout, stderr bytes.Buffer
	res, err := subprocess.StreamSubprocess(context.Background(), subprocess.StreamConfig{
		Command:  "pwd > " + outFile,
		Envelope: envelopeFor(t),
		Stdout:   &stdout,
		Stderr:   &stderr,
		Dir:      tmp,
	})
	if err != nil {
		t.Fatalf("StreamSubprocess: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d want 0", res.ExitCode)
	}

	got, readErr := os.ReadFile(outFile)
	if readErr != nil {
		t.Fatalf("read pwd.out: %v", readErr)
	}
	want, _ := filepath.EvalSymlinks(tmp)
	gotResolved, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got)))
	if gotResolved != want {
		t.Errorf("subprocess cwd = %q, want %q", gotResolved, want)
	}
}

func TestStreamSubprocess_WritesIndividualsToSignalsLog(t *testing.T) {
	_, runID, runDir := testfixtures.WithRunDir(t, "alpha", "")

	patterns, err := signal.CompilePatterns([]interface{}{
		map[string]interface{}{"regex": `^FAIL: (.+)$`, "verdict": "fail", "severity": "high"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	cfg := subprocess.StreamConfig{
		Command:  `printf 'FAIL: boom\nFAIL: bang\n'`,
		Envelope: sensor.Envelope{SensorID: "alpha", Version: "0.0.0", RunID: runID, StartedAt: "2026-05-11T00:00:00Z"},
		Patterns: patterns,
		Stdout:   &stdout,
		Stderr:   &stderr,
		RunDir:   runDir,
	}
	res, err := subprocess.StreamSubprocess(context.Background(), cfg)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(res.Individuals) != 2 {
		t.Fatalf("individuals=%d", len(res.Individuals))
	}

	data, err := os.ReadFile(filepath.Join(runDir, "signals.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("signals.log lines=%d (%q)", len(lines), string(data))
	}
	// Cross-check: stdout has the same JSONL lines
	stdoutLines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(stdoutLines) != 2 {
		t.Fatalf("stdout lines=%d", len(stdoutLines))
	}
	for i := 0; i < 2; i++ {
		if lines[i] != stdoutLines[i] {
			t.Errorf("line %d: signals.log=%q stdout=%q", i, lines[i], stdoutLines[i])
		}
	}
}
