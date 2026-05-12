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
"requires": [{"kind":"sensor","id":"blocking-tick"}],
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
	live, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", customHolderPID,
		v, &stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("AttachLiveDep: %v", err)
	}
	if live.ID != "blocking-tick" {
		t.Errorf("live.ID: got %q, want blocking-tick", live.ID)
	}
	if live.RunID == "" {
		t.Error("live.RunID: got empty, want non-empty")
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
	if entry.RunID != live.RunID {
		t.Errorf("entry.RunID: got %q, want %q (matches AttachLiveDep return)", entry.RunID, live.RunID)
	}
	if !entry.Blocking {
		t.Error("entry.Blocking: got false, want true")
	}
	if len(entry.HeldBy) != 1 {
		t.Fatalf("HeldBy length: got %d, want 1", len(entry.HeldBy))
	}
	h := entry.HeldBy[0]
	if h.Kind != "sensor" || h.ID != "holder-id" || h.PID != customHolderPID {
		t.Errorf("holder: got %+v, want kind=sensor id=holder-id pid=%d", h, customHolderPID)
	}

	// Tear down for cleanliness — kill the spawned subprocess.
	orchestrator.DetachLiveDep(live, root, "holder-id", v, io.Discard, io.Discard)
}

func TestAttachLiveDep_ReapsStaleSameHolder(t *testing.T) {
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")

	v := loadValidator(t)
	var stdout, stderr bytes.Buffer

	// First attach with a "real" pid (current process) so the dep is alive.
	livePID := os.Getpid()
	firstLive, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", livePID,
		v, &stdout, &stderr,
	)
	if err != nil {
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
	orchestrator.DetachLiveDep(firstLive, root, "holder-id", v, io.Discard, io.Discard)
}

func TestRebindDepHolderPID_Match(t *testing.T) {
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")
	v := loadValidator(t)
	var out, errBuf bytes.Buffer

	const oldPID = 12345
	const newPID = 67890
	live, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", oldPID,
		v, &out, &errBuf,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.RebindDepHolderPID("blocking-tick", root, "holder-id", oldPID, newPID); err != nil {
		t.Fatal(err)
	}

	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	entry := rs.FindEntry("blocking-tick")
	if len(entry.HeldBy) != 1 {
		t.Fatalf("HeldBy length: got %d, want 1", len(entry.HeldBy))
	}
	if entry.HeldBy[0].PID != newPID {
		t.Errorf("rebound PID: got %d, want %d", entry.HeldBy[0].PID, newPID)
	}

	orchestrator.DetachLiveDep(live, root, "holder-id", v, io.Discard, io.Discard)
}

func TestRebindDepHolderPID_NoMatch_IsNoop(t *testing.T) {
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")
	v := loadValidator(t)
	var out, errBuf bytes.Buffer

	const realPID = 12345
	live, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", realPID,
		v, &out, &errBuf,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Rebind with non-matching oldPID — should silently succeed.
	if err := orchestrator.RebindDepHolderPID("blocking-tick", root, "holder-id", 99999, 777); err != nil {
		t.Errorf("RebindDepHolderPID: got error %v, want nil (idempotent no-op)", err)
	}

	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	entry := rs.FindEntry("blocking-tick")
	if entry.HeldBy[0].PID != realPID {
		t.Errorf("PID after no-op rebind: got %d, want %d (unchanged)", entry.HeldBy[0].PID, realPID)
	}

	orchestrator.DetachLiveDep(live, root, "holder-id", v, io.Discard, io.Discard)
}

func TestRebindDepHolderPID_DepEntryMissing_IsNoop(t *testing.T) {
	root := t.TempDir()
	// No registry entry exists for this id.
	if err := orchestrator.RebindDepHolderPID("never-attached", root, "holder-id", 1, 2); err != nil {
		t.Errorf("RebindDepHolderPID for missing dep: got error %v, want nil (idempotent no-op)", err)
	}
}

// TestAttachDetachLiveDep_CoexistsWithNonBlockingEntry seeds the registry
// with a non-blocking entry for the same sensor id BEFORE calling
// AttachLiveDep, then verifies:
//   - AttachLiveDep targets only the blocking entry (or creates one):
//     the non-blocking entry's HeldBy is untouched.
//   - DetachLiveDep removes only the blocking entry, by run_id: the
//     non-blocking entry survives the teardown.
//
// This is the invariant that motivated the FindBlockingEntry +
// RemoveEntryByRunID rewrite — under the old FindEntry/RemoveEntry API
// the non-blocking entry would have been mutated or removed by the
// orchestrator's dep mechanics, which it has no claim on.
func TestAttachDetachLiveDep_CoexistsWithNonBlockingEntry(t *testing.T) {
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")
	v := loadValidator(t)

	// Seed a non-blocking entry with the same sensor id. This simulates a
	// concurrent /run-sensor that has its own registry entry for
	// blocking-tick (e.g., an observation phase running while a
	// blocking instance also exists). The dep mechanics must not touch
	// it.
	r := registry.NewRoot(root)
	nonBlockingRunID := "11111-nonblock"
	nonBlockingHolder := registry.HeldByEntry{Kind: "manual", AttachedAt: "2026-05-10T00:00:00Z"}
	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, _ := registry.Load(r)
		rs.Entries = append(rs.Entries, registry.RunningSensorEntry{
			SensorID:   "blocking-tick",
			RunID:      nonBlockingRunID,
			Blocking:   false,
			PID:        os.Getpid(),
			PGID:       os.Getpid(),
			WatcherPID: 0,
			StartedAt:  "2026-05-10T00:00:00Z",
			Command:    "echo non-blocking",
			LogDir:     filepath.Join(".runtime", "sensors", "blocking-tick", nonBlockingRunID),
			HeldBy:     []registry.HeldByEntry{nonBlockingHolder},
		})
		return registry.Save(r, rs)
	}); err != nil {
		t.Fatalf("seed non-blocking entry: %v", err)
	}

	var stdout, stderr bytes.Buffer
	live, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", os.Getpid(),
		v, &stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("AttachLiveDep: %v", err)
	}
	if live.RunID == "" {
		t.Fatal("AttachLiveDep returned empty RunID")
	}
	if live.RunID == nonBlockingRunID {
		t.Fatalf("AttachLiveDep returned the non-blocking entry's RunID %q — should have started/attached a blocking entry instead", live.RunID)
	}

	// Verify both entries exist, exactly one blocking and one not.
	rs, _ := registry.Load(r)
	entries := rs.FindEntries("blocking-tick")
	if len(entries) != 2 {
		t.Fatalf("entries for blocking-tick: got %d, want 2 (one blocking + one non-blocking)", len(entries))
	}
	var nonBlock, block *registry.RunningSensorEntry
	for _, e := range entries {
		if e.Blocking {
			block = e
		} else {
			nonBlock = e
		}
	}
	if nonBlock == nil {
		t.Fatal("non-blocking entry missing after Attach")
	}
	if block == nil {
		t.Fatal("blocking entry missing after Attach")
	}
	if nonBlock.RunID != nonBlockingRunID {
		t.Errorf("non-blocking RunID: got %q, want %q (untouched)", nonBlock.RunID, nonBlockingRunID)
	}
	if len(nonBlock.HeldBy) != 1 || nonBlock.HeldBy[0].Kind != "manual" {
		t.Errorf("non-blocking HeldBy: got %+v, want untouched [manual]", nonBlock.HeldBy)
	}
	if block.RunID != live.RunID {
		t.Errorf("blocking entry RunID: got %q, want %q", block.RunID, live.RunID)
	}

	// Detach by LiveDep — must remove only the blocking entry.
	orchestrator.DetachLiveDep(live, root, "holder-id", v, io.Discard, io.Discard)

	rs, _ = registry.Load(r)
	entries = rs.FindEntries("blocking-tick")
	if len(entries) != 1 {
		t.Fatalf("post-detach entries: got %d, want 1 (non-blocking survives)", len(entries))
	}
	if entries[0].Blocking {
		t.Errorf("post-detach survivor: got Blocking=true, want false")
	}
	if entries[0].RunID != nonBlockingRunID {
		t.Errorf("post-detach survivor RunID: got %q, want %q", entries[0].RunID, nonBlockingRunID)
	}
}
