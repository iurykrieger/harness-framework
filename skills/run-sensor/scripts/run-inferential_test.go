//go:build run_inferential

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
	"github.com/iurykrieger/harness-framework/lib/watcher"
)

// writeInferentialSensor writes an inferential sensor fixture to
// <root>/sensors/<id>.yaml and returns the sensor id.
func writeInferentialSensor(t *testing.T, root, id, command string) string {
	t.Helper()
	s := map[string]interface{}{
		"id": id, "version": "0.1.0",
		"name": id, "description": "fixture",
		"kind": "assertion",
		"type": "inferential", "regulation": "maintainability",
		"phase": "post-integration", "determinism": "low",
		"output": "stream",
		"cost": map[string]interface{}{
			"class":   "expensive",
			"latency": map[string]interface{}{"p50_ms": 1000, "p95_ms": 5000, "timeout_ms": 30000},
			"tokens":  map[string]interface{}{"model": "anthropic/claude-sonnet-4-6", "input_avg": 100, "output_avg": 50, "max_output": 256},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "pull-request"}},
		"execution": map[string]interface{}{
			"command":              command,
			"model":                "anthropic/claude-sonnet-4-6",
			"system_prompt":        "Output JSONL only.",
			"user_prompt_template": "Compare {{a}} to {{b}}.",
			"decoding":             map[string]interface{}{"temperature": 0.0, "max_tokens": 256},
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^PASS", "verdict": "pass", "severity": "info"},
					map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
				},
			},
		},
		"use_cases": []interface{}{"fake-uc"},
		"calibration": map[string]interface{}{
			"confidence_threshold": 0.7,
			"calibration_set":      "tests/cal.jsonl",
			"calibration_size":     120,
			"calibration_date":     "2026-04-15",
		},
	}
	sensorsDir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jb, _ := json.Marshal(s)
	b, err := yaml.JSONToYAML(jb)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sensorsDir, id+".yaml"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

func parseJSONL(t *testing.T, s string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestRunInferential_Pass(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	id := writeInferentialSensor(t, root, "infr-pass", `printf 'PASS judgment-1\n'`)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=foo()",
		"--slot", "b=bar()",
		id,
	}, root, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	if len(lines) < 2 {
		t.Fatalf("expected >=1 individual + aggregate, got %d", len(lines))
	}
	agg := lines[len(lines)-1]
	if agg["verdict"] != "pass" {
		t.Fatalf("aggregate verdict=%v", agg["verdict"])
	}
	md := agg["metadata"].(map[string]interface{})
	if md["output_mode"] != "stream" {
		t.Fatalf("aggregate metadata.output_mode=%v want stream", md["output_mode"])
	}
}

// TestRunInferential_InjectsHarnessPromptIntoEnv verifies that the runner
// renders execution.user_prompt_template against --slot bindings and
// exposes the result to the subprocess as the HARNESS_PROMPT env var.
// The sensor's command echoes $HARNESS_PROMPT on a PASS line; the
// pattern-matched individual's metadata.line should carry the rendered
// prompt body. This is the load-bearing contract for inferential
// sensors invoking an LLM CLI: the subprocess needs the prompt without
// the runner having to inline it into the command string.
func TestRunInferential_InjectsHarnessPromptIntoEnv(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	// writeInferentialSensor's default template is "Compare {{a}} to {{b}}.";
	// with a=foo() and b=bar() the rendered prompt is "Compare foo() to bar().".
	// The command echoes the env var on a PASS-prefixed line, which the
	// declared pattern (^PASS) captures into metadata.line.
	id := writeInferentialSensor(t, root, "infr-prompt-env",
		`printf 'PASS prompt=%s\n' "$HARNESS_PROMPT"`)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=foo()",
		"--slot", "b=bar()",
		id,
	}, root, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	if len(lines) < 2 {
		t.Fatalf("expected >=1 individual + aggregate, got %d:\n%s", len(lines), stdout.String())
	}
	// The first matched individual's metadata.line should contain the
	// rendered prompt. The aggregate (last line) is also inspected to
	// confirm a PASS verdict crossed with exit_code=0 stays pass.
	wantPrompt := "Compare foo() to bar()."
	found := false
	for _, sig := range lines[:len(lines)-1] {
		md, _ := sig["metadata"].(map[string]interface{})
		line, _ := md["line"].(string)
		if strings.Contains(line, wantPrompt) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("HARNESS_PROMPT was not injected into subprocess env;\nexpected an individual metadata.line containing %q\nstdout:\n%s",
			wantPrompt, stdout.String())
	}
	agg := lines[len(lines)-1]
	if agg["verdict"] != "pass" {
		t.Fatalf("aggregate verdict=%v, want pass", agg["verdict"])
	}
}

func TestRunInferential_CalibrationDowngrade(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	// One FAIL line + a HARNESS_AGGREGATE_CONFIDENCE=0.5 line on stdout.
	// Confidence 0.5 < threshold 0.7, so fail -> warn.
	cmd := `printf 'FAIL low-conf\nHARNESS_AGGREGATE_CONFIDENCE=0.5\n'`
	id := writeInferentialSensor(t, root, "infr-calibrate", cmd)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=x", "--slot", "b=y",
		id,
	}, root, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	agg := lines[len(lines)-1]
	if agg["verdict"] != "warn" {
		t.Fatalf("expected fail->warn downgrade, got %v", agg["verdict"])
	}
	md := agg["metadata"].(map[string]interface{})
	if md["calibration_downgrade"] != true {
		t.Fatalf("expected metadata.calibration_downgrade=true, got %v", md)
	}
	// The HARNESS_AGGREGATE_CONFIDENCE individual should NOT appear in counts.
	counts := md["counts"].(map[string]interface{})
	// Only the FAIL line should be counted: 1 fail.
	if counts["fail"].(float64) != 1 {
		t.Fatalf("counts.fail=%v want 1", counts["fail"])
	}
	if counts["pass"].(float64) != 0 {
		t.Fatalf("counts.pass=%v want 0 (HARNESS_AGGREGATE_CONFIDENCE should not be counted)", counts["pass"])
	}
}

func TestRunInferential_RejectsComputational(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	sensorsDir := filepath.Join(root, ".harness", "sensors")
	_ = os.MkdirAll(sensorsDir, 0o755)
	s := map[string]interface{}{
		"id": "wrong", "version": "0.1.0",
		"name": "x", "description": "x",
		"kind": "assertion",
		"type": "computational", "regulation": "maintainability",
		"phase": "on-demand", "determinism": "high",
		"output": "single",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"latency": map[string]interface{}{"p50_ms": 1, "p95_ms": 1, "timeout_ms": 1000},
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 1},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command": "true",
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			},
		},
		"use_cases": []interface{}{"fake-uc"},
	}
	jb, _ := json.Marshal(s)
	b, err := yaml.JSONToYAML(jb)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(sensorsDir, "wrong.yaml"), b, 0o644)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, "wrong"}, root, &stdout, &stderr); code != 2 {
		t.Fatalf("expected 2 (type mismatch), got %d", code)
	}
}

func TestRunInferential_HonoursExitCodeMap(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	id := writeInferentialSensor(t, root, "infr-ecmap", `printf 'PASS judgment\n'; exit 7`)
	// Patch the sensor on disk to add an exit_code_map that maps 7 -> warn/medium.
	sensorPath := filepath.Join(root, ".harness", "sensors", id+".yaml")
	b, _ := os.ReadFile(sensorPath)
	jb, err := yaml.YAMLToJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]interface{}
	_ = json.Unmarshal(jb, &s)
	s["execution"].(map[string]interface{})["exit_code_map"] = []interface{}{
		map[string]interface{}{"exit_code": 7, "verdict": "warn", "severity": "medium"},
	}
	njb, _ := json.Marshal(s)
	nb, err := yaml.JSONToYAML(njb)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(sensorPath, nb, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=x", "--slot", "b=y",
		id,
	}, root, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	agg := lines[len(lines)-1]
	// Worst-of-two between exit warn (from exit_code_map) and stream pass: warn wins.
	if agg["verdict"] != "warn" {
		t.Fatalf("expected warn (from declared exit_code_map for code 7), got %v", agg["verdict"])
	}
	if agg["severity"] != "medium" {
		t.Fatalf("expected severity=medium, got %v", agg["severity"])
	}
}

func TestRunInferential_MissingRequiredEnvAborts(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	id := writeInferentialSensor(t, root, "infr-env", `printf "should not run\n"; exit 0`)
	sensorPath := filepath.Join(root, ".harness", "sensors", id+".yaml")
	b, _ := os.ReadFile(sensorPath)
	jb, err := yaml.YAMLToJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]interface{}
	_ = json.Unmarshal(jb, &s)
	s["requires"] = []interface{}{
		map[string]interface{}{
			"kind":        "env",
			"name":        "DETECT_SENSORS_INF_GHOST",
			"description": "intentionally unset",
		},
	}
	njb, _ := json.Marshal(s)
	nb, err := yaml.JSONToYAML(njb)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(sensorPath, nb, 0o644)

	prev := sensor.LookupEnvFn
	sensor.LookupEnvFn = func(name string) (string, bool) {
		if name == "DETECT_SENSORS_INF_GHOST" {
			return "", false
		}
		return os.LookupEnv(name)
	}
	t.Cleanup(func() { sensor.LookupEnvFn = prev })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, "--slot", "a=x", "--slot", "b=y", id}, root, &stdout, &stderr); code != 0 {
		t.Fatalf("expected exit 0 (Signal printed), got %d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 aggregate, got %d", len(lines))
	}
	agg := lines[0]
	if agg["verdict"] != "error" || agg["severity"] != "high" {
		t.Fatalf("expected error/high, got %v/%v", agg["verdict"], agg["severity"])
	}
	rem, _ := agg["remediation"].(map[string]interface{})
	if rem == nil || !strings.Contains(rem["instructions"].(string), "DETECT_SENSORS_INF_GHOST") {
		t.Fatalf("remediation should name missing var: %+v", rem)
	}
}

func TestRunInferential_UnboundSlot(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	id := writeInferentialSensor(t, root, "infr-slot", `true`)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=x", // missing 'b'
		id,
	}, root, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for unbound slot, got %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "b") {
		t.Fatalf("stderr should name unbound slot 'b': %s", stderr.String())
	}
}

func TestRun_InferentialWithComputationalDep(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	sensorsDir := filepath.Join(root, ".harness", "sensors")
	_ = os.MkdirAll(sensorsDir, 0o755)

	// Setup dep: a kind=setup computational sensor.
	depJSON := sensortest.LoadSetup(t).AsMap()
	depJSON["id"] = "setup-x"
	depExec := depJSON["execution"].(map[string]interface{})
	depExec["command"] = "true"
	depJSONBytes, _ := json.Marshal(depJSON)
	depBytes, err := yaml.JSONToYAML(depJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sensorsDir, "setup-x.yaml"), depBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// Inferential requested sensor that depends on the setup.
	infJSON := sensortest.LoadInferential(t).AsMap()
	infJSON["id"] = "inf-with-dep"
	infJSON["requires"] = []interface{}{
		map[string]interface{}{"kind": "sensor", "id": "setup-x"},
	}
	infExec := infJSON["execution"].(map[string]interface{})
	infExec["user_prompt_template"] = "static prompt"
	infExec["command"] = `echo '{"sensor_id":"inf-with-dep","version":"0.1.0","run_id":"r","started_at":"2026-05-08T00:00:00Z","finished_at":"2026-05-08T00:00:01Z","verdict":"pass","severity":"info","confidence":0.9,"evidence":[],"cost_actual":{"latency_ms":100}}'`
	infJSONBytes, _ := json.Marshal(infJSON)
	infBytes, err := yaml.JSONToYAML(infJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sensorsDir, "inf-with-dep.yaml"), infBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, "inf-with-dep"}, root, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 Signals (dep + inf aggregate), got %d:\n%s", len(lines), out.String())
	}
}

// TestRunInferential_SIGTERMSetsTerminatedExternally mirrors the
// computational SIGTERM test for the inferential runner. The runner's
// signal handler cancels the run ctx; the inferential runner sets
// metadata.terminated_externally on its locally-built aggregate Signal.
func TestRunInferential_SIGTERMSetsTerminatedExternally(t *testing.T) {
	proj := t.TempDir()
	// Hand-roll a minimal inferential sensor with a long-running command.
	// writeInferentialSensor's default fixture is fine — we just override
	// the command to sleep.
	id := writeInferentialSensor(t, proj, "infr-sleeper", "sleep 30")

	// Pre-build the runner binary so we can invoke it with cwd=proj
	// (the runner resolves the sensor against its cwd as projectRoot).
	bin := filepath.Join(t.TempDir(), "runner-inf")
	build := exec.Command("go", "build", "-tags=run_inferential",
		"-o", bin, "./skills/run-sensor/scripts")
	build.Dir = repoRootForTest(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build runner: %v\n%s", err, out)
	}

	cmd := exec.Command(bin,
		"--schemas-dir", schematest.RepoSchemasDir(t),
		"--slot", "a=x", "--slot", "b=y", id)
	cmd.Dir = proj
	cmd.Env = append(os.Environ(), "HARNESS_REGISTRY_ROOT="+proj)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Give the runner time to spawn its subprocess and register the
	// signal handler before delivering SIGTERM. The inferential runner
	// does more setup work (schema validation, DAG resolution) before
	// reaching the streaming subprocess phase, so it needs more time
	// than the computational runner.
	time.Sleep(1 * time.Second)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	out, _ := io.ReadAll(stdout)
	_ = cmd.Wait()

	trimmed := strings.TrimRight(string(out), "\n")
	if trimmed == "" {
		t.Fatalf("no stdout (subprocess may have been killed before emitting aggregate)")
	}
	lines := strings.Split(trimmed, "\n")
	last := lines[len(lines)-1]
	var sig map[string]interface{}
	if err := json.Unmarshal([]byte(last), &sig); err != nil {
		t.Fatalf("parse last line: %v\n%q", err, last)
	}
	md, _ := sig["metadata"].(map[string]interface{})
	if md == nil {
		t.Fatalf("aggregate Signal has no metadata: %v", sig)
	}
	if v, _ := md["terminated_externally"].(bool); !v {
		t.Errorf("expected metadata.terminated_externally=true; got metadata=%v", md)
	}
}

func TestRunInferential_BlockingSensorRejected(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	sensorsDir := filepath.Join(root, ".harness", "sensors")
	_ = os.MkdirAll(sensorsDir, 0o755)

	// Blocking sensors: execution.blocking=true, no timeout_ms (schema forbids it), output=stream.
	s := map[string]interface{}{
		"id": "block-inf", "version": "0.1.0",
		"name": "block inf", "description": "fixture",
		"kind": "assertion",
		"type": "inferential", "regulation": "maintainability",
		"phase": "post-integration", "determinism": "low",
		"output": "stream",
		"cost": map[string]interface{}{
			"class": "expensive",
			// No timeout_ms — blocking sensors forbid it per schema.
			"latency": map[string]interface{}{"p50_ms": 1000, "p95_ms": 5000},
			"tokens":  map[string]interface{}{"model": "anthropic/claude-sonnet-4-6", "input_avg": 100, "output_avg": 50, "max_output": 256},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command":              "sleep 999",
			"model":                "anthropic/claude-sonnet-4-6",
			"system_prompt":        "You are a judge.",
			"user_prompt_template": "Evaluate {{x}}.",
			"decoding":             map[string]interface{}{"temperature": 0.0, "max_tokens": 256},
			"blocking":             true,
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^PASS", "verdict": "pass", "severity": "info"},
				},
			},
		},
		"use_cases": []interface{}{"fake-uc"},
		"calibration": map[string]interface{}{
			"confidence_threshold": 0.7,
			"calibration_set":      "tests/cal.jsonl",
			"calibration_size":     10,
			"calibration_date":     "2026-04-15",
		},
	}
	jb, _ := json.Marshal(s)
	b, err := yaml.JSONToYAML(jb)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(sensorsDir, "block-inf.yaml"), b, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--schemas-dir", schemasDir, "block-inf"}, root, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2 for blocking sensor, got %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "blocking") {
		t.Fatalf("stderr should mention 'blocking': %s", stderr.String())
	}
}

// TestRunInferential_UsesProjectRootAsSubprocessDir verifies that the inferential
// runner sets Dir=projectRoot on the StreamConfig so that sensor commands that
// reference relative paths (e.g. "cat README.md") run from the user's project
// root, not from the plugin root.
//
// The test creates a SENTINEL file in the project root and writes a sensor whose
// command is "cat SENTINEL". If Dir is not set the subprocess runs from the
// test's cwd (the plugin root, which has no SENTINEL), and the command fails.
func TestRunInferential_UsesProjectRootAsSubprocessDir(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	proj := t.TempDir()

	// Place SENTINEL only in the project root so the command fails if Dir is wrong.
	if err := os.WriteFile(filepath.Join(proj, "SENTINEL"), []byte("project-root-confirmed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	id := writeInferentialSensor(t, proj, "infr-cwd-probe", "cat SENTINEL")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=x",
		"--slot", "b=y",
		id,
	}, proj, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s\n(Dir not set on StreamConfig: command ran from wrong dir)", code, stderr.String(), stdout.String())
	}
	lines := parseJSONL(t, stdout.String())
	agg := lines[len(lines)-1]
	if agg["verdict"] != "pass" {
		t.Fatalf("aggregate verdict=%v, want pass\n(cat SENTINEL should succeed when Dir=projectRoot)", agg["verdict"])
	}
}

// TestRunInferential_AcceptsAbsolutePath verifies that run-inferential accepts
// an absolute file path in addition to a bare sensor id. This mirrors the
// fix applied to run-computational in commit 7ebf962.
//
// Without the fix, sensor.ResolveByID rejects absolute paths via the regex
// ^[a-z][a-z0-9-]*$, which broke /heal-sensor retries of inferential sensors
// (retry-original.go passes an absolute path to the runner).
func TestRunInferential_AcceptsAbsolutePath(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	id := writeInferentialSensor(t, root, "infr-abspath", `printf 'PASS judgment\n'`)
	absPath := filepath.Join(root, ".harness", "sensors", id+".yaml")

	// Sanity-check that the path is absolute (test would be meaningless otherwise).
	if !filepath.IsAbs(absPath) {
		t.Fatalf("expected absolute path, got %q", absPath)
	}

	var stdout, stderr bytes.Buffer
	// Pass the absolute path instead of the bare id. An empty projectRoot is
	// intentional here: the runner must NOT use projectRoot for resolution when
	// an absolute path is supplied; it derives the project root from the path itself.
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=foo",
		"--slot", "b=bar",
		absPath,
	}, "" /* projectRoot ignored for abs paths */, &stdout, &stderr)

	// A code=2 with "does not match ^[a-z]" in stderr is the sentinel for
	// the pre-fix ResolveByID rejection. Any other outcome (0=ran fine,
	// 1=schema/dep issue) means the path was accepted by the runner.
	if code == 2 && strings.Contains(stderr.String(), "does not match") {
		t.Fatalf("runner rejected absolute path via ResolveByID regex: stderr=%s", stderr.String())
	}
	// The sensor ran successfully with `printf 'PASS judgment\n'`.
	if code != 0 {
		t.Fatalf("expected exit 0 for absolute path, got %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
}

// TestRunInferential_BlockingDep_AggregateLast verifies the same
// last-line invariant for the inferential runner's ad-hoc deps loop
// (issue #19). When the requested inferential sensor depends on a
// blocking computational dep, the blocking dep is started, the
// inferential command runs, and the blocking dep is torn down. The
// requested sensor's aggregate must remain the LAST JSONL line.
func TestRunInferential_BlockingDep_AggregateLast(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))

	// Replace the watcher spawn with a fake — the test only needs
	// startBlockingDep to succeed, not a real watcher subprocess.
	prev := watcher.SpawnFn
	watcher.SpawnFn = func(opts watcher.SpawnOpts) (int, error) { return 99999, nil }
	t.Cleanup(func() { watcher.SpawnFn = prev })

	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	sensorsDir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Blocking dep — same shape as lib/orchestrator/live_deps_test.go's
	// writeBlockingDep, inlined here to avoid cross-package coupling.
	blockingDepJSON := []byte(`{
"id": "blocking-tick", "version": "1.0.0",
"name": "Blocking tick", "description": "blocking tick",
"determinism": "high", "kind": "setup", "type": "computational",
"output": "stream", "regulation": "behaviour", "phase": "continuous",
"triggers": [{"on": "manual"}],
"use_cases": ["fake-uc"],
"cost": {"class":"cheap","compute":{"cpu":"low","memory_mb":32},"latency":{"p50_ms":10,"p95_ms":50}},
"execution": {
  "command": "while true; do echo TICK; sleep 0.1; done",
  "blocking": true, "graceful_timeout_ms": 200,
  "exit_code_map": [{"exit_code":"*","verdict":"pass","severity":"info"}],
  "output_parsing": {"patterns":[{"regex":"^TICK$","verdict":"pass","severity":"info"}]}
}
}`)
	blockingDepYAML, err := yaml.JSONToYAML(blockingDepJSON)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(sensorsDir, "blocking-tick.yaml"), blockingDepYAML, 0o644)

	// Inferential consumer that depends on the blocking dep. Use the
	// same JSONL-emitting stub command pattern as TestRunInferential_Pass:
	// printf a single line whose first token matches a `^PASS` pattern,
	// then exit 0.
	id := writeInferentialSensor(t, root, "infr-with-blocking", `printf 'PASS judgment-1\n'`)
	// Adjust the sensor YAML to add the requires entry. writeInferentialSensor
	// doesn't take deps, so re-read, mutate, re-write.
	path := filepath.Join(sensorsDir, id+".yaml")
	b, _ := os.ReadFile(path)
	jb, err := yaml.YAMLToJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	_ = json.Unmarshal(jb, &m)
	m["requires"] = []interface{}{
		map[string]interface{}{"kind": "sensor", "id": "blocking-tick"},
	}
	updatedJSON, _ := json.Marshal(m)
	updated, err := yaml.JSONToYAML(updatedJSON)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(path, updated, 0o644)

	var out, errBuf bytes.Buffer
	// The default writeInferentialSensor template is "Compare {{a}} to {{b}}.",
	// so slot bindings must be provided for the rendered prompt to validate.
	if code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=foo()",
		"--slot", "b=bar()",
		id,
	}, root, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d:\n%s", len(lines), out.String())
	}

	var last map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("decode last line: %v\nline=%q", err, lines[len(lines)-1])
	}
	if last["sensor_id"] != id {
		t.Errorf("last sensor_id = %v, want %s (full stream:\n%s)", last["sensor_id"], id, out.String())
	}
	md, _ := last["metadata"].(map[string]interface{})
	if md == nil || md["kind"] != "aggregate" {
		t.Errorf("last metadata.kind = %v, want aggregate", md)
	}
}
