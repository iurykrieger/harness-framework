package coredetect

import (
	"testing"
)

func TestSeedDBRegistered(t *testing.T) {
	if Get("seed-db") == nil {
		t.Fatal("expected non-nil ScaffoldFunc for seed-db")
	}
}
