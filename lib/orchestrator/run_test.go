package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

// writeSensorWithDeps writes a sensor YAML file to dir/.harness/sensors/<id>.yaml,
// matching the <projectRoot>/.harness/sensors/<id>.yaml layout that RunDeps expects.
func writeSensorWithDeps(t *testing.T, projectRoot, id string, depsOn []string, command string) {
	t.Helper()
	sensorsDir := filepath.Join(projectRoot, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := sensortest.LoadComputational(t).AsMap()
	s["id"] = id
	if len(depsOn) > 0 {
		reqs := []interface{}{}
		for _, d := range depsOn {
			reqs = append(reqs, map[string]interface{}{"kind": "sensor", "id": d})
		}
		s["requires"] = reqs
	}
	exec := s["execution"].(map[string]interface{})
	exec["command"] = command
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(filepath.Join(sensorsDir, id+".yaml"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunWithDeps_ChainPasses(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeSensorWithDeps(t, root, "setup-a", nil, "true")
	writeSensorWithDeps(t, root, "use-a", []string{"setup-a"}, "true")

	var out, errBuf bytes.Buffer
	code := RunWithDeps(context.Background(), filepath.Join(root, ".harness", "sensors", "use-a.yaml"), schemasDir, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}

	lines := splitJSONL(out.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 aggregate Signals, got %d:\n%s", len(lines), out.String())
	}
	last := decode(t, lines[len(lines)-1])
	if last["sensor_id"] != "use-a" {
		t.Errorf("last sensor_id = %v, want use-a", last["sensor_id"])
	}
}

func TestRunWithDeps_CascadesOnDepFail(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeSensorWithDeps(t, root, "setup-fail", nil, "false")
	writeSensorWithDeps(t, root, "use-it", []string{"setup-fail"}, "true")

	var out, errBuf bytes.Buffer
	code := RunWithDeps(context.Background(), filepath.Join(root, ".harness", "sensors", "use-it.yaml"), schemasDir, &out, &errBuf)
	// Cascade on a dep failure now returns exit 1 (dep ran but root was skipped).
	if code != 1 {
		t.Fatalf("exit=%d stderr=%s; want 1 (cascade)", code, errBuf.String())
	}

	lines := splitJSONL(out.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 Signals (dep + cascade), got %d", len(lines))
	}
	depSig := decode(t, lines[0])
	cascade := decode(t, lines[1])
	if depSig["verdict"] != "fail" {
		t.Errorf("dep verdict = %v, want fail", depSig["verdict"])
	}
	if cascade["verdict"] != "error" {
		t.Errorf("cascade verdict = %v, want error", cascade["verdict"])
	}
	md := cascade["metadata"].(map[string]interface{})
	if md["kind"] != "cascade" {
		t.Errorf("cascade metadata.kind = %v", md["kind"])
	}
}

func TestRunWithDeps_CycleAborts(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeSensorWithDeps(t, root, "a", []string{"b"}, "true")
	writeSensorWithDeps(t, root, "b", []string{"a"}, "true")

	var out, errBuf bytes.Buffer
	code := RunWithDeps(context.Background(), filepath.Join(root, ".harness", "sensors", "a.yaml"), schemasDir, &out, &errBuf)
	if code != 1 {
		t.Fatalf("expected exit=1 for cycle, got %d", code)
	}
	if !strings.Contains(errBuf.String(), "cycle") {
		t.Errorf("expected stderr to mention cycle, got %q", errBuf.String())
	}
}

func splitJSONL(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func decode(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("invalid JSON %q: %v", s, err)
	}
	return m
}

func TestRunWithDepsRoot_CascadeSkip_DoesNotTouchRegistryOrDir(t *testing.T) {
	proj := t.TempDir()
	sensorsDir := filepath.Join(proj, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Dep fails (exit 1).
	_ = os.WriteFile(filepath.Join(sensorsDir, "dep.yaml"), []byte(`{
      "id": "dep", "version": "0.0.0", "kind": "observation",
      "type": "computational", "output": "single",
      "cost": {"compute": "low"},
      "execution": {"command": "exit 1", "exit_code_map": [{"exit_code": 1, "verdict": "fail", "severity": "high"}]}
    }`), 0o644)
	_ = os.WriteFile(filepath.Join(sensorsDir, "target.yaml"), []byte(`{
      "id": "target", "version": "0.0.0", "kind": "observation",
      "type": "computational", "output": "single",
      "cost": {"compute": "low"},
      "requires": [{"kind": "sensor", "id": "dep"}],
      "execution": {"command": "echo never-runs", "exit_code_map": [{"exit_code": 0, "verdict": "pass", "severity": "info"}]}
    }`), 0o644)

	var stdout, stderr bytes.Buffer
	code := RunWithDepsRoot(context.Background(), "target", proj, "", &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit for cascade")
	}

	targetDir := filepath.Join(proj, ".harness", "runtime", "target")
	if entries, _ := os.ReadDir(targetDir); len(entries) != 0 {
		t.Errorf("target run dir was created during cascade: %+v", entries)
	}

	rs, _ := registry.Load(registry.NewRoot(proj))
	for _, e := range rs.Entries {
		if e.SensorID == "target" {
			t.Errorf("target entry exists despite cascade: %+v", e)
		}
	}
}
