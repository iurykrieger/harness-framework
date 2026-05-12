// test/heal-e2e/heal_e2e_test.go
package healE2E_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
	"github.com/iurykrieger/harness-framework/lib/heal/rules"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

// TestHealE2E_MissingEnvFile_HealAndRetry simulates the full loop:
// 1) run-sensor fails because .env is missing
// 2) classifier confirms setup-shape (env-file-absent rule fires via
//    the curated stderr regex)
// 3) heal applies copy-template
// 4) retry passes
func TestHealE2E_MissingEnvFile_HealAndRetry(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	root := setupFixtureProject(t)
	sensorID := "run-needs-env"
	sensorPath := filepath.Join(root, ".harness", "sensors", sensorID+".json")
	bin := buildRunSensor(t, root)

	// 1) First run: must fail with .env missing.
	out1, _ := runSensor(t, root, bin, sensorID)
	agg1 := lastJSONL(t, out1)
	if agg1["verdict"] == "pass" {
		t.Fatalf("expected first run to fail; got %v", agg1)
	}

	// 2) Classify the aggregate — must be setup-shape.
	sig := signalFromMap(agg1)
	failed := mustLoadFailedView(t, sensorPath)
	res, ok := heal.ClassifyWith(rules.Registered(), sig, failed)
	if !ok {
		t.Fatalf("expected setup-shape classification; aggregate=%v", agg1)
	}
	if res.Shape != heal.ShapeEnvFileAbsent && res.Shape != heal.ShapeMissingEnv {
		t.Fatalf("unexpected shape %q (rule=%q)", res.Shape, res.Rule)
	}

	// 3) Apply copy-template via lib/heal directly.
	src := filepath.Join(root, ".env.example")
	dst := filepath.Join(root, ".env")
	results := heal.Apply(heal.ApplyContext{Root: root, FailedSensor: failed}, []heal.Action{
		{Kind: "copy-template", Src: src, Dst: dst},
	})
	if !results[0].Applied {
		t.Fatalf("copy-template not applied: %v", results[0].Reason)
	}

	// 4) Retry — must pass.
	out2, _ := runSensor(t, root, bin, sensorID)
	agg2 := lastJSONL(t, out2)
	if agg2["verdict"] != "pass" {
		t.Fatalf("expected retry to pass; got %v", agg2)
	}
}

// setupFixtureProject creates a project under the repo root so the
// runner's schema discovery (which walks up from cwd) can find
// schemas/ via the parent directory. Tempdirs under /tmp would
// require copying or symlinking schemas/ in.
func setupFixtureProject(t *testing.T) string {
	t.Helper()
	repo := repoRoot(t)
	scratch := filepath.Join(repo, ".test-tmp")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(scratch, "heal-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })

	// Minimal .env.example with the var the sensor's command reads.
	os.WriteFile(filepath.Join(root, ".env.example"), []byte("RSA_PRIVATE_KEY=stub-key\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".env\n"), 0o644)

	// Sensor that fails when .env is missing.
	os.MkdirAll(filepath.Join(root, ".harness", "sensors"), 0o755)
	s := testfixtures.ValidSensorComputational()
	s["id"] = "run-needs-env"
	s["execution"] = map[string]interface{}{
		"command": "test -f " + filepath.Join(root, ".env") + " || (echo 'open .env: ENOENT no such file' >&2; exit 1)",
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			map[string]interface{}{"exit_code": "*", "verdict": "fail", "severity": "high"},
		},
	}
	body, _ := json.Marshal(s)
	os.WriteFile(filepath.Join(root, ".harness", "sensors", "run-needs-env.json"), body, 0o644)
	return root
}

// buildRunSensor compiles the run-computational binary into the
// fixture project so we can exec it with cmd.Dir = root (where the
// runner's os.Getwd() must equal the projectRoot for ResolveByID to
// find .harness/sensors/<id>.json). Returns the absolute path to the binary.
func buildRunSensor(t *testing.T, root string) string {
	t.Helper()
	bin := filepath.Join(root, "run-sensor")
	cmd := exec.Command("go", "build", "-tags=run_computational", "-o", bin, "./skills/run-sensor/scripts")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build run-sensor: %v\n%s", err, out)
	}
	return bin
}

func runSensor(t *testing.T, root, bin, sensorID string) (string, string) {
	t.Helper()
	cmd := exec.Command(bin, sensorID)
	cmd.Dir = root // projectRoot for ResolveByID; schema discovery walks up to repo root.
	cmd.Env = append(os.Environ(), "HARNESS_FIXTURE_ROOT="+root)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run() // exit code carried in the aggregate
	return stdout.String(), stderr.String()
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	dir := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("repo root not found from %s", wd)
	return ""
}

func lastJSONL(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(lines[i]), &m); err != nil {
			continue
		}
		return m
	}
	t.Fatalf("no JSONL line in output: %q", s)
	return nil
}

func signalFromMap(m map[string]interface{}) heal.Signal {
	var s heal.Signal
	if v, ok := m["verdict"].(string); ok {
		s.Verdict = v
	}
	if v, ok := m["severity"].(string); ok {
		s.Severity = v
	}
	if ev, ok := m["evidence"].([]interface{}); ok {
		for _, e := range ev {
			em, _ := e.(map[string]interface{})
			if em == nil {
				continue
			}
			r, _ := em["rationale"].(string)
			s.Evidence = append(s.Evidence, heal.SignalEvidence{Rationale: r})
		}
	}
	if md, ok := m["metadata"].(map[string]interface{}); ok {
		if h, ok := md["heal_hint"].(string); ok {
			s.Metadata.HealHint = h
		}
	}
	return s
}

func mustLoadFailedView(t *testing.T, path string) heal.FailedSensor {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		ID       string `json:"id"`
		Requires struct {
			Env []struct {
				Name string `json:"name"`
			} `json:"env"`
			Tools   []string `json:"tools"`
			Context []string `json:"context"`
		} `json:"requires"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	envs := []string{}
	for _, e := range v.Requires.Env {
		envs = append(envs, e.Name)
	}
	return heal.FailedSensor{ID: v.ID, EnvNames: envs, Tools: v.Requires.Tools, Context: v.Requires.Context}
}
