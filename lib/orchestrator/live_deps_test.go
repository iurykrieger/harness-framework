package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func TestRunOneWithLiveDeps_AttachesAndDetachesBlockingDep(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	writeConsumer(t, root, "uses-tick")

	ctx := context.Background()
	exit := orchestrator.RunWithDepsRoot(ctx, "uses-tick", root, schemasDir, io.Discard, io.Discard)
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}

	r := registry.NewRoot(root)
	rs, err := registry.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	if rs.FindEntry("blocking-tick") != nil {
		t.Fatal("blocking dep should be torn down after the consumer ran")
	}
}

func writeBlockingDep(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A blocking sensor MUST:
	// - have execution.blocking: true
	// - have output: "stream" (blocking forces stream)
	// - NOT have cost.latency.timeout_ms
	// - have execution.output_parsing.patterns (stream requires it)
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Blocking tick",
"description": "blocking tick",
"determinism": "high",
"kind": "setup",
"type": "computational",
"output": "stream",
"regulation": "behaviour",
"phase": "continuous",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50}
},
"execution": {
  "command": "while true; do echo TICK; sleep 0.1; done",
  "blocking": true,
  "graceful_timeout_ms": 200,
  "exit_code_map": [{"exit_code":"*","verdict":"pass","severity":"info"}],
  "output_parsing": {"patterns":[{"regex":"^TICK$","verdict":"pass","severity":"info"}]}
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeConsumer(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, "sensors")
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Uses tick",
"description": "consumer",
"determinism": "high",
"kind": "assertion",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "on-demand",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"depends_on": ["blocking-tick"],
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50,"timeout_ms":2000}
},
"execution": {
  "command": "echo OK",
  "exit_code_map": [{"exit_code":0,"verdict":"pass","severity":"info"},{"exit_code":"*","verdict":"fail","severity":"high"}]
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// loadValidator returns a schema validator backed by the repo's schemas
// directory; failing to load aborts the test.
func loadValidator(t *testing.T) *schema.Validator {
	t.Helper()
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, code := schema.LoadValidator(schemasDir, io.Discard)
	if code != 0 {
		t.Fatalf("schema validator init failed (code=%d)", code)
	}
	return v
}

// loadDepSensor parses sensors/<id>.json from root into a Sensor struct.
func loadDepSensor(t *testing.T, root, id string) orchestrator.Sensor {
	t.Helper()
	path := filepath.Join(root, "sensors", id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return orchestrator.Sensor{ID: id, Path: path, JSON: m}
}

func TestAttachLiveDep_PassesHolderPID(t *testing.T) {
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")

	v := loadValidator(t)
	var stdout, stderr bytes.Buffer

	const customHolderPID = 99999
	depID, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", customHolderPID,
		v, &stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("AttachLiveDep: %v", err)
	}
	if depID != "blocking-tick" {
		t.Errorf("depID: got %q, want blocking-tick", depID)
	}

	r := registry.NewRoot(root)
	rs, err := registry.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	entry := rs.FindEntry("blocking-tick")
	if entry == nil {
		t.Fatal("expected entry in registry, got none")
	}
	if len(entry.HeldBy) != 1 {
		t.Fatalf("HeldBy length: got %d, want 1", len(entry.HeldBy))
	}
	h := entry.HeldBy[0]
	if h.Kind != "sensor" || h.ID != "holder-id" || h.PID != customHolderPID {
		t.Errorf("holder: got %+v, want kind=sensor id=holder-id pid=%d", h, customHolderPID)
	}

	// Tear down for cleanliness — kill the spawned subprocess.
	orchestrator.DetachLiveDep("blocking-tick", root, "holder-id", v, io.Discard, io.Discard)
}

func TestAttachLiveDep_ReapsStaleSameHolder(t *testing.T) {
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")

	v := loadValidator(t)
	var stdout, stderr bytes.Buffer

	// First attach with a "real" pid (current process) so the dep is alive.
	livePID := os.Getpid()
	if _, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", livePID,
		v, &stdout, &stderr,
	); err != nil {
		t.Fatal(err)
	}

	// Manually inject a dead-pid holder of the same id, simulating a
	// previous run that crashed.
	r := registry.NewRoot(root)
	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, _ := registry.Load(r)
		entry := rs.FindEntry("blocking-tick")
		entry.HeldBy = append(entry.HeldBy, registry.HeldByEntry{
			Kind: "sensor", ID: "holder-id", PID: 999999, AttachedAt: "2026-05-10T00:00:00Z",
		})
		return registry.Save(r, rs)
	}); err != nil {
		t.Fatal(err)
	}

	// Confirm the dead holder is in held_by before re-attach.
	rs, _ := registry.Load(r)
	if got := len(rs.FindEntry("blocking-tick").HeldBy); got != 2 {
		t.Fatalf("pre-condition: HeldBy length = %d, want 2", got)
	}

	// Re-attach with the same holder id — the reap should remove the
	// dead holder before adding the new one. End state: 1 holder (live).
	if _, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", livePID,
		v, &stdout, &stderr,
	); err != nil {
		t.Fatal(err)
	}
	rs, _ = registry.Load(r)
	entry := rs.FindEntry("blocking-tick")
	if len(entry.HeldBy) != 1 {
		t.Fatalf("post-attach HeldBy length: got %d, want 1 (reap should drop dead holder)", len(entry.HeldBy))
	}
	if entry.HeldBy[0].PID != livePID {
		t.Errorf("post-attach holder pid: got %d, want %d (live)", entry.HeldBy[0].PID, livePID)
	}

	// Cleanup.
	orchestrator.DetachLiveDep("blocking-tick", root, "holder-id", v, io.Discard, io.Discard)
}
