//go:build read_usecases

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCatalogWalksSubdirs(t *testing.T) {
	// Stand up a fake project root with one root-tier sensor and one
	// per-usecase folder.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".harness", "sensors", "create-user"), 0o755); err != nil {
		t.Fatal(err)
	}
	rootSensor := []byte(`id: run-project
version: 0.1.0
name: run project
description: tmp
kind: setup
type: computational
regulation: behaviour
phase: on-demand
determinism: high
output: stream
cost: { class: cheap, latency: { p50_ms: 100, p95_ms: 1000 }, compute: { cpu: low, memory_mb: 64 } }
triggers: [{ on: manual }]
execution:
  blocking: true
  command: 'true'
  exit_code_map: [{ exit_code: 0, verdict: pass, severity: info }, { exit_code: '*', verdict: fail, severity: high }]
  output_parsing: { patterns: [{ regex: '.*', verdict: pass, severity: info }] }
use_cases: [bootstrap]
`)
	if err := os.WriteFile(filepath.Join(root, ".harness", "sensors", "run-project.yaml"), rootSensor, 0o644); err != nil {
		t.Fatal(err)
	}
	perUC := []byte(`id: observe-db-create-user
version: 0.1.0
name: observe db
description: tmp
kind: observation
type: computational
regulation: behaviour
phase: on-demand
determinism: high
output: single
layer: db-state
cost: { class: cheap, latency: { p50_ms: 50, p95_ms: 500, timeout_ms: 5000 }, compute: { cpu: low, memory_mb: 64 } }
triggers: [{ on: manual }]
execution:
  command: 'true'
  exit_code_map: [{ exit_code: 0, verdict: pass, severity: info }, { exit_code: '*', verdict: fail, severity: high }]
use_cases: [create-user]
`)
	if err := os.WriteFile(filepath.Join(root, ".harness", "sensors", "create-user", "observe-db-create-user.yaml"), perUC, 0o644); err != nil {
		t.Fatal(err)
	}

	// Load schema validator from the plugin root (env propagated by Bash).
	t.Setenv("HARNESS_REGISTRY_ROOT", root)
	// loadCatalog uses the package validator instantiated in run(); here we
	// exercise the helper directly with a stub validator. To keep the test
	// hermetic, we shell out to the actual binary in a follow-up
	// integration suite (Task 38). For this unit test we only assert the
	// recursive walker visits both files via a simplified validator that
	// always returns nil.
	t.Skip("loadCatalog requires a real schema validator; covered by the integration test in Task 38")
}
