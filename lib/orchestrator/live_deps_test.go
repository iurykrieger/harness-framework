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
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
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
