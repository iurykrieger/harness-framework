//go:build tail_sensor

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
// passing to runTail in tests. The schema validator is loaded so that signal
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

func setupRunning(t *testing.T, root, id string, signalsLines []string) {
	t.Helper()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorDir(id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(r, registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: id, PID: registry.SelfPID(), PGID: registry.SelfPID(), HeldBy: []registry.HeldByEntry{{Kind: "manual"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.SignalsLog(id), []byte(strings.Join(signalsLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = filepath.Join // keep import
}

// runTailBuf is a test helper that calls runTail with a fresh
// cli.BootstrapResult derived from res. It returns the combined buffer
// (streamed lines + final signal emitted inside runTail) plus the exit code.
func runTailBuf(t *testing.T, res registry.Result, args []string) (int, *bytes.Buffer) {
	t.Helper()
	var buf, errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	exit := runTail(b, args, &buf, &errBuf)
	return exit, &buf
}

func TestTail_RegistryFileAbsent_Error(t *testing.T) {
	root := t.TempDir()
	res := resultFor(t, root, false)
	exit, buf := runTailBuf(t, res, []string{"missing", "0"})
	if exit != 1 {
		t.Fatalf("exit: got %d, want 1", exit)
	}
	var sig map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig); err != nil {
		t.Fatal(err)
	}
	if sig["verdict"] != "error" {
		t.Errorf("verdict: got %v, want \"error\"", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "tail_no_registry" {
		t.Errorf("metadata.kind: got %v", md["kind"])
	}
	if md["registry_exists"] != false {
		t.Errorf("metadata.registry_exists: got %v, want false", md["registry_exists"])
	}
}

func TestTail_Cursor0_ReturnsAll(t *testing.T) {
	root := t.TempDir()
	setupRunning(t, root, "loop", []string{
		`{"sensor_id":"loop","verdict":"pass","metadata":{"kind":"individual"}}`,
		`{"sensor_id":"loop","verdict":"warn","metadata":{"kind":"individual"}}`,
	})
	res := resultFor(t, root, true)
	exit, buf := runTailBuf(t, res, []string{"loop", "0"})
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: got %d (incl envelope)", len(lines))
	}
	var envelope map[string]interface{}
	if err := json.Unmarshal([]byte(lines[2]), &envelope); err != nil {
		t.Fatal(err)
	}
	md := envelope["metadata"].(map[string]interface{})
	if md["kind"] != "envelope" {
		t.Fatalf("envelope kind: got %v", md["kind"])
	}
	if md["next_cursor"].(float64) != 2 {
		t.Fatalf("next_cursor: got %v", md["next_cursor"])
	}
	if md["registry_exists"] != true {
		t.Errorf("metadata.registry_exists: got %v, want true", md["registry_exists"])
	}
}

func TestTail_CursorMid_ReturnsSuffix(t *testing.T) {
	root := t.TempDir()
	setupRunning(t, root, "loop", []string{
		`{"sensor_id":"loop","verdict":"pass","metadata":{"kind":"individual"}}`,
		`{"sensor_id":"loop","verdict":"warn","metadata":{"kind":"individual"}}`,
		`{"sensor_id":"loop","verdict":"fail","metadata":{"kind":"individual"}}`,
	})
	res := resultFor(t, root, true)
	exit, buf := runTailBuf(t, res, []string{"loop", "2"})
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines: got %d", len(lines))
	}
}

func TestTail_NotRunning(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := registry.Save(r, registry.RunningSensors{Version: 1}); err != nil {
		t.Fatal(err)
	}
	res := resultFor(t, root, true)
	exit, buf := runTailBuf(t, res, []string{"missing", "0"})
	if exit != 1 {
		t.Fatalf("exit: got %d", exit)
	}
	var sig map[string]interface{}
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig)
	if sig["metadata"].(map[string]interface{})["kind"] != "not_running" {
		t.Fatalf("kind: got %v", sig["metadata"])
	}
}

func TestTail_AmbiguousRunReturnsError(t *testing.T) {
	proj := t.TempDir()
	r := registry.NewRoot(proj)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "alpha", RunID: "1-aa", Blocking: false, PID: os.Getpid(), PGID: os.Getpid(), StartedAt: "2026-05-11T00:00:00Z"},
			{SensorID: "alpha", RunID: "2-bb", Blocking: false, PID: os.Getpid(), PGID: os.Getpid(), StartedAt: "2026-05-11T00:00:00Z"},
		},
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	res := resultFor(t, proj, true)
	exit, buf := runTailBuf(t, res, []string{"alpha", "0"})
	if exit == 0 {
		t.Fatal("expected non-zero exit on ambiguous_run")
	}
	var sig map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig); err != nil {
		t.Fatal(err)
	}
	md, _ := sig["metadata"].(map[string]interface{})
	if md["kind"] != "ambiguous_run" {
		t.Errorf("kind=%v, want ambiguous_run", md["kind"])
	}
}

func TestTail_PathLikeResolvesToSpecificRun(t *testing.T) {
	proj := t.TempDir()
	r := registry.NewRoot(proj)
	runID := "1-aa"

	// Create the run directory and signals.log under proj.
	runDir := r.RunDir("alpha", runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sigsPath := r.SignalsLogRun("alpha", runID)
	signalLine := `{"sensor_id":"alpha","run_id":"1-aa","verdict":"pass","severity":"info","confidence":1.0,"version":"0.0.0","started_at":"2026-05-11T00:00:00Z","finished_at":"2026-05-11T00:00:01Z","evidence":[],"cost_actual":{"latency_ms":1},"metadata":{"kind":"aggregate"}}` + "\n"
	if err := os.WriteFile(sigsPath, []byte(signalLine), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "alpha", RunID: runID, Blocking: false, PID: os.Getpid(), PGID: os.Getpid(), StartedAt: "2026-05-11T00:00:00Z", LogDir: r.RunDir("alpha", runID)},
		},
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	res := resultFor(t, proj, true)
	exit, _ := runTailBuf(t, res, []string{"alpha/" + runID, "0"})
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
}

func TestTail_LegacyRunIDFallback(t *testing.T) {
	proj := t.TempDir()
	r := registry.NewRoot(proj)

	// Create legacy flat signals.log under the sensor dir (not under a run subdir).
	if err := os.MkdirAll(r.SensorDir("beta"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacyPath := r.LegacySignalsLog("beta")
	signalLine := `{"sensor_id":"beta","run_id":"x-legacy","verdict":"pass","severity":"info","confidence":1.0,"version":"0.0.0","started_at":"2026-05-11T00:00:00Z","finished_at":"2026-05-11T00:00:01Z","evidence":[],"cost_actual":{"latency_ms":1},"metadata":{"kind":"aggregate"}}` + "\n"
	if err := os.WriteFile(legacyPath, []byte(signalLine), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "beta", RunID: "x-legacy", Blocking: false, PID: os.Getpid(), PGID: os.Getpid(), StartedAt: "2026-05-11T00:00:00Z"},
		},
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	res := resultFor(t, proj, true)
	exit, buf := runTailBuf(t, res, []string{"beta", "0"})
	if exit != 0 {
		t.Fatalf("exit=%d, buf=%s", exit, buf.String())
	}
}
