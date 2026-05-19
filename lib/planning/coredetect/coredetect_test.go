package coredetect

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/stack"
)

func TestGetUnknownReturnsNil(t *testing.T) {
	if Get("not-a-real-primitive") != nil {
		t.Fatal("expected nil for unknown id")
	}
}

func TestEnsureMissingReturnsEmptySliceWhenIDsEmpty(t *testing.T) {
	got, err := EnsureMissing(stack.Stack{}, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 drafts, got %d", len(got))
	}
}

func TestEnsureMissingErrorsOnUnknownID(t *testing.T) {
	_, err := EnsureMissing(stack.Stack{}, []string{"unknown-thing"})
	if err == nil {
		t.Fatal("expected error for unknown id")
	}
}
