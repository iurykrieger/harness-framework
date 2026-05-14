package watcher

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const fastTimeout = 200 * time.Millisecond
const fastPoll = 10 * time.Millisecond

func writeSignals(t *testing.T, path string, signals []map[string]interface{}) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, s := range signals {
		if err := enc.Encode(s); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
}

func individual(verdict string) map[string]interface{} {
	return map[string]interface{}{
		"sensor_id":   "x",
		"version":     "0.0.0",
		"run_id":      "r",
		"started_at":  "2026-05-14T00:00:00Z",
		"finished_at": "2026-05-14T00:00:00Z",
		"verdict":     verdict,
		"severity":    "info",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": "rat"}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "individual"},
	}
}

func envelopeSig() map[string]interface{} {
	m := individual("pass")
	m["metadata"] = map[string]interface{}{"kind": "envelope"}
	return m
}

func TestWaitForReady_PreExistingPassSignal(t *testing.T) {
	tmp := t.TempDir()
	sigs := filepath.Join(tmp, "signals.log")
	writeSignals(t, sigs, []map[string]interface{}{individual("pass")})

	r := WaitForReady(HealthGateOpts{
		SignalsLogPath: sigs,
		SubprocessPID:  os.Getpid(),
		Timeout:        fastTimeout,
		PollInterval:   fastPoll,
	})
	if r.Outcome != OutcomeReady {
		t.Fatalf("outcome = %s, want ready", r.Outcome)
	}
	if v, _ := r.Signal["verdict"].(string); v != "pass" {
		t.Errorf("signal verdict = %q, want pass", v)
	}
}

func TestWaitForReady_WarnIsAlsoReady(t *testing.T) {
	tmp := t.TempDir()
	sigs := filepath.Join(tmp, "signals.log")
	writeSignals(t, sigs, []map[string]interface{}{individual("warn")})

	r := WaitForReady(HealthGateOpts{
		SignalsLogPath: sigs,
		SubprocessPID:  os.Getpid(),
		Timeout:        fastTimeout,
		PollInterval:   fastPoll,
	})
	if r.Outcome != OutcomeReady {
		t.Fatalf("outcome = %s, want ready", r.Outcome)
	}
}

func TestWaitForReady_FailSignal(t *testing.T) {
	tmp := t.TempDir()
	sigs := filepath.Join(tmp, "signals.log")
	writeSignals(t, sigs, []map[string]interface{}{individual("fail")})

	r := WaitForReady(HealthGateOpts{
		SignalsLogPath: sigs,
		SubprocessPID:  os.Getpid(),
		Timeout:        fastTimeout,
		PollInterval:   fastPoll,
	})
	if r.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", r.Outcome)
	}
	if v, _ := r.Signal["verdict"].(string); v != "fail" {
		t.Errorf("signal verdict = %q, want fail", v)
	}
}

func TestWaitForReady_ErrorSignal(t *testing.T) {
	tmp := t.TempDir()
	sigs := filepath.Join(tmp, "signals.log")
	writeSignals(t, sigs, []map[string]interface{}{individual("error")})

	r := WaitForReady(HealthGateOpts{
		SignalsLogPath: sigs,
		SubprocessPID:  os.Getpid(),
		Timeout:        fastTimeout,
		PollInterval:   fastPoll,
	})
	if r.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", r.Outcome)
	}
}

func TestWaitForReady_EnvelopeLineSkipped(t *testing.T) {
	tmp := t.TempDir()
	sigs := filepath.Join(tmp, "signals.log")
	writeSignals(t, sigs, []map[string]interface{}{envelopeSig(), individual("pass")})

	r := WaitForReady(HealthGateOpts{
		SignalsLogPath: sigs,
		SubprocessPID:  os.Getpid(),
		Timeout:        fastTimeout,
		PollInterval:   fastPoll,
	})
	if r.Outcome != OutcomeReady {
		t.Fatalf("outcome = %s, want ready", r.Outcome)
	}
}

func TestWaitForReady_MalformedLineSkipped(t *testing.T) {
	tmp := t.TempDir()
	sigs := filepath.Join(tmp, "signals.log")
	if err := os.WriteFile(sigs, []byte("garbage not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSignals(t, sigs, []map[string]interface{}{individual("pass")})

	r := WaitForReady(HealthGateOpts{
		SignalsLogPath: sigs,
		SubprocessPID:  os.Getpid(),
		Timeout:        fastTimeout,
		PollInterval:   fastPoll,
	})
	if r.Outcome != OutcomeReady {
		t.Fatalf("outcome = %s, want ready", r.Outcome)
	}
}

func TestWaitForReady_TimedOut(t *testing.T) {
	tmp := t.TempDir()
	sigs := filepath.Join(tmp, "signals.log") // file never created

	start := time.Now()
	r := WaitForReady(HealthGateOpts{
		SignalsLogPath: sigs,
		SubprocessPID:  os.Getpid(),
		Timeout:        80 * time.Millisecond,
		PollInterval:   10 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if r.Outcome != OutcomeTimedOut {
		t.Fatalf("outcome = %s, want timed_out", r.Outcome)
	}
	if elapsed < 70*time.Millisecond {
		t.Errorf("elapsed = %v, want >= 70ms (timeout should be respected)", elapsed)
	}
	if r.Signal != nil {
		t.Errorf("signal = %v, want nil for timed_out", r.Signal)
	}
}

func TestWaitForReady_DiedSilently(t *testing.T) {
	// Spawn a short-lived subprocess we control.
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait() // Reap immediately so PID truly goes dead.

	tmp := t.TempDir()
	sigs := filepath.Join(tmp, "signals.log") // file never created — subprocess died before emitting

	r := WaitForReady(HealthGateOpts{
		SignalsLogPath: sigs,
		SubprocessPID:  pid,
		Timeout:        500 * time.Millisecond,
		PollInterval:   10 * time.Millisecond,
	})
	if r.Outcome != OutcomeDiedSilently {
		t.Fatalf("outcome = %s, want died_silently", r.Outcome)
	}
}

func TestWaitForReady_SignalAppearsMidway(t *testing.T) {
	tmp := t.TempDir()
	sigs := filepath.Join(tmp, "signals.log")
	_ = os.WriteFile(sigs, nil, 0o644)

	// Write the pass signal after a short delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		writeSignals(t, sigs, []map[string]interface{}{individual("pass")})
	}()

	r := WaitForReady(HealthGateOpts{
		SignalsLogPath: sigs,
		SubprocessPID:  os.Getpid(),
		Timeout:        1 * time.Second,
		PollInterval:   20 * time.Millisecond,
	})
	if r.Outcome != OutcomeReady {
		t.Fatalf("outcome = %s, want ready", r.Outcome)
	}
}

func TestWaitForReady_MissingFileTreatedAsEmpty(t *testing.T) {
	tmp := t.TempDir()
	sigs := filepath.Join(tmp, "does-not-exist.log")

	r := WaitForReady(HealthGateOpts{
		SignalsLogPath: sigs,
		SubprocessPID:  os.Getpid(),
		Timeout:        50 * time.Millisecond,
		PollInterval:   10 * time.Millisecond,
	})
	if r.Outcome != OutcomeTimedOut {
		t.Fatalf("outcome = %s, want timed_out (missing file = no signals = timeout)", r.Outcome)
	}
}
