//go:build heal_retry_original

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func TestRetryOriginal_PicksTypeAndShellsOut(t *testing.T) {
	dir := t.TempDir()
	s := testfixtures.ValidSensorComputational()
	s["execution"] = map[string]interface{}{
		"command": "true",
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			map[string]interface{}{"exit_code": "*", "verdict": "fail", "severity": "high"},
		},
	}
	body, _ := json.Marshal(s)
	path := filepath.Join(dir, "smoke-comp.json")
	os.WriteFile(path, body, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensor", path}, &stdout, &stderr)
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
