//go:build list_sensors

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/cli"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
)

// resultFor builds a registry.Result for a tempdir-backed project.
// exists controls whether running_sensors.json is on disk.
func resultFor(t *testing.T, projectRoot string, exists bool) registry.Result {
	t.Helper()
	r := registry.NewRoot(projectRoot)
	state, _, err := registry.LoadOrEmpty(r)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return registry.Result{
		Root:        r,
		ProjectRoot: projectRoot,
		Source:      registry.SourceWalkUp,
		Exists:      exists,
		State:       state,
	}
}

// bootstrapFor wraps a registry.Result in a cli.BootstrapResult suitable for
// passing to runList in tests. The schema validator is loaded so that signal
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

func TestList_FileAbsent_Warn(t *testing.T) {
	root := t.TempDir()
	res := resultFor(t, root, false)
	var buf, errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	exit := runList(b, &buf, &errBuf)
	if exit != 0 {
		t.Fatalf("exit: got %d, want 0", exit)
	}
	var sig map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig); err != nil {
		t.Fatal(err)
	}
	if sig["verdict"] != "warn" {
		t.Errorf("verdict: got %v, want \"warn\"", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "list" {
		t.Errorf("metadata.kind: got %v", md["kind"])
	}
	if md["registry_exists"] != false {
		t.Errorf("metadata.registry_exists: got %v", md["registry_exists"])
	}
	if md["registry_source"] != "walk_up" {
		t.Errorf("metadata.registry_source: got %v", md["registry_source"])
	}
	wantPath := filepath.Join(root, ".runtime", "sensors", "running_sensors.json")
	if md["registry_path"] != wantPath {
		t.Errorf("metadata.registry_path: got %v, want %v", md["registry_path"], wantPath)
	}
	entries, _ := md["entries"].([]interface{})
	if len(entries) != 0 {
		t.Errorf("entries: got %d, want 0", len(entries))
	}
	ev, _ := sig["evidence"].([]interface{})
	rationale := ev[0].(map[string]interface{})["rationale"].(string)
	if !strings.Contains(rationale, "registry not found") || !strings.Contains(rationale, "HARNESS_REGISTRY_ROOT") {
		t.Errorf("rationale should mention registry not found and HARNESS_REGISTRY_ROOT, got: %q", rationale)
	}
}

func TestList_FilePresentEmpty_Pass(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := registry.Save(r, registry.RunningSensors{Version: 1}); err != nil {
		t.Fatal(err)
	}
	res := resultFor(t, root, true)
	var buf, errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	exit := runList(b, &buf, &errBuf)
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}
	var sig map[string]interface{}
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig)
	if sig["verdict"] != "pass" {
		t.Errorf("verdict: got %v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["registry_exists"] != true {
		t.Errorf("metadata.registry_exists: got %v", md["registry_exists"])
	}
}

func TestList_AnnotatesOrphan(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(r, registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "alive", PID: registry.SelfPID(), PGID: registry.SelfPID()},
			{SensorID: "dead", PID: 3_999_999, PGID: 3_999_999},
		},
	}); err != nil {
		t.Fatal(err)
	}
	res := resultFor(t, root, true)
	var buf, errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	_ = runList(b, &buf, &errBuf)
	var sig map[string]interface{}
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig)
	entries := sig["metadata"].(map[string]interface{})["entries"].([]interface{})
	var alive, dead map[string]interface{}
	for _, e := range entries {
		em := e.(map[string]interface{})
		switch em["sensor_id"] {
		case "alive":
			alive = em
		case "dead":
			dead = em
		}
	}
	if alive["pid_alive"].(bool) != true {
		t.Errorf("alive pid_alive: got %v", alive["pid_alive"])
	}
	if dead["pid_alive"].(bool) != false {
		t.Errorf("dead pid_alive: got %v", dead["pid_alive"])
	}
	if dead["state"] != "orphan" {
		t.Errorf("dead state: got %v", dead["state"])
	}
}

// TestList_MultipleEntriesPerSensor verifies that when the registry carries
// multiple active runs of the same sensor (different run_ids), runList emits
// one entry per run, includes run_id and blocking on each, and conditionally
// adds watcher_pid / watcher_alive only for blocking entries.
func TestList_MultipleEntriesPerSensor(t *testing.T) {
	proj := t.TempDir()
	r := registry.NewRoot(proj)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
		{SensorID: "alpha", RunID: "1-aa", Blocking: false, PID: registry.SelfPID(), PGID: registry.SelfPID(), StartedAt: "2026-05-11T00:00:00Z"},
		{SensorID: "alpha", RunID: "2-bb", Blocking: true, PID: registry.SelfPID(), PGID: registry.SelfPID(), WatcherPID: 9999, StartedAt: "2026-05-11T00:00:00Z"},
	}}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	res := resultFor(t, proj, true)
	var buf, errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	exit := runList(b, &buf, &errBuf)
	if exit != 0 {
		t.Fatalf("exit: got %d, want 0", exit)
	}
	var sig map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig); err != nil {
		t.Fatal(err)
	}
	md, _ := sig["metadata"].(map[string]interface{})
	entries, _ := md["entries"].([]interface{})
	if len(entries) != 2 {
		t.Fatalf("entries=%d, want 2", len(entries))
	}
	for _, raw := range entries {
		e, _ := raw.(map[string]interface{})
		if _, ok := e["run_id"].(string); !ok {
			t.Errorf("entry missing run_id: %+v", e)
		}
		if _, ok := e["blocking"].(bool); !ok {
			t.Errorf("entry missing blocking: %+v", e)
		}
	}
	// Non-blocking entry must NOT have watcher_pid / watcher_alive.
	nonBlocking := entries[0].(map[string]interface{})
	if nonBlocking["blocking"].(bool) {
		nonBlocking = entries[1].(map[string]interface{})
	}
	if _, ok := nonBlocking["watcher_pid"]; ok {
		t.Errorf("non-blocking entry should not carry watcher_pid: %+v", nonBlocking)
	}
	if _, ok := nonBlocking["watcher_alive"]; ok {
		t.Errorf("non-blocking entry should not carry watcher_alive: %+v", nonBlocking)
	}
	// Blocking entry MUST have watcher_pid / watcher_alive.
	blocking := entries[0].(map[string]interface{})
	if !blocking["blocking"].(bool) {
		blocking = entries[1].(map[string]interface{})
	}
	if _, ok := blocking["watcher_pid"]; !ok {
		t.Errorf("blocking entry missing watcher_pid: %+v", blocking)
	}
	if _, ok := blocking["watcher_alive"]; !ok {
		t.Errorf("blocking entry missing watcher_alive: %+v", blocking)
	}
}

// TestList_RegistryMigratedSignal_ViaBootstrap verifies that when the registry
// file contains legacy entries (e.g. missing watcher_pid), cli.Bootstrap emits
// a registry_migrated warn Signal before runList emits the main list Signal.
// The migration behavior is owned by cli.Bootstrap; this test exercises the
// full path (Bootstrap + runList) to confirm both signals appear in order.
func TestList_RegistryMigratedSignal_ViaBootstrap(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{
  "version": 1,
  "entries": [
    {
      "sensor_id": "x", "pid": 1234, "pgid": 1234,
      "watcher_pid": -1, "started_at": "t", "command": "c",
      "log_dir": ".runtime/sensors/x",
      "held_by": [{"kind": "manual", "attached_at": "t"}]
    }
  ]
}`)
	if err := os.WriteFile(r.RegistryFile(), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	// Ensure marker dir exists for walk-up discovery.
	if err := os.MkdirAll(filepath.Join(dir, "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", dir)

	// Run via Bootstrap so the migrated-signal path is exercised.
	var buf, errBuf bytes.Buffer
	b := cli.Bootstrap("list-sensors", &buf, &errBuf)
	if b.ExitCode != 0 {
		t.Fatalf("Bootstrap exit: %d; stderr: %s", b.ExitCode, errBuf.String())
	}
	exit := runList(b, &buf, &errBuf)
	if exit != 0 {
		t.Fatalf("runList exit: got %d, want 0", exit)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d JSONL line(s); want 2 (warn + list). Output:\n%s", len(lines), buf.String())
	}

	var warn map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &warn); err != nil {
		t.Fatal(err)
	}
	if warn["verdict"] != "warn" {
		t.Errorf("line 0 verdict: %v", warn["verdict"])
	}
	md, _ := warn["metadata"].(map[string]interface{})
	if md["kind"] != "registry_migrated" {
		t.Errorf("line 0 metadata.kind: %v", md["kind"])
	}

	var main map[string]interface{}
	if err := json.Unmarshal([]byte(lines[1]), &main); err != nil {
		t.Fatal(err)
	}
	if main["verdict"] != "pass" {
		t.Errorf("line 1 verdict: %v", main["verdict"])
	}
	mainMD, _ := main["metadata"].(map[string]interface{})
	entries, _ := mainMD["entries"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("entries: got %d, want 1", len(entries))
	}
	e0, _ := entries[0].(map[string]interface{})
	if pid, _ := e0["watcher_pid"].(float64); pid != 0 {
		t.Errorf("entry watcher_pid: got %v, want 0", e0["watcher_pid"])
	}
}
