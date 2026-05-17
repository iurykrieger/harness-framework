package fixture_test

import (
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/fixture"
)

func TestResolve_Hit(t *testing.T) {
	pool := fixture.Pool{"x.json": "/abs/x.json"}
	got, err := fixture.Resolve(pool, "x.json")
	if err != nil {
		t.Fatalf("Resolve hit: %v", err)
	}
	if got != "/abs/x.json" {
		t.Fatalf("got %q, want /abs/x.json", got)
	}
}

func TestResolve_Miss(t *testing.T) {
	pool := fixture.Pool{"x.json": "/abs/x.json", "y.json": "/abs/y.json"}
	_, err := fixture.Resolve(pool, "missing.json")
	if err == nil {
		t.Fatalf("expected error on miss")
	}
	msg := err.Error()
	if !strings.Contains(msg, "missing.json") {
		t.Errorf("error should cite name, got: %q", msg)
	}
	if !strings.Contains(msg, "2") {
		t.Errorf("error should cite pool size 2, got: %q", msg)
	}
}

func TestResolve_NestedName(t *testing.T) {
	pool := fixture.Pool{"orders/big.json": "/abs/orders/big.json"}
	got, err := fixture.Resolve(pool, "orders/big.json")
	if err != nil {
		t.Fatalf("Resolve nested: %v", err)
	}
	if got != "/abs/orders/big.json" {
		t.Fatalf("got %q, want /abs/orders/big.json", got)
	}
}
