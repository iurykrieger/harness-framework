package coredetect

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/stack"
)

func TestSetupPostgresNilWithoutDBClient(t *testing.T) {
	fn := Get("setup-postgres")
	if fn == nil {
		t.Fatal("expected non-nil ScaffoldFunc for setup-postgres")
	}
	if fn(stack.Stack{}) != nil {
		t.Fatal("expected nil Draft for setup-postgres on stack without db-client")
	}
}

func TestSetupPostgresWithDBClient(t *testing.T) {
	fn := Get("setup-postgres")
	if fn == nil {
		t.Fatal("expected non-nil ScaffoldFunc for setup-postgres")
	}
	s := stack.Stack{
		Components: []stack.Component{
			{Role: stack.RoleDBClient, Name: "postgres"},
		},
	}
	d := fn(s)
	if d == nil {
		t.Fatal("expected non-nil Draft for setup-postgres on stack with db-client")
	}
}
