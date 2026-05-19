package coredetect

import (
	"testing"
)

func TestInstallDepsRegistered(t *testing.T) {
	if Get("install-deps") == nil {
		t.Fatal("expected non-nil ScaffoldFunc for install-deps")
	}
}
