package orchestrator

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// prepareRuntimeDir creates .runtime/sensors/<sensorID>/<runID>/ under
// projectRoot and returns the paths to raw.log and signals.log inside it.
// The directory's existence is the precondition for the
// "signals.log == stdout JSONL" invariant downstream.
func prepareRuntimeDir(projectRoot, sensorID, runID string) (rawLogPath, signalsLogPath string, err error) {
	dir := filepath.Join(projectRoot, ".runtime", "sensors", sensorID, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("runtime_dir: %w", err)
	}
	return filepath.Join(dir, "raw.log"), filepath.Join(dir, "signals.log"), nil
}

// emitSignalWithPersistence writes a Signal to stdout JSONL and appends a
// copy to .runtime/sensors/<sensorID>/<runID>/signals.log. The runID is
// read from sig["run_id"] — the helper deliberately omits a separate
// runID parameter so there is exactly one source of truth. When
// sig["run_id"] is missing or empty, the helper falls back to a
// timestamp-based string and logs a warning to stderr.
//
// Errors writing to signals.log are logged to stderr and do NOT abort
// the emission — stdout remains the canonical sink.
func emitSignalWithPersistence(sig map[string]interface{}, stdout io.Writer, projectRoot, sensorID string, stderr io.Writer) error {
	runID, _ := sig["run_id"].(string)
	if runID == "" {
		runID = fmt.Sprintf("ts-%d", time.Now().UTC().UnixNano())
		fmt.Fprintf(stderr, "orchestrator: signal missing run_id, using fallback %q\n", runID)
	}
	_, signalsLogPath, dirErr := prepareRuntimeDir(projectRoot, sensorID, runID)
	if dirErr != nil {
		fmt.Fprintf(stderr, "orchestrator: cannot prepare runtime dir for persistence: %v\n", dirErr)
		return json.NewEncoder(stdout).Encode(sig)
	}
	f, openErr := os.OpenFile(signalsLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if openErr != nil {
		fmt.Fprintf(stderr, "orchestrator: cannot open signals.log %q: %v\n", signalsLogPath, openErr)
		return json.NewEncoder(stdout).Encode(sig)
	}
	defer f.Close()
	multi := io.MultiWriter(stdout, f)
	return json.NewEncoder(multi).Encode(sig)
}
