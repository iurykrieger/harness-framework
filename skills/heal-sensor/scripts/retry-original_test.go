//go:build heal_retry_original

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

// moduleRoot returns the absolute path of the Go module root by walking
// up from this source file until go.mod is found.
func moduleRoot() string {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func TestRetryOriginal_PicksTypeAndShellsOut(t *testing.T) {
	root := moduleRoot()
	if root == "" {
		t.Skip("could not locate module root; skipping E2E test")
	}
	t.Setenv("CLAUDE_PLUGIN_ROOT", root)

	// The runner accepts absolute paths as well as sensor IDs. Write the
	// sensor fixture into a temp project tree that mirrors the canonical
	// layout (<projectDir>/.harness/sensors/<id>.yaml) so dependency
	// resolution works.
	projectDir := t.TempDir()
	sensorsDir := filepath.Join(projectDir, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := sensortest.LoadComputational(t).AsMap()
	s["execution"] = map[string]interface{}{
		"command": "true",
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			map[string]interface{}{"exit_code": "*", "verdict": "fail", "severity": "high"},
		},
	}
	jsonBody, _ := json.Marshal(s)
	body, err := yaml.JSONToYAML(jsonBody)
	if err != nil {
		t.Fatal(err)
	}
	// id = "smoke-comp" (from fixture); file must be .harness/sensors/smoke-comp.yaml
	sensorFile := filepath.Join(sensorsDir, "smoke-comp.yaml")
	if err := os.WriteFile(sensorFile, body, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensor", sensorFile}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"verdict":"pass"`)) {
		t.Fatalf("expected pass aggregate; got %s", stdout.String())
	}
}

func TestRetryOriginal_MissingSensor(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensor", "/nonexistent.json"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
}

func TestRetryOriginal_UsesPluginRootAndContract(t *testing.T) {
	// Mock the `go` binary so the test can inspect args.
	tmpDir := t.TempDir()
	argsFile := filepath.Join(tmpDir, "args.txt")
	envFile := filepath.Join(tmpDir, "env.txt")
	// Use octal \037 instead of \x1f: dash (Ubuntu /bin/sh) passes \xNN
	// through as literal text; \037 is POSIX-portable.
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\037' "$@" > %q
env > %q
exit 0
`, argsFile, envFile)
	goBin := filepath.Join(tmpDir, "go")
	if err := os.WriteFile(goBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_PLUGIN_ROOT", "/fake/plugin/root")

	// Minimal valid sensor JSON.
	sensorPath := filepath.Join(t.TempDir(), "s.json")
	_ = os.WriteFile(sensorPath, []byte(`{"type":"computational"}`), 0o644)

	var stdout, stderr bytes.Buffer
	exit := run([]string{"--sensor=" + sensorPath}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}

	args, _ := os.ReadFile(argsFile)
	got := strings.Split(strings.TrimRight(string(args), "\x1f"), "\x1f")
	want := []string{"-C", "/fake/plugin/root", "run", "-tags=run_computational", "./skills/run-sensor/scripts", sensorPath}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %#v, want %#v", got, want)
	}

	env, _ := os.ReadFile(envFile)
	if !strings.Contains(string(env), "GOWORK=off") {
		t.Errorf("env missing GOWORK=off: %s", env)
	}
}
