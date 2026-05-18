//go:build find_on_disk

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFindOnDisk_FlagParsing_RoleRequired(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestFindOnDisk_HappyPath_EmitsSample(t *testing.T) {
	dir := t.TempDir()
	fxPath := filepath.Join(dir, "trigger.json")
	if err := os.WriteFile(fxPath, []byte(`{"sku":"abc"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--role=trigger", "--search-paths=" + dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d (stderr=%s)", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if got["source"] != "disk" {
		t.Errorf("source = %v, want disk", got["source"])
	}
	payloadB64, _ := got["payload_b64"].(string)
	decoded, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil || string(decoded) != `{"sku":"abc"}` {
		t.Errorf("payload_b64 decoded = %q, want %q", string(decoded), `{"sku":"abc"}`)
	}
}

func TestFindOnDisk_NoMatch_EmitsEmptySource(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"--role=trigger", "--search-paths=" + dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var got map[string]any
	_ = json.Unmarshal(stdout.Bytes(), &got)
	if got["source"] != "" {
		t.Errorf("source = %v, want empty string", got["source"])
	}
}
