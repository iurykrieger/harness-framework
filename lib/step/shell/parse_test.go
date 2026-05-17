package shell

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestCompileParsePatterns_Nil(t *testing.T) {
	got, err := compileParsePatterns(nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got != nil {
		t.Fatalf("got non-nil patterns for nil ParseConfig: %v", got)
	}
}

func TestCompileParsePatterns_Empty(t *testing.T) {
	got, err := compileParsePatterns(&sensor.ParseConfig{})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got != nil {
		t.Fatalf("got non-nil patterns for empty ParseConfig: %v", got)
	}
}

func TestCompileParsePatterns_Compiles(t *testing.T) {
	got, err := compileParsePatterns(&sensor.ParseConfig{
		Patterns: []sensor.Pattern{
			{Regex: "ERROR", Verdict: "fail", Severity: "high"},
			{Regex: "WARN", Verdict: "warn", Severity: "medium"},
		},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d patterns, want 2", len(got))
	}
	if got[0].Verdict != "fail" {
		t.Fatalf("first verdict = %q", got[0].Verdict)
	}
}

func TestCompileParsePatterns_InvalidRegex(t *testing.T) {
	_, err := compileParsePatterns(&sensor.ParseConfig{
		Patterns: []sensor.Pattern{
			{Regex: "[unclosed", Verdict: "fail", Severity: "high"},
		},
	})
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestCompileParsePatterns_WithCaptures(t *testing.T) {
	one := 1
	two := 2
	got, err := compileParsePatterns(&sensor.ParseConfig{
		Patterns: []sensor.Pattern{
			{
				Regex:    `^(\S+):(\d+)$`,
				Verdict:  "warn",
				Severity: "medium",
				Captures: &sensor.Captures{File: &one, LineStart: &two},
			},
		},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d patterns, want 1", len(got))
	}
	if got[0].Captures["file"] != 1 || got[0].Captures["line_start"] != 2 {
		t.Fatalf("captures = %v", got[0].Captures)
	}
}
