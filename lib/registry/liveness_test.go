package registry_test

import (
	"os"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func TestIsPIDAlive_SelfIsAlive(t *testing.T) {
	if !registry.IsPIDAlive(os.Getpid()) {
		t.Fatal("expected self pid to be alive")
	}
}

func TestIsPIDAlive_ZeroIsDead(t *testing.T) {
	if registry.IsPIDAlive(0) {
		t.Fatal("expected pid 0 to be reported dead")
	}
}

func TestIsPIDAlive_VeryLargePIDIsDead(t *testing.T) {
	// PID space caps well below 4_000_000 on Darwin/Linux; this PID
	// is essentially guaranteed not to exist.
	if registry.IsPIDAlive(3_999_999) {
		t.Fatal("expected nonexistent pid to be reported dead")
	}
}
