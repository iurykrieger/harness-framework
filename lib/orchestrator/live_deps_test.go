package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
	"github.com/iurykrieger/harness-framework/lib/watcher"
)

func TestRunOneWithLiveDeps_AttachesAndDetachesBlockingDep(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
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
	dir := filepath.Join(root, ".harness", "sensors")
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
	dir := filepath.Join(root, ".harness", "sensors")
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
	schemasDir := schematest.RepoSchemasDir(t)
	v, code := schema.LoadValidator(schemasDir, io.Discard)
	if code != 0 {
		t.Fatalf("schema validator init failed (code=%d)", code)
	}
	return v
}

// loadDepSensor parses sensors/<id>.json from root into a Sensor struct.
func loadDepSensor(t *testing.T, root, id string) orchestrator.Sensor {
	t.Helper()
	path := filepath.Join(root, ".harness", "sensors", id+".json")
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
	result, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", customHolderPID,
		v, &stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("AttachLiveDep: %v", err)
	}
	live := result.Live
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
	firstResult, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", livePID,
		v, &stdout, &stderr,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstLive := firstResult.Live

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
	result, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", oldPID,
		v, &out, &errBuf,
	)
	if err != nil {
		t.Fatal(err)
	}
	live := result.Live

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
	result, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", realPID,
		v, &out, &errBuf,
	)
	if err != nil {
		t.Fatal(err)
	}
	live := result.Live

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
			LogDir:     r.RelativeRunDir("blocking-tick", nonBlockingRunID),
			HeldBy:     []registry.HeldByEntry{nonBlockingHolder},
		})
		return registry.Save(r, rs)
	}); err != nil {
		t.Fatalf("seed non-blocking entry: %v", err)
	}

	var stdout, stderr bytes.Buffer
	result, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", os.Getpid(),
		v, &stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("AttachLiveDep: %v", err)
	}
	live := result.Live
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

func TestRunWithDepsRoot_AcceptsAbsolutePath(t *testing.T) {
	proj := t.TempDir()
	// Materialize a minimal valid computational sensor at an absolute path
	// OUTSIDE the project's .harness/sensors/ tree.
	s := sensortest.LoadComputational(t).AsMap()
	s["id"] = "abs-path-target"
	body, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	absSensorPath := filepath.Join(t.TempDir(), "sensor.json")
	if err := os.WriteFile(absSensorPath, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// projectRoot does NOT contain .harness/sensors/. The caller passes an
	// absolute path, which sensor.Resolve must accept verbatim.
	t.Setenv("HARNESS_REGISTRY_ROOT", proj)

	var stdout, stderr bytes.Buffer
	code := orchestrator.RunWithDepsRoot(context.Background(), absSensorPath, proj, schematest.RepoSchemasDir(t), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%s", code, stderr.String())
	}
	// Aggregate is the last JSONL line on stdout.
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		t.Fatalf("no stdout; stderr=%s", stderr.String())
	}
	lines := strings.Split(out, "\n")
	var agg map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &agg); err != nil {
		t.Fatalf("decode aggregate: %v; raw=%q", err, lines[len(lines)-1])
	}
	if got, _ := agg["sensor_id"].(string); got != "abs-path-target" {
		t.Errorf("aggregate.sensor_id = %q, want %q", got, "abs-path-target")
	}
}

func TestRunWithDepsRoot_AbsolutePathCascadeSensorID(t *testing.T) {
	proj := t.TempDir()
	// Target sensor declares a requires[kind=sensor] dep that DOES NOT exist.
	s := sensortest.LoadComputational(t).AsMap()
	s["id"] = "abs-cascade-target"
	s["requires"] = []interface{}{
		map[string]interface{}{"kind": "sensor", "id": "nonexistent-dep"},
	}
	body, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	absSensorPath := filepath.Join(t.TempDir(), "sensor.json")
	if err := os.WriteFile(absSensorPath, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Setenv("HARNESS_REGISTRY_ROOT", proj)

	var stdout, stderr bytes.Buffer
	_ = orchestrator.RunWithDepsRoot(context.Background(), absSensorPath, proj, schematest.RepoSchemasDir(t), &stdout, &stderr)
	// We do not assert exit code (it will be non-zero on dep failure).
	// We DO assert that any sensor_id emitted on stdout matches the
	// logical id pattern, NOT the abs path.
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		// Possibly the dep-resolution error went to stderr; that's OK
		// for this test — there's nothing on stdout to verify against.
		return
	}
	for _, line := range strings.Split(out, "\n") {
		var sig map[string]interface{}
		if err := json.Unmarshal([]byte(line), &sig); err != nil {
			continue
		}
		if id, _ := sig["sensor_id"].(string); id != "" {
			// The id must match the schema regex; specifically it must
			// not contain any path separators or absolute-path shape.
			if strings.ContainsAny(id, "/\\") {
				t.Errorf("sensor_id %q contains path separators (should be logical id only); raw=%s", id, line)
			}
		}
	}
}

// writeBlockingDepWithRequiresEnv writes a blocking-tick-shaped fixture
// that additionally declares a requires[kind=env] precondition. Used by
// the spawn-fresh gate test: the env name is chosen so process env can
// never satisfy it.
func writeBlockingDepWithRequiresEnv(t *testing.T, root, id, envName string) {
	t.Helper()
	body := map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Blocking with env requirement",
		"description": "blocking fixture requiring " + envName,
		"determinism": "high",
		"kind":        "setup",
		"type":        "computational",
		"output":      "stream",
		"regulation":  "behaviour",
		"phase":       "continuous",
		"triggers":    []interface{}{map[string]interface{}{"on": "manual"}},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"},
			},
		},
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50},
		},
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": envName},
		},
		"execution": map[string]interface{}{
			"command":             "while true; do echo TICK; sleep 0.1; done",
			"blocking":            true,
			"graceful_timeout_ms": 200,
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": "*", "verdict": "pass", "severity": "info"},
			},
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^TICK$", "verdict": "pass", "severity": "info"},
				},
			},
		},
	}
	writeSensorJSON(t, root, id, body)
}

// writeBlockingDepWithRequiresTool writes a blocking-tick-shaped fixture
// that declares a requires[kind=tool] precondition. Used by the re-attach
// gate-bypass test: the tool name is chosen so PATH cannot resolve it,
// which would fail PreflightGate if it ran — proving re-attach skipped it.
func writeBlockingDepWithRequiresTool(t *testing.T, root, id, toolName string) {
	t.Helper()
	body := map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Blocking with tool requirement",
		"description": "blocking fixture requiring tool " + toolName,
		"determinism": "high",
		"kind":        "setup",
		"type":        "computational",
		"output":      "stream",
		"regulation":  "behaviour",
		"phase":       "continuous",
		"triggers":    []interface{}{map[string]interface{}{"on": "manual"}},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"},
			},
		},
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50},
		},
		"requires": []interface{}{
			map[string]interface{}{"kind": "tool", "name": toolName},
		},
		"execution": map[string]interface{}{
			"command":             "while true; do echo TICK; sleep 0.1; done",
			"blocking":            true,
			"graceful_timeout_ms": 200,
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": "*", "verdict": "pass", "severity": "info"},
			},
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^TICK$", "verdict": "pass", "severity": "info"},
				},
			},
		},
	}
	writeSensorJSON(t, root, id, body)
}

// TestAttachLiveDep_SpawnFreshGateFails_ReturnsGateSignalNoSpawn verifies
// the core #36 fix: when the spawn-fresh branch hits a dep whose
// requires[kind=env] is unsatisfied, AttachLiveDep must NOT spawn the
// subprocess, NOT create a registry entry, and must return the canonical
// preflight-failed signal so the caller can cascade it.
func TestAttachLiveDep_SpawnFreshGateFails_ReturnsGateSignalNoSpawn(t *testing.T) {
	const envName = "__HARNESS_ATTACH_NEVER_SET__"
	root := t.TempDir()
	writeBlockingDepWithRequiresEnv(t, root, "needs-env-blocking", envName)

	dep := loadDepSensor(t, root, "needs-env-blocking")
	v, _ := schema.LoadValidator(schematest.RepoSchemasDir(t), io.Discard)

	var out, errBuf bytes.Buffer
	result, err := orchestrator.AttachLiveDep(
		context.Background(),
		dep,
		root,
		"holder-x",
		os.Getpid(),
		v,
		&out,
		&errBuf,
	)
	if err != nil {
		t.Fatalf("err: got %v, want nil (gate-fail is not a hard error)", err)
	}
	if result.GateSignal == nil {
		t.Fatal("GateSignal: got nil, want non-nil (env is unset)")
	}
	if result.Live.ID != "" || result.Live.RunID != "" {
		t.Errorf("Live: got %+v, want zero-value (no spawn happened)", result.Live)
	}
	md := result.GateSignal["metadata"].(map[string]interface{})
	if md["kind"] != "failed" || md["cause"] != "preflight_failed" {
		t.Errorf("GateSignal metadata: kind=%v, cause=%v; want failed/preflight_failed", md["kind"], md["cause"])
	}

	rs, _ := registry.Load(registry.NewRoot(root))
	if rs.FindEntry("needs-env-blocking") != nil {
		t.Error("registry has an entry for needs-env-blocking; expected none (no spawn)")
	}
}

// TestAttachLiveDep_ReattachToLiveDep_DoesNotGate seeds a live blocking
// entry for a dep whose requires[kind=tool] would fail PreflightGate, then
// calls AttachLiveDep. Re-attach must NOT run the gate — the dep is
// already alive with whatever env/PATH it had at original spawn; the
// holder's environment can legitimately differ.
func TestAttachLiveDep_ReattachToLiveDep_DoesNotGate(t *testing.T) {
	root := t.TempDir()
	writeBlockingDepWithRequiresTool(t, root, "live-dep-with-missing-tool", "absolutely-not-on-path-XYZ")

	r := registry.NewRoot(root)
	rs := registry.RunningSensors{Entries: []registry.RunningSensorEntry{{
		SensorID:   "live-dep-with-missing-tool",
		RunID:      fmt.Sprintf("%d-fake", os.Getpid()),
		Blocking:   true,
		PID:        os.Getpid(),
		PGID:       os.Getpid(),
		WatcherPID: 0,
		StartedAt:  "2026-05-12T00:00:00Z",
		Command:    "stub",
		LogDir:     r.RelativeRunDir("live-dep-with-missing-tool", fmt.Sprintf("%d-fake", os.Getpid())),
		HeldBy:     []registry.HeldByEntry{},
	}}}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	dep := loadDepSensor(t, root, "live-dep-with-missing-tool")
	v, _ := schema.LoadValidator(schematest.RepoSchemasDir(t), io.Discard)

	var out, errBuf bytes.Buffer
	result, err := orchestrator.AttachLiveDep(
		context.Background(),
		dep,
		root,
		"holder-x",
		os.Getpid(),
		v,
		&out,
		&errBuf,
	)
	if err != nil {
		t.Fatalf("err: got %v, want nil", err)
	}
	if result.GateSignal != nil {
		t.Errorf("GateSignal: got non-nil %v, want nil (re-attach must not gate)", result.GateSignal)
	}
	if result.Live.ID != "live-dep-with-missing-tool" {
		t.Errorf("Live.ID: got %q, want live-dep-with-missing-tool", result.Live.ID)
	}
}

// TestRunWithDepsImpl_BlockingDep_AggregateLast verifies the documented
// contract that the requested sensor's aggregate Signal is the LAST
// JSONL line on stdout, even when a blocking dep's teardown emits its
// own aggregate during detach (issue #19).
func TestRunWithDepsImpl_BlockingDep_AggregateLast(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	writeConsumer(t, root, "uses-tick")

	var out, errBuf bytes.Buffer
	exit := orchestrator.RunWithDepsRoot(context.Background(), "uses-tick", root, schemasDir, &out, &errBuf)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, errBuf.String())
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 JSONL Signals, got %d:\n%s", len(lines), out.String())
	}

	var last map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("decode last line: %v\nline=%q", err, lines[len(lines)-1])
	}
	if last["sensor_id"] != "uses-tick" {
		t.Errorf("last line sensor_id = %v, want uses-tick (entire stream:\n%s)", last["sensor_id"], out.String())
	}
	md, _ := last["metadata"].(map[string]interface{})
	if md == nil || md["kind"] != "aggregate" {
		t.Errorf("last line metadata.kind = %v, want aggregate", md)
	}
}

// writeNonBlockingFailingDep writes a non-blocking setup sensor whose
// command returns exit 1, producing verdict=fail. Used to seed a
// cascade scenario.
func writeNonBlockingFailingDep(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Failing dep",
"description": "exits non-zero",
"determinism": "high",
"kind": "setup",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "on-demand",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "fail", "expected_severity": "high"}]},
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50,"timeout_ms":2000}
},
"execution": {
  "command": "exit 1",
  "exit_code_map": [{"exit_code":0,"verdict":"pass","severity":"info"},{"exit_code":"*","verdict":"fail","severity":"high"}]
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeConsumerWithTwoDeps writes a consumer that depends on both
// `firstDep` and `secondDep` (in that order). The first listed dep
// is the failing-non-blocking one (so FirstFailedDep selects it).
// The second listed dep is the blocking one (which RunDeps will
// still attempt to attach because deps are processed in declaration
// order, and a failed non-blocking sibling does not stop processing
// of other deps unless they declare the failed one as their own dep).
//
// NOTE: This fixture deliberately makes the blocking dep NOT depend
// on the failing one, so the blocking dep attaches successfully and
// is on the LiveStack when the cascade signal for the consumer is
// built. That is the exact topology described in the spec's
// "Cascade with blocking deps already attached" section.
func writeConsumerWithTwoDeps(t *testing.T, root, id, firstDep, secondDep string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Consumer with two deps",
"description": "for cascade-with-blocking-dep test",
"determinism": "high",
"kind": "assertion",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "on-demand",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"requires": [{"kind":"sensor","id":"` + firstDep + `"},{"kind":"sensor","id":"` + secondDep + `"}],
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50,"timeout_ms":2000}
},
"execution": {
  "command": "echo never-runs",
  "exit_code_map": [{"exit_code":0,"verdict":"pass","severity":"info"}]
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunWithDepsImpl_BlockingDep_CascadeAggregateLast verifies that
// when a target cascades because one of its non-blocking deps failed,
// AND a sibling blocking dep was already attached, the cascade Signal
// for the requested sensor is the LAST JSONL line on stdout (the
// blocking dep's teardown signals appear before it).
func TestRunWithDepsImpl_BlockingDep_CascadeAggregateLast(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeNonBlockingFailingDep(t, root, "fails")
	writeBlockingDep(t, root, "blocking-tick")
	writeConsumerWithTwoDeps(t, root, "cascaded", "fails", "blocking-tick")

	var out, errBuf bytes.Buffer
	exit := orchestrator.RunWithDepsRoot(context.Background(), "cascaded", root, schemasDir, &out, &errBuf)
	if exit != 1 {
		t.Fatalf("exit=%d stderr=%s; want 1 (cascade)", exit, errBuf.String())
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d:\n%s", len(lines), out.String())
	}

	var last map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("decode last line: %v\nline=%q", err, lines[len(lines)-1])
	}
	if last["sensor_id"] != "cascaded" {
		t.Errorf("last sensor_id = %v, want cascaded (full stream:\n%s)", last["sensor_id"], out.String())
	}
	md, _ := last["metadata"].(map[string]interface{})
	if md == nil || md["kind"] != "cascade" {
		t.Errorf("last metadata.kind = %v, want cascade", md)
	}
}

// writeBlockingDepWithDep writes a blocking sensor (output=stream, blocking=true)
// that declares requires[{kind:"sensor", id:depID}]. Used to express chain
// topologies like failing-setup → blocking-intermediate → consumer, where the
// blocking intermediate has its own failing dep upstream.
func writeBlockingDepWithDep(t *testing.T, root, id, depID string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Blocking tick with dep",
"description": "blocking tick depending on ` + depID + `",
"determinism": "high",
"kind": "setup",
"type": "computational",
"output": "stream",
"regulation": "behaviour",
"phase": "continuous",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"requires": [{"kind":"sensor","id":"` + depID + `"}],
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

// writeConsumerWithDep writes an assertion sensor that declares exactly one
// requires[kind=sensor] entry pointing at depID. Differs from writeConsumer
// (which hard-codes "blocking-tick" as the dep) by parameterising the dep id.
func writeConsumerWithDep(t *testing.T, root, id, depID string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Consumer with one dep",
"description": "for cascade-through-blocking-dep test",
"determinism": "high",
"kind": "assertion",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "on-demand",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"requires": [{"kind":"sensor","id":"` + depID + `"}],
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50,"timeout_ms":2000}
},
"execution": {
  "command": "echo never-runs",
  "exit_code_map": [{"exit_code":0,"verdict":"pass","severity":"info"}]
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunWithDepsImpl_CascadeThroughBlockingDep verifies that when a failing
// non-blocking dep sits UPSTREAM of a blocking dep in the requires chain
// (failing-setup → blocking-intermediate → consumer), the cascade Signal
// propagates through the blocking dep. The blocking intermediate must NOT
// be attached as a subprocess; the consumer must NOT run its command.
//
// This is the regression test for issue #20.
func TestRunWithDepsImpl_CascadeThroughBlockingDep(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeNonBlockingFailingDep(t, root, "fails")
	writeBlockingDepWithDep(t, root, "blocking-intermediate", "fails")
	writeConsumerWithDep(t, root, "consumer", "blocking-intermediate")

	var out, errBuf bytes.Buffer
	exit := orchestrator.RunWithDepsRoot(context.Background(), "consumer", root, schemasDir, &out, &errBuf)
	if exit != 1 {
		t.Fatalf("exit=%d stderr=%s; want 1 (cascade)", exit, errBuf.String())
	}

	// The JSONL stream must contain exactly three Signals, in this order:
	//   1. failing-setup aggregate (verdict=fail)
	//   2. blocking-intermediate cascade (kind=cascade, failed_dep_id=fails)
	//   3. consumer cascade           (kind=cascade, failed_dep_id=blocking-intermediate)
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), out.String())
	}

	type want struct {
		sensorID    string
		verdict     string
		kind        string
		failedDepID string // empty when not applicable
	}
	wants := []want{
		{sensorID: "fails", verdict: "fail", kind: "aggregate"},
		{sensorID: "blocking-intermediate", verdict: "error", kind: "cascade", failedDepID: "fails"},
		{sensorID: "consumer", verdict: "error", kind: "cascade", failedDepID: "blocking-intermediate"},
	}

	for i, w := range wants {
		var sig map[string]interface{}
		if err := json.Unmarshal([]byte(lines[i]), &sig); err != nil {
			t.Fatalf("line %d unmarshal: %v\nline=%q", i, err, lines[i])
		}
		if got := sig["sensor_id"]; got != w.sensorID {
			t.Errorf("line %d sensor_id = %v, want %s", i, got, w.sensorID)
		}
		if got := sig["verdict"]; got != w.verdict {
			t.Errorf("line %d verdict = %v, want %s", i, got, w.verdict)
		}
		md, _ := sig["metadata"].(map[string]interface{})
		if got := md["kind"]; got != w.kind {
			t.Errorf("line %d metadata.kind = %v, want %s", i, got, w.kind)
		}
		if w.failedDepID != "" {
			if got := md["failed_dep_id"]; got != w.failedDepID {
				t.Errorf("line %d metadata.failed_dep_id = %v, want %s", i, got, w.failedDepID)
			}
			// Cascade Signals must have zero cost — the dep never ran.
			cost, _ := sig["cost_actual"].(map[string]interface{})
			if got := cost["latency_ms"]; got != float64(0) {
				t.Errorf("line %d cost_actual.latency_ms = %v, want 0", i, got)
			}
		}
	}

	// The blocking intermediate must NOT have spawned a subprocess: the
	// registry must contain no entry for it.
	r := registry.NewRoot(root)
	rs, err := registry.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	if rs.FindEntry("blocking-intermediate") != nil {
		t.Error("blocking-intermediate must not have been attached; found a registry entry")
	}
}

func TestStartBlockingDep_UsesRunIDLayout(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))

	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")

	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{}
	holder := registry.HeldByEntry{Kind: "sensor", ID: "holder-id", PID: 99999, AttachedAt: "2026-05-14T00:00:00Z"}

	runID, err := orchestrator.ExportedStartBlockingDep(&rs, r, dep, holder, root)
	if err != nil {
		t.Fatalf("ExportedStartBlockingDep: %v", err)
	}
	t.Cleanup(func() {
		teardownEntry(t, r, runID)
	})

	if _, err := os.Stat(r.RawLogRun("blocking-tick", runID)); err != nil {
		t.Errorf("RawLogRun missing: %v", err)
	}
	if _, err := os.Stat(r.SignalsLogRun("blocking-tick", runID)); err != nil {
		t.Errorf("SignalsLogRun missing: %v", err)
	}
	if _, err := os.Stat(r.LegacyRawLog("blocking-tick")); err == nil {
		t.Error("LegacyRawLog still exists at flat path; expected staged-then-renamed away")
	}
	if _, err := os.Stat(r.LegacySignalsLog("blocking-tick")); err == nil {
		t.Error("LegacySignalsLog should never be created on the fix path")
	}

	entry := rs.FindEntry("blocking-tick")
	if entry == nil {
		t.Fatal("registry entry missing")
	}
	if entry.RunID != runID {
		t.Errorf("entry.RunID: got %q, want %q", entry.RunID, runID)
	}
	if entry.LogDir != r.RelativeRunDir("blocking-tick", runID) {
		t.Errorf("entry.LogDir: got %q, want %q", entry.LogDir, r.RelativeRunDir("blocking-tick", runID))
	}
	if entry.WatcherPID <= 0 {
		t.Errorf("entry.WatcherPID: got %d, want > 0", entry.WatcherPID)
	}
}

// pluginRootForTest returns the plugin checkout's absolute path. Tests
// must point CLAUDE_PLUGIN_ROOT at it so lib/watcher.Spawn can find the
// watcher source tree.
func pluginRootForTest(t *testing.T) string {
	t.Helper()
	// orchestrator package lives at <pluginRoot>/lib/orchestrator
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

// teardownEntry tears down a spawned subprocess + watcher recorded under
// runID. Idempotent; safe to call on a partially-completed test.
func teardownEntry(t *testing.T, r registry.Root, runID string) {
	t.Helper()
	rs, err := registry.Load(r)
	if err != nil {
		return
	}
	entry := rs.FindEntryByRunID(runID)
	if entry == nil {
		return
	}
	if entry.PGID > 0 {
		_ = syscallKill(-entry.PGID, syscall.SIGKILL)
	}
	if entry.WatcherPID > 0 {
		_ = syscallKill(entry.WatcherPID, syscall.SIGKILL)
	}
}

// syscallKill wraps syscall.Kill so the test file does not need its
// own platform-conditional build tag. SIGKILL is universally available
// on darwin and linux (the only test targets per the project's go test
// invocation).
func syscallKill(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

func writeBlockingDepNoPatterns(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Same as writeBlockingDep but output: "single" so output_parsing is
	// not required by the schema. blocking: true is still legal for
	// output: single.
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Quiet blocker",
"description": "blocker without patterns",
"determinism": "high",
"kind": "setup",
"type": "computational",
"output": "single",
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
  "command": "sleep 5",
  "blocking": true,
  "graceful_timeout_ms": 200,
  "exit_code_map": [{"exit_code":"*","verdict":"pass","severity":"info"}]
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStartBlockingDep_NoPatterns_StillSpawnsWatcher(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))
	root := t.TempDir()
	writeBlockingDepNoPatterns(t, root, "quiet-blocker")
	dep := loadDepSensor(t, root, "quiet-blocker")

	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{}
	holder := registry.HeldByEntry{Kind: "sensor", ID: "h", PID: 1, AttachedAt: "2026-05-14T00:00:00Z"}

	runID, err := orchestrator.ExportedStartBlockingDep(&rs, r, dep, holder, root)
	if err != nil {
		t.Fatalf("ExportedStartBlockingDep: %v", err)
	}
	t.Cleanup(func() { teardownEntry(t, r, runID) })

	entry := rs.FindEntry("quiet-blocker")
	if entry == nil || entry.WatcherPID <= 0 {
		t.Fatalf("expected watcher_pid > 0, entry=%+v", entry)
	}
	// signals.log MUST exist and be empty (or contain only post-spawn signals).
	info, err := os.Stat(r.SignalsLogRun("quiet-blocker", runID))
	if err != nil {
		t.Fatalf("signals.log: %v", err)
	}
	_ = info
}

func writeBlockingDepWithErrorPattern(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Error emitter",
"description": "emits one ERROR line then sleeps",
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
  "command": "while true; do echo 'ERROR: ouch'; sleep 0.1; done",
  "blocking": true,
  "graceful_timeout_ms": 200,
  "exit_code_map": [{"exit_code":"*","verdict":"pass","severity":"info"}],
  "output_parsing": {"patterns":[{"regex":"^ERROR","verdict":"fail","severity":"high","rationale":"error line"}]}
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStartBlockingDep_PatternsEmitSignals(t *testing.T) {
	// The orchestrator test binary's TestMain replaces watcher.SpawnFn with
	// a fake returning a fixed PID; opt back into the real spawner so this
	// test exercises the actual watcher pipeline (compile via `go run`,
	// fsnotify, pattern matching, append to signals.log).
	prev := watcher.SpawnFn
	watcher.SpawnFn = watcher.RealSpawn
	t.Cleanup(func() { watcher.SpawnFn = prev })

	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))
	root := t.TempDir()
	writeBlockingDepWithErrorPattern(t, root, "errs")
	dep := loadDepSensor(t, root, "errs")

	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{}
	holder := registry.HeldByEntry{Kind: "sensor", ID: "h", PID: 1, AttachedAt: "2026-05-14T00:00:00Z"}

	runID, err := orchestrator.ExportedStartBlockingDep(&rs, r, dep, holder, root)
	if err != nil {
		t.Fatalf("ExportedStartBlockingDep: %v", err)
	}
	t.Cleanup(func() { teardownEntry(t, r, runID) })

	// Poll signals.log for up to 15 seconds (allow for cold compile cache).
	sigsPath := r.SignalsLogRun("errs", runID)
	deadline := time.Now().Add(15 * time.Second)
	var lines []string
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(sigsPath)
		if len(data) > 0 {
			lines = strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) >= 1 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(lines) == 0 {
		t.Fatalf("signals.log empty after 15s; expected >=1 individual signal")
	}
	var sig map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &sig); err != nil {
		t.Fatalf("parse signal: %v (line=%q)", err, lines[0])
	}
	if v, _ := sig["verdict"].(string); v != "fail" {
		t.Errorf("verdict: got %q, want fail", v)
	}
	if s, _ := sig["severity"].(string); s != "high" {
		t.Errorf("severity: got %q, want high", s)
	}
	md, _ := sig["metadata"].(map[string]interface{})
	if k, _ := md["kind"].(string); k != "individual" {
		t.Errorf("metadata.kind: got %q, want individual", k)
	}
}

func TestStartBlockingDep_PluginRootMissing(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", "")
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")

	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{}
	holder := registry.HeldByEntry{Kind: "sensor", ID: "h", PID: 1, AttachedAt: "2026-05-14T00:00:00Z"}

	_, err := orchestrator.ExportedStartBlockingDep(&rs, r, dep, holder, root)
	if err == nil {
		t.Fatal("expected error when CLAUDE_PLUGIN_ROOT empty, got nil")
	}
	if !strings.Contains(err.Error(), "plugin root not set") {
		t.Errorf("error: got %q, want substring 'plugin root not set'", err)
	}
	if len(rs.Entries) != 0 {
		t.Errorf("registry should be untouched; got %d entries", len(rs.Entries))
	}
	if _, statErr := os.Stat(r.SensorDir("blocking-tick")); statErr == nil {
		t.Error("SensorDir created despite early error; want no side effects")
	}
}

func TestStartBlockingDep_WatcherSpawnFailure(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")

	prev := watcher.SpawnFn
	watcher.SpawnFn = func(opts watcher.SpawnOpts) (int, error) {
		return 0, fmt.Errorf("forced")
	}
	t.Cleanup(func() { watcher.SpawnFn = prev })

	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{}
	holder := registry.HeldByEntry{Kind: "sensor", ID: "h", PID: 1, AttachedAt: "2026-05-14T00:00:00Z"}

	_, err := orchestrator.ExportedStartBlockingDep(&rs, r, dep, holder, root)
	if err == nil {
		t.Fatal("expected error from forced watcher failure, got nil")
	}
	if !strings.Contains(err.Error(), "forced") {
		t.Errorf("error: got %q, want substring 'forced'", err)
	}
	if len(rs.Entries) != 0 {
		t.Errorf("registry should be untouched; got %d entries", len(rs.Entries))
	}
	// Run dir must have been removed.
	entries, _ := os.ReadDir(r.SensorDir("blocking-tick"))
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("found run-dir %q after watcher-failure cleanup; should be gone", e.Name())
		}
	}
}

func TestDetachLiveDep_KillsWatcher(t *testing.T) {
	// The orchestrator test binary's TestMain replaces watcher.SpawnFn with
	// a fake returning a fixed PID; opt back into the real spawner so this
	// test exercises the actual watcher pipeline and we can observe the
	// watcher process being killed on detach.
	prev := watcher.SpawnFn
	watcher.SpawnFn = watcher.RealSpawn
	t.Cleanup(func() { watcher.SpawnFn = prev })

	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")

	v := loadValidator(t)
	var stdout, stderr bytes.Buffer

	// DetachLiveDep removes the holder using PID: os.Getpid(); match it so
	// the only-holder branch fires and we actually exercise stopBlockingDep.
	holderPID := os.Getpid()
	result, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "h", holderPID,
		v, &stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("AttachLiveDep: %v", err)
	}

	r := registry.NewRoot(root)
	rsBefore, _ := registry.Load(r)
	entry := rsBefore.FindEntry("blocking-tick")
	if entry == nil || entry.WatcherPID <= 0 {
		t.Fatalf("expected live watcher in registry, entry=%+v", entry)
	}
	savedWatcherPID := entry.WatcherPID

	// Detach should tear down the dep (only holder) and kill the watcher.
	orchestrator.DetachLiveDep(result.Live, root, "h", v, &stdout, &stderr)

	// Poll up to 3 seconds for the watcher to be reaped. The watcher was
	// spawned via exec.Command + cmd.Process.Release(), so the test
	// process is its parent but no Wait goroutine is running — once
	// SIGTERM/SIGKILL hits, the kernel keeps the entry as a zombie
	// until we Wait4() it. Wait4 with WNOHANG returns the pid when
	// the process is reapable (terminated), 0 when still running, and
	// ECHILD when already reaped or not a child. Any of "reaped" or
	// "no longer a child" means the watcher process is dead.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var ws syscall.WaitStatus
		wpid, err := syscall.Wait4(savedWatcherPID, &ws, syscall.WNOHANG, nil)
		if wpid == savedWatcherPID || err != nil {
			return // success: terminated and reaped, or not our child anymore
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("watcher pid %d still alive 3s after DetachLiveDep", savedWatcherPID)
	// Best-effort cleanup so the orphan does not survive the test process.
	_ = syscall.Kill(savedWatcherPID, syscall.SIGKILL)
}
