package exec

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

// buildAggregate constructs the final aggregate signal that closes a
// sensor's signal stream. The verdict is the worst of every per-step
// verdict observed by Run; severity follows the canonical
// verdict→severity mapping. metadata.kind="aggregate" identifies this
// signal as the run-level summary (vs. individuals emitted by streaming
// steps); metadata.steps[] carries one {id, type, verdict} per
// executed step so /heal-sensor can attribute the failure to a specific
// step without re-parsing the whole stream.
func buildAggregate(s *sensor.Sensor, verdict signal.Verdict, perStep []map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"sensor_id": s.ID,
		"version":   s.Version,
		"verdict":   string(verdict),
		"severity":  string(severityFromVerdict(verdict)),
		"metadata": map[string]interface{}{
			"kind":  "aggregate",
			"steps": perStep,
		},
	}
}

// severityFromVerdict is the project-wide verdict→severity mapping the
// aggregate signal uses. The same mapping is duplicated locally in
// lib/step/http and lib/step/assert (rule of three; extract only when
// a fourth caller appears).
func severityFromVerdict(v signal.Verdict) signal.Severity {
	switch v {
	case signal.VerdictPass:
		return signal.SeverityInfo
	case signal.VerdictWarn:
		return signal.SeverityMedium
	case signal.VerdictFail:
		return signal.SeverityHigh
	case signal.VerdictError:
		return signal.SeverityCritical
	}
	return signal.SeverityInfo
}
