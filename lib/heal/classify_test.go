package heal_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

type stubRule struct {
	name    string
	matched bool
	shape   heal.Shape
	detail  string
}

func (s stubRule) Name() string { return s.name }
func (s stubRule) Match(_ heal.Signal, _ heal.FailedSensor) (bool, heal.Shape, string) {
	return s.matched, s.shape, s.detail
}

func TestClassify_FirstMatchWins(t *testing.T) {
	rules := []heal.Rule{
		stubRule{name: "r1", matched: false},
		stubRule{name: "r2", matched: true, shape: heal.ShapeMissingEnv, detail: "FOO"},
		stubRule{name: "r3", matched: true, shape: heal.ShapeBinaryNotFound},
	}
	res, ok := heal.ClassifyWith(rules, heal.Signal{}, heal.FailedSensor{})
	if !ok {
		t.Fatal("expected match")
	}
	if res.Rule != "r2" {
		t.Errorf("rule = %q, want r2", res.Rule)
	}
	if res.Shape != heal.ShapeMissingEnv {
		t.Errorf("shape = %v", res.Shape)
	}
	if res.Detail != "FOO" {
		t.Errorf("detail = %q", res.Detail)
	}
}

func TestClassify_NoMatch(t *testing.T) {
	rules := []heal.Rule{
		stubRule{name: "r1"},
		stubRule{name: "r2"},
	}
	_, ok := heal.ClassifyWith(rules, heal.Signal{}, heal.FailedSensor{})
	if ok {
		t.Fatal("expected no match")
	}
}

func TestShape_IsKnown(t *testing.T) {
	cases := map[heal.Shape]bool{
		heal.ShapeMissingEnv:         true,
		heal.ShapeBinaryNotFound:     true,
		heal.ShapeEnvFileAbsent:      true,
		heal.ShapeServiceUnavailable: true,
		heal.ShapeMissingContext:     true, // NEW
		heal.Shape("nonsense"):       false,
		heal.Shape(""):               false,
	}
	for s, want := range cases {
		if got := s.IsKnown(); got != want {
			t.Errorf("Shape(%q).IsKnown() = %v, want %v", s, got, want)
		}
	}
}

func TestShape_IsKnown_IncludesSubprocessFailed(t *testing.T) {
	if !heal.ShapeSubprocessFailed.IsKnown() {
		t.Fatal("ShapeSubprocessFailed must be in IsKnown's switch")
	}
}
