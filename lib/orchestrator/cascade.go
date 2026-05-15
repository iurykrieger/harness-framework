package orchestrator

import (
	"fmt"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// BuildCascadeSignal constructs the Signal map emitted for a sensor that
// was skipped because one of its (transitive) dependencies produced a
// non-pass verdict. The structure is described in
// docs/superpowers/specs/2026-05-08-sensor-dependencies-design.md
// (section "Cascade Signal envelope").
//
// failedDepSignal is the aggregate Signal of the dep that failed; the
// caller is responsible for ensuring it carries verdict, severity,
// sensor_id, and run_id.
func BuildCascadeSignal(skipped Sensor, failedDepSignal map[string]interface{}) map[string]interface{} {
	now := sensor.NowFn().Format("2006-01-02T15:04:05Z")
	failedID, _ := failedDepSignal["sensor_id"].(string)
	failedRunID, _ := failedDepSignal["run_id"].(string)
	failedVerdict, _ := failedDepSignal["verdict"].(string)
	failedSeverity, _ := failedDepSignal["severity"].(string)

	version, _ := skipped.JSON["version"].(string)
	execMap, _ := skipped.JSON["execution"].(map[string]interface{})
	command, _ := execMap["command"].(string)

	rationale := fmt.Sprintf(
		"Skipped: dependency %q produced verdict=%s/%s in run_id=%s. See its Signal earlier in this JSONL stream.",
		failedID, failedVerdict, failedSeverity, failedRunID,
	)

	return map[string]interface{}{
		"sensor_id":   skipped.ID,
		"version":     version,
		"run_id":      sensor.NewRunIDFn(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence": []interface{}{
			map[string]interface{}{
				"rationale": rationale,
				"file":      filepath.Join(".harness", "sensors", failedID+".yaml"),
			},
		},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind":                "cascade",
			"command":             command,
			"exit_code":           nil,
			"timed_out":           false,
			"counts":              map[string]interface{}{"pass": 0, "warn": 0, "fail": 0, "error": 1},
			"failed_dep_id":       failedID,
			"failed_dep_run_id":   failedRunID,
			"failed_dep_verdict":  failedVerdict,
			"failed_dep_severity": failedSeverity,
		},
	}
}
