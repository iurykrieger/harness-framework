package sensor

// BuildErrorSignal constructs a Signal-shaped map representing the
// "sensor could not run" outcome. Verdict is error, severity high; the
// caller supplies the rationale (free-form explanation) and remediation
// instructions (imperative text the next agent turn should act on).
//
// The returned map already conforms to schemas/signal.json — callers should
// still validate before emitting, in case envelope fields are malformed.
func BuildErrorSignal(env Envelope, outputMode, rationale, remediation string) map[string]interface{} {
	finished := NowFn().Format("2006-01-02T15:04:05Z")
	sig := map[string]interface{}{
		"sensor_id":   env.SensorID,
		"version":     env.Version,
		"run_id":      env.RunID,
		"started_at":  env.StartedAt,
		"finished_at": finished,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind":        "aggregate",
			"output_mode": outputMode,
		},
	}
	if remediation != "" {
		sig["remediation"] = map[string]interface{}{
			"instructions": remediation,
		}
	}
	return sig
}
