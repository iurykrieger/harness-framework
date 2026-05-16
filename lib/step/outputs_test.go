package step_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/step"
)

func TestExtractOutput_Regex(t *testing.T) {
	spec := step.OutputSpec{From: "stdout", Regex: `^DONE: (.+)$`}
	got, err := step.ExtractOutput(spec, step.OutputSource{Stdout: "DONE: abc-123\n"})
	if err != nil || got != "abc-123" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestExtractOutput_JSONPath(t *testing.T) {
	spec := step.OutputSpec{From: "response.body", JSONPath: "$.id"}
	src := step.OutputSource{ResponseBody: []byte(`{"id":"xyz","other":1}`)}
	got, err := step.ExtractOutput(spec, src)
	if err != nil || got != "xyz" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestExtractOutput_FromStatus(t *testing.T) {
	spec := step.OutputSpec{From: "response.status"}
	got, err := step.ExtractOutput(spec, step.OutputSource{ResponseStatus: 201})
	if err != nil || got != "201" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestExtractOutput_RegexNoMatchErrors(t *testing.T) {
	spec := step.OutputSpec{From: "stdout", Regex: `^nope`}
	if _, err := step.ExtractOutput(spec, step.OutputSource{Stdout: "DONE\n"}); err == nil {
		t.Fatal("expected error for no-match regex")
	}
}

func TestExtractOutput_Trim(t *testing.T) {
	spec := step.OutputSpec{From: "stdout", Trim: true}
	got, err := step.ExtractOutput(spec, step.OutputSource{Stdout: "  hello  \n"})
	if err != nil || got != "hello" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestExtractOutput_Header(t *testing.T) {
	spec := step.OutputSpec{From: "response.headers.content-type"}
	src := step.OutputSource{ResponseHeader: map[string]string{"content-type": "application/json"}}
	got, err := step.ExtractOutput(spec, src)
	if err != nil || got != "application/json" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}
