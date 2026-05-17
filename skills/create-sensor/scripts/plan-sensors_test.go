//go:build plan_sensors

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPlanSingleUsecase(t *testing.T) {
	in, err := os.Open(filepath.Join("testdata", "ledgers", "single-usecase.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var stdout, stderr bytes.Buffer
	code := run(in, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	expected, err := os.ReadFile(filepath.Join("testdata", "plan-output", "single-usecase.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimRight(stdout.Bytes(), "\n"), bytes.TrimRight(expected, "\n")) {
		t.Fatalf("output mismatch.\ngot:\n%s\nwant:\n%s", stdout.String(), string(expected))
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	// Run the same input ten times; must produce byte-identical stdout.
	body, err := os.ReadFile(filepath.Join("testdata", "ledgers", "two-grouped.json"))
	if err != nil {
		t.Fatal(err)
	}
	var first []byte
	for i := 0; i < 10; i++ {
		var stdout, stderr bytes.Buffer
		if code := run(bytes.NewReader(body), &stdout, &stderr); code != 0 {
			t.Fatalf("iter %d exit %d", i, code)
		}
		if first == nil {
			first = append([]byte(nil), stdout.Bytes()...)
			continue
		}
		if !bytes.Equal(first, stdout.Bytes()) {
			t.Fatalf("iter %d diverged:\nfirst:\n%s\nthis:\n%s", i, string(first), stdout.String())
		}
	}
}

func TestPlanGroupsByJourneyAndShape(t *testing.T) {
	// two-grouped has two usecases in the same journey + same trigger.shape
	// + overlapping tags → ONE planned sensor.
	in, err := os.Open(filepath.Join("testdata", "ledgers", "two-grouped.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var stdout, stderr bytes.Buffer
	if code := run(in, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	// Count plan lines (exclude aggregate).
	plans, agg := parseStdout(t, stdout.Bytes())
	if len(plans) != 1 {
		t.Fatalf("want 1 sensor planned, got %d", len(plans))
	}
	if agg.SensorsPlanned != 1 {
		t.Fatalf("aggregate sensors_planned mismatch: %d", agg.SensorsPlanned)
	}
	if agg.UsecasesConsumed != 2 {
		t.Fatalf("aggregate usecases_consumed mismatch: %d", agg.UsecasesConsumed)
	}
}

// parseStdout is a small helper test-side. plan-sensors emits JSONL with
// the aggregate as the LAST line.
func parseStdout(t *testing.T, body []byte) ([]map[string]any, struct {
	Aggregate        bool
	Verdict          string
	SensorsPlanned   int `json:"sensors_planned"`
	UsecasesConsumed int `json:"usecases_consumed"`
}) {
	t.Helper()
	lines := bytes.Split(bytes.TrimRight(body, "\n"), []byte("\n"))
	if len(lines) < 1 {
		t.Fatalf("empty stdout")
	}
	var aggregate struct {
		Aggregate        bool
		Verdict          string
		SensorsPlanned   int `json:"sensors_planned"`
		UsecasesConsumed int `json:"usecases_consumed"`
	}
	if err := json.Unmarshal(lines[len(lines)-1], &aggregate); err != nil {
		t.Fatalf("parse aggregate: %v", err)
	}
	var plans []map[string]any
	for _, ln := range lines[:len(lines)-1] {
		var p map[string]any
		if err := json.Unmarshal(ln, &p); err != nil {
			t.Fatalf("parse plan line: %v", err)
		}
		plans = append(plans, p)
	}
	return plans, aggregate
}

func TestPlanTwoSplitTriggerShape(t *testing.T) { runSnapshot(t, "two-split-trigger-shape") }
func TestPlanBucketTooLarge(t *testing.T)       { runSnapshot(t, "bucket-too-large") }
func TestPlanInferential(t *testing.T)          { runSnapshot(t, "inferential") }
func TestPlanObservation(t *testing.T)          { runSnapshot(t, "observation") }

func runSnapshot(t *testing.T, name string) {
	t.Helper()
	in, err := os.Open(filepath.Join("testdata", "ledgers", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var stdout, stderr bytes.Buffer
	if code := run(in, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	expected, err := os.ReadFile(filepath.Join("testdata", "plan-output", name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimRight(stdout.Bytes(), "\n"), bytes.TrimRight(expected, "\n")) {
		t.Fatalf("output mismatch for %s.\ngot:\n%s\nwant:\n%s", name, stdout.String(), string(expected))
	}
}
