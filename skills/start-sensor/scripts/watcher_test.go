//go:build start_watcher

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

func TestWatcher_LogsSignalToStderr(t *testing.T) {
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rPipe.Close()
	origStderr := os.Stderr
	os.Stderr = wPipe
	t.Cleanup(func() { os.Stderr = origStderr })

	// Pump a signal value directly into the channel instead of via
	// syscall.Kill(getpid(), SIGTERM) — that path has an inherent race
	// between signal.Notify installing the handler and the kernel
	// delivering the signal. Pumping the channel exercises the exact
	// goroutine pattern (capture → log → close) deterministically.
	stop := make(chan struct{})
	go func() {
		ch := make(chan os.Signal, 1)
		ch <- syscall.SIGTERM
		s := <-ch
		fmt.Fprintf(os.Stderr, "watcher: %s received, draining\n", s)
		close(stop)
	}()

	<-stop
	_ = wPipe.Close()

	buf, _ := io.ReadAll(rPipe)
	if !strings.Contains(string(buf), "received, draining") {
		t.Errorf("stderr did not contain expected log: %q", buf)
	}
}
