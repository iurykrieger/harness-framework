// export_test.go exposes package-internal symbols for use in
// orchestrator_test package tests. This file is compiled only during
// `go test` and never included in the production binary.
package orchestrator

import "github.com/iurykrieger/harness-framework/lib/registry"

// ExportedStartBlockingDep is a test-only shim that calls the unexported
// startBlockingDep so orchestrator_test can exercise it directly without
// going through the full AttachLiveDep codepath (which holds flock,
// does liveness checks, etc.). Returns the freshly-minted run_id so the
// caller can address the registry entry by run_id (for cleanup,
// path resolution, etc).
func ExportedStartBlockingDep(rs *registry.RunningSensors, r registry.Root, dep Sensor, holder registry.HeldByEntry, projectRoot string) (string, error) {
	sp, err := startBlockingDep(rs, r, dep, holder, projectRoot)
	return sp.RunID, err
}
