# Streaming Sensors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reframe sensors as streaming subprocesses that emit individual Signals (per-line) and a final aggregate Signal, replace the HTTP-based inferential client with a subprocess-based one, and promote shared helpers to a top-level `lib/` package.

**Architecture:** Each runner spawns `sh -c <command>`, scans merged stdout+stderr line-by-line, matches against `output_parsing.patterns` (declarative regex with optional capture groups), emits each match as a JSONL Signal, and ends with one aggregate Signal whose verdict is the worse of `exit_code_map[exitCode]` and the highest-rank verdict observed in the stream. Inferential runs the same pipeline against an LLM CLI subprocess and applies the calibration downgrade only to the aggregate.

**Tech Stack:** Go 1.25, `github.com/santhosh-tekuri/jsonschema/v5` (Draft 2020-12, cross-file `$ref`), JSON Schema, build tags (`run_computational`, `run-inferential`).

**Spec:** `docs/superpowers/specs/2026-05-06-streaming-sensors-design.md`

---

## Task 1: Migrate pure helpers to top-level `lib/`

This is a pure refactor: move schema/envelope/path/exitcode/template helpers out of `skills/run-sensor/scripts/lib/` and into a top-level `lib/` package, split into one file per responsibility. The legacy `ExecuteComputational` and `ExecuteInferential` (and the HTTP client) are deleted now — Tasks 7 and 8 build new runners that don't need them.

**Files:**
- Create: `lib/schema.go`
- Create: `lib/schema_test.go`
- Create: `lib/envelope.go`
- Create: `lib/envelope_test.go`
- Create: `lib/path.go`
- Create: `lib/path_test.go`
- Create: `lib/exitcode.go`
- Create: `lib/exitcode_test.go`
- Create: `lib/template.go`
- Create: `lib/template_test.go`
- Create: `lib/testhelpers_test.go` (shared test fixtures)
- Delete: `skills/run-sensor/scripts/lib/lib.go`
- Delete: `skills/run-sensor/scripts/lib/lib_test.go`
- Modify (will be rewritten in Tasks 7/8 — for now leave as a stub that compiles): `skills/run-sensor/scripts/run-computational.go`, `skills/run-sensor/scripts/run-inferential.go`

- [ ] **Step 1: Create `lib/schema.go`**

```go
// Package lib holds the harness primitives shared by every script: schema
// validation, sensor envelope construction, path resolution, exit-code
// mapping, template rendering, regex pattern matching, subprocess streaming,
// and aggregate-verdict computation. Scripts under skills/<skill>/scripts/
// import this package; they themselves stay skill-local.
package lib

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

const (
	schemaBaseURL = "https://harness-framework/schemas/"
	sensorURL     = schemaBaseURL + "sensor.json"
	signalURL     = schemaBaseURL + "signal.json"
)

// Target identifies which schema an instance is checked against.
type Target string

const (
	TargetSensor Target = "sensor"
	TargetSignal Target = "signal"
)

// Validator holds the compiled sensor and signal schemas with cross-file
// $ref already resolved.
type Validator struct {
	sensor *jsonschema.Schema
	signal *jsonschema.Schema
}

// NewValidator loads sensor.json and signal.json from schemasDir.
func NewValidator(schemasDir string) (*Validator, error) {
	sensorBytes, err := os.ReadFile(filepath.Join(schemasDir, "sensor.json"))
	if err != nil {
		return nil, fmt.Errorf("read sensor.json: %w", err)
	}
	signalBytes, err := os.ReadFile(filepath.Join(schemasDir, "signal.json"))
	if err != nil {
		return nil, fmt.Errorf("read signal.json: %w", err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource(signalURL, strings.NewReader(string(signalBytes))); err != nil {
		return nil, fmt.Errorf("register signal schema: %w", err)
	}
	if err := c.AddResource(sensorURL, strings.NewReader(string(sensorBytes))); err != nil {
		return nil, fmt.Errorf("register sensor schema: %w", err)
	}
	sensor, err := c.Compile(sensorURL)
	if err != nil {
		return nil, fmt.Errorf("compile sensor schema: %w", err)
	}
	signal, err := c.Compile(signalURL)
	if err != nil {
		return nil, fmt.Errorf("compile signal schema: %w", err)
	}
	return &Validator{sensor: sensor, signal: signal}, nil
}

// Validate runs the schema for target against instance.
func (v *Validator) Validate(target Target, instance interface{}) error {
	switch target {
	case TargetSensor:
		return v.sensor.Validate(instance)
	case TargetSignal:
		return v.signal.Validate(instance)
	default:
		return fmt.Errorf("unknown target %q", target)
	}
}

// FindSchemasDir walks up from start looking for schemas/sensor.json + schemas/signal.json.
func FindSchemasDir(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(abs, "schemas")
		if hasFile(filepath.Join(candidate, "sensor.json")) && hasFile(filepath.Join(candidate, "signal.json")) {
			return candidate, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("schemas directory not found by walking up from %s", start)
		}
		abs = parent
	}
}

func hasFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// PrintValidationError writes an indented rendering of the error tree.
func PrintValidationError(w io.Writer, err *jsonschema.ValidationError, indent string) {
	path := err.InstanceLocation
	if path == "" {
		path = "<root>"
	}
	fmt.Fprintf(w, "%sINVALID at %s: %s\n", indent, path, err.Message)
	for _, c := range err.Causes {
		PrintValidationError(w, c, indent+"  ")
	}
}

// PrintValidationOrPlain prints an indented validation tree if err is a
// jsonschema.ValidationError; otherwise it prints err.Error().
func PrintValidationOrPlain(err error, stderr io.Writer) {
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		PrintValidationError(stderr, ve, "")
	} else {
		fmt.Fprintln(stderr, "INVALID:", err)
	}
}

// LoadValidator resolves schemasDir (walks up if empty) and returns a Validator.
// Returns (nil, exit code) on failure, with the message already printed to stderr.
func LoadValidator(schemasDir string, stderr io.Writer) (*Validator, int) {
	if schemasDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(stderr, "error: getwd:", err)
			return nil, 2
		}
		d, err := FindSchemasDir(cwd)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return nil, 2
		}
		schemasDir = d
	}
	v, err := NewValidator(schemasDir)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return nil, 2
	}
	return v, 0
}
```

- [ ] **Step 2: Create `lib/envelope.go`**

```go
package lib

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// NowFn and NewRunIDFn are package-level overrideable hooks so tests can pin
// timestamps and run ids.
var (
	NowFn      = func() time.Time { return time.Now().UTC() }
	NewRunIDFn = NewUUIDv4
)

// Envelope is the run-scoped Signal scaffold reused across individuals and
// the aggregate within a single sensor invocation.
type Envelope struct {
	SensorID   string `json:"sensor_id"`
	Version    string `json:"version"`
	RunID      string `json:"run_id"`
	StartedAt  string `json:"started_at"`
	SensorType string `json:"sensor_type"`
}

// BuildEnvelope constructs an envelope from a parsed sensor JSON.
func BuildEnvelope(sensor map[string]interface{}) (Envelope, error) {
	id, _ := sensor["id"].(string)
	version, _ := sensor["version"].(string)
	sensorType, _ := sensor["type"].(string)
	if id == "" || version == "" || sensorType == "" {
		return Envelope{}, errors.New("sensor missing id/version/type")
	}
	return Envelope{
		SensorID:   id,
		Version:    version,
		RunID:      NewRunIDFn(),
		StartedAt:  NowFn().Format("2006-01-02T15:04:05Z"),
		SensorType: sensorType,
	}, nil
}

// NewUUIDv4 generates a RFC 4122 v4 UUID without external dependencies.
func NewUUIDv4() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
```

- [ ] **Step 3: Create `lib/path.go`**

```go
package lib

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ResolveSensorPath strips a leading @, makes the path absolute (relative to
// baseDir), and verifies the file exists.
func ResolveSensorPath(arg, baseDir string) (string, error) {
	arg = strings.TrimPrefix(arg, "@")
	if arg == "" {
		return "", errors.New("empty path")
	}
	if !filepath.IsAbs(arg) {
		arg = filepath.Join(baseDir, arg)
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// MultiFlag implements flag.Value for repeatable string flags (--slot k=v --slot k2=v2).
type MultiFlag []string

func (m *MultiFlag) String() string     { return strings.Join(*m, ",") }
func (m *MultiFlag) Set(s string) error { *m = append(*m, s); return nil }

// LoadAndValidateSensor resolves the path argument, reads, parses, and
// schema-validates the sensor JSON against the sensor schema. Returns sensor,
// abs path, exit code (0 on success).
func LoadAndValidateSensor(arg, schemasDir string, stderr io.Writer) (map[string]interface{}, string, int) {
	cwd, _ := os.Getwd()
	sensorPath, err := ResolveSensorPath(arg, cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error: resolve:", err)
		return nil, "", 2
	}
	v, code := LoadValidator(schemasDir, stderr)
	if code != 0 {
		return nil, "", code
	}
	var sensor map[string]interface{}
	if code := readJSONFile(sensorPath, &sensor, stderr); code != 0 {
		return nil, "", code
	}
	if err := v.Validate(TargetSensor, sensor); err != nil {
		PrintValidationOrPlain(err, stderr)
		return nil, "", 1
	}
	return sensor, sensorPath, 0
}

func readJSONFile(path string, dst interface{}, stderr io.Writer) int {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, "error: read:", err)
		return 2
	}
	if err := json.Unmarshal(b, dst); err != nil {
		fmt.Fprintln(stderr, "error: parse:", err)
		return 2
	}
	return 0
}
```

- [ ] **Step 4: Create `lib/exitcode.go`**

```go
package lib

// MapExitCode resolves an exit code via sensor.execution.exit_code_map.
// "*" is the wildcard fallback. Returns ("error", "high") if no entry matches
// and no wildcard is present.
func MapExitCode(code int, ecMap []interface{}) (verdict, severity string) {
	var fallbackV, fallbackS string
	haveFallback := false
	for _, item := range ecMap {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		switch ec := m["exit_code"].(type) {
		case float64:
			if int(ec) == code {
				v, _ := m["verdict"].(string)
				s, _ := m["severity"].(string)
				return v, s
			}
		case string:
			if ec == "*" {
				fallbackV, _ = m["verdict"].(string)
				fallbackS, _ = m["severity"].(string)
				haveFallback = true
			}
		}
	}
	if haveFallback {
		return fallbackV, fallbackS
	}
	return "error", "high"
}
```

- [ ] **Step 5: Create `lib/template.go`**

```go
package lib

import "regexp"

// slotPattern matches {{slot_name}} (whitespace tolerated).
var slotPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

// RenderTemplate substitutes {{slot}} placeholders. Returns the rendered text
// and the deduplicated list of slots that were referenced but not bound.
func RenderTemplate(tmpl string, bindings map[string]string) (string, []string) {
	var missing []string
	seen := map[string]bool{}
	rendered := slotPattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		key := slotPattern.FindStringSubmatch(match)[1]
		if val, ok := bindings[key]; ok {
			return val
		}
		if !seen[key] {
			seen[key] = true
			missing = append(missing, key)
		}
		return match
	})
	return rendered, missing
}
```

- [ ] **Step 6: Create `lib/testhelpers_test.go` (shared fixtures + clock freeze)**

```go
package lib

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// repoSchemasDir returns the absolute path to schemas/ in the repo root,
// resolved from this test file's own location (independent of cwd).
func repoSchemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// .../lib/testhelpers_test.go → 1 level up to repo root.
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), ".."))
	dir := filepath.Join(repoRoot, "schemas")
	if _, err := os.Stat(filepath.Join(dir, "sensor.json")); err != nil {
		t.Fatalf("schemas dir not where expected (%s): %v", dir, err)
	}
	return dir
}

// freezeClock pins NowFn and NewRunIDFn for deterministic Signal output.
// Returns a restore function; defer it.
func freezeClock(t *testing.T) func() {
	t.Helper()
	origNow, origID := NowFn, NewRunIDFn
	frozen := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	NowFn = func() time.Time { return frozen }
	NewRunIDFn = func() string { return "00000000-0000-4000-8000-000000000000" }
	return func() { NowFn, NewRunIDFn = origNow, origID }
}

// validSensorComputational returns a minimal sensor that passes the schema.
func validSensorComputational() map[string]interface{} {
	return map[string]interface{}{
		"id": "smoke-comp", "version": "0.1.0",
		"name": "smoke", "description": "fixture",
		"type": "computational", "regulation": "maintainability",
		"phase": "on-demand", "determinism": "high",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 100, "timeout_ms": 5000},
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 64},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command": "true",
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
				map[string]interface{}{"exit_code": 1, "verdict": "fail", "severity": "high"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "x", "expected_verdict": "pass", "expected_severity": "info"}},
		},
	}
}

// validSensorInferential returns a minimal inferential sensor that passes
// the post-Task-3 schema (command + LLM-CLI fields).
func validSensorInferential() map[string]interface{} {
	return map[string]interface{}{
		"id": "smoke-inf", "version": "0.1.0",
		"name": "smoke inf", "description": "fixture",
		"type": "inferential", "regulation": "maintainability",
		"phase": "post-integration", "determinism": "low",
		"cost": map[string]interface{}{
			"class":   "expensive",
			"latency": map[string]interface{}{"p50_ms": 6000, "p95_ms": 15000, "timeout_ms": 60000},
			"tokens":  map[string]interface{}{"model": "anthropic/claude-sonnet-4-6", "input_avg": 4000, "output_avg": 400, "max_output": 1024},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "pull-request"}},
		"execution": map[string]interface{}{
			"command":              "claude -p {{prompt}}",
			"model":                "anthropic/claude-sonnet-4-6",
			"system_prompt":        "You are a similarity judge. Output JSON only.",
			"user_prompt_template": "Compare {{a}} to {{b}}.",
			"decoding":             map[string]interface{}{"temperature": 0.0, "max_tokens": 1024},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "f", "expected_verdict": "warn", "expected_severity": "medium"}},
		},
		"calibration": map[string]interface{}{
			"confidence_threshold": 0.7,
			"calibration_set":      "tests/cal.jsonl",
			"calibration_size":     120,
			"calibration_date":     "2026-04-15",
		},
	}
}

// writeTempJSON writes v as JSON to a temp file and returns its path.
func writeTempJSON(t *testing.T, v interface{}) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.json")
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
```

- [ ] **Step 7: Create per-file test files (split from the old `lib_test.go`)**

Each of these is the corresponding subset of the existing `skills/run-sensor/scripts/lib/lib_test.go`. Copy verbatim, dropping any reference to `ExecuteComputational`/`ExecuteInferential`/`startMockAnthropic` (those tests are deleted; Tasks 7 and 8 add new ones).

`lib/path_test.go`: only `TestResolveSensorPath`.

`lib/envelope_test.go`: `TestBuildEnvelope`, `TestBuildEnvelope_MissingFields`.

`lib/exitcode_test.go`: `TestMapExitCode`.

`lib/template_test.go`: `TestRenderTemplate`.

`lib/schema_test.go`: `TestFindSchemasDir`, `TestFindSchemasDir_Missing`, `TestValidator_AcceptsValidSensors`, `TestValidator_RejectsMutations`, plus the local helper `flattenValidationError` (which only `TestValidator_RejectsMutations` uses).

The `repoSchemasDir`/`freezeClock`/`validSensor*`/`writeTempJSON` helpers come from `testhelpers_test.go` — DO NOT redefine them here.

`flattenValidationError` (lives in `lib/schema_test.go`):

```go
import (
	"errors"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func flattenValidationError(err error) string {
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		var b strings.Builder
		PrintValidationError(&b, ve, "")
		return b.String()
	}
	return err.Error()
}
```

- [ ] **Step 8: Stub the runners so the package still compiles**

Replace `skills/run-sensor/scripts/run-computational.go` with a placeholder that matches the old CLI shape but exits with `1` and a "not yet implemented under streaming model" message. Same for `run-inferential.go`. Tasks 7 and 8 replace them.

`skills/run-sensor/scripts/run-computational.go`:

```go
//go:build run_computational

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "run-computational: not yet implemented under streaming model (see Task 7)")
	os.Exit(1)
}
```

`skills/run-sensor/scripts/run-inferential.go`:

```go
//go:build run_inferential

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "run-inferential: not yet implemented under streaming model (see Task 8)")
	os.Exit(1)
}
```

Also delete the existing `run-computational_test.go` and `run-inferential_test.go` — Tasks 7 and 8 introduce new tests against the new design.

- [ ] **Step 9: Delete the old `lib`**

```bash
rm -rf skills/run-sensor/scripts/lib
rm skills/run-sensor/scripts/run-computational_test.go
rm skills/run-sensor/scripts/run-inferential_test.go
```

- [ ] **Step 10: Run all tests and `go vet`**

```bash
go test ./lib/...
go vet -tags=run_computational ./...
go vet -tags=run_inferential ./...
go build -tags=run_computational ./skills/run-sensor/scripts
go build -tags=run_inferential ./skills/run-sensor/scripts
```

Expected: `ok github.com/iurykrieger/harness-framework/lib`. Vet and builds clean.

- [ ] **Step 11: Commit**

```bash
git add lib schemas skills go.mod go.sum CLAUDE.md
git commit -m "refactor: promote lib to top-level, drop HTTP inferential runner

- Split skills/run-sensor/scripts/lib/lib.go into lib/{schema,envelope,path,exitcode,template}.go.
- Drop ExecuteComputational/ExecuteInferential and the Anthropic HTTP client
  ahead of the streaming rewrite.
- Stub the two runners so the build stays green; Tasks 7/8 reintroduce them."
```

---

## Task 2: Add `output_parsing` to `schemas/sensor.json`

Adds a structured `execution.output_parsing` object with `patterns[]`. Each pattern has `regex`, `verdict` (via `signal.json#/$defs/Verdict`), `severity` (via `Severity`), and an optional `captures` map (capture-group index per evidence field).

**Files:**
- Modify: `schemas/sensor.json`
- Create: `lib/schema_output_parsing_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/schema_output_parsing_test.go`:

```go
package lib

import (
	"strings"
	"testing"
)

func TestValidator_AcceptsOutputParsing(t *testing.T) {
	v, err := NewValidator(repoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := validSensorComputational()
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{
			map[string]interface{}{
				"regex":    `^FAIL\s+(\S+)`,
				"verdict":  "fail",
				"severity": "high",
				"captures": map[string]interface{}{"file": 1},
			},
			map[string]interface{}{
				"regex":    `^PASS\s+(\S+)`,
				"verdict":  "pass",
				"severity": "info",
			},
		},
	}
	if err := v.Validate(TargetSensor, s); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidator_RejectsEmptyPatterns(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorComputational()
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{}, // empty — must be rejected
	}
	err := v.Validate(TargetSensor, s)
	if err == nil {
		t.Fatal("expected error for empty patterns")
	}
}

func TestValidator_RejectsBadVerdictInPattern(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorComputational()
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{
			map[string]interface{}{"regex": "x", "verdict": "broken", "severity": "info"},
		},
	}
	err := v.Validate(TargetSensor, s)
	if err == nil || !strings.Contains(flattenValidationError(err), "$defs/Verdict") {
		t.Fatalf("expected Verdict ref violation, got %v", err)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./lib/ -run 'TestValidator_(AcceptsOutputParsing|RejectsEmptyPatterns|RejectsBadVerdictInPattern)' -v
```

Expected: All three FAIL because the schema does not yet define `output_parsing` (`additionalProperties: false` on `execution` rejects unknown keys).

- [ ] **Step 3: Update `schemas/sensor.json`**

In `schemas/sensor.json`, inside `properties.execution.properties`, REPLACE the existing `output_parsing` line:

```json
"output_parsing": { "type": "string", "description": "Computational only. How stdout/stderr is parsed into Signal.evidence and Signal.remediation." },
```

with:

```json
"output_parsing": {
  "type": "object",
  "additionalProperties": false,
  "required": ["patterns"],
  "description": "Structured rules for turning subprocess output lines into individual Signals. Applies to both computational and inferential sensors. Optional — when absent, only the aggregate Signal is emitted.",
  "properties": {
    "patterns": {
      "type": "array",
      "minItems": 1,
      "description": "Ordered match rules. First pattern that matches a line wins.",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["regex", "verdict", "severity"],
        "properties": {
          "regex":    { "type": "string", "description": "Go regexp (RE2)." },
          "verdict":  { "$ref": "signal.json#/$defs/Verdict" },
          "severity": { "$ref": "signal.json#/$defs/Severity" },
          "captures": {
            "type": "object",
            "additionalProperties": false,
            "description": "Map a Signal.evidence field to a 1-based capture-group index in regex.",
            "properties": {
              "file":       { "type": "integer", "minimum": 1 },
              "line_start": { "type": "integer", "minimum": 1 },
              "line_end":   { "type": "integer", "minimum": 1 },
              "excerpt":    { "type": "integer", "minimum": 1 },
              "rationale":  { "type": "integer", "minimum": 1 }
            }
          }
        }
      }
    }
  }
}
```

- [ ] **Step 4: Run tests, verify they pass**

```bash
go test ./lib/ -run 'TestValidator_(AcceptsOutputParsing|RejectsEmptyPatterns|RejectsBadVerdictInPattern)' -v
go test ./lib/...
```

Expected: All three PASS, full lib suite still passes.

- [ ] **Step 5: Commit**

```bash
git add schemas/sensor.json lib/schema_output_parsing_test.go
git commit -m "feat(schema): add structured output_parsing.patterns to sensor.json

Each pattern declares regex, verdict, severity, and an optional capture-group
map for evidence fields. Empty patterns rejected; verdict/severity validated
against the same enums signal.json defines."
```

---

## Task 3: Make `command` mandatory both branches; relax `exit_code_map` for inferential

**Files:**
- Modify: `schemas/sensor.json`
- Create: `lib/schema_command_required_test.go`

- [ ] **Step 1: Write the failing tests**

Create `lib/schema_command_required_test.go`:

```go
package lib

import "testing"

func TestValidator_InferentialRequiresCommand(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorInferential()
	delete(s["execution"].(map[string]interface{}), "command")
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected inferential without command to fail")
	}
}

func TestValidator_ComputationalRequiresCommand(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorComputational()
	delete(s["execution"].(map[string]interface{}), "command")
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected computational without command to fail")
	}
}

func TestValidator_InferentialAllowsMissingExitCodeMap(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorInferential()
	// Inferential sensors typically don't declare exit_code_map.
	if _, has := s["execution"].(map[string]interface{})["exit_code_map"]; has {
		t.Fatal("fixture should not have exit_code_map")
	}
	if err := v.Validate(TargetSensor, s); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidator_ComputationalForbidsLLMFields(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorComputational()
	s["execution"].(map[string]interface{})["model"] = "anthropic/claude-sonnet-4-6"
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected computational with model to fail")
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

```bash
go test ./lib/ -run TestValidator_ -v
```

Expected: `TestValidator_InferentialRequiresCommand`, `TestValidator_ComputationalRequiresCommand`, `TestValidator_InferentialAllowsMissingExitCodeMap`, `TestValidator_ComputationalForbidsLLMFields` all FAIL — current schema has `command` in the computational `then` only, and forbids `command` for inferential.

- [ ] **Step 3: Update `schemas/sensor.json` `allOf`**

REPLACE the entire `allOf` block (currently lines ~291–337) with:

```json
"allOf": [
  {
    "if":   { "properties": { "type": { "const": "computational" } }, "required": ["type"] },
    "then": {
      "properties": {
        "cost": {
          "required": ["compute"],
          "not": { "required": ["tokens"] }
        },
        "execution": {
          "required": ["command", "exit_code_map"],
          "not": {
            "anyOf": [
              { "required": ["model"] },
              { "required": ["system_prompt"] },
              { "required": ["user_prompt_template"] },
              { "required": ["decoding"] }
            ]
          }
        }
      }
    }
  },
  {
    "if":   { "properties": { "type": { "const": "inferential" } }, "required": ["type"] },
    "then": {
      "required": ["calibration"],
      "properties": {
        "cost": {
          "required": ["tokens"],
          "not": { "required": ["compute"] }
        },
        "execution": {
          "required": ["command", "model", "system_prompt", "user_prompt_template", "decoding"]
        }
      }
    }
  }
]
```

The two changes are: `command` is now in the `required` list for the inferential branch, and the inferential `then` no longer forbids `command`/`env`/`exit_code_map`/`output_parsing` (the old `not.anyOf` block is removed). Inferential sensors may declare `exit_code_map` if they want — but they're not forced to.

- [ ] **Step 4: Run tests, verify they pass**

```bash
go test ./lib/...
```

Expected: All schema tests pass.

- [ ] **Step 5: Commit**

```bash
git add schemas/sensor.json lib/schema_command_required_test.go
git commit -m "feat(schema): make command mandatory for both sensor types

Inferential sensors now run as subprocesses too (an LLM CLI), so command
is required across both branches. exit_code_map stays mandatory for
computational and becomes optional for inferential, where the verdict
typically comes from the streamed output."
```

---

## Task 4: Implement `lib/patterns.go`

Compile `output_parsing.patterns[]` into Go regexp objects, match a line, and extract capture groups into a structured `PatternMatch`.

**Files:**
- Create: `lib/patterns.go`
- Create: `lib/patterns_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/patterns_test.go`:

```go
package lib

import (
	"reflect"
	"strings"
	"testing"
)

func TestCompilePatterns_HappyPath(t *testing.T) {
	raw := []interface{}{
		map[string]interface{}{
			"regex":    `^FAIL\s+(\S+)`,
			"verdict":  "fail",
			"severity": "high",
			"captures": map[string]interface{}{"file": float64(1)},
		},
	}
	pats, err := CompilePatterns(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(pats) != 1 || pats[0].Verdict != "fail" || pats[0].Severity != "high" {
		t.Fatalf("unexpected: %+v", pats)
	}
	if pats[0].Captures["file"] != 1 {
		t.Fatalf("capture index: %v", pats[0].Captures)
	}
}

func TestCompilePatterns_BadRegex(t *testing.T) {
	raw := []interface{}{
		map[string]interface{}{"regex": "([unclosed", "verdict": "fail", "severity": "high"},
	}
	if _, err := CompilePatterns(raw); err == nil || !strings.Contains(err.Error(), "regex") {
		t.Fatalf("expected regex error, got %v", err)
	}
}

func TestMatchLine_FirstMatchWins(t *testing.T) {
	pats, _ := CompilePatterns([]interface{}{
		map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
		map[string]interface{}{"regex": "^FAIL", "verdict": "warn", "severity": "low"},
	})
	m, ok := MatchLine("FAIL TestFoo", pats)
	if !ok || m.Verdict != "fail" {
		t.Fatalf("expected first pattern to win, got %+v", m)
	}
}

func TestMatchLine_NoMatch(t *testing.T) {
	pats, _ := CompilePatterns([]interface{}{
		map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
	})
	if _, ok := MatchLine("hello world", pats); ok {
		t.Fatal("expected no match")
	}
}

func TestMatchLine_CaptureExtraction(t *testing.T) {
	pats, _ := CompilePatterns([]interface{}{
		map[string]interface{}{
			"regex":    `^(\S+):(\d+):(\d+)\s+error\s+(.+)$`,
			"verdict":  "fail",
			"severity": "high",
			"captures": map[string]interface{}{
				"file":       float64(1),
				"line_start": float64(2),
				"rationale":  float64(4),
			},
		},
	})
	m, ok := MatchLine("src/foo.ts:10:5 error 'x' is unused", pats)
	if !ok {
		t.Fatal("expected match")
	}
	if m.File != "src/foo.ts" {
		t.Fatalf("file=%q", m.File)
	}
	if m.LineStart == nil || *m.LineStart != 10 {
		t.Fatalf("line_start=%v", m.LineStart)
	}
	if m.Rationale != "'x' is unused" {
		t.Fatalf("rationale=%q", m.Rationale)
	}
}

func TestMatchLine_RationaleFallsBackToLine(t *testing.T) {
	pats, _ := CompilePatterns([]interface{}{
		map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
	})
	m, _ := MatchLine("FAIL TestFoo", pats)
	if m.Rationale != "FAIL TestFoo" {
		t.Fatalf("expected fallback to full line, got %q", m.Rationale)
	}
}

func TestMatchLine_LineFieldAlwaysSet(t *testing.T) {
	pats, _ := CompilePatterns([]interface{}{
		map[string]interface{}{"regex": ".+", "verdict": "pass", "severity": "info"},
	})
	m, _ := MatchLine("anything", pats)
	if m.Line != "anything" {
		t.Fatalf("Line not preserved: %q", m.Line)
	}
}

func TestCompilePatterns_AcceptsIntCaptureIndex(t *testing.T) {
	// JSON unmarshalled with json.Unmarshal puts numbers into float64; some
	// callers may construct test fixtures with int. Accept both.
	raw := []interface{}{
		map[string]interface{}{
			"regex":    "^x",
			"verdict":  "pass",
			"severity": "info",
			"captures": map[string]interface{}{"file": 1},
		},
	}
	pats, err := CompilePatterns(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pats[0].Captures, map[string]int{"file": 1}) {
		t.Fatalf("captures: %v", pats[0].Captures)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./lib/ -run 'TestCompilePatterns|TestMatchLine' -v
```

Expected: All FAIL with "undefined: CompilePatterns" / "undefined: MatchLine".

- [ ] **Step 3: Implement `lib/patterns.go`**

```go
package lib

import (
	"fmt"
	"regexp"
	"strconv"
)

// Pattern is a compiled output_parsing.patterns[] entry.
type Pattern struct {
	Regex    *regexp.Regexp
	Verdict  string
	Severity string
	Captures map[string]int // evidence field name → 1-based capture-group index
}

// CompilePatterns turns a raw output_parsing.patterns array (as parsed from
// JSON: []interface{} of map[string]interface{}) into compiled Patterns.
func CompilePatterns(raw []interface{}) ([]Pattern, error) {
	out := make([]Pattern, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("pattern[%d]: not an object", i)
		}
		rawRegex, _ := m["regex"].(string)
		re, err := regexp.Compile(rawRegex)
		if err != nil {
			return nil, fmt.Errorf("pattern[%d] regex: %w", i, err)
		}
		verdict, _ := m["verdict"].(string)
		severity, _ := m["severity"].(string)
		captures := map[string]int{}
		if cap, ok := m["captures"].(map[string]interface{}); ok {
			for k, v := range cap {
				switch n := v.(type) {
				case float64:
					captures[k] = int(n)
				case int:
					captures[k] = n
				}
			}
		}
		out = append(out, Pattern{Regex: re, Verdict: verdict, Severity: severity, Captures: captures})
	}
	return out, nil
}

// PatternMatch is the result of a successful pattern match against a line.
type PatternMatch struct {
	Verdict   string
	Severity  string
	File      string
	LineStart *int // pointer so omission is distinguishable from zero
	LineEnd   *int
	Excerpt   string
	Rationale string
	Line      string // raw matched line
}

// MatchLine walks patterns in order; first match wins. Returns ok=false if no
// pattern matched. When a match has no `rationale` capture, Rationale falls
// back to the whole line.
func MatchLine(line string, patterns []Pattern) (PatternMatch, bool) {
	for _, p := range patterns {
		m := p.Regex.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		match := PatternMatch{
			Verdict:  p.Verdict,
			Severity: p.Severity,
			Line:     line,
		}
		if idx, ok := p.Captures["file"]; ok && idx < len(m) {
			match.File = m[idx]
		}
		if idx, ok := p.Captures["excerpt"]; ok && idx < len(m) {
			match.Excerpt = m[idx]
		}
		if idx, ok := p.Captures["rationale"]; ok && idx < len(m) {
			match.Rationale = m[idx]
		} else {
			match.Rationale = line
		}
		if idx, ok := p.Captures["line_start"]; ok && idx < len(m) {
			if n, err := strconv.Atoi(m[idx]); err == nil {
				match.LineStart = &n
			}
		}
		if idx, ok := p.Captures["line_end"]; ok && idx < len(m) {
			if n, err := strconv.Atoi(m[idx]); err == nil {
				match.LineEnd = &n
			}
		}
		return match, true
	}
	return PatternMatch{}, false
}
```

- [ ] **Step 4: Run tests, verify they pass**

```bash
go test ./lib/ -run 'TestCompilePatterns|TestMatchLine' -v
```

Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/patterns.go lib/patterns_test.go
git commit -m "feat(lib): add Pattern compile + MatchLine with capture extraction"
```

---

## Task 5: Implement `lib/aggregate.go`

Compute the aggregate verdict via worst-of-two between the exit-code mapping and the highest-rank verdict observed in the stream. Add a deterministic helper to pick top-N evidence items.

**Files:**
- Create: `lib/aggregate.go`
- Create: `lib/aggregate_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/aggregate_test.go`:

```go
package lib

import "testing"

func TestAggregate_WorstOfTwo(t *testing.T) {
	cases := []struct {
		name        string
		exitVerd    string
		exitSev     string
		streamVerd  string
		streamSev   string
		wantVerd    string
		wantSev     string
	}{
		{"both pass", "pass", "info", "pass", "info", "pass", "info"},
		{"exit pass, stream fail", "pass", "info", "fail", "high", "fail", "high"},
		{"exit fail, stream pass", "fail", "high", "pass", "info", "fail", "high"},
		{"exit warn, stream fail", "warn", "low", "fail", "high", "fail", "high"},
		{"exit error, stream pass", "error", "high", "pass", "info", "error", "high"},
		{"both fail", "fail", "medium", "fail", "high", "fail", "medium"}, // ties → exit side
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Aggregate(AggregateInput{
				ExitVerdict: c.exitVerd, ExitSeverity: c.exitSev,
				StreamVerdict: c.streamVerd, StreamSeverity: c.streamSev,
			})
			if r.Verdict != c.wantVerd || r.Severity != c.wantSev {
				t.Errorf("got %s/%s, want %s/%s", r.Verdict, r.Severity, c.wantVerd, c.wantSev)
			}
		})
	}
}

func TestAggregate_TimeoutForcesError(t *testing.T) {
	r := Aggregate(AggregateInput{
		ExitVerdict: "pass", ExitSeverity: "info",
		StreamVerdict: "pass", StreamSeverity: "info",
		TimedOut: true,
	})
	if r.Verdict != "error" || r.Severity != "high" {
		t.Fatalf("got %+v", r)
	}
}

func TestMaxStreamVerdict_Empty(t *testing.T) {
	v, s := MaxStreamVerdict(nil)
	if v != "pass" || s != "info" {
		t.Fatalf("got %s/%s", v, s)
	}
}

func TestMaxStreamVerdict_Mixed(t *testing.T) {
	individuals := []map[string]interface{}{
		{"verdict": "pass", "severity": "info"},
		{"verdict": "warn", "severity": "low"},
		{"verdict": "fail", "severity": "medium"},
		{"verdict": "warn", "severity": "low"},
	}
	v, s := MaxStreamVerdict(individuals)
	if v != "fail" || s != "medium" {
		t.Fatalf("got %s/%s", v, s)
	}
}

func TestSelectTopEvidence_PrefersWorseVerdict(t *testing.T) {
	individuals := []map[string]interface{}{
		{"verdict": "pass", "severity": "info", "evidence": []interface{}{
			map[string]interface{}{"rationale": "ok 1"},
		}},
		{"verdict": "fail", "severity": "high", "evidence": []interface{}{
			map[string]interface{}{"rationale": "bad 1"},
		}},
		{"verdict": "warn", "severity": "low", "evidence": []interface{}{
			map[string]interface{}{"rationale": "warn 1"},
		}},
	}
	ev := SelectTopEvidence(individuals, 2)
	if len(ev) != 2 {
		t.Fatalf("len=%d", len(ev))
	}
	first := ev[0].(map[string]interface{})["rationale"].(string)
	if first != "bad 1" {
		t.Fatalf("expected fail evidence first, got %q", first)
	}
}

func TestCountVerdicts(t *testing.T) {
	individuals := []map[string]interface{}{
		{"verdict": "pass"}, {"verdict": "pass"}, {"verdict": "fail"}, {"verdict": "warn"},
	}
	got := CountVerdicts(individuals)
	want := map[string]int{"pass": 2, "warn": 1, "fail": 1, "error": 0}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %d want %d", k, got[k], v)
		}
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./lib/ -run 'TestAggregate|TestMaxStreamVerdict|TestSelectTopEvidence|TestCountVerdicts' -v
```

Expected: All FAIL with undefined symbols.

- [ ] **Step 3: Implement `lib/aggregate.go`**

```go
package lib

import "sort"

// VerdictRank gives an ordinal for verdicts: pass < warn < fail < error.
var VerdictRank = map[string]int{
	"pass":  0,
	"warn":  1,
	"fail":  2,
	"error": 3,
}

// AggregateInput collects everything needed to compute the aggregate verdict.
type AggregateInput struct {
	ExitVerdict    string // from MapExitCode
	ExitSeverity   string
	StreamVerdict  string // from MaxStreamVerdict over individuals
	StreamSeverity string
	TimedOut       bool
}

// AggregateResult is what goes into the aggregate Signal.
type AggregateResult struct {
	Verdict  string
	Severity string
}

// Aggregate applies the worst-of-two rule. Timeout always forces verdict=error
// regardless of the inputs (the run is incomplete; trust nothing else).
func Aggregate(in AggregateInput) AggregateResult {
	if in.TimedOut {
		return AggregateResult{Verdict: "error", Severity: "high"}
	}
	if VerdictRank[in.StreamVerdict] > VerdictRank[in.ExitVerdict] {
		return AggregateResult{Verdict: in.StreamVerdict, Severity: in.StreamSeverity}
	}
	return AggregateResult{Verdict: in.ExitVerdict, Severity: in.ExitSeverity}
}

// MaxStreamVerdict scans individuals and returns the highest-rank verdict and
// the severity of the first individual that hit that rank. Empty list → ("pass","info").
func MaxStreamVerdict(individuals []map[string]interface{}) (string, string) {
	best := "pass"
	bestSev := "info"
	bestRank := 0
	for _, s := range individuals {
		v, _ := s["verdict"].(string)
		if VerdictRank[v] > bestRank {
			bestRank = VerdictRank[v]
			best = v
			bestSev, _ = s["severity"].(string)
		}
	}
	return best, bestSev
}

// SelectTopEvidence returns up to n evidence items, prioritising the
// most-severe individuals. Stable order: verdict rank desc, then original order.
func SelectTopEvidence(individuals []map[string]interface{}, n int) []interface{} {
	type tagged struct {
		idx  int
		rank int
		s    map[string]interface{}
	}
	tagged_ := make([]tagged, len(individuals))
	for i, s := range individuals {
		v, _ := s["verdict"].(string)
		tagged_[i] = tagged{i, VerdictRank[v], s}
	}
	sort.SliceStable(tagged_, func(i, j int) bool {
		if tagged_[i].rank != tagged_[j].rank {
			return tagged_[i].rank > tagged_[j].rank
		}
		return tagged_[i].idx < tagged_[j].idx
	})
	out := []interface{}{}
	for _, t := range tagged_ {
		if len(out) >= n {
			break
		}
		ev, _ := t.s["evidence"].([]interface{})
		for _, item := range ev {
			if len(out) >= n {
				break
			}
			out = append(out, item)
		}
	}
	return out
}

// CountVerdicts returns a 4-key histogram (pass/warn/fail/error) over individuals.
// Keys missing from the input are present with value 0.
func CountVerdicts(individuals []map[string]interface{}) map[string]int {
	counts := map[string]int{"pass": 0, "warn": 0, "fail": 0, "error": 0}
	for _, s := range individuals {
		v, _ := s["verdict"].(string)
		if _, ok := counts[v]; ok {
			counts[v]++
		}
	}
	return counts
}
```

- [ ] **Step 4: Run tests, verify they pass**

```bash
go test ./lib/ -run 'TestAggregate|TestMaxStreamVerdict|TestSelectTopEvidence|TestCountVerdicts' -v
```

Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/aggregate.go lib/aggregate_test.go
git commit -m "feat(lib): add Aggregate worst-of-two + verdict counts + top-N evidence"
```

---

## Task 6: Implement `lib/stream.go` (`StreamSubprocess`)

Spawn `sh -c <command>` with timeout, scan merged stdout+stderr line-by-line, emit per-match individual Signals as JSONL on `Stdout`, return the bookkeeping the caller needs to build the aggregate.

**Files:**
- Create: `lib/stream.go`
- Create: `lib/stream_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/stream_test.go`:

```go
package lib

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func mustCompilePatterns(t *testing.T, raw []interface{}) []Pattern {
	t.Helper()
	pats, err := CompilePatterns(raw)
	if err != nil {
		t.Fatal(err)
	}
	return pats
}

func envelopeFor(t *testing.T) Envelope {
	t.Helper()
	return Envelope{
		SensorID: "smoke", Version: "0.1.0",
		RunID: "00000000-0000-4000-8000-000000000000",
		StartedAt:  "2026-05-06T12:00:00Z",
		SensorType: "computational",
	}
}

func decodeJSONL(t *testing.T, s string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestStreamSubprocess_EmitsJSONLPerMatch(t *testing.T) {
	defer freezeClock(t)()
	v, err := NewValidator(repoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	patterns := mustCompilePatterns(t, []interface{}{
		map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
		map[string]interface{}{"regex": "^PASS", "verdict": "pass", "severity": "info"},
	})

	var stdout, stderr bytes.Buffer
	res, err := StreamSubprocess(context.Background(), StreamConfig{
		Command:   `printf 'PASS a\nFAIL b\nignored line\n'; exit 1`,
		Patterns:  patterns,
		Envelope:  envelopeFor(t),
		Validator: v,
		Stdout:    &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("stream: %v stderr=%s", err, stderr.String())
	}
	if res.ExitCode != 1 {
		t.Fatalf("exit=%d", res.ExitCode)
	}
	if res.TimedOut {
		t.Fatal("unexpected timeout")
	}
	lines := decodeJSONL(t, stdout.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 individuals (PASS + FAIL), got %d: %s", len(lines), stdout.String())
	}
	if lines[0]["verdict"] != "pass" || lines[1]["verdict"] != "fail" {
		t.Fatalf("verdicts: %v / %v", lines[0]["verdict"], lines[1]["verdict"])
	}
	md := lines[0]["metadata"].(map[string]interface{})
	if md["kind"] != "individual" || md["line"] != "PASS a" {
		t.Fatalf("metadata: %v", md)
	}
}

func TestStreamSubprocess_ShellFeatures(t *testing.T) {
	defer freezeClock(t)()
	v, _ := NewValidator(repoSchemasDir(t))
	patterns := mustCompilePatterns(t, []interface{}{
		map[string]interface{}{"regex": "^WARN", "verdict": "warn", "severity": "low"},
	})
	var stdout, stderr bytes.Buffer
	// pipe + 2>&1 + glob: things strings.Fields would mangle
	res, _ := StreamSubprocess(context.Background(), StreamConfig{
		Command:   `printf 'WARN x\nINFO y\n' | grep -E '^(WARN|INFO)'`,
		Patterns:  patterns,
		Envelope:  envelopeFor(t),
		Validator: v,
		Stdout:    &stdout, Stderr: &stderr,
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", res.ExitCode, stderr.String())
	}
	lines := decodeJSONL(t, stdout.String())
	if len(lines) != 1 || lines[0]["verdict"] != "warn" {
		t.Fatalf("expected 1 warn, got %v", lines)
	}
}

func TestStreamSubprocess_Timeout(t *testing.T) {
	defer freezeClock(t)()
	v, _ := NewValidator(repoSchemasDir(t))
	var stdout, stderr bytes.Buffer
	res, _ := StreamSubprocess(context.Background(), StreamConfig{
		Command:   `sleep 10`,
		Patterns:  nil,
		Envelope:  envelopeFor(t),
		Validator: v,
		Stdout:    &stdout, Stderr: &stderr,
		TimeoutMS: 200,
	})
	if !res.TimedOut {
		t.Fatal("expected timed_out=true")
	}
}

func TestStreamSubprocess_BinaryNotFound(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	var stdout, stderr bytes.Buffer
	// sh exits non-zero with "command not found"; ExitCode is non-zero, no individuals.
	res, err := StreamSubprocess(context.Background(), StreamConfig{
		Command:   "this-binary-definitely-does-not-exist-zzz arg1 arg2",
		Patterns:  nil,
		Envelope:  envelopeFor(t),
		Validator: v,
		Stdout:    &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if res.ExitCode == 0 {
		t.Fatalf("expected non-zero exit, got %d", res.ExitCode)
	}
}

func TestStreamSubprocess_NoPatternsNoIndividuals(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	var stdout, stderr bytes.Buffer
	res, _ := StreamSubprocess(context.Background(), StreamConfig{
		Command:   `printf 'whatever\n'`,
		Patterns:  nil,
		Envelope:  envelopeFor(t),
		Validator: v,
		Stdout:    &stdout, Stderr: &stderr,
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d", res.ExitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no JSONL output, got: %s", stdout.String())
	}
	if len(res.Individuals) != 0 {
		t.Fatalf("expected zero individuals, got %d", len(res.Individuals))
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./lib/ -run 'TestStreamSubprocess' -v
```

Expected: All FAIL with "undefined: StreamSubprocess" / "undefined: StreamConfig".

- [ ] **Step 3: Implement `lib/stream.go`**

```go
package lib

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// StreamConfig is the input to StreamSubprocess.
type StreamConfig struct {
	Command   string            // raw shell command, executed via sh -c
	Env       map[string]string // additional env vars
	TimeoutMS int               // hard cap; 0 means no timeout
	Patterns  []Pattern         // compiled output_parsing.patterns
	Envelope  Envelope          // sensor_id, version, run_id, started_at, sensor_type
	Validator *Validator        // for per-individual signal validation; may be nil to skip
	Stdout    io.Writer         // JSONL goes here
	Stderr    io.Writer         // diagnostic messages (validation warnings, etc.)
}

// StreamResult holds what the caller needs to build the aggregate Signal.
type StreamResult struct {
	ExitCode    int
	TimedOut    bool
	ElapsedMS   int
	Individuals []map[string]interface{} // also already encoded onto Stdout
	CommandRun  string                   // exact string passed to sh -c
}

// StreamSubprocess spawns sh -c <Command>, scans merged stdout+stderr line by
// line, emits one JSONL Signal per matching line, and returns an aggregate-
// ready summary when the process exits or times out.
//
// It returns a non-nil error only for setup failures (missing command,
// pipe creation). A subprocess that fails to spawn (e.g. binary not found)
// is reported via a non-zero ExitCode, not an error.
func StreamSubprocess(ctx context.Context, cfg StreamConfig) (StreamResult, error) {
	if cfg.Command == "" {
		return StreamResult{}, errors.New("stream: empty command")
	}
	res := StreamResult{CommandRun: cfg.Command, ExitCode: -1}

	if cfg.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.Command)
	if len(cfg.Env) > 0 {
		envList := append([]string{}, cmd.Environ()...)
		for k, v := range cfg.Env {
			envList = append(envList, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = envList
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return res, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return res, fmt.Errorf("stderr pipe: %w", err)
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		// e.g. /bin/sh missing — extremely unlikely on POSIX hosts.
		res.ElapsedMS = int(time.Since(start) / time.Millisecond)
		return res, fmt.Errorf("start: %w", err)
	}

	// Drain stdout and stderr concurrently. Each goroutine pushes matched
	// individuals onto a shared buffered channel; main loop emits JSONL.
	type emit struct{ sig map[string]interface{} }
	emits := make(chan emit, 64)
	var wg sync.WaitGroup
	scan := func(r io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			m, ok := MatchLine(line, cfg.Patterns)
			if !ok {
				continue
			}
			emits <- emit{sig: buildIndividualSignal(cfg.Envelope, m)}
		}
	}
	wg.Add(2)
	go scan(stdoutPipe)
	go scan(stderrPipe)
	go func() { wg.Wait(); close(emits) }()

	for e := range emits {
		if cfg.Validator != nil {
			if err := cfg.Validator.Validate(TargetSignal, e.sig); err != nil {
				fmt.Fprintf(cfg.Stderr, "warning: skipping invalid individual signal: %v\n", err)
				continue
			}
		}
		res.Individuals = append(res.Individuals, e.sig)
		_ = json.NewEncoder(cfg.Stdout).Encode(e.sig)
	}

	waitErr := cmd.Wait()
	res.ElapsedMS = int(time.Since(start) / time.Millisecond)
	res.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	_ = waitErr
	return res, nil
}

// buildIndividualSignal assembles a Signal-shaped map for one matched line.
// Fields with zero values are omitted (file/line_start/line_end/excerpt) so
// the Signal stays minimal when captures don't apply.
func buildIndividualSignal(env Envelope, m PatternMatch) map[string]interface{} {
	ev := map[string]interface{}{"rationale": m.Rationale}
	if m.File != "" {
		ev["file"] = m.File
	}
	if m.LineStart != nil {
		ev["line_start"] = *m.LineStart
	}
	if m.LineEnd != nil {
		ev["line_end"] = *m.LineEnd
	}
	if m.Excerpt != "" {
		ev["excerpt"] = m.Excerpt
	}
	return map[string]interface{}{
		"sensor_id":   env.SensorID,
		"version":     env.Version,
		"run_id":      env.RunID,
		"started_at":  env.StartedAt,
		"finished_at": NowFn().Format("2006-01-02T15:04:05Z"),
		"verdict":     m.Verdict,
		"severity":    m.Severity,
		"confidence":  1.0,
		"evidence":    []interface{}{ev},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind": "individual",
			"line": m.Line,
		},
	}
}
```

- [ ] **Step 4: Run tests, verify they pass**

```bash
go test ./lib/ -run 'TestStreamSubprocess' -v
go test ./lib/...
```

Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/stream.go lib/stream_test.go
git commit -m "feat(lib): add StreamSubprocess — sh -c spawn, JSONL per match, timeout"
```

---

## Task 7: Rewrite `run-computational.go` against the new pipeline

Thin CLI wrapper: parse args, load+validate sensor, build envelope, compile patterns, call `StreamSubprocess`, build aggregate Signal, validate, encode as the final JSONL line.

**Files:**
- Modify (full rewrite): `skills/run-sensor/scripts/run-computational.go`
- Create: `skills/run-sensor/scripts/run-computational_test.go`

- [ ] **Step 1: Write the failing test**

Create `skills/run-sensor/scripts/run-computational_test.go`:

```go
//go:build run_computational

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoSchemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	// .../skills/run-sensor/scripts/run-computational_test.go → 3 levels up.
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "schemas")
}

func writeSensor(t *testing.T, exec map[string]interface{}) string {
	t.Helper()
	sensor := map[string]interface{}{
		"id": "comp-test", "version": "0.1.0",
		"name": "comp-test", "description": "fixture",
		"type": "computational", "regulation": "maintainability",
		"phase": "on-demand", "determinism": "high",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 100, "timeout_ms": 5000},
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 64},
		},
		"triggers":  []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": exec,
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "x", "expected_verdict": "pass", "expected_severity": "info"}},
		},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sensor.json")
	b, _ := json.Marshal(sensor)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func parseJSONL(t *testing.T, s string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestRunComputational_AllPass(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeSensor(t, map[string]interface{}{
		"command": `printf 'PASS a\nPASS b\n'`,
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			map[string]interface{}{"exit_code": 1, "verdict": "fail", "severity": "high"},
		},
		"output_parsing": map[string]interface{}{
			"patterns": []interface{}{
				map[string]interface{}{"regex": "^PASS", "verdict": "pass", "severity": "info"},
				map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
			},
		},
	})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	if len(lines) != 3 {
		t.Fatalf("want 2 individuals + 1 aggregate, got %d", len(lines))
	}
	agg := lines[len(lines)-1]
	if agg["verdict"] != "pass" {
		t.Fatalf("aggregate verdict=%v", agg["verdict"])
	}
	if md := agg["metadata"].(map[string]interface{}); md["kind"] != "aggregate" {
		t.Fatalf("aggregate metadata.kind=%v", md["kind"])
	}
}

func TestRunComputational_LogStyle_StreamFailEclipsesPassExit(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeSensor(t, map[string]interface{}{
		"command": `printf 'INFO ok\nERROR something broke\nINFO ok\n'; exit 0`,
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
		},
		"output_parsing": map[string]interface{}{
			"patterns": []interface{}{
				map[string]interface{}{"regex": "^INFO", "verdict": "pass", "severity": "info"},
				map[string]interface{}{"regex": "^ERROR", "verdict": "fail", "severity": "high"},
			},
		},
	})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	agg := lines[len(lines)-1]
	if agg["verdict"] != "fail" {
		t.Fatalf("worst-of-two should pick stream fail; got %v", agg["verdict"])
	}
}

func TestRunComputational_FatalNoStream(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeSensor(t, map[string]interface{}{
		"command": `false`,
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			map[string]interface{}{"exit_code": 1, "verdict": "fail", "severity": "high"},
		},
	})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	if len(lines) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(lines))
	}
	if lines[0]["verdict"] != "fail" {
		t.Fatalf("aggregate verdict=%v", lines[0]["verdict"])
	}
}

func TestRunComputational_Timeout(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeSensor(t, map[string]interface{}{
		"command": `sleep 10`,
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
		},
	})
	// Patch latency.timeout_ms to 200ms.
	b, _ := os.ReadFile(path)
	var s map[string]interface{}
	_ = json.Unmarshal(b, &s)
	s["cost"].(map[string]interface{})["latency"].(map[string]interface{})["timeout_ms"] = 200
	nb, _ := json.Marshal(s)
	_ = os.WriteFile(path, nb, 0o644)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	agg := lines[len(lines)-1]
	if agg["verdict"] != "error" {
		t.Fatalf("expected timeout → error, got %v", agg["verdict"])
	}
	md := agg["metadata"].(map[string]interface{})
	if md["timed_out"] != true {
		t.Fatalf("expected timed_out=true, got %v", md["timed_out"])
	}
}

func TestRunComputational_RejectsInferential(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	// Hand-roll an inferential sensor.
	dir := t.TempDir()
	path := filepath.Join(dir, "sensor.json")
	sensor := map[string]interface{}{
		"id": "wrong-type", "version": "0.1.0",
		"name": "x", "description": "x",
		"type": "inferential", "regulation": "maintainability",
		"phase": "post-integration", "determinism": "low",
		"cost": map[string]interface{}{
			"class":   "expensive",
			"latency": map[string]interface{}{"p50_ms": 1, "p95_ms": 1, "timeout_ms": 1},
			"tokens":  map[string]interface{}{"model": "anthropic/claude-sonnet-4-6", "input_avg": 1, "output_avg": 1, "max_output": 1},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command":              "true",
			"model":                "anthropic/claude-sonnet-4-6",
			"system_prompt":        "x",
			"user_prompt_template": "x",
			"decoding":             map[string]interface{}{"temperature": 0.0, "max_tokens": 1},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "f", "expected_verdict": "pass", "expected_severity": "info"}},
		},
		"calibration": map[string]interface{}{
			"confidence_threshold": 0.7,
			"calibration_set":      "x", "calibration_size": 1,
			"calibration_date": "2026-04-15",
		},
	}
	b, _ := json.Marshal(sensor)
	_ = os.WriteFile(path, b, 0o644)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected exit 2 (type mismatch), got %d", code)
	}
}

func TestRunComputational_MissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test -tags=run_computational ./skills/run-sensor/scripts/... -v
```

Expected: All FAIL because the runner is still the stub.

- [ ] **Step 3: Implement `skills/run-sensor/scripts/run-computational.go`**

Full replacement:

```go
//go:build run_computational

// Command run-computational runs a streaming computational sensor end-to-end.
//
// Usage:
//
//	go run -tags=run_computational ./skills/run-sensor/scripts <sensor-path>
//
// Stdout is JSONL: one Signal per matched output line, terminated by the
// aggregate Signal. Exit codes: 0 ok (Signals printed), 1 schema/pattern
// failure, 2 usage or I/O error.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/iurykrieger/harness-framework/lib"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run-computational", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var schemasDir string
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: run-computational [--schemas-dir=DIR] <sensor-path>")
		return 2
	}

	sensor, _, code := lib.LoadAndValidateSensor(rest[0], schemasDir, stderr)
	if code != 0 {
		return code
	}
	if t, _ := sensor["type"].(string); t != "computational" {
		fmt.Fprintf(stderr, "error: sensor.type=%q (run-computational requires 'computational')\n", t)
		return 2
	}
	v, code := lib.LoadValidator(schemasDir, stderr)
	if code != 0 {
		return code
	}
	envelope, err := lib.BuildEnvelope(sensor)
	if err != nil {
		fmt.Fprintln(stderr, "error: envelope:", err)
		return 2
	}

	execMap := sensor["execution"].(map[string]interface{})
	command, _ := execMap["command"].(string)

	var patterns []lib.Pattern
	if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
		raw, _ := op["patterns"].([]interface{})
		patterns, err = lib.CompilePatterns(raw)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
	}

	timeoutMS := int(asNumber(sensor["cost"].(map[string]interface{})["latency"].(map[string]interface{})["timeout_ms"]))

	envExtra := map[string]string{}
	if envObj, ok := execMap["env"].(map[string]interface{}); ok {
		for k, val := range envObj {
			envExtra[k] = fmt.Sprintf("%v", val)
		}
	}

	res, _ := lib.StreamSubprocess(context.Background(), lib.StreamConfig{
		Command:   command,
		Env:       envExtra,
		TimeoutMS: timeoutMS,
		Patterns:  patterns,
		Envelope:  envelope,
		Validator: v,
		Stdout:    stdout,
		Stderr:    stderr,
	})

	ecMap, _ := execMap["exit_code_map"].([]interface{})
	exitVerd, exitSev := lib.MapExitCode(res.ExitCode, ecMap)
	streamVerd, streamSev := lib.MaxStreamVerdict(res.Individuals)
	agg := lib.Aggregate(lib.AggregateInput{
		ExitVerdict:    exitVerd,
		ExitSeverity:   exitSev,
		StreamVerdict:  streamVerd,
		StreamSeverity: streamSev,
		TimedOut:       res.TimedOut,
	})

	signal := buildAggregateSignal(envelope, res, agg, command)
	if err := v.Validate(lib.TargetSignal, signal); err != nil {
		lib.PrintValidationOrPlain(err, stderr)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(signal)
	return 0
}

func buildAggregateSignal(env lib.Envelope, res lib.StreamResult, agg lib.AggregateResult, command string) map[string]interface{} {
	finished := lib.NowFn().Format(time.RFC3339)
	evidence := lib.SelectTopEvidence(res.Individuals, 20)
	return map[string]interface{}{
		"sensor_id":   env.SensorID,
		"version":     env.Version,
		"run_id":      env.RunID,
		"started_at":  env.StartedAt,
		"finished_at": finished,
		"verdict":     agg.Verdict,
		"severity":    agg.Severity,
		"confidence":  1.0,
		"evidence":    evidence,
		"cost_actual": map[string]interface{}{"latency_ms": res.ElapsedMS},
		"metadata": map[string]interface{}{
			"kind":      "aggregate",
			"command":   command,
			"exit_code": res.ExitCode,
			"timed_out": res.TimedOut,
			"counts":    lib.CountVerdicts(res.Individuals),
		},
	}
}

func asNumber(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}
```

- [ ] **Step 4: Run tests, verify they pass**

```bash
go test -tags=run_computational ./skills/run-sensor/scripts/... -v
go vet -tags=run_computational ./...
```

Expected: All PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add skills/run-sensor/scripts/run-computational.go skills/run-sensor/scripts/run-computational_test.go
git commit -m "feat(run-sensor): rewrite computational runner against streaming pipeline"
```

---

## Task 8: Rewrite `run-inferential.go` against the streaming pipeline (subprocess, no HTTP)

The inferential runner spawns the sensor's `execution.command` exactly like the computational runner — no Anthropic HTTP client. The only difference is post-aggregation: apply the calibration `fail → warn` downgrade when aggregate `confidence < calibration.confidence_threshold`. Confidence at the aggregate level is taken from `metadata.confidence` of the worst stream individual; if absent, defaults to 1.0. Slot bindings render `execution.user_prompt_template` (kept for documentation/auditing) into env var `HARNESS_PROMPT` so the subprocess can read it; the runner stays oblivious to LLM details.

**Files:**
- Modify (full rewrite): `skills/run-sensor/scripts/run-inferential.go`
- Create: `skills/run-sensor/scripts/run-inferential_test.go`

- [ ] **Step 1: Write the failing test**

Create `skills/run-sensor/scripts/run-inferential_test.go`:

```go
//go:build run_inferential

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoSchemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "schemas")
}

func writeInferentialSensor(t *testing.T, command string) string {
	t.Helper()
	sensor := map[string]interface{}{
		"id": "infr-test", "version": "0.1.0",
		"name": "infr-test", "description": "fixture",
		"type": "inferential", "regulation": "maintainability",
		"phase": "post-integration", "determinism": "low",
		"cost": map[string]interface{}{
			"class":   "expensive",
			"latency": map[string]interface{}{"p50_ms": 1000, "p95_ms": 5000, "timeout_ms": 30000},
			"tokens":  map[string]interface{}{"model": "anthropic/claude-sonnet-4-6", "input_avg": 100, "output_avg": 50, "max_output": 256},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "pull-request"}},
		"execution": map[string]interface{}{
			"command":              command,
			"model":                "anthropic/claude-sonnet-4-6",
			"system_prompt":        "Output JSONL only.",
			"user_prompt_template": "Compare {{a}} to {{b}}.",
			"decoding":             map[string]interface{}{"temperature": 0.0, "max_tokens": 256},
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^PASS", "verdict": "pass", "severity": "info"},
					map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
				},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "f", "expected_verdict": "warn", "expected_severity": "medium"}},
		},
		"calibration": map[string]interface{}{
			"confidence_threshold": 0.7,
			"calibration_set":      "tests/cal.jsonl",
			"calibration_size":     120,
			"calibration_date":     "2026-04-15",
		},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sensor.json")
	b, _ := json.Marshal(sensor)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func parseJSONL(t *testing.T, s string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestRunInferential_Pass(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeInferentialSensor(t, `printf 'PASS judgment-1\n'`)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=foo()",
		"--slot", "b=bar()",
		path,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	if len(lines) < 2 {
		t.Fatalf("expected ≥1 individual + aggregate, got %d", len(lines))
	}
	agg := lines[len(lines)-1]
	if agg["verdict"] != "pass" {
		t.Fatalf("aggregate verdict=%v", agg["verdict"])
	}
}

func TestRunInferential_CalibrationDowngrade(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	// One FAIL line + a metadata.confidence=0.5 hint lower than threshold 0.7.
	// We set HARNESS_AGGREGATE_CONFIDENCE via a shell preamble that the runner
	// reads from stderr (see HARNESS_CONFIDENCE_LINE protocol below).
	cmd := `printf 'FAIL low-conf\n'; printf 'HARNESS_AGGREGATE_CONFIDENCE=0.5\n' >&2`
	path := writeInferentialSensor(t, cmd)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=x", "--slot", "b=y",
		path,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	agg := lines[len(lines)-1]
	if agg["verdict"] != "warn" {
		t.Fatalf("expected fail→warn downgrade, got %v", agg["verdict"])
	}
	md := agg["metadata"].(map[string]interface{})
	if md["calibration_downgrade"] != true {
		t.Fatalf("expected metadata.calibration_downgrade=true, got %v", md)
	}
}

func TestRunInferential_RejectsComputational(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	// Re-use writeSensor from the computational test? No — different package.
	// Hand-roll a computational sensor.
	dir := t.TempDir()
	path := filepath.Join(dir, "sensor.json")
	sensor := map[string]interface{}{
		"id": "wrong", "version": "0.1.0",
		"name": "x", "description": "x",
		"type": "computational", "regulation": "maintainability",
		"phase": "on-demand", "determinism": "high",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"latency": map[string]interface{}{"p50_ms": 1, "p95_ms": 1, "timeout_ms": 1000},
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 1},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command": "true",
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "x", "expected_verdict": "pass", "expected_severity": "info"}},
		},
	}
	b, _ := json.Marshal(sensor)
	_ = os.WriteFile(path, b, 0o644)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected 2 (type mismatch), got %d", code)
	}
}

func TestRunInferential_UnboundSlot(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeInferentialSensor(t, `true`)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=x", // missing 'b'
		path,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for unbound slot, got %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "b") {
		t.Fatalf("stderr should name unbound slot 'b': %s", stderr.String())
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test -tags=run_inferential ./skills/run-sensor/scripts/... -v
```

Expected: All FAIL (stub still in place).

- [ ] **Step 3: Implement `skills/run-sensor/scripts/run-inferential.go`**

Full replacement:

```go
//go:build run_inferential

// Command run-inferential runs a streaming inferential sensor end-to-end. The
// sensor's execution.command spawns an LLM CLI (e.g. `claude -p ...`); the
// runner does not talk HTTP. The user_prompt_template is rendered against
// --slot bindings and exposed to the subprocess as the HARNESS_PROMPT env var.
// The subprocess may emit a single line of the form
// `HARNESS_AGGREGATE_CONFIDENCE=<float>` on stderr to influence the
// calibration downgrade decision; otherwise confidence defaults to 1.0.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/iurykrieger/harness-framework/lib"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run-inferential", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var schemasDir string
	var slots lib.MultiFlag
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	fs.Var(&slots, "slot", "key=value slot binding for user_prompt_template (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: run-inferential [--schemas-dir=DIR] [--slot k=v]... <sensor-path>")
		return 2
	}

	bindings, err := parseSlots(slots)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}

	sensor, _, code := lib.LoadAndValidateSensor(rest[0], schemasDir, stderr)
	if code != 0 {
		return code
	}
	if t, _ := sensor["type"].(string); t != "inferential" {
		fmt.Fprintf(stderr, "error: sensor.type=%q (run-inferential requires 'inferential')\n", t)
		return 2
	}
	v, code := lib.LoadValidator(schemasDir, stderr)
	if code != 0 {
		return code
	}
	envelope, err := lib.BuildEnvelope(sensor)
	if err != nil {
		fmt.Fprintln(stderr, "error: envelope:", err)
		return 2
	}

	execMap := sensor["execution"].(map[string]interface{})
	command, _ := execMap["command"].(string)
	userTemplate, _ := execMap["user_prompt_template"].(string)
	rendered, missing := lib.RenderTemplate(userTemplate, bindings)
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "error: unbound slots: %s (provide via --slot key=value)\n", strings.Join(missing, ", "))
		return 1
	}

	var patterns []lib.Pattern
	if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
		raw, _ := op["patterns"].([]interface{})
		patterns, err = lib.CompilePatterns(raw)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
	}

	timeoutMS := int(asNumber(sensor["cost"].(map[string]interface{})["latency"].(map[string]interface{})["timeout_ms"]))

	// Capture stderr so we can extract HARNESS_AGGREGATE_CONFIDENCE while still
	// surfacing diagnostics to the user.
	var stderrCapture bytes.Buffer
	stderrTee := io.MultiWriter(stderr, &stderrCapture)

	res, _ := lib.StreamSubprocess(context.Background(), lib.StreamConfig{
		Command:   command,
		Env:       map[string]string{"HARNESS_PROMPT": rendered},
		TimeoutMS: timeoutMS,
		Patterns:  patterns,
		Envelope:  envelope,
		Validator: v,
		Stdout:    stdout,
		Stderr:    stderrTee,
	})

	confidence := extractConfidence(stderrCapture.Bytes(), 1.0)

	ecMap, _ := execMap["exit_code_map"].([]interface{})
	exitVerd, exitSev := lib.MapExitCode(res.ExitCode, ecMap)
	streamVerd, streamSev := lib.MaxStreamVerdict(res.Individuals)
	agg := lib.Aggregate(lib.AggregateInput{
		ExitVerdict:    exitVerd,
		ExitSeverity:   exitSev,
		StreamVerdict:  streamVerd,
		StreamSeverity: streamSev,
		TimedOut:       res.TimedOut,
	})

	downgrade := false
	if cal, ok := sensor["calibration"].(map[string]interface{}); ok {
		thresh, _ := cal["confidence_threshold"].(float64)
		if agg.Verdict == "fail" && confidence < thresh {
			agg.Verdict = "warn"
			agg.Severity = "low"
			downgrade = true
		}
	}

	signal := buildAggregateSignal(envelope, res, agg, command, confidence, downgrade)
	if err := v.Validate(lib.TargetSignal, signal); err != nil {
		lib.PrintValidationOrPlain(err, stderr)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(signal)
	return 0
}

func parseSlots(raw []string) (map[string]string, error) {
	out := map[string]string{}
	for _, s := range raw {
		i := strings.IndexByte(s, '=')
		if i <= 0 {
			return nil, fmt.Errorf("slot %q is not key=value", s)
		}
		out[s[:i]] = s[i+1:]
	}
	return out, nil
}

// extractConfidence scans captured stderr for a line of the form
// HARNESS_AGGREGATE_CONFIDENCE=<float>. Returns the parsed value, or fallback
// if the line is missing or unparseable.
func extractConfidence(stderr []byte, fallback float64) float64 {
	sc := bufio.NewScanner(bytes.NewReader(stderr))
	for sc.Scan() {
		line := sc.Text()
		const prefix = "HARNESS_AGGREGATE_CONFIDENCE="
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if v, err := strconv.ParseFloat(line[len(prefix):], 64); err == nil {
			return v
		}
	}
	return fallback
}

func buildAggregateSignal(env lib.Envelope, res lib.StreamResult, agg lib.AggregateResult, command string, confidence float64, downgrade bool) map[string]interface{} {
	finished := lib.NowFn().Format(time.RFC3339)
	evidence := lib.SelectTopEvidence(res.Individuals, 20)
	md := map[string]interface{}{
		"kind":      "aggregate",
		"command":   command,
		"exit_code": res.ExitCode,
		"timed_out": res.TimedOut,
		"counts":    lib.CountVerdicts(res.Individuals),
	}
	if downgrade {
		md["calibration_downgrade"] = true
	}
	return map[string]interface{}{
		"sensor_id":   env.SensorID,
		"version":     env.Version,
		"run_id":      env.RunID,
		"started_at":  env.StartedAt,
		"finished_at": finished,
		"verdict":     agg.Verdict,
		"severity":    agg.Severity,
		"confidence":  confidence,
		"evidence":    evidence,
		"cost_actual": map[string]interface{}{"latency_ms": res.ElapsedMS},
		"metadata":    md,
	}
}

func asNumber(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}
```

- [ ] **Step 4: Run tests, verify they pass**

```bash
go test -tags=run_inferential ./skills/run-sensor/scripts/... -v
go vet -tags=run_inferential ./...
```

Expected: All PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add skills/run-sensor/scripts/run-inferential.go skills/run-sensor/scripts/run-inferential_test.go
git commit -m "feat(run-sensor): rewrite inferential runner as streaming subprocess

Drops the in-runner Anthropic HTTP client. Sensors now declare an LLM CLI
in execution.command; the rendered user prompt is exposed to the
subprocess via HARNESS_PROMPT, and the subprocess may emit
HARNESS_AGGREGATE_CONFIDENCE=<float> on stderr to drive the calibration
downgrade. fail→warn downgrade applied to the aggregate Signal only."
```

---

## Task 9: Update `skills/run-sensor/SKILL.md`

Document the JSONL contract (individuals + aggregate as the last line), the new fenced-block convention, and that both runners are now subprocess-based.

**Files:**
- Modify: `skills/run-sensor/SKILL.md`

- [ ] **Step 1: Replace `SKILL.md` body**

Replace the entire content of `skills/run-sensor/SKILL.md` with:

````markdown
---
name: run-sensor
description: Use when the user invokes /run-sensor or asks to run a harness sensor. Takes a path to a sensor JSON file (e.g. `@sensors/<id>.json`). Reads `sensor.type` and dispatches to either `run-computational.go` or `run-inferential.go`. Both runners spawn a subprocess (the project's lint/build/test/log/etc. command, or an LLM CLI for inferential), stream individual Signals as JSONL while it runs, and emit one final aggregate Signal as the LAST JSONL line.
---

# run-sensor

Execute a harness sensor and emit Signals back to the caller.

There are exactly two scripts, one per sensor type. Both follow the same streaming model: a single `go run` produces JSONL on stdout — one Signal per matched output line during the run, then one aggregate Signal as the final line.

## Invocation

```
/run-sensor <path-to-sensor.json>
```

The argument may use `@`-prefix file syntax, a repo-relative path, or an absolute path. If absent, ask the user. Do not invent a sensor.

## Procedure

### 1. Read `sensor.type`

`sensor.type` is required by `schemas/sensor.json`, so it is present in every well-formed sensor file. Use the Read tool against the resolved path. Branch on the value.

### 2a. `computational`

```bash
go run -tags=run_computational ./skills/run-sensor/scripts <SENSOR_PATH>
```

The script does everything: resolves the path (including `@` prefix), validates against `schemas/sensor.json`, spawns `sh -c <execution.command>` with the configured env capped by `cost.latency.timeout_ms`, scans stdout+stderr line-by-line, matches each line against `execution.output_parsing.patterns` (when declared), emits a Signal per match as JSONL, and ends with one aggregate Signal whose verdict is the worse of `exit_code_map[exitCode]` and the highest verdict observed in the stream. Pass its stdout through to step 3.

Exit codes: `0` Signals printed; `1` schema/pattern compile failure; `2` usage or I/O error (sensor unreadable, malformed JSON, wrong type).

### 2b. `inferential`

```bash
go run -tags=run_inferential ./skills/run-sensor/scripts \
  [--slot key1=value1] [--slot key2=value2] ... \
  <SENSOR_PATH>
```

Same streaming model. The sensor's `execution.command` is the LLM CLI (e.g. `claude -p ...`). The runner renders `execution.user_prompt_template` against `--slot` bindings and exposes the result to the subprocess as `HARNESS_PROMPT`. The subprocess prints judgment lines that get matched by `output_parsing.patterns` like any other sensor.

Calibration: if the subprocess emits a single `HARNESS_AGGREGATE_CONFIDENCE=<float>` line on stderr, that value becomes the aggregate Signal's `confidence` and feeds the `fail → warn` downgrade rule (`confidence < calibration.confidence_threshold`). If absent, `confidence` defaults to 1.0 and no downgrade ever triggers.

Exit codes: `0` Signals printed; `1` schema/pattern failure or unbound `{{slot}}`; `2` usage or I/O error (sensor unreadable, wrong type, slot not in `key=value` form).

### 3. Emit

The runner prints JSONL on stdout. Surface it in your response with two fenced blocks:

- A ```jsonl``` block with the individual Signals (omit when there were none).
- A ```json``` block with the aggregate Signal as the **last** content of your response.

Calling agents parse bottom-up, so the aggregate stays unambiguously identifiable.

## Output contract

The final ```json``` block is the aggregate. Its `metadata.kind` is always `"aggregate"`. Per-line Signals carry `metadata.kind: "individual"` and `metadata.line` with the raw matched text.

```json
{ ...aggregate Signal conforming to schemas/signal.json... }
```

## Error envelope

When the runner exits non-zero before any Signal can be produced, emit a Signal that still validates against `schemas/signal.json` as the trailing fenced ```json``` block. Capture the runner's stderr verbatim into `evidence[0].rationale`.

```json
{
  "sensor_id":   "<sensor.id, or 'run-sensor' if the sensor was unreadable>",
  "version":     "<sensor.version, or '0.0.0'>",
  "run_id":      "<a UUID>",
  "started_at":  "<when you started, ISO-8601 UTC>",
  "finished_at": "<now, ISO-8601 UTC>",
  "verdict":     "error",
  "severity":    "high",
  "confidence":  1.0,
  "evidence":    [{ "rationale": "<captured stderr from the failing runner>" }],
  "remediation": { "instructions": "<what the caller should change to recover>" },
  "cost_actual": { "latency_ms": <elapsed> },
  "metadata":    { "kind": "aggregate" }
}
```

## Notes & limits

- Both runners use `sh -c`, so `execution.command` may use pipes, redirects, globs, and quoted args without escaping.
- The streaming buffer caps individual lines at 1 MB. Longer lines are silently truncated by the scanner.
- Inferential reproducibility: even with `temperature: 0`, sampling drift across model versions is real. Trust `confidence` (when emitted), not exact verdict equality.
- Computational sensors must be hermetic. If a sensor's command depends on uncommitted state, that's a sensor-design bug; don't paper over it in the runner.
- Slot bindings (`--slot key=value`) populate the inferential prompt template only. Computational sensors ignore `--slot`.
````

- [ ] **Step 2: Build the skill, sanity-check the front matter**

```bash
head -3 skills/run-sensor/SKILL.md
```

Expected: `---` line, `name: run-sensor`, `description: ...`.

- [ ] **Step 3: Commit**

```bash
git add skills/run-sensor/SKILL.md
git commit -m "docs(skill): document JSONL streaming contract for run-sensor"
```

---

## Task 10: Update `CLAUDE.md` rule 4 to permit a top-level `lib/`

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Edit rule 4**

In `CLAUDE.md`, REPLACE:

```
4. **Scripts are skill-local.** Each script lives under `skills/<skill-name>/scripts/`. Skills are self-contained — never share code across skills via a top-level `scripts/`. Duplicate before coupling.
```

with:

```
4. **Scripts are skill-local; libraries can be shared.** Each script lives under `skills/<skill-name>/scripts/` and stays self-contained — never share *scripts* across skills via a top-level `scripts/`. Duplicate scripts before coupling. A top-level `lib/` package is permitted for stable, schema-tied primitives that several skills genuinely need (schema validation, envelope construction, subprocess streaming, exit-code mapping, template rendering); skill-specific logic does not belong there.
```

- [ ] **Step 2: Confirm the build still passes**

```bash
go test ./...
go test -tags=run_computational ./...
go test -tags=run_inferential ./...
```

Expected: All green.

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: amend rule 4 to allow a top-level lib/ for shared primitives"
```

---

## Self-review notes

- **Spec coverage:** every spec section is touched by at least one task — schema (Task 2 for `output_parsing`, Task 3 for `command`/`exit_code_map`), runner pipeline (Tasks 6/7/8), aggregation (Task 5), pattern matching (Task 4), top-level `lib/` (Task 1), CLAUDE.md amendment (Task 10), SKILL.md contract (Task 9). The HTTP teardown happens implicitly in Task 1 (delete) and Task 8 (subprocess rewrite).
- **Type consistency:** the public surface of `lib` (`Pattern`, `PatternMatch`, `StreamConfig`, `StreamResult`, `Envelope`, `AggregateInput`, `AggregateResult`, function names) is identical across Tasks 4–8. Confirmed.
- **No placeholders:** every step shows the actual code or command. No "TODO", no "similar to Task N".
- **TDD discipline:** each implementation task starts with a failing test, runs it to confirm failure, implements, runs to confirm pass, commits. Task 1 is a refactor with the existing tests as safety net (the only deviation, called out explicitly).
