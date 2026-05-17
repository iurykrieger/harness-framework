package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/watcher"
)

// writeMidRunConsumer writes an assertion sensor whose command would block
// for slowSeconds seconds (well past any reasonable test budget). The
// orchestrator must cancel this consumer when its blocking dep dies
// mid-run; absent that cancellation the test will hang until slowSeconds
// elapses, falsifying the wall-clock bound.
func writeMidRunConsumer(t *testing.T, root, id, depID string, slowSeconds int) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{
"id": "%s",
"version": "1.0.0",
"name": "Mid-run consumer",
"description": "blocks long enough for dep to die mid-run",
"determinism": "high",
"kind": "assertion",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "on-demand",
"triggers": [{"on": "manual"}],
"use_cases": ["fake-uc"],
"requires": [{"kind":"sensor","id":"%s"}],
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50,"timeout_ms":%d}
},
"execution": {
  "command": "sleep %d; echo OK",
  "exit_code_map": [{"exit_code":0,"verdict":"pass","severity":"info"},{"exit_code":"*","verdict":"fail","severity":"high"}]
}
}`, id, depID, slowSeconds*1000+5000, slowSeconds)
	if err := os.WriteFile(filepath.Join(dir, id+".yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunWithDepsImpl_DepDiesMidRun_CancelsConsumer is the regression test
// for the gap documented in issue #49. A blocking dep boots healthy
// (passes the health gate), the consumer starts running, then the dep
// dies mid-run. The orchestrator must:
//
//  1. Observe the dep's subprocess death within the polling window.
//  2. Cancel the consumer's subprocess so its wall-clock is bounded by
//     dep-death + a small grace, NOT by the consumer's own timeout.
//  3. Annotate the consumer's aggregate with metadata.cancelled_by =
//     <dep.id> and force verdict=error, so downstream consumers
//     (FirstFailedDep, heal-sensor, reports) can distinguish this from a
//     natural fail / timeout.
//
// Wall-clock bound: the consumer's command is "sleep 60", so absent
// cancellation the test would hang until that sleep finishes. We assert
// the orchestrator returns in well under 60 seconds.
func TestRunWithDepsImpl_DepDiesMidRun_CancelsConsumer(t *testing.T) {
	// Tight polling so the test does not have to wait the production
	// 100ms minimum tick — we use 20ms here.
	restoreDepDeath := orchestrator.SetDepDeathPollInterval(20 * time.Millisecond)
	defer restoreDepDeath()

	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	writeMidRunConsumer(t, root, "uses-tick", "blocking-tick", 60)

	// Goroutine that waits for the dep entry to appear in the registry,
	// then kills its subprocess group to simulate mid-run death. The
	// orchestrator's death-watcher must then cancel the consumer.
	depKilled := make(chan struct{})
	go func() {
		defer close(depKilled)
		r := registry.NewRoot(root)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			rs, err := registry.Load(r)
			if err == nil {
				entry := rs.FindBlockingEntry("blocking-tick")
				if entry != nil && entry.PGID > 0 && watcher.IsSubprocessAlive(entry.PID) {
					_ = syscallKill(-entry.PGID, syscall.SIGTERM)
					// Wait until the kernel reports the subprocess gone so
					// the orchestrator's poller has something to observe.
					killDeadline := time.Now().Add(1 * time.Second)
					for time.Now().Before(killDeadline) {
						if !watcher.IsSubprocessAlive(entry.PID) {
							return
						}
						time.Sleep(10 * time.Millisecond)
					}
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	var out, errBuf bytes.Buffer
	start := time.Now()
	exit := orchestrator.RunWithDepsRoot(context.Background(), "uses-tick", root, schemasDir, &out, &errBuf)
	elapsed := time.Since(start)
	<-depKilled

	if elapsed > 10*time.Second {
		t.Fatalf("RunWithDepsRoot took %v, want <10s (consumer was not cancelled when dep died mid-run); stderr=%s", elapsed, errBuf.String())
	}
	if exit != 0 {
		t.Fatalf("exit=%d, want 0 (root sensor's aggregate was emitted, just with error verdict); stderr=%s\nstdout=%s", exit, errBuf.String(), out.String())
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("no JSONL output")
	}
	var last map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("decode last line: %v\nline=%q\nfull=%s", err, lines[len(lines)-1], out.String())
	}
	if last["sensor_id"] != "uses-tick" {
		t.Fatalf("last line sensor_id = %v, want uses-tick (full stream:\n%s)", last["sensor_id"], out.String())
	}

	if v, _ := last["verdict"].(string); v != "error" {
		t.Errorf("consumer verdict = %q, want error (cancelled by dep death); aggregate=%s", v, lines[len(lines)-1])
	}
	if s, _ := last["severity"].(string); s != "high" {
		t.Errorf("consumer severity = %q, want high", s)
	}
	md, _ := last["metadata"].(map[string]interface{})
	if md == nil {
		t.Fatalf("aggregate has no metadata: %s", lines[len(lines)-1])
	}
	if md["cancelled_by"] != "blocking-tick" {
		t.Errorf("metadata.cancelled_by = %v, want \"blocking-tick\"", md["cancelled_by"])
	}
	reason, _ := md["cancellation_reason"].(string)
	if reason == "" {
		t.Errorf("metadata.cancellation_reason missing, want a non-empty diagnostic (got metadata=%v)", md)
	}
}
