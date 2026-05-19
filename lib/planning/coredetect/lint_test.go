package coredetect

import (
	"testing"
)

func TestLintRegistered(t *testing.T) {
	if Get("lint") == nil {
		t.Fatal("expected non-nil ScaffoldFunc for lint")
	}
}
