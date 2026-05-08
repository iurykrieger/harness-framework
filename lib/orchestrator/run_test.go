package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func writeSensorWithDeps(t *testing.T, dir, id string, depsOn []string, command string) {
	t.Helper()
	s := testfixtures.ValidSensorComputational()
	s["id"] = id
	if len(depsOn) > 0 {
		ds := []interface{}{}
		for _, d := range depsOn {
			ds = append(ds, d)
		}
		s["depends_on"] = ds
	}
	exec := s["execution"].(map[string]interface{})
	exec["command"] = command
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, id+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunWithDeps_ChainPasses(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	root := t.TempDir()
	writeSensorWithDeps(t, root, "setup-a", nil, "true")
	writeSensorWithDeps(t, root, "use-a", []string{"setup-a"}, "true")

	var out, errBuf bytes.Buffer
	code := RunWithDeps(context.Background(), filepath.Join(root, "use-a.json"), schemasDir, &out, &errBuf)
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
	schemasDir := testfixtures.RepoSchemasDir(t)
	root := t.TempDir()
	writeSensorWithDeps(t, root, "setup-fail", nil, "false")
	writeSensorWithDeps(t, root, "use-it", []string{"setup-fail"}, "true")

	var out, errBuf bytes.Buffer
	code := RunWithDeps(context.Background(), filepath.Join(root, "use-it.json"), schemasDir, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
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
	schemasDir := testfixtures.RepoSchemasDir(t)
	root := t.TempDir()
	writeSensorWithDeps(t, root, "a", []string{"b"}, "true")
	writeSensorWithDeps(t, root, "b", []string{"a"}, "true")

	var out, errBuf bytes.Buffer
	code := RunWithDeps(context.Background(), filepath.Join(root, "a.json"), schemasDir, &out, &errBuf)
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
