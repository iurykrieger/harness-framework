package transcript

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestScan_Empty(t *testing.T) {
	entries, err := Scan(filepath.Join("testdata", "empty.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d entries; want 0", len(entries))
	}
}

func TestScan_RunSensorError(t *testing.T) {
	entries, err := Scan(filepath.Join("testdata", "run-sensor-error.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries; want 3", len(entries))
	}
	// 0: user slash command
	if got := entries[0].Text(); !strings.Contains(got, "<command-name>/run-sensor</command-name>") {
		t.Fatalf("entry 0 Text=%q", got)
	}
	// 1: assistant with tool_use
	uses := entries[1].ToolUses()
	if len(uses) != 1 || uses[0].Name != "Bash" {
		t.Fatalf("entry 1 ToolUses=%+v", uses)
	}
	// 2: user with tool_result containing aggregate JSONL
	results := entries[2].ToolResults()
	if len(results) != 1 {
		t.Fatalf("entry 2 ToolResults=%+v", results)
	}
	body := results[0].ResultText()
	if !strings.Contains(body, `"verdict":"error"`) {
		t.Fatalf("tool_result text missing verdict: %q", body)
	}
}

func TestScan_NonexistentPath(t *testing.T) {
	_, err := Scan("does-not-exist.jsonl")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
