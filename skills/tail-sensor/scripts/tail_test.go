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

func TestTail_RegistryFileAbsent_Error(t *testing.T) {
	root := t.TempDir()
	res := resultFor(t, root, false)
	var buf, errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	exit := runTail(b, []string{"missing", "0"}, &buf, &errBuf)
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
	var buf, errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	exit := runTail(b, []string{"loop", "0"}, &buf, &errBuf)
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
	var buf, errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	exit := runTail(b, []string{"loop", "2"}, &buf, &errBuf)
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
	var buf, errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	exit := runTail(b, []string{"missing", "0"}, &buf, &errBuf)
	if exit != 1 {
		t.Fatalf("exit: got %d", exit)
	}
	var sig map[string]interface{}
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig)
	if sig["metadata"].(map[string]interface{})["kind"] != "not_running" {
		t.Fatalf("kind: got %v", sig["metadata"])
	}
}
