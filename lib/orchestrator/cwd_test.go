package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

func TestRunWithDepsRoot_SubprocessCwdIsProjectRoot(t *testing.T) {
	proj := t.TempDir()
	schemasDir := schematest.RepoSchemasDir(t)
	_ = os.MkdirAll(filepath.Join(proj, ".harness", "sensors"), 0o755)

	// Build a valid sensor that writes its cwd to a file under projectRoot.
	s := sensortest.LoadComputational(t).AsMap()
	s["id"] = "cwd-probe"
	exec := s["execution"].(map[string]interface{})
	exec["command"] = "pwd > $HARNESS_REGISTRY_ROOT/probe.out"
	b, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(filepath.Join(proj, ".harness", "sensors", "cwd-probe.yaml"), b, 0o644)

	t.Setenv("HARNESS_REGISTRY_ROOT", proj)

	// Run from an unrelated cwd to prove the runner's cwd doesn't leak.
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	var stdout, stderr bytes.Buffer
	exit := orchestrator.RunWithDepsRoot(context.Background(), "cwd-probe", proj, schemasDir, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}

	got, _ := os.ReadFile(filepath.Join(proj, "probe.out"))
	want, _ := filepath.EvalSymlinks(proj)
	gotResolved, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got)))
	if gotResolved != want {
		t.Errorf("subprocess cwd = %q, want %q", gotResolved, want)
	}
}
