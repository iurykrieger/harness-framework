// Package transcript reads Claude Code session transcripts in their
// real on-disk JSONL shape and exposes a small, ergonomic API to
// callers (the Stop hook today, future hooks tomorrow).
//
// The JSONL shape we care about:
//
//	{"type":"user","message":{"role":"user","content":"…"}}
//	{"type":"assistant","message":{"role":"assistant","content":[{"type":"text",…},{"type":"tool_use",…}]}}
//	{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"…","content":"…"|[…]}]}}
//
// Unknown top-level fields are ignored (encoding/json default).
package transcript

import (
	"encoding/json"
	"strings"
)

type Entry struct {
	Type    string  `json:"type"`
	Message Message `json:"message"`
}

type Message struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

// Content holds either a plain string ("…") or a list of typed blocks.
// Exactly one of String or Blocks is populated after UnmarshalJSON.
type Content struct {
	String string
	Blocks []Block
}

func (c *Content) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		c.String = s
		c.Blocks = nil
		return nil
	}
	var blocks []Block
	if err := json.Unmarshal(data, &blocks); err != nil {
		return err
	}
	c.String = ""
	c.Blocks = blocks
	return nil
}

type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`        // type="text"
	ToolUseID string          `json:"tool_use_id,omitempty"` // type="tool_result"
	ID        string          `json:"id,omitempty"`          // type="tool_use"
	Name      string          `json:"name,omitempty"`        // type="tool_use"
	Input     json.RawMessage `json:"input,omitempty"`       // type="tool_use"
	// Content is the raw value of a tool_result's nested content,
	// which is itself either a string or a block array. Decode lazily
	// via ResultText().
	Content json.RawMessage `json:"content,omitempty"` // type="tool_result"
}

// ResultText returns the textual payload of a tool_result block,
// flattening a nested block array if present.
func (b Block) ResultText() string {
	if len(b.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(b.Content, &s); err == nil {
		return s
	}
	var blocks []Block
	if err := json.Unmarshal(b.Content, &blocks); err == nil {
		var out strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" {
				out.WriteString(blk.Text)
			}
		}
		return out.String()
	}
	return ""
}

// Text returns the flat textual content of an Entry's message,
// concatenating type="text" blocks when content is a block array,
// or the raw string when content is a string.
func (e Entry) Text() string {
	if e.Message.Content.String != "" {
		return e.Message.Content.String
	}
	var out strings.Builder
	for _, b := range e.Message.Content.Blocks {
		if b.Type == "text" {
			out.WriteString(b.Text)
		}
	}
	return out.String()
}

// ToolUses returns the tool_use blocks in an Entry's message content,
// empty when the content is a plain string or contains no tool_uses.
func (e Entry) ToolUses() []Block {
	var out []Block
	for _, b := range e.Message.Content.Blocks {
		if b.Type == "tool_use" {
			out = append(out, b)
		}
	}
	return out
}

// ToolResults returns the tool_result blocks in an Entry's message
// content, empty when the content is a plain string or contains no
// tool_results.
func (e Entry) ToolResults() []Block {
	var out []Block
	for _, b := range e.Message.Content.Blocks {
		if b.Type == "tool_result" {
			out = append(out, b)
		}
	}
	return out
}
