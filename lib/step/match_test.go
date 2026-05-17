package step_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/step"
)

func TestMatch_Equals(t *testing.T) {
	if !step.Match(step.Matcher{Equals: 201}, 201) {
		t.Fatal("int equality")
	}
	if !step.Match(step.Matcher{Equals: 201}, "201") {
		t.Fatal("numeric coercion from string")
	}
	if step.Match(step.Matcher{Equals: 201}, "two-hundred-one") {
		t.Fatal("non-numeric string should not match numeric expectation")
	}
	if !step.Match(step.Matcher{Equals: "pass"}, "pass") {
		t.Fatal("string equality")
	}
}

func TestMatch_GteLte(t *testing.T) {
	if !step.Match(step.Matcher{Gte: ptr(500.0)}, 600) {
		t.Fatal("gte hit")
	}
	if step.Match(step.Matcher{Gte: ptr(500.0)}, 400) {
		t.Fatal("gte miss")
	}
	if !step.Match(step.Matcher{Lte: ptr(500.0)}, "200") {
		t.Fatal("lte with stringified number")
	}
}

func TestMatch_RegexAndContains(t *testing.T) {
	if !step.Match(step.Matcher{Matches: "^abc"}, "abcdef") {
		t.Fatal("regex anchor")
	}
	if !step.Match(step.Matcher{Contains: "json"}, "application/json") {
		t.Fatal("substring")
	}
}

func ptr(x float64) *float64 { return &x }
