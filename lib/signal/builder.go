package signal

import (
	"time"

	"github.com/google/uuid"
)

// Builder constructs the canonical Signal envelope by accumulating fields
// via a fluent API. Build() applies safe defaults (run_id, started/finished
// timestamps, confidence, evidence) so each caller only specifies what
// changes from defaults.
type Builder struct {
	sensorID   string
	version    string
	verdict    string
	severity   string
	kind       string
	rationale  string
	evidence   []interface{}
	latencyMS  int
	metadata   map[string]interface{}
	diagnose   map[string]interface{}
	runID      string
	startedAt  string
	finishedAt string
}

// NewBuilder creates a Builder with sensor id and version pre-bound. When
// version is empty, Build() emits "0.0.0" (compatible with pre-validation
// signals such as discovery_error and bootstrap_failed).
func NewBuilder(sensorID, version string) *Builder {
	return &Builder{sensorID: sensorID, version: version}
}

func (b *Builder) WithVerdict(verdict, severity string) *Builder {
	b.verdict = verdict
	b.severity = severity
	return b
}

func (b *Builder) WithKind(kind string) *Builder {
	b.kind = kind
	return b
}

func (b *Builder) WithRationale(s string) *Builder {
	b.rationale = s
	return b
}

func (b *Builder) WithEvidence(ev []interface{}) *Builder {
	b.evidence = ev
	return b
}

func (b *Builder) WithMetadata(extra map[string]interface{}) *Builder {
	if b.metadata == nil {
		b.metadata = map[string]interface{}{}
	}
	for k, v := range extra {
		b.metadata[k] = v
	}
	return b
}

func (b *Builder) WithDiagnose(diagnose map[string]interface{}) *Builder {
	b.diagnose = diagnose
	return b
}

func (b *Builder) WithLatencyMS(ms int) *Builder {
	b.latencyMS = ms
	return b
}

// WithRunID overrides run_id, started_at, and finished_at. Use when the
// signal belongs to a pre-existing envelope (e.g., an aggregate Signal
// derived from a run already in progress).
func (b *Builder) WithRunID(runID, startedAt, finishedAt string) *Builder {
	b.runID = runID
	b.startedAt = startedAt
	b.finishedAt = finishedAt
	return b
}

// Build emits the final signal as map[string]interface{} ready for
// json.NewEncoder(...).Encode(). It does NOT validate against
// schemas/signal.json — use signal.ValidateOrEmergency for that.
func (b *Builder) Build() map[string]interface{} {
	version := b.version
	if version == "" {
		version = "0.0.0"
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	runID := b.runID
	if runID == "" {
		runID = uuid.NewString()
	}
	startedAt := b.startedAt
	if startedAt == "" {
		startedAt = now
	}
	finishedAt := b.finishedAt
	if finishedAt == "" {
		finishedAt = now
	}

	md := map[string]interface{}{}
	for k, v := range b.metadata {
		md[k] = v
	}
	for k, v := range b.diagnose {
		md[k] = v
	}
	if b.kind != "" {
		md["kind"] = b.kind
	}

	evidence := b.evidence
	if evidence == nil && b.rationale != "" {
		evidence = []interface{}{
			map[string]interface{}{"rationale": b.rationale},
		}
	}

	return map[string]interface{}{
		"sensor_id":   b.sensorID,
		"version":     version,
		"run_id":      runID,
		"started_at":  startedAt,
		"finished_at": finishedAt,
		"verdict":     b.verdict,
		"severity":    b.severity,
		"confidence":  1.0,
		"evidence":    evidence,
		"cost_actual": map[string]interface{}{"latency_ms": b.latencyMS},
		"metadata":    md,
	}
}
