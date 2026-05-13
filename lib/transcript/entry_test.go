package transcript

import (
	"encoding/json"
	"testing"
)

func TestEntry_Text_StringContent(t *testing.T) {
	raw := []byte(`{"type":"user","message":{"role":"user","content":"hello"}}`)
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	if got := e.Text(); got != "hello" {
		t.Fatalf("Text()=%q want %q", got, "hello")
	}
}

func TestEntry_Text_BlockArrayContent(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first"},{"type":"tool_use","id":"u1","name":"Bash","input":{"command":"ls"}},{"type":"text","text":"second"}]}}`)
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	if got := e.Text(); got != "firstsecond" {
		t.Fatalf("Text()=%q want %q (text blocks only, concatenated)", got, "firstsecond")
	}
}

func TestEntry_ToolUses(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"Bash","input":{"command":"go run ./x"}}]}}`)
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	uses := e.ToolUses()
	if len(uses) != 1 {
		t.Fatalf("got %d tool_uses, want 1", len(uses))
	}
	if uses[0].Name != "Bash" {
		t.Fatalf("Name=%q", uses[0].Name)
	}
	if got := string(uses[0].Input); got != `{"command":"go run ./x"}` {
		t.Fatalf("Input=%s", got)
	}
}

func TestEntry_ToolResults_StringContent(t *testing.T) {
	raw := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"line1\nline2"}]}}`)
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	results := e.ToolResults()
	if len(results) != 1 {
		t.Fatalf("got %d tool_results, want 1", len(results))
	}
	if got := results[0].ResultText(); got != "line1\nline2" {
		t.Fatalf("ResultText()=%q", got)
	}
}

func TestEntry_ToolResults_BlockArrayContent(t *testing.T) {
	// Real Claude Code sometimes nests tool_result content as a block array.
	raw := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":[{"type":"text","text":"line1\nline2"}]}]}}`)
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	results := e.ToolResults()
	if len(results) != 1 {
		t.Fatalf("got %d tool_results, want 1", len(results))
	}
	if got := results[0].ResultText(); got != "line1\nline2" {
		t.Fatalf("ResultText()=%q", got)
	}
}
