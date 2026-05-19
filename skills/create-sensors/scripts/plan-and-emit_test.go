//go:build plan_and_emit

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanAndEmitRejectsEmptyStdin(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for empty ledger")
	}
	var sig map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &sig); err != nil {
		t.Fatalf("expected JSON Signal on stdout, got %q", stdout.String())
	}
	if v, _ := sig["verdict"].(string); v != "error" {
		t.Fatalf("expected verdict=error, got %v", sig["verdict"])
	}
}
