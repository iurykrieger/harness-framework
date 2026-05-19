package coredetect

import (
	"testing"
)

func TestTypeCheckRegistered(t *testing.T) {
	if Get("type-check") == nil {
		t.Fatal("expected non-nil ScaffoldFunc for type-check")
	}
}
