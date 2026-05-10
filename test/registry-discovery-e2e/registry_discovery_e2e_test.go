// test/registry-discovery-e2e/registry_discovery_e2e_test.go
//
// Black-box regression guard for issue #6: the registry-touching skills
// must agree on the registry path regardless of which subdirectory the
// caller is in, as long as the project root is reachable via either the
// sensors/ marker walk-up or HARNESS_REGISTRY_ROOT.
package registryDiscoveryE2E_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// repoRoot returns the harness-framework repo root by walking up from
// the test's cwd until it sees go.mod.
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

// ensureBinaries compiles the start_sensor, list_sensors, stop_sensor,
// and watcher binaries once per test run into a shared tempdir. The
// watcher binary lives next to the start binary because start.go's
// watcherBinaryPath() looks for "watcher" alongside the running exe.
var (
	buildOnce  sync.Once
	startBin   string
	listBin    string
	stopBin    string
	watcherBin string
	buildErr   error
)

func ensureBinaries(t *testing.T) {
	t.Helper()
	buildOnce.Do(func() {
		root := repoRoot(t)
		bin, err := os.MkdirTemp("", "registry-e2e-bin-")
		if err != nil {
			buildErr = fmt.Errorf("mkdir bin: %w", err)
			return
		}
		startBin = filepath.Join(bin, "start-sensor")
		listBin = filepath.Join(bin, "list-sensors")
		stopBin = filepath.Join(bin, "stop-sensor")
		watcherBin = filepath.Join(bin, "watcher")

		for _, b := range []struct {
			tags string
			out  string
			pkg  string
		}{
			{"start_sensor", startBin, "./skills/start-sensor/scripts"},
			{"list_sensors", listBin, "./skills/list-sensors/scripts"},
			{"stop_sensor", stopBin, "./skills/stop-sensor/scripts"},
			{"start_watcher", watcherBin, "./skills/start-sensor/scripts"},
		} {
			cmd := exec.Command("go", "build", "-tags="+b.tags, "-o", b.out, b.pkg)
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			if err != nil {
				buildErr = fmt.Errorf("build %s: %v\n%s", b.tags, err, out)
				return
			}
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
}

// makeScratchDir creates a temp directory under <repoRoot>/.test-tmp so
// that the schema walk-up (which climbs up from cwd looking for
// schemas/) can reach the repo root's schemas/ directory. Cleanup is
// registered automatically via t.Cleanup; callers do not need to.
//
// .test-tmp is gitignored (see repo .gitignore) so leftover dirs from
// crashed tests are harmless to commits.
func makeScratchDir(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	scratch := filepath.Join(root, ".test-tmp")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(scratch, "registry-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// makeProject scaffolds <parent>/proj/sensors/<id>.json containing a
// trivial blocking sensor. Returns the project root path.
func makeProject(t *testing.T, parent, id, command string) string {
	t.Helper()
	proj := filepath.Join(parent, "proj")
	if err := os.MkdirAll(filepath.Join(proj, "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	sensor := map[string]interface{}{
		"id":          id,
		"version":     "1.0.0",
		"name":        "registry-e2e fixture",
		"description": "blocking sleep used by integration test",
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
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command":  command,
			"blocking": true,
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^.*$", "verdict": "pass", "severity": "info"},
				},
			},
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": "*", "verdict": "pass", "severity": "info"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{
					"fixture":           "sensors/fixtures/" + id + "/pass.txt",
					"expected_verdict":  "pass",
					"expected_severity": "info",
				},
			},
		},
	}
	body, _ := json.MarshalIndent(sensor, "", "  ")
	if err := os.WriteFile(filepath.Join(proj, "sensors", id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return proj
}

// killWatcherIfAlive sends SIGKILL to a watcher PID after stop-sensor
// has had a chance to terminate it. Tolerates "process not found" (the
// happy-path: watcher already exited).
func killWatcherIfAlive(pid int) {
	if pid <= 0 {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	// Best-effort SIGKILL; ignore errors (process may already be dead).
	_ = proc.Signal(syscall.SIGKILL)
}

// runIn runs binary in dir with optional env overrides. Returns
// (stdout, stderr, exitCode).
func runIn(t *testing.T, binary, dir string, args []string, extraEnv map[string]string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir

	// Filter out env vars we're about to override, so the child sees a
	// single value rather than the OS-defined "last wins" behavior.
	base := os.Environ()
	if len(extraEnv) > 0 {
		filtered := base[:0]
		for _, kv := range base {
			keep := true
			for k := range extraEnv {
				if strings.HasPrefix(kv, k+"=") {
					keep = false
					break
				}
			}
			if keep {
				filtered = append(filtered, kv)
			}
		}
		base = filtered
	}
	env := base
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exit := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run %s: %v", binary, err)
	}
	return stdout.String(), stderr.String(), exit
}

func lastJSON(t *testing.T, s string) map[string]interface{} {
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
	t.Fatalf("no JSON line in output: %q", s)
	return nil
}

// TestE2E_DiscoverySharesStateAcrossCwds is the regression guard for
// issue #6: /start-sensor in cwd A must register an entry that
// /list-sensors in cwd B (a sub-directory of A) can see.
func TestE2E_DiscoverySharesStateAcrossCwds(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	ensureBinaries(t)

	// Project must live under the repo root so the schema walk-up
	// (which climbs from cwd to find schemas/) can reach repo/schemas/.
	parent := makeScratchDir(t)
	proj := makeProject(t, parent, "sleeper", "sleep 60")
	deep := filepath.Join(proj, "nested", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HARNESS_REGISTRY_ROOT", "")

	// Step 1: start from proj/.
	stdout, stderr, exit := runIn(t, startBin, proj, []string{"sleeper"}, nil)
	if exit != 0 {
		t.Fatalf("start exit %d\nstdout=%s\nstderr=%s", exit, stdout, stderr)
	}
	startSig := lastJSON(t, stdout)
	if startSig["verdict"] != "pass" {
		t.Fatalf("start verdict: got %v\nstdout=%s", startSig["verdict"], stdout)
	}
	startMD := startSig["metadata"].(map[string]interface{})
	wantPath := filepath.Join(proj, ".runtime", "sensors", "running_sensors.json")
	if startMD["registry_path"] != wantPath {
		t.Errorf("start registry_path: got %v, want %v", startMD["registry_path"], wantPath)
	}

	// Capture watcher PID so we can SIGKILL it if stop-sensor leaves it alive.
	watcherPID := 0
	if v, ok := startMD["watcher_pid"].(float64); ok {
		watcherPID = int(v)
	}

	// Always clean up the sensor process at the end.
	t.Cleanup(func() {
		_, _, _ = runIn(t, stopBin, proj, []string{"sleeper"}, nil)
		// Defensive: if stop-sensor failed to kill the watcher, SIGKILL it
		// directly so the scratch dir cleanup can remove proj/ (the watcher's
		// cwd prevents rmdir on macOS otherwise).
		time.Sleep(50 * time.Millisecond)
		killWatcherIfAlive(watcherPID)
	})

	// Give the watcher a beat to settle before the list call.
	time.Sleep(100 * time.Millisecond)

	// Step 2: list from the deep sub-directory; the entry MUST be visible.
	stdout2, stderr2, exit2 := runIn(t, listBin, deep, nil, nil)
	if exit2 != 0 {
		t.Fatalf("list exit %d\nstdout=%s\nstderr=%s", exit2, stdout2, stderr2)
	}
	listSig := lastJSON(t, stdout2)
	if listSig["verdict"] != "pass" {
		t.Fatalf("list verdict: got %v (want pass)\nstdout=%s", listSig["verdict"], stdout2)
	}
	listMD := listSig["metadata"].(map[string]interface{})
	entries, _ := listMD["entries"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("entries: got %d, want 1\nstdout=%s", len(entries), stdout2)
	}
	if entries[0].(map[string]interface{})["sensor_id"] != "sleeper" {
		t.Fatalf("entry sensor_id: got %v", entries[0])
	}
	if listMD["registry_path"] != wantPath {
		t.Errorf("list registry_path: got %v, want %v", listMD["registry_path"], wantPath)
	}
	if listMD["registry_source"] != "walk_up" {
		t.Errorf("list registry_source: got %v, want walk_up", listMD["registry_source"])
	}
}

// TestE2E_OutsideProjectFailsDiscovery: list from a directory with no
// sensors/ marker anywhere up to filesystem root (modulo the test's
// tempdir parent) returns an error signal.
func TestE2E_OutsideProjectFailsDiscovery(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	ensureBinaries(t)

	outside := t.TempDir()
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	stdout, stderr, exit := runIn(t, listBin, outside, nil, nil)
	if exit != 1 {
		t.Fatalf("list exit: got %d, want 1\nstdout=%s\nstderr=%s", exit, stdout, stderr)
	}
	sig := lastJSON(t, stdout)
	if sig["verdict"] != "error" {
		t.Fatalf("verdict: got %v, want error", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "registry_discovery_failed" {
		t.Errorf("kind: got %v", md["kind"])
	}
	ev := sig["evidence"].([]interface{})
	rationale := ev[0].(map[string]interface{})["rationale"].(string)
	if !strings.Contains(rationale, "HARNESS_REGISTRY_ROOT") || !strings.Contains(rationale, "sensors") {
		t.Errorf("rationale should mention both strategies, got: %q", rationale)
	}
}

// TestE2E_EnvVarOverridesDiscovery: with HARNESS_REGISTRY_ROOT set to a
// project, /list-sensors run from a directory outside that project sees
// the project's entries.
func TestE2E_EnvVarOverridesDiscovery(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	ensureBinaries(t)

	// Both project and outside dirs must live under the repo root so the
	// schema walk-up (which climbs from cwd to find schemas/) can reach
	// repo/schemas/. The env-var contract is tested via HARNESS_REGISTRY_ROOT
	// pointing at proj while list-sensors runs from outside/.
	parent := makeScratchDir(t)
	proj := makeProject(t, parent, "sleeper", "sleep 60")
	outside := makeScratchDir(t)

	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	stdout, stderr, exit := runIn(t, startBin, proj, []string{"sleeper"}, nil)
	if exit != 0 {
		t.Fatalf("start exit %d\nstdout=%s\nstderr=%s", exit, stdout, stderr)
	}

	// Capture watcher PID so we can SIGKILL it if stop-sensor leaves it alive.
	watcherPID := 0
	startSig := lastJSON(t, stdout)
	if md, ok := startSig["metadata"].(map[string]interface{}); ok {
		if v, ok := md["watcher_pid"].(float64); ok {
			watcherPID = int(v)
		}
	}

	t.Cleanup(func() {
		_, _, _ = runIn(t, stopBin, proj, []string{"sleeper"}, nil)
		// Defensive: if stop-sensor failed to kill the watcher, SIGKILL it
		// directly so the scratch dir cleanup can remove proj/ (the watcher's
		// cwd prevents rmdir on macOS otherwise).
		time.Sleep(50 * time.Millisecond)
		killWatcherIfAlive(watcherPID)
	})

	time.Sleep(100 * time.Millisecond)

	stdout2, stderr2, exit2 := runIn(t, listBin, outside, nil, map[string]string{
		"HARNESS_REGISTRY_ROOT": proj,
	})
	if exit2 != 0 {
		t.Fatalf("list exit %d\nstdout=%s\nstderr=%s", exit2, stdout2, stderr2)
	}
	sig := lastJSON(t, stdout2)
	if sig["verdict"] != "pass" {
		t.Fatalf("verdict: got %v\nstdout=%s", sig["verdict"], stdout2)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["registry_source"] != "env" {
		t.Errorf("registry_source: got %v, want env", md["registry_source"])
	}
	entries, _ := md["entries"].([]interface{})
	if len(entries) != 1 || entries[0].(map[string]interface{})["sensor_id"] != "sleeper" {
		t.Fatalf("entries: got %+v", entries)
	}
}
