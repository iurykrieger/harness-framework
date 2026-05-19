package coredetect

import (
	"testing"
)

func TestRunAllTestsRegistered(t *testing.T) {
	if Get("run-all-tests") == nil {
		t.Fatal("expected non-nil ScaffoldFunc for run-all-tests")
	}
}
