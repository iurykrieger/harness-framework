package layer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/stack"
	"sigs.k8s.io/yaml"
)

func loadStack(t *testing.T, name string) stack.Stack {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var s stack.Stack
	if err := yaml.Unmarshal(body, &s); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return s
}

func TestHasRole(t *testing.T) {
	s := loadStack(t, "stack-http-api-postgres.yaml")
	if !hasRole(s, "db-client") {
		t.Fatal("expected db-client to be present")
	}
	if hasRole(s, "metrics") {
		t.Fatal("metrics must not be present")
	}
}

func TestHasArchetype(t *testing.T) {
	s := loadStack(t, "stack-http-api-postgres.yaml")
	if !hasArchetype(s, "http-api") {
		t.Fatal("http-api expected")
	}
	if hasArchetype(s, "library") {
		t.Fatal("library must not be present")
	}
}

func TestHasLogShape(t *testing.T) {
	s := loadStack(t, "stack-http-api-postgres.yaml")
	if !hasLogShape(s) {
		t.Fatal("expected >=1 log_shape")
	}
}

func TestHasCoreSensor(t *testing.T) {
	if hasCoreSensor(nil, "run-project") {
		t.Fatal("empty catalog must not have run-project")
	}
}
