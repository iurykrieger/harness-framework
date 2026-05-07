package lib

import (
	"reflect"
	"strings"
	"testing"
)

func TestCompilePatterns_HappyPath(t *testing.T) {
	raw := []interface{}{
		map[string]interface{}{
			"regex":    `^FAIL\s+(\S+)`,
			"verdict":  "fail",
			"severity": "high",
			"captures": map[string]interface{}{"file": float64(1)},
		},
	}
	pats, err := CompilePatterns(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(pats) != 1 || pats[0].Verdict != "fail" || pats[0].Severity != "high" {
		t.Fatalf("unexpected: %+v", pats)
	}
	if pats[0].Captures["file"] != 1 {
		t.Fatalf("capture index: %v", pats[0].Captures)
	}
}

func TestCompilePatterns_BadRegex(t *testing.T) {
	raw := []interface{}{
		map[string]interface{}{"regex": "([unclosed", "verdict": "fail", "severity": "high"},
	}
	if _, err := CompilePatterns(raw); err == nil || !strings.Contains(err.Error(), "regex") {
		t.Fatalf("expected regex error, got %v", err)
	}
}

func TestMatchLine_FirstMatchWins(t *testing.T) {
	pats, _ := CompilePatterns([]interface{}{
		map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
		map[string]interface{}{"regex": "^FAIL", "verdict": "warn", "severity": "low"},
	})
	m, ok := MatchLine("FAIL TestFoo", pats)
	if !ok || m.Verdict != "fail" {
		t.Fatalf("expected first pattern to win, got %+v", m)
	}
}

func TestMatchLine_NoMatch(t *testing.T) {
	pats, _ := CompilePatterns([]interface{}{
		map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
	})
	if _, ok := MatchLine("hello world", pats); ok {
		t.Fatal("expected no match")
	}
}

func TestMatchLine_CaptureExtraction(t *testing.T) {
	pats, _ := CompilePatterns([]interface{}{
		map[string]interface{}{
			"regex":    `^(\S+):(\d+):(\d+)\s+error\s+(.+)$`,
			"verdict":  "fail",
			"severity": "high",
			"captures": map[string]interface{}{
				"file":       float64(1),
				"line_start": float64(2),
				"rationale":  float64(4),
			},
		},
	})
	m, ok := MatchLine("src/foo.ts:10:5 error 'x' is unused", pats)
	if !ok {
		t.Fatal("expected match")
	}
	if m.File != "src/foo.ts" {
		t.Fatalf("file=%q", m.File)
	}
	if m.LineStart == nil || *m.LineStart != 10 {
		t.Fatalf("line_start=%v", m.LineStart)
	}
	if m.Rationale != "'x' is unused" {
		t.Fatalf("rationale=%q", m.Rationale)
	}
}

func TestMatchLine_RationaleFallsBackToLine(t *testing.T) {
	pats, _ := CompilePatterns([]interface{}{
		map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
	})
	m, _ := MatchLine("FAIL TestFoo", pats)
	if m.Rationale != "FAIL TestFoo" {
		t.Fatalf("expected fallback to full line, got %q", m.Rationale)
	}
}

func TestMatchLine_LineFieldAlwaysSet(t *testing.T) {
	pats, _ := CompilePatterns([]interface{}{
		map[string]interface{}{"regex": ".+", "verdict": "pass", "severity": "info"},
	})
	m, _ := MatchLine("anything", pats)
	if m.Line != "anything" {
		t.Fatalf("Line not preserved: %q", m.Line)
	}
}

func TestCompilePatterns_AcceptsIntCaptureIndex(t *testing.T) {
	// JSON unmarshalled with json.Unmarshal puts numbers into float64; some
	// callers may construct test fixtures with int. Accept both.
	raw := []interface{}{
		map[string]interface{}{
			"regex":    "^x",
			"verdict":  "pass",
			"severity": "info",
			"captures": map[string]interface{}{"file": 1},
		},
	}
	pats, err := CompilePatterns(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pats[0].Captures, map[string]int{"file": 1}) {
		t.Fatalf("captures: %v", pats[0].Captures)
	}
}
