//go:build replay_fixture

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

// uniqueSensorID returns a schema-valid id ([a-z][a-z0-9-]*) that no
// previous test run could have created — used to assert "the real repo's
// .harness/runtime/ has no entry by this name after the script runs."
func uniqueSensorID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// writeTempSensor materializes a computational sensor JSON file with the
// given id. Returns the absolute file path.
func writeTempSensor(t *testing.T, id string) string {
	t.Helper()
	s := testfixtures.ValidSensorComputational()
	s["id"] = id
	body, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal sensor: %v", err)
	}
	path := filepath.Join(t.TempDir(), "sensor.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write sensor: %v", err)
	}
	return path
}

// writeTempFixture writes content to a tempfile and returns its absolute path.
func writeTempFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// repoRootDir resolves the harness-framework repo root from cwd, mirroring
// the script's own repoRoot() helper. Used to assert pollution did not
// land in the project's .harness/runtime/ tree.
func repoRootDir(t *testing.T) string {
	t.Helper()
	got := repoRoot()
	if got == "" {
		t.Fatal("repoRoot() returned empty; test cannot locate project root")
	}
	return got
}

func TestReplayFixture_PreservesSensorIDAndIsolatesRuntime(t *testing.T) {
	id := uniqueSensorID("replay-iso")
	sensorPath := writeTempSensor(t, id)
	fixturePath := writeTempFixture(t, "")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensor", sensorPath, "--fixture", fixturePath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}

	// The aggregate Signal is the last line of stdout (JSONL). Decode it.
	out := strings.TrimSpace(stdout.String())
	lines := strings.Split(out, "\n")
	if len(lines) == 0 {
		t.Fatalf("no stdout; stderr=%s", stderr.String())
	}
	var agg map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &agg); err != nil {
		t.Fatalf("decode aggregate: %v; raw=%q", err, lines[len(lines)-1])
	}
	if got, _ := agg["sensor_id"].(string); got != id {
		t.Fatalf("aggregate.sensor_id = %q, want %q (no replay- prefix)", got, id)
	}

	// Runtime isolation: the project's .harness/runtime/<id>/ MUST NOT
	// exist. The script set HARNESS_REGISTRY_ROOT to a tempdir for the
	// runner; any artifacts went there and were removed on exit.
	polluted := filepath.Join(repoRootDir(t), ".harness", "runtime", id)
	if _, err := os.Stat(polluted); !os.IsNotExist(err) {
		t.Fatalf("runtime pollution: %s exists (stat err=%v)", polluted, err)
	}
}
