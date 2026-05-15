package fixture_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/fixture"
)

func TestDiscover_FlatAndNested(t *testing.T) {
	root := t.TempDir()
	must := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(".harness/fixtures/order-valid.json", `{"id":"x"}`)
	must(".harness/fixtures/orders/large.json", `{"big":true}`)

	pool, err := fixture.Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got, ok := pool["order-valid.json"]; !ok || got != filepath.Join(root, ".harness/fixtures/order-valid.json") {
		t.Errorf("flat fixture not discovered: %v", pool)
	}
	if got, ok := pool["orders/large.json"]; !ok || got != filepath.Join(root, ".harness/fixtures/orders/large.json") {
		t.Errorf("nested fixture not discovered: %v", pool)
	}
	if len(pool) != 2 {
		t.Errorf("expected 2 fixtures, got %d: %v", len(pool), pool)
	}
}

func TestDiscover_RejectOversize(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, 2*1024*1024)
	p := filepath.Join(root, ".harness/fixtures/big.bin")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, big, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Discover(root); err == nil {
		t.Fatalf("expected oversize fixture to be rejected")
	}
}

func TestDiscover_OversizeOverride(t *testing.T) {
	root := t.TempDir()
	body := make([]byte, 4096)
	p := filepath.Join(root, ".harness/fixtures/medium.bin")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_FIXTURE_MAX_BYTES", "1024")
	if _, err := fixture.Discover(root); err == nil {
		t.Fatalf("expected fixture above override cap to be rejected")
	}
	t.Setenv("HARNESS_FIXTURE_MAX_BYTES", "8192")
	pool, err := fixture.Discover(root)
	if err != nil {
		t.Fatalf("Discover with raised cap: %v", err)
	}
	if _, ok := pool["medium.bin"]; !ok {
		t.Fatalf("expected medium.bin to be discovered, got %v", pool)
	}
}

func TestDiscover_NoDirReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	pool, err := fixture.Discover(root)
	if err != nil {
		t.Fatalf("missing dir should not error, got: %v", err)
	}
	if len(pool) != 0 {
		t.Fatalf("expected empty pool, got %d entries", len(pool))
	}
}

func TestDiscover_RequiresProjectRoot(t *testing.T) {
	if _, err := fixture.Discover(""); err == nil {
		t.Fatalf("expected error for empty projectRoot")
	}
}
