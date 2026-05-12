// export_test.go exposes package-internal symbols for use in
// orchestrator_test package tests. This file is compiled only during
// `go test` and never included in the production binary.
package orchestrator

import "github.com/iurykrieger/harness-framework/lib/registry"

// ExportedStartBlockingDep is a test-only shim that calls the unexported
// startBlockingDep so orchestrator_test can exercise it directly without
// going through the full AttachLiveDep codepath (which holds flock,
// does liveness checks, etc.).
func ExportedStartBlockingDep(rs *registry.RunningSensors, r registry.Root, dep Sensor, holder registry.HeldByEntry) (string, error) {
	return startBlockingDep(rs, r, dep, holder)
}
