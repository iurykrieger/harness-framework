package coredetect

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func TestRunProjectScaffolded(t *testing.T) {
	fn := Get("run-project")
	if fn == nil {
		t.Fatal("expected non-nil ScaffoldFunc for run-project")
	}
	d := fn(stack.Stack{})
	if d == nil {
		t.Fatal("expected non-nil Draft for run-project with empty stack")
	}
	if d.Kind != sensor.KindSetup {
		t.Fatalf("expected Kind=%q, got %q", sensor.KindSetup, d.Kind)
	}
}
