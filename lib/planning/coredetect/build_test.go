package coredetect

import (
	"testing"
)

func TestBuildRegistered(t *testing.T) {
	if Get("build") == nil {
		t.Fatal("expected non-nil ScaffoldFunc for build")
	}
}
