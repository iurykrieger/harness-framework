//go:build start_watcher

package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAndAppendSignals(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw.log")
	signals := filepath.Join(dir, "signals.log")
	if err := os.WriteFile(raw, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	patterns := []map[string]interface{}{
		{"regex": "^ERROR (.*)$", "verdict": "fail", "severity": "high", "rationale": "match"},
	}
	patternsJSON, _ := json.Marshal(patterns)
	envelope := map[string]interface{}{
		"sensor_id":   "watch-logs",
		"version":     "1.0.0",
		"run_id":      "00000000-0000-0000-0000-000000000000",
		"started_at":  "2026-05-09T15:30:00Z",
		"sensor_type": "computational",
	}
	envelopeJSON, _ := json.Marshal(envelope)

	cfg := watcherConfig{
		RawLog:        raw,
		SignalsLog:    signals,
		PatternsJSON:  string(patternsJSON),
		EnvelopeJSON:  string(envelopeJSON),
		SubprocessPID: -1, // skip reaper
	}

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runWatcher(cfg, stop) }()

	// Append a matching line to raw.log.
	time.Sleep(20 * time.Millisecond)
	f, _ := os.OpenFile(raw, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("ERROR oh no\n")
	f.Close()

	// Wait for signals.log to populate.
	deadline := time.Now().Add(time.Second)
	var line string
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(signals)
		if len(data) > 0 {
			line = strings.TrimSpace(string(data))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	<-done

	if line == "" {
		t.Fatal("no signal written")
	}
	var sig map[string]interface{}
	if err := json.Unmarshal([]byte(line), &sig); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if sig["verdict"] != "fail" {
		t.Fatalf("verdict: got %v", sig["verdict"])
	}
}

func TestSignalsLogIsValidJSONLNewlineDelimited(t *testing.T) {
	// Appended lines must be valid JSON each, separated by \n.
	dir := t.TempDir()
	signals := filepath.Join(dir, "signals.log")
	for i := 0; i < 3; i++ {
		appendSignal(signals, map[string]interface{}{"verdict": "pass", "i": i})
	}
	f, _ := os.Open(signals)
	defer f.Close()
	sc := bufio.NewScanner(f)
	count := 0
	for sc.Scan() {
		var m map[string]interface{}
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %d invalid JSON: %v", count, err)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("got %d lines, want 3", count)
	}
}

func TestRunWatcher_StopReturnsEvenWhenReaperWaitsForLiveSubprocess(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw.log")
	signals := filepath.Join(dir, "signals.log")
	if err := os.WriteFile(raw, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := watcherConfig{
		RawLog:        raw,
		SignalsLog:    signals,
		PatternsJSON:  "[]",
		EnvelopeJSON:  `{"sensor_id":"x","version":"1.0.0","run_id":"00000000-0000-0000-0000-000000000000","started_at":"2026-05-09T15:30:00Z","sensor_type":"computational"}`,
		SubprocessPID: os.Getpid(), // ourselves: definitely alive, never dies during the test
		RegistryRoot:  dir,
		SensorID:      "x",
	}

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runWatcher(cfg, stop) }()

	time.Sleep(20 * time.Millisecond)
	close(stop)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runWatcher returned %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runWatcher did not return within 1s after stop closed (reaper-hang regression)")
	}
}
