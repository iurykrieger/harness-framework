package stack

// Stack is the typed view of a stack.json file. Mirrors the schema
// shape one-to-one with JSON tags. Optional fields use pointers or
// nullable slices so absence is distinguishable from zero.
type Stack struct {
	Version    string      `json:"version"`
	DetectedAt string      `json:"detected_at"`
	DetectedBy string      `json:"detected_by"`
	Languages  []Language  `json:"languages"`
	Components []Component `json:"components"`
	LogShapes  []LogShape  `json:"log_shapes"`
}

type Language struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type Component struct {
	Role          Role       `json:"role"`
	Name          string     `json:"name"`
	Version       string     `json:"version,omitempty"`
	ConfigSummary string     `json:"config_summary,omitempty"`
	Evidence      []Evidence `json:"evidence"`
}

type Evidence struct {
	File      string `json:"file"`
	LineStart *int   `json:"line_start,omitempty"`
	LineEnd   *int   `json:"line_end,omitempty"`
	Rationale string `json:"rationale"`
}

type LogShape struct {
	ID             string   `json:"id"`
	ProducedBy     []string `json:"produced_by"`
	Format         Format   `json:"format"`
	Fields         []Field  `json:"fields,omitempty"`
	SeverityValues []string `json:"severity_values,omitempty"`
	Sample         string   `json:"sample"`
}

type Field struct {
	Key           string       `json:"key"`
	Meaning       FieldMeaning `json:"meaning"`
	ExampleValues []string     `json:"example_values,omitempty"`
}

// Role is the enum from $defs/Role in stack.json.
type Role string

const (
	RoleHTTPServer     Role = "http-server"
	RoleHTTPRouter     Role = "http-router"
	RoleHTTPMiddleware Role = "http-middleware"
	RoleLogger         Role = "logger"
	RoleLogEncoder     Role = "log-encoder"
	RoleTracer         Role = "tracer"
	RoleMetrics        Role = "metrics"
	RoleQueueConsumer  Role = "queue-consumer"
	RoleQueueProducer  Role = "queue-producer"
	RoleDBClient       Role = "db-client"
	RoleRPC            Role = "rpc"
	RoleTestRunner     Role = "test-runner"
)

// Format is the enum from $defs/Format.
type Format string

const (
	FormatJSON              Format = "json"
	FormatLogfmt            Format = "logfmt"
	FormatPlain             Format = "plain"
	FormatStackTrace        Format = "stack-trace"
	FormatCombinedLogFormat Format = "combined-log-format"
)

// FieldMeaning is the enum from $defs/FieldMeaning.
type FieldMeaning string

const (
	MeaningSeverity   FieldMeaning = "severity"
	MeaningMessage    FieldMeaning = "message"
	MeaningTimestamp  FieldMeaning = "timestamp"
	MeaningTraceID    FieldMeaning = "trace_id"
	MeaningSpanID     FieldMeaning = "span_id"
	MeaningStatusCode FieldMeaning = "status_code"
	MeaningLatencyMS  FieldMeaning = "latency_ms"
	MeaningMethod     FieldMeaning = "method"
	MeaningPath       FieldMeaning = "path"
	MeaningUserID     FieldMeaning = "user_id"
	MeaningRequestID  FieldMeaning = "request_id"
	MeaningService    FieldMeaning = "service"
	MeaningVersion    FieldMeaning = "version"
	MeaningOther      FieldMeaning = "other"
)

// ShapesByRole returns log_shapes whose produced_by[] intersects the
// names of components with the given role.
func (s Stack) ShapesByRole(role Role) []LogShape {
	names := map[string]struct{}{}
	for _, c := range s.Components {
		if c.Role == role {
			names[c.Name] = struct{}{}
		}
	}
	if len(names) == 0 {
		return nil
	}
	var out []LogShape
	for _, sh := range s.LogShapes {
		for _, n := range sh.ProducedBy {
			if _, ok := names[n]; ok {
				out = append(out, sh)
				break
			}
		}
	}
	return out
}

// ShapesProducedBy returns log_shapes whose produced_by[] contains name.
func (s Stack) ShapesProducedBy(name string) []LogShape {
	var out []LogShape
	for _, sh := range s.LogShapes {
		for _, n := range sh.ProducedBy {
			if n == name {
				out = append(out, sh)
				break
			}
		}
	}
	return out
}

// FieldsByMeaning returns fields whose meaning matches.
func (sh LogShape) FieldsByMeaning(m FieldMeaning) []Field {
	var out []Field
	for _, f := range sh.Fields {
		if f.Meaning == m {
			out = append(out, f)
		}
	}
	return out
}

// HasSeverity returns true when any field has meaning=severity.
func (sh LogShape) HasSeverity() bool {
	return len(sh.FieldsByMeaning(MeaningSeverity)) > 0
}
