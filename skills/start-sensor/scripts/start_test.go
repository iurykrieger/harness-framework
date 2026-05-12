//go:build start_sensor

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/cli"
	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func TestMain(m *testing.M) {
	// Install a stub watcher binary (copy of /usr/bin/true) in the test
	// binary's directory so tests that reach the spawn-watcher step do not
	// fail with "no such file". The watcher exits immediately (pass), which
	// is fine because watcher behaviour is covered by start_watcher tests.
	exe, err := os.Executable()
	if err != nil {
		panic("TestMain: os.Executable failed: " + err.Error())
	}
	watcher := filepath.Join(filepath.Dir(exe), "watcher")
	if _, serr := os.Stat(watcher); os.IsNotExist(serr) {
		stub, rerr := os.ReadFile("/usr/bin/true")
		if rerr != nil {
			panic("TestMain: read /usr/bin/true: " + rerr.Error())
		}
		if werr := os.WriteFile(watcher, stub, 0o755); werr != nil {
			panic("TestMain: write watcher stub: " + werr.Error())
		}
	}
	os.Exit(m.Run())
}

// testResult builds a minimal registry.Result usable by bootstrapFor in
// tests. The diagnostic fields it carries (registry_path, _source,
// _exists) populate every emitted signal's metadata; tests that need to
// assert those values can also use this helper.
func testResult(projectRoot string) registry.Result {
	return registry.Result{
		Root:        registry.NewRoot(projectRoot),
		ProjectRoot: projectRoot,
		Source:      registry.SourceWalkUp,
		Exists:      false,
		State:       registry.RunningSensors{Version: 1},
	}
}

// bootstrapFor wraps a registry.Result in a cli.BootstrapResult suitable for
// passing to runStart in tests. The schema validator is loaded so that signal
// validation runs; errors are written to errBuf.
func bootstrapFor(t *testing.T, res registry.Result, errBuf *bytes.Buffer) cli.BootstrapResult {
	t.Helper()
	v, _ := schema.LoadValidator("", errBuf)
	return cli.BootstrapResult{
		Res:       res,
		Validator: v,
		Diagnose:  registry.DiagnoseMetadata(res),
	}
}

func writeFixtureSensor(t *testing.T, projectRoot, id string, body map[string]interface{}) string {
	t.Helper()
	dir := filepath.Join(projectRoot, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body["id"] = id
	data, _ := json.MarshalIndent(body, "", "  ")
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStart_RejectsNonBlocking(t *testing.T) {
	root := t.TempDir()
	writeFixtureSensor(t, root, "not-blocking", map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Not blocking fixture",
		"description": "non-blocking fixture",
		"determinism": "high",
		"kind":        "observation",
		"type":        "computational",
		"regulation":  "behaviour",
		"phase":       "on-demand",
		"output":      "single",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50, "timeout_ms": 1000},
		},
		"triggers": []interface{}{
			map[string]interface{}{"on": "manual"},
		},
		"execution": map[string]interface{}{
			"command": "echo hi",
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{
					"fixture":           "sensors/fixtures/not-blocking/pass.txt",
					"expected_verdict":  "pass",
					"expected_severity": "info",
				},
			},
		},
	})
	exit, sig := runStart(bootstrapFor(t, testResult(root), new(bytes.Buffer)), []string{"not-blocking"})
	if exit != 2 {
		t.Fatalf("expected exit 2, got %d", exit)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "failed" {
		t.Errorf("metadata.kind: got %v, want failed", md["kind"])
	}
	if md["cause"] != "not_blocking" {
		t.Errorf("metadata.cause: got %v, want not_blocking", md["cause"])
	}
}

func TestStart_RejectsAlreadyRunning(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(r, registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "loop", Blocking: true, PID: registry.SelfPID(), PGID: registry.SelfPID(), HeldBy: []registry.HeldByEntry{{Kind: "manual"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	writeFixtureSensor(t, root, "loop", blockingFixtureBody())
	exit, sig := runStart(bootstrapFor(t, testResult(root), new(bytes.Buffer)), []string{"loop"})
	if exit != 1 {
		t.Fatalf("expected exit 1, got %d", exit)
	}
	if sig["metadata"].(map[string]interface{})["kind"] != "rejected" {
		t.Fatalf("metadata.kind: got %v", sig["metadata"].(map[string]interface{})["kind"])
	}
}

func blockingFixtureBody() map[string]interface{} {
	return map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Blocking fixture",
		"description": "blocking fixture",
		"determinism": "high",
		"kind":        "observation",
		"type":        "computational",
		"regulation":  "behaviour",
		"phase":       "continuous",
		"output":      "stream",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50},
		},
		"triggers": []interface{}{
			map[string]interface{}{"on": "manual"},
		},
		"execution": map[string]interface{}{
			"command":  "while true; do echo TICK; sleep 0.1; done",
			"blocking": true,
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^TICK$", "verdict": "pass", "severity": "info"},
				},
			},
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": "*", "verdict": "pass", "severity": "info"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{
					"fixture":           "sensors/fixtures/loop/pass.txt",
					"expected_verdict":  "pass",
					"expected_severity": "info",
				},
			},
		},
	}
}

// writeBlockingTarget writes a fixture sensor at <root>/sensors/<id>.json
// with execution.blocking=true and a stream output_parsing pattern.
// Adds requires[kind=sensor] if dependsOn is non-nil, prepare[] if non-nil.
func writeBlockingTarget(t *testing.T, root, id string, dependsOn []string, prepare []map[string]string) {
	t.Helper()
	body := blockingFixtureBody()
	if len(dependsOn) > 0 {
		rs := []interface{}{}
		for _, d := range dependsOn {
			rs = append(rs, map[string]interface{}{"kind": "sensor", "id": d})
		}
		body["requires"] = rs
	}
	if len(prepare) > 0 {
		var rs []interface{}
		if existing, ok := body["requires"].([]interface{}); ok {
			rs = existing
		}
		for _, p := range prepare {
			rs = append(rs, map[string]interface{}{"kind": "step", "command": p["command"]})
		}
		body["requires"] = rs
	}
	writeFixtureSensor(t, root, id, body)
}

// writeNonBlockingSetupDep writes a kind=setup non-blocking sensor that
// runs `command` (typically "true" or "false").
func writeNonBlockingSetupDep(t *testing.T, root, id, command string) {
	t.Helper()
	body := map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Setup " + id,
		"description": "setup fixture for " + id,
		"determinism": "high",
		"kind":        "setup",
		"type":        "computational",
		"regulation":  "behaviour",
		"phase":       "on-demand",
		"output":      "single",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50, "timeout_ms": 1000},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command": command,
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
				map[string]interface{}{"exit_code": "*", "verdict": "fail", "severity": "high"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"},
			},
		},
	}
	writeFixtureSensor(t, root, id, body)
}

// writeBlockingDepFixtureForStart writes a blocking sensor (its own
// minimal fixture, distinct from blockingFixtureBody — that one is the
// TARGET, this one is a blocking DEP).
func writeBlockingDepFixtureForStart(t *testing.T, root, id string) {
	t.Helper()
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
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// cleanupStartedTarget kills target subproc + watcher for test teardown.
func cleanupStartedTarget(t *testing.T, root, id string) {
	t.Helper()
	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	entry := rs.FindEntry(id)
	if entry == nil {
		return
	}
	if entry.PGID > 0 {
		_ = killGroup(entry.PGID)
	}
	if entry.WatcherPID > 0 {
		_ = killPID(entry.WatcherPID)
	}
}

// cleanupBlockingDep detaches/stops a blocking dep that was attached
// during the test, so the temp dir can be safely cleaned up. Resolves
// the live blocking entry by sensor id and addresses Detach by
// (id, run_id); silently noops if the entry is no longer present.
func cleanupBlockingDep(t *testing.T, root, depID, holderID string) {
	t.Helper()
	v, code := schema.LoadValidator(testfixtures.RepoSchemasDir(t), io.Discard)
	if code != 0 {
		return
	}
	r := registry.NewRoot(root)
	rs, err := registry.Load(r)
	if err != nil {
		return
	}
	entry := rs.FindBlockingEntry(depID)
	if entry == nil {
		return
	}
	orchestrator.DetachLiveDep(orchestrator.LiveDep{ID: depID, RunID: entry.RunID}, root, holderID, v, io.Discard, io.Discard)
}

func TestStart_WithSetupDepPASS(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingSetupDep(t, root, "setup-pass", "true")
	writeBlockingTarget(t, root, "target", []string{"setup-pass"}, nil)

	exit, sig := runStart(bootstrapFor(t, testResult(root), new(bytes.Buffer)), []string{"target"})
	defer cleanupStartedTarget(t, root, "target")

	if exit != 0 {
		t.Fatalf("exit: got %d, want 0; sig=%+v", exit, sig)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "started" {
		t.Errorf("metadata.kind: got %v, want started", md["kind"])
	}
	depChain, _ := md["dep_chain"].([]interface{})
	if len(depChain) != 0 {
		t.Errorf("dep_chain (blocking only): got %v, want []", depChain)
	}
	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	if rs.FindEntry("target") == nil {
		t.Errorf("target should be in registry after started")
	}
}

func TestStart_WithSetupDepFAIL(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingSetupDep(t, root, "setup-fail", "false")
	writeBlockingTarget(t, root, "target", []string{"setup-fail"}, nil)

	exit, sig := runStart(bootstrapFor(t, testResult(root), new(bytes.Buffer)), []string{"target"})

	if exit != 1 {
		t.Fatalf("exit: got %d, want 1; sig=%+v", exit, sig)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "failed" {
		t.Errorf("metadata.kind: got %v, want failed", md["kind"])
	}
	if md["cause"] != "dep_cascade" {
		t.Errorf("metadata.cause: got %v, want dep_cascade", md["cause"])
	}
	if md["failed_dep_id"] != "setup-fail" {
		t.Errorf("metadata.failed_dep_id: got %v, want setup-fail", md["failed_dep_id"])
	}

	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	if rs.FindEntry("target") != nil {
		t.Errorf("target should NOT be in registry on dep cascade")
	}
}

func TestStart_WithBlockingDepStartFresh(t *testing.T) {
	root := t.TempDir()
	writeBlockingDepFixtureForStart(t, root, "blocking-tick")
	writeBlockingTarget(t, root, "target", []string{"blocking-tick"}, nil)

	exit, sig := runStart(bootstrapFor(t, testResult(root), new(bytes.Buffer)), []string{"target"})
	defer cleanupStartedTarget(t, root, "target")
	defer cleanupBlockingDep(t, root, "blocking-tick", "target")

	if exit != 0 {
		t.Fatalf("exit: got %d, want 0; sig=%+v", exit, sig)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "started" {
		t.Fatalf("metadata.kind: got %v, want started", md["kind"])
	}
	depChain, _ := md["dep_chain"].([]interface{})
	if len(depChain) != 1 || depChain[0] != "blocking-tick" {
		t.Errorf("dep_chain: got %v, want [blocking-tick]", depChain)
	}

	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	depEntry := rs.FindEntry("blocking-tick")
	if depEntry == nil {
		t.Fatal("blocking-tick should be in registry")
	}
	if len(depEntry.HeldBy) != 1 {
		t.Fatalf("HeldBy length: got %d, want 1", len(depEntry.HeldBy))
	}
	targetEntry := rs.FindEntry("target")
	if targetEntry == nil {
		t.Fatal("target should be in registry")
	}
	if depEntry.HeldBy[0].PID != targetEntry.PID {
		t.Errorf("dep holder PID: got %d, want target subprocess PID %d (rebind failed)",
			depEntry.HeldBy[0].PID, targetEntry.PID)
	}
}

func TestStart_PrepareFAIL(t *testing.T) {
	root := t.TempDir()
	writeBlockingTarget(t, root, "target", nil, []map[string]string{
		{"command": "false"},
	})

	exit, sig := runStart(bootstrapFor(t, testResult(root), new(bytes.Buffer)), []string{"target"})

	if exit != 1 {
		t.Fatalf("exit: got %d, want 1; sig=%+v", exit, sig)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "failed" {
		t.Errorf("metadata.kind: got %v, want failed", md["kind"])
	}
	if md["cause"] != "prepare_failed" {
		t.Errorf("metadata.cause: got %v, want prepare_failed", md["cause"])
	}
	lc, _ := md["lifecycle"].(map[string]interface{})
	if lc == nil {
		t.Fatal("metadata.lifecycle missing")
	}
	prep, _ := lc["prepare"].([]interface{})
	if len(prep) != 1 {
		t.Fatalf("lifecycle.prepare: got %d entries, want 1", len(prep))
	}
	step := prep[0].(map[string]interface{})
	if step["verdict"] != "fail" {
		t.Errorf("prepare[0].verdict: got %v, want fail", step["verdict"])
	}

	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	if rs.FindEntry("target") != nil {
		t.Error("target should NOT be in registry after prepare fail")
	}
}

func TestStart_WithBlockingDepAttach(t *testing.T) {
	root := t.TempDir()
	writeBlockingDepFixtureForStart(t, root, "blocking-tick")
	writeBlockingTarget(t, root, "target", []string{"blocking-tick"}, nil)

	// Pre-populate registry with an alive blocking-tick entry so the
	// orchestrator attaches (dep_attached) instead of starting fresh
	// (dep_started). Hold pid is os.Getpid() — current process is alive.
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	preExistingHolder := registry.HeldByEntry{
		Kind: "sensor", ID: "pre-existing-holder", PID: os.Getpid(), AttachedAt: "2026-05-10T00:00:00Z",
	}
	preExistingRunID := "preexisting-runid"
	if err := registry.Save(r, registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{{
			SensorID:  "blocking-tick",
			RunID:     preExistingRunID,
			Blocking:  true,
			PID:       os.Getpid(),
			PGID:      os.Getpid(),
			StartedAt: "2026-05-10T00:00:00Z",
			Command:   "while true; do echo TICK; sleep 0.1; done",
			LogDir:    filepath.Join(".harness", "runtime", "blocking-tick"),
			HeldBy:    []registry.HeldByEntry{preExistingHolder},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	preExistingDepPID := os.Getpid()

	exit, sig := runStart(bootstrapFor(t, testResult(root), new(bytes.Buffer)), []string{"target"})
	defer cleanupStartedTarget(t, root, "target")
	defer cleanupBlockingDep(t, root, "blocking-tick", "target")
	defer cleanupBlockingDep(t, root, "blocking-tick", "pre-existing-holder")

	if exit != 0 {
		t.Fatalf("exit: got %d, want 0; sig=%+v", exit, sig)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "started" {
		t.Fatalf("metadata.kind: got %v, want started", md["kind"])
	}

	rs, _ := registry.Load(r)
	depEntry := rs.FindEntry("blocking-tick")
	if depEntry == nil {
		t.Fatal("blocking-tick should remain in registry")
	}
	// Subprocess pid unchanged — we attached, not relaunched.
	if depEntry.PID != preExistingDepPID {
		t.Errorf("dep subprocess pid changed: got %d, want %d (should not have relaunched)",
			depEntry.PID, preExistingDepPID)
	}
	// Two holders: the pre-existing one plus our target's holder.
	if len(depEntry.HeldBy) != 2 {
		t.Fatalf("HeldBy length: got %d, want 2 (pre-existing + new target holder)", len(depEntry.HeldBy))
	}
	// One holder is from "target" pointing to target's subproc pid (post-rebind).
	targetEntry := rs.FindEntry("target")
	if targetEntry == nil {
		t.Fatal("target should be in registry")
	}
	foundTargetHolder := false
	foundPreExistingHolder := false
	for _, h := range depEntry.HeldBy {
		if h.Kind == "sensor" && h.ID == "target" && h.PID == targetEntry.PID {
			foundTargetHolder = true
		}
		if h.Kind == "sensor" && h.ID == "pre-existing-holder" {
			foundPreExistingHolder = true
		}
	}
	if !foundTargetHolder {
		t.Errorf("expected target holder with pid=%d in HeldBy=%+v", targetEntry.PID, depEntry.HeldBy)
	}
	if !foundPreExistingHolder {
		t.Errorf("pre-existing holder should be preserved in HeldBy=%+v", depEntry.HeldBy)
	}
}

func TestStart_PrepareFAIL_DetachesLiveStack(t *testing.T) {
	root := t.TempDir()
	writeBlockingDepFixtureForStart(t, root, "blocking-tick")
	writeBlockingTarget(t, root, "target", []string{"blocking-tick"}, []map[string]string{
		{"command": "false"},
	})

	exit, sig := runStart(bootstrapFor(t, testResult(root), new(bytes.Buffer)), []string{"target"})

	if exit != 1 {
		t.Fatalf("exit: got %d, want 1; sig=%+v", exit, sig)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["cause"] != "prepare_failed" {
		t.Fatalf("metadata.cause: got %v, want prepare_failed", md["cause"])
	}

	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	if rs.FindEntry("blocking-tick") != nil {
		t.Error("blocking-tick should be torn down after detachAll on prepare fail")
	}
	if rs.FindEntry("target") != nil {
		t.Error("target should NOT be in registry after prepare fail")
	}
}

// TestStart_WritesNewRunIDLayout asserts that /start-sensor places
// raw.log / signals.log under <SensorDir>/<runID>/ where runID matches
// the composite <pid>-<short-uuid8> shape. Also verifies the started
// Signal's metadata.run_id, the registry entry's RunID, Blocking:true,
// and LogDir all carry the same composite value.
func TestStart_WritesNewRunIDLayout(t *testing.T) {
	root := t.TempDir()
	writeFixtureSensor(t, root, "longrun", blockingFixtureBody())

	exit, sig := runStart(bootstrapFor(t, testResult(root), new(bytes.Buffer)), []string{"longrun"})
	defer cleanupStartedTarget(t, root, "longrun")

	if exit != 0 {
		t.Fatalf("exit=%d sig=%+v", exit, sig)
	}
	md, _ := sig["metadata"].(map[string]interface{})
	if md == nil {
		t.Fatalf("missing metadata: %+v", sig)
	}
	if md["kind"] != "started" {
		t.Fatalf("metadata.kind: got %v, want started", md["kind"])
	}
	runID, _ := sig["run_id"].(string)
	composite := regexp.MustCompile(`^\d+-[0-9a-f]{8}$`)
	if !composite.MatchString(runID) {
		t.Fatalf("run_id %q does not match <pid>-<short-uuid8> shape", runID)
	}

	r := registry.NewRoot(root)
	rawPath := r.RawLogRun("longrun", runID)
	if _, err := os.Stat(rawPath); err != nil {
		t.Fatalf("raw.log not at new path %s: %v", rawPath, err)
	}
	sigsPath := r.SignalsLogRun("longrun", runID)
	if _, err := os.Stat(sigsPath); err != nil {
		t.Fatalf("signals.log not at new path %s: %v", sigsPath, err)
	}

	rs, _ := registry.Load(r)
	entry := rs.FindEntry("longrun")
	if entry == nil {
		t.Fatal("longrun should be in registry after started")
	}
	if entry.RunID != runID {
		t.Errorf("entry.RunID: got %q, want %q", entry.RunID, runID)
	}
	if !entry.Blocking {
		t.Errorf("entry.Blocking: got false, want true")
	}
	wantLogDir := filepath.Join(".harness", "runtime", "longrun", runID)
	if entry.LogDir != wantLogDir {
		t.Errorf("entry.LogDir: got %q, want %q", entry.LogDir, wantLogDir)
	}
}

func TestStart_AllowsStartWhenOnlyNonBlockingEntryExists(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".harness", "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureSensor(t, proj, "shared", blockingFixtureBody())

	// Pre-seed a NON-blocking entry of the same sensor; it must NOT block /start-sensor.
	r := registry.NewRoot(proj)
	_ = os.MkdirAll(r.SensorsDir(), 0o755)
	rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{{
		SensorID: "shared", RunID: "999-runX", Blocking: false,
		PID: os.Getpid(), PGID: os.Getpid(), StartedAt: "2026-05-11T00:00:00Z",
	}}}
	_ = registry.Save(r, rs)

	exit, sig := runStart(bootstrapFor(t, testResult(proj), new(bytes.Buffer)), []string{"shared"})
	if exit != 0 {
		t.Fatalf("exit=%d, sig=%+v", exit, sig)
	}
	md, _ := sig["metadata"].(map[string]interface{})
	if md["kind"] != "started" {
		t.Errorf("expected started, got %v", md["kind"])
	}

	// cleanup
	after, _ := registry.Load(r)
	for _, e := range after.Entries {
		if e.Blocking {
			_ = syscall.Kill(-e.PGID, syscall.SIGKILL)
		}
	}
}
