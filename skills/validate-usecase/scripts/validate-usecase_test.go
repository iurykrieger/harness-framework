//go:build validate_usecase

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	sensorPkg "github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestRejectsMissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for missing arg")
	}
	out := stdout.String()
	if !strings.Contains(out, "usecase") {
		t.Fatalf("expected error signal naming usecase, got %q", out)
	}
}

func TestRejectsEmptyArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{""}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for empty arg")
	}
	out := stdout.String()
	if !strings.Contains(out, "usecase") {
		t.Fatalf("expected error signal naming usecase, got %q", out)
	}
}

func TestWorstResult(t *testing.T) {
	tests := []struct {
		name     string
		rows     []verdictRow
		expected string
	}{
		{"all pass", []verdictRow{{Verdict: "pass"}, {Verdict: "pass"}}, "pass"},
		{"one warn", []verdictRow{{Verdict: "pass"}, {Verdict: "warn"}}, "warn"},
		{"one fail", []verdictRow{{Verdict: "pass"}, {Verdict: "fail"}}, "fail"},
		{"one error", []verdictRow{{Verdict: "pass"}, {Verdict: "error"}}, "error"},
		{"error beats fail", []verdictRow{{Verdict: "fail"}, {Verdict: "error"}}, "error"},
		{"empty", nil, "pass"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := worstResult(tc.rows)
			if got != tc.expected {
				t.Errorf("worstResult(%v) = %q, want %q", tc.rows, got, tc.expected)
			}
		})
	}
}

func TestLastAggregateVerdict(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		wantVerdict     string
		wantFinishedAt  string
	}{
		{
			name:        "empty input",
			input:       "",
			wantVerdict: "error",
		},
		{
			name:        "single valid signal",
			input:       `{"verdict":"pass","finished_at":"2026-01-01T00:00:00Z"}` + "\n",
			wantVerdict: "pass",
			wantFinishedAt: "2026-01-01T00:00:00Z",
		},
		{
			name: "multiple signals, last wins",
			input: `{"verdict":"fail","finished_at":"2026-01-01T00:00:00Z"}` + "\n" +
				`{"verdict":"pass","finished_at":"2026-01-01T00:01:00Z"}` + "\n",
			wantVerdict:    "pass",
			wantFinishedAt: "2026-01-01T00:01:00Z",
		},
		{
			name:        "trailing empty lines ignored",
			input:       `{"verdict":"warn","finished_at":"2026-01-01T00:00:00Z"}` + "\n\n",
			wantVerdict: "warn",
			wantFinishedAt: "2026-01-01T00:00:00Z",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			verdict, finishedAt := lastAggregateVerdict([]byte(tc.input))
			if verdict != tc.wantVerdict {
				t.Errorf("verdict: got %q, want %q", verdict, tc.wantVerdict)
			}
			if finishedAt != tc.wantFinishedAt {
				t.Errorf("finishedAt: got %q, want %q", finishedAt, tc.wantFinishedAt)
			}
		})
	}
}

func TestSafeDiv(t *testing.T) {
	if safeDiv(0, 0) != 0 {
		t.Error("safeDiv(0,0) should be 0")
	}
	if safeDiv(3, 4) != 0.75 {
		t.Errorf("safeDiv(3,4) = %v, want 0.75", safeDiv(3, 4))
	}
}

// TestIsCompositeDetectsSteps verifies isComposite on typed sensor values.
func TestIsCompositeDetectsSteps(t *testing.T) {
	t.Run("no steps", func(t *testing.T) {
		var s sensorPkg.Sensor
		if isComposite(s) {
			t.Error("expected false for sensor with no steps")
		}
	})
	t.Run("non-sensor step type", func(t *testing.T) {
		s := sensorPkg.Sensor{
			Execution: sensorPkg.Execution{
				Steps: []sensorPkg.StepConfig{{ID: "s1", Type: "shell"}},
			},
		}
		if isComposite(s) {
			t.Error("expected false for shell step")
		}
	})
	t.Run("sensor step type", func(t *testing.T) {
		s := sensorPkg.Sensor{
			Execution: sensorPkg.Execution{
				Steps: []sensorPkg.StepConfig{{ID: "s1", Type: "sensor", Ref: "other"}},
			},
		}
		if !isComposite(s) {
			t.Error("expected true for sensor step")
		}
	})
}

// TestErrorSignalIsValidJSON verifies that the error signal emitted for missing
// arg is valid JSON and contains a verdict field.
func TestErrorSignalIsValidJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	run(nil, &stdout, &stderr)
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		t.Fatal("expected JSON on stdout, got empty")
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\ncontent: %q", err, out)
	}
	if _, ok := m["verdict"]; !ok {
		t.Errorf("signal missing verdict field: %v", m)
	}
}
