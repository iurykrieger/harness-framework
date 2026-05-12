# Structural Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Pure structural refactor — relocate root-level `test/` into context-owning packages, delete root `scripts/`, introduce typed `Sensor`/`Signal` Go struct mirrors of the JSON schemas, dissolve `lib/testfixtures/` into per-entity `<pkg>test` helpers + `testdata/` JSON, and formalize the fixture taxonomy as CLAUDE.md rule 10.

**Architecture:** Eight sequential commits, each CI-green on its own. Mechanical moves first (commits 1–2), then additive shape.go (commit 3), then per-entity fixture migration that lets call sites switch incrementally (commits 4–5), then helper inlining + delete-`testfixtures` (commit 6), then small consolidations (commits 7–8). No schema changes; no behavior changes; no new third-party deps.

**Tech Stack:** Go 1.25 (single module `github.com/iurykrieger/harness-framework`), standard library, existing `github.com/santhosh-tekuri/jsonschema/v5` for schema validation in tests.

**Reference spec:** `docs/superpowers/specs/2026-05-12-structural-refactor-design.md`. The spec's DoD numbers are the source of truth for "done".

---

## File Structure

### New files

| Path | Purpose |
|---|---|
| `lib/sensor/shape.go` | Typed `Sensor` struct + nested types + enums mirroring `schemas/sensor.json`. Method `AsMap() map[string]interface{}` for bridge with map-based APIs. |
| `lib/sensor/shape_test.go` | Round-trip tests for the three canonical sensors. |
| `lib/signal/shape.go` | Typed `Signal` struct + `Verdict`/`Severity` enums + `Evidence`/`Remediation`/`CostActual` mirroring `schemas/signal.json`. Method `AsMap()`. |
| `lib/signal/shape_test.go` | Round-trip + parity test (`Builder.Build()` deep-equal to `Builder.BuildTyped().AsMap()`). |
| `lib/sensor/testdata/canonical-computational.json` | JSON serialization of the current `testfixtures.ValidSensorComputational()` map. |
| `lib/sensor/testdata/canonical-inferential.json` | Idem for `ValidSensorInferential()`. |
| `lib/sensor/testdata/canonical-setup.json` | Idem for `ValidSensorSetup()`. |
| `lib/sensor/sensortest/canonical.go` | Package `sensortest`. `LoadComputational/Inferential/Setup(t)` returning `*sensor.Sensor`. |
| `lib/sensor/sensortest/canonical_test.go` | Asserts each JSON validates against `schemas/sensor.json`. |
| `lib/schema/schematest/repodir.go` | Package `schematest`. `RepoSchemasDir(t) string` via `runtime.Caller`. |

### Files relocated (preserve history with `git mv`)

| From | To | Notes |
|---|---|---|
| `test/integration_runtime_logs_test.go` | `lib/orchestrator/integration_runtime_logs_test.go` | Package `integration` → `orchestrator_test`. Build tag `//go:build integration` preserved. |
| `test/heal-e2e/heal_e2e_test.go` | `lib/heal/heal_e2e_test.go` | Package `healE2E_test` → `heal_test`. No build tag. |
| `test/registry-discovery-e2e/registry_discovery_e2e_test.go` | `lib/registry/discovery_e2e_test.go` | Package `registryDiscoveryE2E_test` → `registry_test`. No build tag. |
| `test/fixtures/stack-discovery/` (entire subtree, with nested `go.mod`) | `lib/stack/testdata/stack-discovery/` | Go convention: `testdata/` is ignored by `go list ./...`. |

### Files modified

| Path | Change |
|---|---|
| `lib/stack/e2e_fixture_test.go` (lines 162 + 172) | Replace `"test", "fixtures", "stack-discovery"` with `"testdata", "stack-discovery"`; update the matching error string. |
| `lib/sensor/envelope.go` | Add `BuildEnvelopeTyped(s *Sensor) Envelope`. Fold `BuildErrorSignal` from `error.go`. |
| `lib/signal/builder.go` | Add `(b *Builder) BuildTyped() Signal`. |
| `lib/testfixtures/sensor.go` | Builders return both `map[string]interface{}` (existing API) and a sibling `<Name>Sensor()` returning `*sensor.Sensor`. (Used by Task 3 to populate testdata files; deleted in Task 6.) |
| `lib/cli/bootstrap.go` | Append `MultiFlag` type from `lib/cli/flag.go`. |
| `lib/sensor/env_test.go` | Migrate to `package sensor_test`; keep only `CheckRequiredEnv` tests. |
| `lib/orchestrator/main_test.go` | Replace `os.ReadFile("/usr/bin/true")` with literal `#!/bin/sh\nexit 0\n`. |
| `lib/subprocess/stream_test.go` | Inline `freezeClock` and `withRunDir` helpers (formerly in `lib/testfixtures/`). |
| `CLAUDE.md` | Replace closing sentence of rule 9 to reference rule 10; add rule 10 documenting the fixture taxonomy. |

### Files deleted

| Path | Why |
|---|---|
| `scripts/migrate-requires.go` | One-shot migration completed (no v1 sensors remain). |
| `scripts/migrate-requires_test.go` | Idem. |
| `scripts/smoke-requires-deps-logs.sh` | Broken (references old `sensors/` path) and redundant with `lib/sensor/requires_test.go`. |
| `scripts/` (directory, after the three files above are gone) | Repo-root `scripts/` violates rule 4. |
| `test/heal-e2e/`, `test/registry-discovery-e2e/`, `test/fixtures/`, `test/` (after their contents move out) | Root `test/` is gone. |
| `lib/cli/flag.go` | Folded into `bootstrap.go`. |
| `lib/sensor/error.go` | Folded into `envelope.go`. |
| `lib/testfixtures/clock.go` | Inlined into `lib/subprocess/stream_test.go`. |
| `lib/testfixtures/paths.go` | `WithRunDir` inlined into `stream_test.go`; `RepoSchemasDir` moved to `schematest/`. |
| `lib/testfixtures/sensor.go` | Canonical fixtures replaced by `testdata/*.json` + `sensortest/`. |
| `lib/testfixtures/` (directory, after the three files above are gone) | God-package no longer needed. |

### Files moved out / renamed for the env_test split (Task 7)

| From | To |
|---|---|
| `lib/sensor/env_test.go` (currently contains tests for env, error, missing-env) | Split into `lib/sensor/env_test.go` (env only), `lib/sensor/error_test.go`, `lib/sensor/missing_env_signal_test.go`. All in `package sensor_test`. |

---

## Task 1: Relocate root-level test/ into context-owning packages

**Why first:** Pure moves. No dependencies on later work. Once these tests live in their owning packages, the rest of the refactor can edit them in place without crossing directories.

**Files:**
- Move: `test/integration_runtime_logs_test.go` → `lib/orchestrator/integration_runtime_logs_test.go`
- Move: `test/heal-e2e/heal_e2e_test.go` → `lib/heal/heal_e2e_test.go`
- Move: `test/registry-discovery-e2e/registry_discovery_e2e_test.go` → `lib/registry/discovery_e2e_test.go`
- Move: `test/fixtures/stack-discovery/` → `lib/stack/testdata/stack-discovery/`
- Modify: `lib/stack/e2e_fixture_test.go` (lines 162, 172)

- [ ] **Step 1.1: Move `integration_runtime_logs_test.go`**

```bash
git mv test/integration_runtime_logs_test.go lib/orchestrator/integration_runtime_logs_test.go
```

- [ ] **Step 1.2: Update its package declaration**

Open `lib/orchestrator/integration_runtime_logs_test.go`. Change line ~7 from:
```go
package integration
```
to:
```go
package orchestrator_test
```

Also delete the file-path header line `// test/integration_runtime_logs_test.go` (line ~3) since the path is now wrong and adds no value.

- [ ] **Step 1.3: Move `heal_e2e_test.go`**

```bash
git mv test/heal-e2e/heal_e2e_test.go lib/heal/heal_e2e_test.go
```

- [ ] **Step 1.4: Update its package declaration**

Open `lib/heal/heal_e2e_test.go`. Change line 2 from:
```go
package healE2E_test
```
to:
```go
package heal_test
```

Delete the file-path header `// test/heal-e2e/heal_e2e_test.go` (line 1).

- [ ] **Step 1.5: Move `registry_discovery_e2e_test.go`**

```bash
git mv test/registry-discovery-e2e/registry_discovery_e2e_test.go lib/registry/discovery_e2e_test.go
```

- [ ] **Step 1.6: Update its package declaration**

Open `lib/registry/discovery_e2e_test.go`. Change line 7 from:
```go
package registryDiscoveryE2E_test
```
to:
```go
package registry_test
```

Delete the file-path header `// test/registry-discovery-e2e/registry_discovery_e2e_test.go` (line 1).

- [ ] **Step 1.7: Move the stack-discovery fixture directory**

```bash
git mv test/fixtures/stack-discovery lib/stack/testdata/stack-discovery
```

The directory contains `go.mod`, `go.sum`, `cmd/server/main.go`, `expected-stack.json`, `expected-stdout.log`. Since `testdata/` is excluded by `go list ./...`, the nested `go.mod` stops being a recursion problem.

- [ ] **Step 1.8: Update the path reference in `lib/stack/e2e_fixture_test.go`**

Open `lib/stack/e2e_fixture_test.go`. Around line 162:

Replace:
```go
		candidate := filepath.Join(dir, "test", "fixtures", "stack-discovery")
```
with:
```go
		candidate := filepath.Join(dir, "testdata", "stack-discovery")
```

Around line 172:

Replace:
```go
	t.Fatal("test/fixtures/stack-discovery not found")
```
with:
```go
	t.Fatal("lib/stack/testdata/stack-discovery not found")
```

- [ ] **Step 1.9: Confirm the empty `test/` tree is gone**

```bash
rm -rf test/heal-e2e test/registry-discovery-e2e test/fixtures test
```

Expected: empty (the directories are now leaves with no files left). If `git status` shows untracked emptied directories, just remove them; if `test/` itself still exists with no content, also `rmdir` it.

Verify:
```bash
[ ! -d test ] && echo OK
```
Expected: `OK`.

- [ ] **Step 1.10: Run the full test suite to verify no regressions**

```bash
go test ./...
go test ./... -tags=integration
go test -tags=run_computational ./...
go test -tags=run_inferential ./...
```
Expected: all four invocations exit 0 with PASS lines for the packages they touch.

If any test fails because of an import path or a missing file, fix the failure before committing. The most common causes are: a forgotten package-name update in one of the three moved files, or a missed reference to `test/fixtures/...` somewhere other than `lib/stack/e2e_fixture_test.go`.

- [ ] **Step 1.11: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
chore: relocate root-level test/ into context-owning packages

- test/integration_runtime_logs_test.go -> lib/orchestrator/ (package orchestrator_test, integration build tag preserved)
- test/heal-e2e/heal_e2e_test.go -> lib/heal/ (package heal_test)
- test/registry-discovery-e2e/registry_discovery_e2e_test.go -> lib/registry/discovery_e2e_test.go (package registry_test)
- test/fixtures/stack-discovery -> lib/stack/testdata/stack-discovery (Go testdata convention; nested go.mod is now ignored by go list)
- lib/stack/e2e_fixture_test.go: update fixture path
EOF
)"
```

---

## Task 2: Delete root scripts/

**Why second:** The directory has no in-tree importers. Deleting it is independent of the other work.

**Files:**
- Delete: `scripts/migrate-requires.go`
- Delete: `scripts/migrate-requires_test.go`
- Delete: `scripts/smoke-requires-deps-logs.sh`
- Delete: `scripts/` (directory)

- [ ] **Step 2.1: Remove the scripts directory**

```bash
git rm -r scripts
```

Expected `git status` to show three deletions and no other changes.

- [ ] **Step 2.2: Confirm nothing referenced these scripts at compile time**

```bash
go build ./...
```
Expected: exit 0.

- [ ] **Step 2.3: Re-run the full test matrix to be safe**

```bash
go test ./...
go test ./... -tags=integration
go test -tags=run_computational ./...
go test -tags=run_inferential ./...
```
Expected: all four pass.

- [ ] **Step 2.4: Commit**

```bash
git commit -m "$(cat <<'EOF'
chore: remove completed migration scripts and broken smoke

- scripts/migrate-requires.{go,_test.go}: v1->v2 requires migration is complete (no v1 sensors remain in the repo); git history preserves the script if it is ever needed again.
- scripts/smoke-requires-deps-logs.sh: broken since .harness/sensors/ move (commit 35d6d0a) and redundant with lib/sensor/requires_test.go.
- scripts/: directory removed; rule 4 violation gone.
EOF
)"
```

---

## Task 3: Typed shape.go for sensor and signal

**Why third:** Additive (no call-site changes). Establishes the typed surface that Tasks 4 and 5 will use. Mirrors `lib/stack/shape.go` exactly.

**Files:**
- Create: `lib/sensor/shape.go`
- Create: `lib/sensor/shape_test.go`
- Create: `lib/signal/shape.go`
- Create: `lib/signal/shape_test.go`
- Modify: `lib/sensor/envelope.go` (add `BuildEnvelopeTyped`)
- Modify: `lib/signal/builder.go` (add `BuildTyped`)

### Sub-task 3A: Write failing test for `lib/sensor/shape.go`

- [ ] **Step 3A.1: Create the test file with a round-trip case for the computational fixture**

Create `lib/sensor/shape_test.go`:
```go
package sensor_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

// canonicalize re-serializes a map[string]interface{} through json.Marshal
// + json.Unmarshal so all numeric values are float64 (matching how
// json.Unmarshal decodes numbers into interface{}). Without this step
// reflect.DeepEqual compares int vs float64 and always returns false.
func canonicalize(t *testing.T, m map[string]interface{}) map[string]interface{} {
	t.Helper()
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("canonicalize marshal: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("canonicalize unmarshal: %v", err)
	}
	return out
}

func roundTripSensor(t *testing.T, orig map[string]interface{}) {
	t.Helper()
	body, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal orig: %v", err)
	}
	var typed sensor.Sensor
	if err := json.Unmarshal(body, &typed); err != nil {
		t.Fatalf("unmarshal -> Sensor: %v", err)
	}
	got := typed.AsMap()
	want := canonicalize(t, orig)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip diff\nwant=%#v\n got=%#v", want, got)
	}
}

func TestSensorShape_RoundTrip_Computational(t *testing.T) {
	roundTripSensor(t, testfixtures.ValidSensorComputational())
}

func TestSensorShape_RoundTrip_Inferential(t *testing.T) {
	roundTripSensor(t, testfixtures.ValidSensorInferential())
}

func TestSensorShape_RoundTrip_Setup(t *testing.T) {
	roundTripSensor(t, testfixtures.ValidSensorSetup())
}
```

- [ ] **Step 3A.2: Run the test to confirm it fails (no `Sensor` type yet)**

```bash
go test ./lib/sensor/ -run TestSensorShape_RoundTrip
```
Expected: FAIL with `undefined: sensor.Sensor`.

### Sub-task 3B: Implement `lib/sensor/shape.go`

- [ ] **Step 3B.1: Create the shape file with the full typed surface**

Create `lib/sensor/shape.go`:
```go
package sensor

import "encoding/json"

// Sensor is the typed view of a sensor.json file. Mirrors the schema
// shape one-to-one with JSON tags. Optional fields use pointers or
// omitempty so absence is distinguishable from zero.
type Sensor struct {
	ID              string           `json:"id"`
	Version         string           `json:"version"`
	Name            string           `json:"name"`
	Description     string           `json:"description"`
	Kind            Kind             `json:"kind"`
	Type            Type             `json:"type"`
	Regulation      Regulation       `json:"regulation"`
	Phase           Phase            `json:"phase"`
	Determinism    Determinism       `json:"determinism"`
	Output          Output           `json:"output"`
	Cost            Cost             `json:"cost"`
	Triggers        []Trigger        `json:"triggers"`
	Requires        []Requirement    `json:"requires,omitempty"`
	Execution       Execution        `json:"execution"`
	SelfCorrection  *SelfCorrection  `json:"self_correction,omitempty"`
	Verification    Verification     `json:"verification"`
	BlindSpots      []string         `json:"blind_spots,omitempty"`
	Calibration     *Calibration     `json:"calibration,omitempty"`
	References      []string         `json:"references,omitempty"`
}

type Cost struct {
	Class                     CostClass   `json:"class"`
	Latency                   Latency     `json:"latency"`
	Tokens                    *Tokens     `json:"tokens,omitempty"`
	Compute                   *Compute    `json:"compute,omitempty"`
	MonetaryEstimateUSDPerRun *float64    `json:"monetary_estimate_usd_per_run,omitempty"`
	Guardrails                *Guardrails `json:"guardrails,omitempty"`
}

type Latency struct {
	P50MS     int  `json:"p50_ms"`
	P95MS     int  `json:"p95_ms"`
	TimeoutMS *int `json:"timeout_ms,omitempty"`
}

type Tokens struct {
	Model     string `json:"model"`
	InputAvg  int    `json:"input_avg"`
	OutputAvg int    `json:"output_avg"`
	MaxOutput int    `json:"max_output"`
}

type Compute struct {
	CPU      CPUClass `json:"cpu"`
	MemoryMB int      `json:"memory_mb"`
}

type Guardrails struct {
	OnTimeout       string  `json:"on_timeout,omitempty"`
	OnTokenOverrun  string  `json:"on_token_overrun,omitempty"`
	OnPhaseMismatch string  `json:"on_phase_mismatch,omitempty"`
	FallbackSensor  *string `json:"fallback_sensor,omitempty"`
}

type Trigger struct {
	On      TriggerOn `json:"on"`
	When    string    `json:"when,omitempty"`
	Cadence string    `json:"cadence,omitempty"`
}

// Requirement is a flat discriminated union keyed by Kind. Fields not
// applicable to a given kind are zero-valued and omitted from JSON via
// omitempty. This mirrors how lib/sensor/requires.go and Project()
// already read the array.
type Requirement struct {
	Kind        RequirementKind    `json:"kind"`
	ID          string             `json:"id,omitempty"`
	Name        string             `json:"name,omitempty"`
	Description string             `json:"description,omitempty"`
	Optional    *bool              `json:"optional,omitempty"`
	Path        string             `json:"path,omitempty"`
	Scope       string             `json:"scope,omitempty"`
	Command     string             `json:"command,omitempty"`
	TimeoutMS   *int               `json:"timeout_ms,omitempty"`
	ExitCodeMap []ExitCodeMapEntry `json:"exit_code_map,omitempty"`
}

type Execution struct {
	Command            string            `json:"command"`
	Env                map[string]string `json:"env,omitempty"`
	Blocking           bool              `json:"blocking,omitempty"`
	GracefulTimeoutMS  *int              `json:"graceful_timeout_ms,omitempty"`
	Teardown           []LifecycleStep   `json:"teardown,omitempty"`
	ExitCodeMap        []ExitCodeMapEntry `json:"exit_code_map,omitempty"`
	OutputParsing      *OutputParsing    `json:"output_parsing,omitempty"`
	Model              string            `json:"model,omitempty"`
	SystemPrompt       string            `json:"system_prompt,omitempty"`
	UserPromptTemplate string            `json:"user_prompt_template,omitempty"`
	Decoding           *Decoding         `json:"decoding,omitempty"`
}

type LifecycleStep struct {
	Command     string             `json:"command"`
	TimeoutMS   *int               `json:"timeout_ms,omitempty"`
	ExitCodeMap []ExitCodeMapEntry `json:"exit_code_map,omitempty"`
}

// ExitCodeMapEntry's ExitCode field accepts either an integer or the
// literal "*" wildcard. Using interface{} preserves the schema's oneOf;
// callers inspect the runtime type.
type ExitCodeMapEntry struct {
	ExitCode interface{} `json:"exit_code"`
	Verdict  string      `json:"verdict"`
	Severity string      `json:"severity"`
}

type OutputParsing struct {
	Patterns []Pattern `json:"patterns"`
}

type Pattern struct {
	Regex    string    `json:"regex"`
	Verdict  string    `json:"verdict"`
	Severity string    `json:"severity"`
	Captures *Captures `json:"captures,omitempty"`
}

type Captures struct {
	File      *int `json:"file,omitempty"`
	LineStart *int `json:"line_start,omitempty"`
	LineEnd   *int `json:"line_end,omitempty"`
	Excerpt   *int `json:"excerpt,omitempty"`
	Rationale *int `json:"rationale,omitempty"`
}

type Decoding struct {
	Temperature float64  `json:"temperature"`
	TopP        *float64 `json:"top_p,omitempty"`
	MaxTokens   int      `json:"max_tokens"`
	Seed        *int     `json:"seed,omitempty"`
}

type SelfCorrection struct {
	MaxRetries *int    `json:"max_retries,omitempty"`
	OnWarn     string  `json:"on_warn,omitempty"`
	OnFail     string  `json:"on_fail,omitempty"`
	OnError    string  `json:"on_error,omitempty"`
	Escalation string  `json:"escalation,omitempty"`
}

type Verification struct {
	GoldenCases     []GoldenCase `json:"golden_cases"`
	SelfTestCommand string       `json:"self_test_command,omitempty"`
}

type GoldenCase struct {
	Fixture          string `json:"fixture"`
	ExpectedVerdict  string `json:"expected_verdict"`
	ExpectedSeverity string `json:"expected_severity"`
	Notes            string `json:"notes,omitempty"`
}

type Calibration struct {
	ConfidenceThreshold float64 `json:"confidence_threshold"`
	DriftCheckCadence   string  `json:"drift_check_cadence,omitempty"`
	CalibrationSet      string  `json:"calibration_set"`
	CalibrationSize     int     `json:"calibration_size"`
	CalibrationDate     string  `json:"calibration_date"`
}

// Kind is the enum from sensor.json::properties.kind.
type Kind string

const (
	KindObservation Kind = "observation"
	KindAssertion   Kind = "assertion"
	KindSetup       Kind = "setup"
)

// Type is the enum from sensor.json::properties.type.
type Type string

const (
	TypeComputational Type = "computational"
	TypeInferential   Type = "inferential"
)

type Regulation string

const (
	RegulationMaintainability      Regulation = "maintainability"
	RegulationArchitectureFitness  Regulation = "architecture-fitness"
	RegulationBehaviour            Regulation = "behaviour"
)

type Phase string

const (
	PhasePreCommit        Phase = "pre-commit"
	PhasePreMerge         Phase = "pre-merge"
	PhasePostIntegration  Phase = "post-integration"
	PhaseContinuous       Phase = "continuous"
	PhaseOnDemand         Phase = "on-demand"
)

type Determinism string

const (
	DeterminismHigh   Determinism = "high"
	DeterminismMedium Determinism = "medium"
	DeterminismLow    Determinism = "low"
)

type Output string

const (
	OutputSingle Output = "single"
	OutputStream Output = "stream"
)

type CostClass string

const (
	CostClassCheap     CostClass = "cheap"
	CostClassMedium    CostClass = "medium"
	CostClassExpensive CostClass = "expensive"
)

type CPUClass string

const (
	CPULow    CPUClass = "low"
	CPUMedium CPUClass = "medium"
	CPUHigh   CPUClass = "high"
)

type TriggerOn string

const (
	TriggerPullRequest    TriggerOn = "pull-request"
	TriggerFileChange     TriggerOn = "file-change"
	TriggerCron           TriggerOn = "cron"
	TriggerMetricAnomaly  TriggerOn = "metric-anomaly"
	TriggerManual         TriggerOn = "manual"
	TriggerAgentRequest   TriggerOn = "agent-request"
)

type RequirementKind string

const (
	RequireSensor     RequirementKind = "sensor"
	RequireTool       RequirementKind = "tool"
	RequireEnv        RequirementKind = "env"
	RequireContext    RequirementKind = "context"
	RequirePermission RequirementKind = "permission"
	RequireStep       RequirementKind = "step"
)

// AsMap returns the map[string]interface{} representation by JSON
// round-trip. Used by call sites that still consume sensor data as
// loosely-typed maps (validator input, signal metadata payloads).
func (s *Sensor) AsMap() map[string]interface{} {
	body, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil
	}
	return out
}
```

- [ ] **Step 3B.2: Run the round-trip tests to verify they pass**

```bash
go test ./lib/sensor/ -run TestSensorShape_RoundTrip -v
```
Expected: three PASS lines.

If a test fails with a key diff, the failure is almost always one of:
1. A missing field in `Sensor` or one of its nested types (look at the diff and add it).
2. A field marked `omitempty` that should be required (or vice versa).
3. An enum-typed field whose JSON tag is wrong.

Fix the struct and re-run. Do NOT change the test or the fixture to match the struct.

### Sub-task 3C: Write failing test for `lib/signal/shape.go`

- [ ] **Step 3C.1: Create the test file with a round-trip case and a Builder-parity case**

Create `lib/signal/shape_test.go`:
```go
package signal_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/signal"
)

func canonicalSignalMap() map[string]interface{} {
	return map[string]interface{}{
		"sensor_id":   "demo",
		"version":     "0.1.0",
		"run_id":      "00000000-0000-4000-8000-000000000000",
		"started_at":  "2026-05-12T12:00:00Z",
		"finished_at": "2026-05-12T12:00:01Z",
		"verdict":     "pass",
		"severity":    "info",
		"confidence":  1.0,
		"evidence": []interface{}{
			map[string]interface{}{"rationale": "smoke fixture"},
		},
		"cost_actual": map[string]interface{}{"latency_ms": 12},
		"metadata":    map[string]interface{}{"kind": "aggregate"},
	}
}

func canonicalize(t *testing.T, m map[string]interface{}) map[string]interface{} {
	t.Helper()
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func TestSignalShape_RoundTrip(t *testing.T) {
	orig := canonicalSignalMap()
	body, _ := json.Marshal(orig)
	var typed signal.Signal
	if err := json.Unmarshal(body, &typed); err != nil {
		t.Fatalf("unmarshal -> Signal: %v", err)
	}
	got := typed.AsMap()
	want := canonicalize(t, orig)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip diff\nwant=%#v\n got=%#v", want, got)
	}
}

func TestSignalShape_BuilderParity(t *testing.T) {
	b := signal.NewBuilder("demo", "0.1.0").
		WithVerdict("pass", "info").
		WithKind("aggregate").
		WithRationale("smoke fixture").
		WithLatencyMS(12).
		WithRunID(
			"00000000-0000-4000-8000-000000000000",
			"2026-05-12T12:00:00Z",
			"2026-05-12T12:00:01Z",
		)
	asMap := b.Build()
	asTypedMap := b.BuildTyped().AsMap()
	if !reflect.DeepEqual(canonicalize(t, asMap), canonicalize(t, asTypedMap)) {
		t.Fatalf("Build vs BuildTyped diff\nmap=%#v\n typed=%#v", asMap, asTypedMap)
	}
}
```

- [ ] **Step 3C.2: Run the test to confirm it fails**

```bash
go test ./lib/signal/ -run TestSignalShape -v
```
Expected: FAIL with `undefined: signal.Signal` and `b.BuildTyped undefined`.

### Sub-task 3D: Implement `lib/signal/shape.go`

- [ ] **Step 3D.1: Create the signal shape file**

Create `lib/signal/shape.go`:
```go
package signal

import "encoding/json"

// Signal is the typed view of a signal.json instance. Mirrors the schema
// shape one-to-one with JSON tags. Optional fields use pointers or
// omitempty so absence is distinguishable from zero.
type Signal struct {
	SensorID    string                 `json:"sensor_id"`
	Version     string                 `json:"version"`
	RunID       string                 `json:"run_id"`
	StartedAt   string                 `json:"started_at"`
	FinishedAt  string                 `json:"finished_at"`
	Verdict     Verdict                `json:"verdict"`
	Severity    Severity               `json:"severity"`
	Score       *float64               `json:"score,omitempty"`
	Confidence  float64                `json:"confidence"`
	Evidence    []Evidence             `json:"evidence"`
	Remediation *Remediation           `json:"remediation,omitempty"`
	CostActual  CostActual             `json:"cost_actual"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

type Evidence struct {
	File      string `json:"file,omitempty"`
	LineStart *int   `json:"line_start,omitempty"`
	LineEnd   *int   `json:"line_end,omitempty"`
	Excerpt   string `json:"excerpt,omitempty"`
	Rationale string `json:"rationale"`
}

type Remediation struct {
	Instructions   string          `json:"instructions,omitempty"`
	SuggestedEdits []SuggestedEdit `json:"suggested_edits,omitempty"`
	References     []string        `json:"references,omitempty"`
}

type SuggestedEdit struct {
	File  string `json:"file"`
	Patch string `json:"patch"`
}

type CostActual struct {
	LatencyMS    int     `json:"latency_ms"`
	InputTokens  *int    `json:"input_tokens,omitempty"`
	OutputTokens *int    `json:"output_tokens,omitempty"`
	Model        *string `json:"model,omitempty"`
}

// Verdict is the enum from signal.json::$defs/Verdict.
type Verdict string

const (
	VerdictPass  Verdict = "pass"
	VerdictWarn  Verdict = "warn"
	VerdictFail  Verdict = "fail"
	VerdictError Verdict = "error"
)

// Severity is the enum from signal.json::$defs/Severity.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// AsMap returns the map[string]interface{} representation by JSON
// round-trip. Used by call sites that still consume signals as
// loosely-typed maps.
func (s *Signal) AsMap() map[string]interface{} {
	body, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil
	}
	return out
}
```

- [ ] **Step 3D.2: Add `BuildTyped` to `lib/signal/builder.go`**

Open `lib/signal/builder.go`. At the end of the file (after the closing `}` of `Build()`), append:

```go

// BuildTyped is the typed companion to Build(). Returns Signal instead of
// map[string]interface{}. Useful when downstream code wants struct
// access; falls back to AsMap() for map-consuming APIs.
func (b *Builder) BuildTyped() Signal {
	m := b.Build()
	body, err := json.Marshal(m)
	if err != nil {
		return Signal{}
	}
	var s Signal
	if err := json.Unmarshal(body, &s); err != nil {
		return Signal{}
	}
	return s
}
```

If `lib/signal/builder.go` does not already import `"encoding/json"`, add it to the existing import block.

- [ ] **Step 3D.3: Run the signal tests to verify they pass**

```bash
go test ./lib/signal/ -v
```
Expected: all existing tests + `TestSignalShape_RoundTrip` + `TestSignalShape_BuilderParity` pass.

### Sub-task 3E: Add `BuildEnvelopeTyped` to `lib/sensor/envelope.go`

- [ ] **Step 3E.1: Write a failing test for the typed envelope helper**

Append to `lib/sensor/envelope_test.go` (creating it as `package sensor_test` if not present — verify by reading the existing file first):

```go
func TestBuildEnvelopeTyped(t *testing.T) {
	prev := sensor.NowFn
	defer func() { sensor.NowFn = prev }()
	sensor.NowFn = func() time.Time {
		return time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	}
	s := &sensor.Sensor{ID: "demo", Version: "0.1.0", Type: sensor.TypeComputational}
	env := sensor.BuildEnvelopeTyped(s)
	if env.SensorID != "demo" || env.Version != "0.1.0" || env.SensorType != "computational" {
		t.Fatalf("envelope mismatch: %+v", env)
	}
	if env.RunID == "" {
		t.Fatalf("run id was empty")
	}
}
```

Confirm the existing imports include `time`, `testing`, and the local `sensor` package via `github.com/iurykrieger/harness-framework/lib/sensor`. Add them if not.

- [ ] **Step 3E.2: Run the test to confirm it fails**

```bash
go test ./lib/sensor/ -run TestBuildEnvelopeTyped -v
```
Expected: FAIL with `BuildEnvelopeTyped undefined`.

- [ ] **Step 3E.3: Implement `BuildEnvelopeTyped`**

Open `lib/sensor/envelope.go`. Append after the existing `BuildEnvelope` function:

```go

// BuildEnvelopeTyped is the typed companion to BuildEnvelope. Produces
// the same Envelope from a *Sensor instead of a map. Behaviour and
// error semantics are identical except inputs cannot be nil.
func BuildEnvelopeTyped(s *Sensor) Envelope {
	return Envelope{
		SensorID:   s.ID,
		Version:    s.Version,
		RunID:      NewRunIDFn(),
		StartedAt:  NowFn().Format("2006-01-02T15:04:05Z"),
		SensorType: string(s.Type),
	}
}
```

- [ ] **Step 3E.4: Run the test to verify it passes**

```bash
go test ./lib/sensor/ -run TestBuildEnvelopeTyped -v
```
Expected: PASS.

### Sub-task 3F: Run the full test matrix and commit

- [ ] **Step 3F.1: Run all tests**

```bash
go test ./...
go test ./... -tags=integration
go test -tags=run_computational ./...
go test -tags=run_inferential ./...
go vet ./...
```
Expected: every invocation exits 0.

- [ ] **Step 3F.2: Commit**

```bash
git add lib/sensor/shape.go lib/sensor/shape_test.go lib/sensor/envelope.go lib/sensor/envelope_test.go
git add lib/signal/shape.go lib/signal/shape_test.go lib/signal/builder.go
git commit -m "$(cat <<'EOF'
refactor(sensor,signal): add typed shape.go mirroring JSON schemas

Adds lib/sensor/shape.go and lib/signal/shape.go: typed Go struct mirrors
of schemas/sensor.json and schemas/signal.json. Mirrors lib/stack/shape.go
in style (typed enums + const blocks, omitempty for optional fields,
pointers for nullable-with-distinct-absent semantics).

- lib/sensor: Sensor + Cost, Latency, Tokens, Compute, Guardrails,
  Trigger, Requirement (flat discriminated union keyed by kind),
  Execution, LifecycleStep, ExitCodeMapEntry, OutputParsing, Pattern,
  Captures, Decoding, SelfCorrection, Verification, GoldenCase,
  Calibration. AsMap() bridge for map-consuming call sites.
- lib/signal: Signal + Evidence, Remediation, SuggestedEdit, CostActual.
  Verdict and Severity enums. AsMap() bridge.
- BuildEnvelopeTyped(*Sensor) Envelope in lib/sensor/envelope.go.
- BuildTyped() Signal in lib/signal/builder.go.

Round-trip tests confirm json.Marshal(map) -> json.Unmarshal(Sensor) ->
AsMap() == canonicalize(map) for all three canonical fixtures. Builder
parity test confirms Build() and BuildTyped().AsMap() agree.
EOF
)"
```

---

## Task 4: Canonical fixtures as testdata JSON + sensortest loader

**Why fourth:** Once `Sensor` exists (Task 3), the canonical fixtures stop needing Go map literals. Convert them to JSON files in `lib/sensor/testdata/`; introduce `lib/sensor/sensortest/` to load them. This is the first step of dismantling `lib/testfixtures/`.

**Files:**
- Create: `lib/sensor/testdata/canonical-computational.json`
- Create: `lib/sensor/testdata/canonical-inferential.json`
- Create: `lib/sensor/testdata/canonical-setup.json`
- Create: `lib/sensor/sensortest/canonical.go`
- Create: `lib/sensor/sensortest/canonical_test.go`
- Modify: all files that today call `testfixtures.ValidSensor*` (listed below)

### Sub-task 4A: Generate the three canonical JSON files

- [ ] **Step 4A.1: Add a one-shot Go program to emit each fixture as canonical JSON**

This is a throw-away helper inside `lib/testfixtures/dump_test.go` — its only purpose is to materialize the maps as JSON files. After the JSON files exist, this helper is removed (deletion happens automatically when `lib/testfixtures/` is removed in Task 6).

Create `lib/testfixtures/dump_test.go`:
```go
package testfixtures

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestDumpCanonical writes the three canonical fixtures to
// lib/sensor/testdata/canonical-*.json. Idempotent. Run it once to
// generate the files; subsequent runs overwrite with the same content.
//
// Tagged with the env knob HARNESS_DUMP_FIXTURES=1 so it does not run
// during normal go test ./...
func TestDumpCanonical(t *testing.T) {
	if os.Getenv("HARNESS_DUMP_FIXTURES") != "1" {
		t.Skip("set HARNESS_DUMP_FIXTURES=1 to dump")
	}
	_, thisFile, _, _ := runtime.Caller(0)
	// .../lib/testfixtures/dump_test.go -> ../sensor/testdata
	dir := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "sensor", "testdata"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := map[string]map[string]interface{}{
		"canonical-computational.json": ValidSensorComputational(),
		"canonical-inferential.json":   ValidSensorInferential(),
		"canonical-setup.json":         ValidSensorSetup(),
	}
	for name, m := range cases {
		body, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		body = append(body, '\n')
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}
```

- [ ] **Step 4A.2: Run the dumper**

```bash
HARNESS_DUMP_FIXTURES=1 go test ./lib/testfixtures/ -run TestDumpCanonical -v
```
Expected: PASS. After the run, `lib/sensor/testdata/canonical-{computational,inferential,setup}.json` exist.

Verify:
```bash
ls lib/sensor/testdata/
```
Expected: at least three new files.

- [ ] **Step 4A.3: Remove the dumper test file**

```bash
git rm lib/testfixtures/dump_test.go
```
(The fixture builders themselves stay in `lib/testfixtures/sensor.go` until Task 6.)

### Sub-task 4B: Create the `sensortest` package and loader

- [ ] **Step 4B.1: Write a failing test that asserts the loaders return `*Sensor` and the JSON validates**

Create `lib/sensor/sensortest/canonical_test.go`:
```go
package sensortest_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

func schemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	// .../lib/sensor/sensortest/canonical_test.go -> 3 levels up to repo root.
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "schemas"))
}

func TestCanonicalLoadersReturnSensor(t *testing.T) {
	for _, tc := range []struct {
		name string
		load func(*testing.T) any
	}{
		{"computational", func(t *testing.T) any { return sensortest.LoadComputational(t) }},
		{"inferential", func(t *testing.T) any { return sensortest.LoadInferential(t) }},
		{"setup", func(t *testing.T) any { return sensortest.LoadSetup(t) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.load(t)
			if s == nil {
				t.Fatalf("loader returned nil")
			}
		})
	}
}

func TestCanonicalJSONValidatesAgainstSchema(t *testing.T) {
	v, err := schema.NewValidator(schemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		load func(*testing.T) any
	}{
		{"computational", func(t *testing.T) any { return sensortest.LoadComputational(t).AsMap() }},
		{"inferential", func(t *testing.T) any { return sensortest.LoadInferential(t).AsMap() }},
		{"setup", func(t *testing.T) any { return sensortest.LoadSetup(t).AsMap() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := v.Validate(schema.TargetSensor, tc.load(t)); err != nil {
				t.Fatalf("schema validate: %v", err)
			}
		})
	}
}
```

- [ ] **Step 4B.2: Run the test to confirm it fails**

```bash
go test ./lib/sensor/sensortest/ -v
```
Expected: FAIL with `package sensortest is not in...` or `undefined: sensortest.LoadComputational`.

- [ ] **Step 4B.3: Implement the loader**

Create `lib/sensor/sensortest/canonical.go`:
```go
// Package sensortest exposes test helpers that load canonical sensor
// fixtures from lib/sensor/testdata/. The package depends only on
// lib/sensor; production code MUST NOT import it.
package sensortest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// LoadComputational returns the canonical computational sensor.
func LoadComputational(t *testing.T) *sensor.Sensor { return load(t, "canonical-computational.json") }

// LoadInferential returns the canonical inferential sensor.
func LoadInferential(t *testing.T) *sensor.Sensor { return load(t, "canonical-inferential.json") }

// LoadSetup returns the canonical setup sensor.
func LoadSetup(t *testing.T) *sensor.Sensor { return load(t, "canonical-setup.json") }

func load(t *testing.T, name string) *sensor.Sensor {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	// .../lib/sensor/sensortest/canonical.go -> ../testdata
	p := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "testdata", name))
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	var s sensor.Sensor
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("unmarshal %s: %v", p, err)
	}
	return &s
}
```

- [ ] **Step 4B.4: Run the sensortest tests to verify they pass**

```bash
go test ./lib/sensor/sensortest/ -v
```
Expected: both tests PASS.

### Sub-task 4C: Migrate every `testfixtures.ValidSensor*` call site

The following files reference `testfixtures.ValidSensor*` and must be updated:
- `lib/schema/validator_test.go` (~32 references)
- `lib/sensor/persist_test.go`
- `lib/sensor/envelope_test.go`
- `lib/cli/bootstrap_test.go`
- `lib/heal/heal_e2e_test.go` (moved here in Task 1; was `test/heal-e2e/heal_e2e_test.go`)
- `lib/sensor/shape_test.go` (added in Task 3 — still uses testfixtures; switch now)

- [ ] **Step 4C.1: Replace each call site**

For each file, apply this transformation:

| Before | After |
|---|---|
| `testfixtures.ValidSensorComputational()` | `sensortest.LoadComputational(t).AsMap()` |
| `testfixtures.ValidSensorInferential()` | `sensortest.LoadInferential(t).AsMap()` |
| `testfixtures.ValidSensorSetup()` | `sensortest.LoadSetup(t).AsMap()` |

And add to imports of each touched file:
```go
"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
```

Remove the `lib/testfixtures` import from each file IF AND ONLY IF that file no longer references `testfixtures.*` after this step. Files that still call `testfixtures.RepoSchemasDir(...)` or `testfixtures.FreezeClock/WithRunDir` keep the import — they will be migrated in Tasks 5 and 6.

Apply the change one file at a time and run `go test ./<pkg>/` after each so failures stay localized.

Concretely:
1. Edit `lib/schema/validator_test.go`, run `go test ./lib/schema/`.
2. Edit `lib/sensor/persist_test.go`, run `go test ./lib/sensor/`.
3. Edit `lib/sensor/envelope_test.go`, run `go test ./lib/sensor/`.
4. Edit `lib/cli/bootstrap_test.go`, run `go test ./lib/cli/`.
5. Edit `lib/heal/heal_e2e_test.go`, run `go test ./lib/heal/`.
6. Edit `lib/sensor/shape_test.go`, run `go test ./lib/sensor/`.

Each should be PASS before moving on.

- [ ] **Step 4C.2: Confirm `lib/testfixtures/sensor.go` no longer has callers**

```bash
grep -rn 'testfixtures\.ValidSensor' --include='*.go' .
```
Expected: no output.

- [ ] **Step 4C.3: Run the full matrix**

```bash
go test ./...
go test ./... -tags=integration
go test -tags=run_computational ./...
go test -tags=run_inferential ./...
go vet ./...
```
Expected: all green.

- [ ] **Step 4C.4: Commit**

```bash
git add lib/sensor/testdata/ lib/sensor/sensortest/ lib/schema/validator_test.go lib/sensor/persist_test.go lib/sensor/envelope_test.go lib/cli/bootstrap_test.go lib/heal/heal_e2e_test.go lib/sensor/shape_test.go
git commit -m "$(cat <<'EOF'
refactor(sensor): canonical fixtures as testdata JSON + sensortest loader

- lib/sensor/testdata/canonical-{computational,inferential,setup}.json:
  the three canonical fixtures, JSON-serialized from the existing
  testfixtures.ValidSensor*() builders.
- lib/sensor/sensortest/canonical.go: thin Load*(t) helpers returning
  *sensor.Sensor via json.Unmarshal.
- lib/sensor/sensortest/canonical_test.go: round-trip + schema validation
  for each fixture against schemas/sensor.json.
- All call sites in lib/schema, lib/sensor, lib/cli, lib/heal switch
  from testfixtures.ValidSensor*() to sensortest.Load*(t).AsMap().

lib/testfixtures/sensor.go is still referenced by Task 5 and 6 work
(RepoSchemasDir, FreezeClock, WithRunDir) -- it dies in Task 6.
EOF
)"
```

---

## Task 5: schematest package for RepoSchemasDir

**Why fifth:** `RepoSchemasDir` is the cross-cutting helper with the widest reach (~50 call sites). Moving it before the final `testfixtures` deletion keeps each step independently verifiable.

**Files:**
- Create: `lib/schema/schematest/repodir.go`
- Modify: every file calling `testfixtures.RepoSchemasDir(t)` (listed below)

### Sub-task 5A: Create the schematest package

- [ ] **Step 5A.1: Write a failing test that asserts `RepoSchemasDir` returns a valid path**

Create `lib/schema/schematest/repodir_test.go`:
```go
package schematest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
)

func TestRepoSchemasDir_ReturnsValidPath(t *testing.T) {
	dir := schematest.RepoSchemasDir(t)
	for _, name := range []string{"sensor.json", "signal.json", "stack.json"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s in returned dir: %v", name, err)
		}
	}
}
```

- [ ] **Step 5A.2: Run the test to confirm it fails**

```bash
go test ./lib/schema/schematest/ -v
```
Expected: FAIL with `package schematest is not in...` or `undefined: schematest.RepoSchemasDir`.

- [ ] **Step 5A.3: Implement `RepoSchemasDir`**

Create `lib/schema/schematest/repodir.go`:
```go
// Package schematest exposes test helpers that resolve the in-repo
// schemas/ directory. Production code MUST NOT import it.
package schematest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// RepoSchemasDir returns the absolute path to <repo>/schemas/, resolved
// from this file's own location via runtime.Caller. Independent of cwd.
func RepoSchemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// .../lib/schema/schematest/repodir.go -> 3 levels up to repo root.
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	dir := filepath.Join(repoRoot, "schemas")
	if _, err := os.Stat(filepath.Join(dir, "sensor.json")); err != nil {
		t.Fatalf("schemas dir not where expected (%s): %v", dir, err)
	}
	return dir
}
```

- [ ] **Step 5A.4: Run the test to verify it passes**

```bash
go test ./lib/schema/schematest/ -v
```
Expected: PASS.

### Sub-task 5B: Migrate every `testfixtures.RepoSchemasDir` call site

- [ ] **Step 5B.1: Replace each call site**

Find all call sites:
```bash
grep -rn 'testfixtures\.RepoSchemasDir' --include='*.go' .
```

For each file, apply this transformation:

| Before | After |
|---|---|
| `testfixtures.RepoSchemasDir(t)` | `schematest.RepoSchemasDir(t)` |

And add the new import to each touched file:
```go
"github.com/iurykrieger/harness-framework/lib/schema/schematest"
```

Remove the `lib/testfixtures` import IF AND ONLY IF the file no longer references `testfixtures.*` after this step.

Concrete files to edit (per the grep — adjust if a new caller appeared):
- `lib/cli/bootstrap_test.go`
- `lib/schema/discover_test.go`
- `lib/schema/validator_test.go`
- `lib/sensor/envelope_test.go`
- `lib/sensor/persist_test.go`
- `lib/registry/sanitize_test.go`
- `lib/orchestrator/run_test.go`
- `lib/orchestrator/cascade_test.go`
- `lib/orchestrator/lifecycle_test.go`
- `lib/orchestrator/live_deps_test.go`
- `lib/orchestrator/preflight_test.go`
- `lib/subprocess/stream_test.go`
- `lib/heal/heal_e2e_test.go`

Apply one file at a time and run `go test ./<pkg>/` after each.

- [ ] **Step 5B.2: Confirm no more `RepoSchemasDir` callers exist on `testfixtures`**

```bash
grep -rn 'testfixtures\.RepoSchemasDir' --include='*.go' .
```
Expected: no output.

- [ ] **Step 5B.3: Run the full matrix**

```bash
go test ./...
go test ./... -tags=integration
go test -tags=run_computational ./...
go test -tags=run_inferential ./...
go vet ./...
```
Expected: all green.

- [ ] **Step 5B.4: Commit**

```bash
git add lib/schema/schematest/ lib/cli/bootstrap_test.go lib/schema/discover_test.go lib/schema/validator_test.go lib/sensor/envelope_test.go lib/sensor/persist_test.go lib/registry/sanitize_test.go lib/orchestrator/ lib/subprocess/stream_test.go lib/heal/heal_e2e_test.go
git commit -m "$(cat <<'EOF'
refactor(schema): introduce schematest package for RepoSchemasDir

- lib/schema/schematest/repodir.go: RepoSchemasDir(t) via runtime.Caller.
- All call sites switch from testfixtures.RepoSchemasDir(t) to
  schematest.RepoSchemasDir(t).

testfixtures.RepoSchemasDir is now dead in the call graph; its file
will be removed in the next commit along with FreezeClock and WithRunDir.
EOF
)"
```

---

## Task 6: Inline FreezeClock and WithRunDir; delete lib/testfixtures; CLAUDE.md rule 10

**Why sixth:** `lib/testfixtures/` now has only `clock.go::FreezeClock` (3 callers, 1 file) and `paths.go::WithRunDir` (2 callers, 1 file) as live symbols. Both inline cleanly into their sole consumer. After inlining, the package is empty and gets deleted. The taxonomy doc closes the loop.

**Files:**
- Modify: `lib/subprocess/stream_test.go` (inline both helpers)
- Delete: `lib/testfixtures/clock.go`
- Delete: `lib/testfixtures/paths.go`
- Delete: `lib/testfixtures/sensor.go`
- Delete: `lib/testfixtures/` (directory)
- Modify: `CLAUDE.md` (update rule 9 closing sentence; add rule 10)

### Sub-task 6A: Inline the two helpers

- [ ] **Step 6A.1: Open `lib/subprocess/stream_test.go` and add the two helpers at the bottom of the file**

Append after the last existing function:

```go
// freezeClock pins sensor.NowFn and sensor.NewRunIDFn for deterministic
// Signal output. Returns a restore function; defer it.
func freezeClock(t *testing.T) func() {
	t.Helper()
	origNow, origID := sensor.NowFn, sensor.NewRunIDFn
	frozen := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	sensor.NowFn = func() time.Time { return frozen }
	sensor.NewRunIDFn = func() string { return "00000000-0000-4000-8000-000000000000" }
	return func() { sensor.NowFn = origNow; sensor.NewRunIDFn = origID }
}

// withRunDir materializes a temp registry Root, a populated <run-id>/
// directory with empty raw.log and signals.log files. Returns the Root,
// the synthesized run_id (<pid>-<short>), and the run directory path.
func withRunDir(t testing.TB, sensorID, runIDSeed string) (root registry.Root, runID, runDir string) {
	t.Helper()
	proj := t.TempDir()
	root = registry.NewRoot(proj)
	if runIDSeed == "" {
		runIDSeed = fmt.Sprintf("%d-test0001", os.Getpid())
	}
	runID = runIDSeed
	runDir = root.RunDir(sensorID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir runDir: %v", err)
	}
	for _, fname := range []string{"raw.log", "signals.log"} {
		f, err := os.Create(filepath.Join(runDir, fname))
		if err != nil {
			t.Fatalf("create %s: %v", fname, err)
		}
		_ = f.Close()
	}
	return root, runID, runDir
}
```

Add to the import block (if not already present): `"fmt"`, `"os"`, `"path/filepath"`, `"time"`, and `"github.com/iurykrieger/harness-framework/lib/registry"`.

- [ ] **Step 6A.2: Replace all 5 call sites in the same file**

| Before | After |
|---|---|
| `testfixtures.FreezeClock(t)` | `freezeClock(t)` |
| `testfixtures.WithRunDir(t, X, Y)` | `withRunDir(t, X, Y)` |

Remove the `"github.com/iurykrieger/harness-framework/lib/testfixtures"` import line from `stream_test.go`.

- [ ] **Step 6A.3: Run the subprocess tests to verify they pass**

```bash
go test ./lib/subprocess/ -v
```
Expected: all PASS.

### Sub-task 6B: Delete `lib/testfixtures/`

- [ ] **Step 6B.1: Confirm no remaining importers**

```bash
grep -rn '"github.com/iurykrieger/harness-framework/lib/testfixtures"' --include='*.go' .
grep -rn 'testfixtures\.' --include='*.go' .
```
Expected: both return no output. If anything is left, fix it before proceeding.

- [ ] **Step 6B.2: Remove the directory**

```bash
git rm -r lib/testfixtures
```

- [ ] **Step 6B.3: Run the full test matrix**

```bash
go test ./...
go test ./... -tags=integration
go test -tags=run_computational ./...
go test -tags=run_inferential ./...
go vet ./...
```
Expected: all green.

### Sub-task 6C: Update CLAUDE.md

- [ ] **Step 6C.1: Edit rule 9's closing sentence**

Open `CLAUDE.md`. In rule 9 (line 31 currently), find:
```
Cross-package test fixtures live in `lib/testfixtures/` (regular Go package, not `_test.go`) so subpackage tests can import them.
```

Replace with:
```
Cross-package test fixtures follow the taxonomy in rule 10.
```

- [ ] **Step 6C.2: Add rule 10 immediately after rule 9**

After the line ending rule 9, insert (preserve indentation/blank lines as in the file):
```markdown
10. **Test data and test helpers are split by purpose.** Three locations, three purposes:
    - `<pkg>/testdata/` — Go-convention static fixtures (JSON, txt, jsonl, nested go.mod sub-modules). Per-package; consumed by `_test.go` of the same package or by another package's `<pkg>test` helper via relative path. Ignored by `go build`.
    - `lib/<pkg>/<pkg>test/` — Go test helpers (functions taking `*testing.T`) that load/decorate testdata for cross-package use. Each `<pkg>test` package is owned by exactly one `<pkg>` and depends only on `<pkg>` and the standard library / testing. Convention follows `net/http/httptest` and `testing/iotest`. The package is importable from production code in principle; do not do so.
    - `.harness/sensors/fixtures/` — sensor-domain fixture data referenced by `verification.golden_cases[].fixture` in sensor JSON. NOT a Go test fixture. Lives in the user project tree (under `.harness/`) and is consumed at sensor runtime.

    A single shared "fixtures" or "testhelpers" package across the whole `lib/` tree is explicitly disallowed.
```

- [ ] **Step 6C.3: Run `go test ./...` one more time (CLAUDE.md changes shouldn't affect builds, but confirm)**

```bash
go test ./...
```
Expected: PASS.

- [ ] **Step 6C.4: Commit**

```bash
git add lib/subprocess/stream_test.go CLAUDE.md
git rm -r lib/testfixtures 2>/dev/null || true
git add -A
git commit -m "$(cat <<'EOF'
refactor: inline FreezeClock and WithRunDir; delete lib/testfixtures

- lib/subprocess/stream_test.go absorbs freezeClock (3 callers in same
  file) and withRunDir (2 callers in same file) as unexported helpers.
- lib/testfixtures/ deleted: god-package whose three concerns were
  redistributed in Tasks 4-6 (canonical fixtures -> sensortest +
  testdata JSON; RepoSchemasDir -> schematest; clock/rundir helpers ->
  inlined).
- CLAUDE.md rule 9's closing sentence now points to rule 10. Rule 10
  formalizes the three-tier fixture taxonomy: <pkg>/testdata/,
  lib/<pkg>/<pkg>test/, and .harness/sensors/fixtures/.
EOF
)"
```

---

## Task 7: Misc consolidations (cli/flag, sensor/error, env_test split)

**Why seventh:** Three independent small consolidations, each visible from CLAUDE.md rule 9's spirit (avoid single-function files / single-concern test files in inconsistent packages). Pick them up as one commit since they're all under 30 LoC each.

**Files:**
- Delete: `lib/cli/flag.go`
- Modify: `lib/cli/bootstrap.go` (absorb `MultiFlag`)
- Delete: `lib/sensor/error.go`
- Modify: `lib/sensor/envelope.go` (absorb `BuildErrorSignal`)
- Modify: `lib/sensor/env_test.go` (strip non-env tests, switch to `package sensor_test`)
- Create: `lib/sensor/error_test.go`
- Create: `lib/sensor/missing_env_signal_test.go`

### Sub-task 7A: Fold `lib/cli/flag.go` into `bootstrap.go`

- [ ] **Step 7A.1: Append `MultiFlag` to `lib/cli/bootstrap.go`**

Open `lib/cli/bootstrap.go`. At the end of the file (after the closing `}` of `Bootstrap`), append:

```go

// MultiFlag implements flag.Value for repeatable string flags
// (--slot k=v --slot k2=v2).
type MultiFlag []string

func (m *MultiFlag) String() string     { return strings.Join(*m, ",") }
func (m *MultiFlag) Set(s string) error { *m = append(*m, s); return nil }
```

Add `"strings"` to the import block.

- [ ] **Step 7A.2: Delete `lib/cli/flag.go`**

```bash
git rm lib/cli/flag.go
```

- [ ] **Step 7A.3: Run tests**

```bash
go test ./lib/cli/ -v
go test ./...
```
Expected: PASS.

### Sub-task 7B: Fold `lib/sensor/error.go` into `envelope.go`

- [ ] **Step 7B.1: Append `BuildErrorSignal` to `lib/sensor/envelope.go`**

Open `lib/sensor/envelope.go`. After `BuildEnvelopeTyped` (added in Task 3E), append:

```go

// BuildErrorSignal constructs a Signal-shaped map representing the
// "sensor could not run" outcome. Verdict is error, severity high; the
// caller supplies the rationale (free-form explanation) and remediation
// instructions (imperative text the next agent turn should act on).
//
// The returned map already conforms to schemas/signal.json -- callers
// should still validate before emitting, in case envelope fields are
// malformed.
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
```

- [ ] **Step 7B.2: Delete `lib/sensor/error.go`**

```bash
git rm lib/sensor/error.go
```

- [ ] **Step 7B.3: Run tests**

```bash
go test ./lib/sensor/ -v
```
Expected: PASS.

### Sub-task 7C: Split `lib/sensor/env_test.go` into three files

The current file is in `package sensor` (white-box) and tests three concerns. Split into three files, each `package sensor_test`.

- [ ] **Step 7C.1: Read the current `lib/sensor/env_test.go`**

(For reference. The file you read in Step 7C.2 onward already exists at that path with the full content shown in the spec.)

- [ ] **Step 7C.2: Create `lib/sensor/error_test.go` with the `BuildErrorSignal` tests**

Create `lib/sensor/error_test.go`:
```go
package sensor_test

import (
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func stableNowError() time.Time {
	return time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
}

func TestBuildErrorSignal_ShapeAndRemediation(t *testing.T) {
	prev := sensor.NowFn
	defer func() { sensor.NowFn = prev }()
	sensor.NowFn = stableNowError

	env := sensor.Envelope{
		SensorID: "x", Version: "0.1.0", RunID: "abc",
		StartedAt: "2026-05-08T00:00:00Z", SensorType: "computational",
	}
	sig := sensor.BuildErrorSignal(env, "single", "missing required env var GITHUB_TOKEN", "export GITHUB_TOKEN and re-run")

	if sig["verdict"] != "error" || sig["severity"] != "high" {
		t.Fatalf("verdict/severity mismatch: %v %v", sig["verdict"], sig["severity"])
	}
	rem, ok := sig["remediation"].(map[string]interface{})
	if !ok {
		t.Fatalf("remediation missing")
	}
	if rem["instructions"] != "export GITHUB_TOKEN and re-run" {
		t.Fatalf("remediation.instructions=%v", rem["instructions"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "aggregate" || md["output_mode"] != "single" {
		t.Fatalf("metadata wrong: %+v", md)
	}
}

func TestBuildErrorSignal_OmitsRemediationWhenEmpty(t *testing.T) {
	env := sensor.Envelope{SensorID: "x", Version: "0.1.0", RunID: "r", StartedAt: "2026-05-08T00:00:00Z"}
	sig := sensor.BuildErrorSignal(env, "stream", "rationale", "")
	if _, ok := sig["remediation"]; ok {
		t.Fatalf("remediation should be omitted when empty")
	}
}
```

- [ ] **Step 7C.3: Create `lib/sensor/missing_env_signal_test.go` with the `BuildMissingEnvSignal` test**

Create `lib/sensor/missing_env_signal_test.go`:
```go
package sensor_test

import (
	"strings"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func stableNowMissingEnv() time.Time {
	return time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
}

func TestBuildMissingEnvSignal_ShapeWrappedToGate(t *testing.T) {
	prev := sensor.NowFn
	defer func() { sensor.NowFn = prev }()
	sensor.NowFn = stableNowMissingEnv

	env := sensor.Envelope{SensorID: "x", Version: "0.1.0", RunID: "r1", StartedAt: "2026-05-08T00:00:00Z"}
	missing := []sensor.MissingEnv{
		{Name: "GH_TOKEN", Description: "PAT"},
		{Name: "REGION"},
	}
	sig := sensor.BuildMissingEnvSignal(env, "stream", missing)

	if sig["verdict"] != "error" {
		t.Fatalf("verdict = %v", sig["verdict"])
	}
	ev := sig["evidence"].([]interface{})
	if len(ev) != 2 {
		t.Fatalf("evidence length = %d, want 2", len(ev))
	}
	md := sig["metadata"].(map[string]interface{})
	if md["heal_hint"] != "missing-env:GH_TOKEN" {
		t.Errorf("heal_hint = %v, want %q", md["heal_hint"], "missing-env:GH_TOKEN")
	}
	rem := sig["remediation"].(map[string]interface{})
	if !strings.Contains(rem["instructions"].(string), "GH_TOKEN") {
		t.Errorf("remediation missing GH_TOKEN: %v", rem["instructions"])
	}
}
```

- [ ] **Step 7C.4: Rewrite `lib/sensor/env_test.go` to keep only `CheckRequiredEnv` tests, in `package sensor_test`**

Overwrite `lib/sensor/env_test.go`:
```go
package sensor_test

import (
	"reflect"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func withFakeEnv(t *testing.T, env map[string]string) {
	t.Helper()
	prev := sensor.LookupEnvFn
	sensor.LookupEnvFn = func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
	t.Cleanup(func() { sensor.LookupEnvFn = prev })
}

func TestCheckRequiredEnv_NoRequires(t *testing.T) {
	got := sensor.CheckRequiredEnv(map[string]interface{}{})
	if got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestCheckRequiredEnv_EmptyEnv(t *testing.T) {
	got := sensor.CheckRequiredEnv(map[string]interface{}{
		"requires": []interface{}{},
	})
	if got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestCheckRequiredEnv_RequiredMissing(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	got := sensor.CheckRequiredEnv(map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "GITHUB_TOKEN", "description": "PAT"},
			map[string]interface{}{"kind": "env", "name": "GCP_PROJECT"},
		},
	})
	want := []sensor.MissingEnv{
		{Name: "GITHUB_TOKEN", Description: "PAT"},
		{Name: "GCP_PROJECT"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestCheckRequiredEnv_RequiredPresent(t *testing.T) {
	withFakeEnv(t, map[string]string{"GITHUB_TOKEN": "ghp_xxx"})
	got := sensor.CheckRequiredEnv(map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "GITHUB_TOKEN"},
		},
	})
	if got != nil {
		t.Fatalf("expected nil (env present), got %#v", got)
	}
}

func TestCheckRequiredEnv_OptionalMissingIsIgnored(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	got := sensor.CheckRequiredEnv(map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "DEBUG", "optional": true},
			map[string]interface{}{"kind": "env", "name": "REGION"},
		},
	})
	if len(got) != 1 || got[0].Name != "REGION" {
		t.Fatalf("expected only REGION missing, got %+v", got)
	}
}

func TestCheckRequiredEnv_MalformedEntriesIgnored(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	got := sensor.CheckRequiredEnv(map[string]interface{}{
		"requires": []interface{}{
			"not-an-object",
			map[string]interface{}{"kind": "env"},
			map[string]interface{}{"kind": "env", "name": ""},
			map[string]interface{}{"kind": "env", "name": "REAL_ONE"},
			map[string]interface{}{"kind": "env", "description": "orphan"},
		},
	})
	if len(got) != 1 || got[0].Name != "REAL_ONE" {
		t.Fatalf("expected only REAL_ONE, got %+v", got)
	}
}
```

`LookupEnvFn` and `MissingEnv` are already exported (verify with `grep -n 'LookupEnvFn' lib/sensor/env.go` — capital L). The black-box test above compiles as written.

- [ ] **Step 7C.5: Run the sensor tests**

```bash
go test ./lib/sensor/ -v
```
Expected: all tests PASS, including the three new files plus the existing `path_test`, `project_test`, `requires_test`, `persist_test`, `envelope_test`, `shape_test`.

### Sub-task 7D: Commit

- [ ] **Step 7D.1: Run the full matrix**

```bash
go test ./...
go test ./... -tags=integration
go test -tags=run_computational ./...
go test -tags=run_inferential ./...
go vet ./...
```
Expected: all green.

- [ ] **Step 7D.2: Commit**

```bash
git add lib/cli/bootstrap.go lib/sensor/envelope.go lib/sensor/env_test.go lib/sensor/error_test.go lib/sensor/missing_env_signal_test.go
git rm lib/cli/flag.go lib/sensor/error.go 2>/dev/null || true
git add -A
git commit -m "$(cat <<'EOF'
refactor: misc consolidations (cli/flag, sensor/error, env_test split)

- lib/cli/flag.go (10 LoC, single type) folded into bootstrap.go.
- lib/sensor/error.go (17 LoC, single function) folded into envelope.go
  (BuildErrorSignal builds from an Envelope, so co-location is natural).
- lib/sensor/env_test.go split into env_test.go (CheckRequiredEnv only),
  error_test.go (BuildErrorSignal), missing_env_signal_test.go
  (BuildMissingEnvSignal). env_test stays package sensor (white-box —
  needs lookupEnvFn seam); the other two are package sensor_test.
EOF
)"
```

---

## Task 8: orchestrator main_test.go portable watcher stub

**Why eighth:** Last cleanup. Independent of every other task. Improves portability of the orchestrator test suite without touching production code.

**Files:**
- Modify: `lib/orchestrator/main_test.go`

- [ ] **Step 8.1: Read the current file to confirm shape**

```bash
cat lib/orchestrator/main_test.go
```
Expected: the `TestMain` function copies `/usr/bin/true` to `<dir>/watcher`.

- [ ] **Step 8.2: Replace the body of `TestMain` with a portable stub**

Overwrite `lib/orchestrator/main_test.go`:
```go
package orchestrator_test

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	// Install a no-op watcher binary in the test binary's directory so
	// tests that reach the spawn-watcher step do not fail with "no such
	// file". The stub is a POSIX shell script that always exits 0;
	// behaviour-coverage for the real watcher lives in lib/watcher.
	exe, err := os.Executable()
	if err != nil {
		panic("TestMain: os.Executable failed: " + err.Error())
	}
	watcherPath := filepath.Join(filepath.Dir(exe), "watcher")
	if _, serr := os.Stat(watcherPath); os.IsNotExist(serr) {
		if werr := os.WriteFile(watcherPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); werr != nil {
			panic("TestMain: write watcher stub: " + werr.Error())
		}
	}
	os.Exit(m.Run())
}
```

- [ ] **Step 8.3: Run the orchestrator tests to verify they pass**

```bash
go test ./lib/orchestrator/ -v
```
Expected: PASS.

- [ ] **Step 8.4: Run the full matrix one final time**

```bash
go test ./...
go test ./... -tags=integration
go test -tags=run_computational ./...
go test -tags=run_inferential ./...
go vet ./...
```
Expected: all green.

- [ ] **Step 8.5: Commit**

```bash
git add lib/orchestrator/main_test.go
git commit -m "$(cat <<'EOF'
test(orchestrator): replace /usr/bin/true copy with portable stub

The previous TestMain copied /usr/bin/true to disk as a fake watcher
binary. That path is not guaranteed to exist on Alpine and on some
minimal Linux images. Replace with a literal #!/bin/sh\nexit 0\n
stub written by os.WriteFile; semantics (no-op exit 0) are identical.
EOF
)"
```

---

## Final verification (post-Task-8)

After all eight commits are in, run the DoD checks from the spec:

- [ ] **DoD 1–4: Test matrix**

```bash
go test ./... -tags=integration
go test -tags=run_computational ./...
go test -tags=run_inferential ./...
go vet -tags=run_computational ./...
go vet -tags=run_inferential ./...
```
Expected: all green.

- [ ] **DoD 5: scripts/ violation gone**

```bash
find . -type d -name scripts -maxdepth 2 -not -path "./.git/*"
```
Expected: only `./skills/*/scripts` (one path per skill).

- [ ] **DoD 6: test/ removed**

```bash
find . -type d -name test -maxdepth 2 -not -path "./.git/*"
```
Expected: empty.

- [ ] **DoD 7: shape round-trips**

```bash
go test ./lib/sensor/ -run TestSensorShape -v
go test ./lib/signal/ -run TestSignalShape -v
```
Expected: PASS.

- [ ] **DoD 8: testfixtures dead**

```bash
grep -rn 'testfixtures' --include='*.go' . | grep -v docs/superpowers
```
Expected: empty.

- [ ] **DoD 9: only two `<pkg>test` packages**

```bash
find lib -type d -name '*test' -not -path '*/testdata/*'
```
Expected: `lib/schema/schematest`, `lib/sensor/sensortest`.

- [ ] **DoD 10: canonical JSON validates against schema**

```bash
go test ./lib/sensor/sensortest/ -v
```
Expected: PASS.

- [ ] **DoD 11: CLAUDE.md taxonomy in place**

```bash
grep -n 'rule 10\|10\. \*\*Test data' CLAUDE.md
```
Expected: one or more matches showing rule 10's heading.

- [ ] **Open the PR**

Branch is `worktree-refactor-2`. Title: `refactor: structural cleanup (test moves, typed shapes, fixture taxonomy)`. Body links to `docs/superpowers/specs/2026-05-12-structural-refactor-design.md`. The eight commits stand on their own; reviewers can read them in order.
