//go:build derive_from_contract

package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestDeriveFromContract_FlagParsing_FlagsRequired(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestDeriveFromContract_JSONSchemaHappyPath(t *testing.T) {
	// Use the existing canonical JSON Schema testdata under lib/fixture.
	// From skills/detect-usecases/scripts/, walk up to repo root.
	abs, err := filepath.Abs("../../../lib/fixture/testdata/contract/json_schema/order.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--source=json-schema", "--decl-path=" + abs}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d (stderr=%s)", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["source"] != "contract" {
		t.Errorf("source = %v, want contract", got["source"])
	}
	if got["ext"] != "json" {
		t.Errorf("ext = %v, want json", got["ext"])
	}
}

func TestDeriveFromContract_UnsupportedSource(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--source=go-struct", "--decl-path=/dev/null"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}
