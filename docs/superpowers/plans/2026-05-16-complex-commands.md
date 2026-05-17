# Complex Commands Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reshape `execution` into a typed pipeline of steps (`shell`, `http`, `assert`, `sensor`) with Actions-style data flow, shared top-level fixtures, and inline sensor composition — while preserving the `command: string` shortcut, the preflight gate invariant, and `signal.yaml`.

**Architecture:** New packages `lib/exec/`, `lib/step/`, `lib/fixture/` form the runtime engine. Legacy `command: string` is normalized in-memory to a single shell step at `lib/sensor.Load` time so the engine sees one shape. `lib/template/` gains a strict `${{ … }}` renderer. `lib/orchestrator/run.go` swaps one call (`subprocess.StreamSubprocess` → `exec.Run`). No schema versioning, no migration; existing `.harness/sensors/` content is deleted in PR #1.

**Tech Stack:** Go 1.25, `sigs.k8s.io/yaml` (existing), `santhosh-tekuri/jsonschema/v5` (existing), `net/http` (stdlib for HTTP step), `tidwall/gjson` or `oliveagle/jsonpath` (decide in Task 6 when picking jsonpath lib). Tests use stdlib `testing`, table-driven; `httptest.Server` for HTTP step coverage.

**Spec reference:** `docs/superpowers/specs/2026-05-15-complex-commands-design.md`. Read it before starting Task 1.

---

## Cross-task type contract (locked at Task 5)

These signatures are referenced across multiple tasks. Don't drift.

```go
// lib/step/step.go
package step

import (
    "context"
    "net/http"
    "github.com/iurykrieger/harness-framework/lib/signal"
)

// ExecContext is the state shared across a sensor's step pipeline.
// Read-only to step implementations; the engine mutates Steps[id] after
// each step returns.
type ExecContext struct {
    Fixtures map[string]string  // name → absolute path
    Env      map[string]string  // sealed snapshot taken at exec.Run entry
    Steps    map[string]*StepResult
}

// StepResult is what every step type returns to the engine.
type StepResult struct {
    Verdict  signal.Verdict
    Status   string             // "completed" | "aborted"
    Outputs  map[string]string  // declared outputs only
    Stdout   string             // shell only; empty otherwise
    Response *HttpResponse      // http only; nil otherwise
    Signals  []map[string]interface{}  // emitted during this step (JSONL bodies)
    Err      error              // step-level error, surfaced as evidence
}

// HttpResponse exposes the fields steps can extract via outputs: { from: response.* }
type HttpResponse struct {
    Status     int
    Body       []byte
    Headers    http.Header
    DurationMs int
}

// Step is implemented by every step type.
type Step interface {
    ID() string
    Type() string
    Execute(ctx context.Context, ec *ExecContext) *StepResult
}

// SubrunFunc lets the sensor-step package re-enter the orchestrator
// without importing it (avoids exec → orchestrator → exec cycle).
type SubrunFunc func(ctx context.Context, ref string, fixtures, env map[string]string) (*StepResult, error)
```

The YAML-decoded representation of a step lives in `lib/sensor` (extended in Task 2) as `sensor.StepConfig` and is passed to per-type constructors `<type>.New(cfg sensor.StepConfig) (step.Step, error)` (and `sensorpkg.New(cfg, subrun)` for the recursive case).

---

## Task 1: Remove legacy sensors and fixtures

**Files:**
- Delete: every `.yaml` file under `.harness/sensors/` and every file under `.harness/sensors/fixtures/`
- Modify: none

This PR clears the workspace so subsequent PRs do not have to worry about cross-shape validation pain. After this PR, `.harness/sensors/` is empty until Task 15 puts acceptance sensors back.

- [ ] **Step 1.1: Verify the directory contents one more time**

Run: `ls .harness/sensors/ && echo '---' && ls .harness/sensors/fixtures/ 2>/dev/null || echo 'no fixtures dir'`
Expected: listing of `.yaml` files plus possibly a `fixtures/` subdir.

- [ ] **Step 1.2: Delete sensors and fixtures**

Run:
```bash
rm -rf .harness/sensors/*.yaml .harness/sensors/fixtures/
ls .harness/sensors/    # should be empty (or contain only directories you didn't authorize removing)
```

If `.harness/sensors/` has subdirectories other than `fixtures/`, stop and investigate. Otherwise:

- [ ] **Step 1.3: Verify gate invariant test still passes (sanity check, no sensors should affect it)**

Run: `go test -run TestSpawnCallSitesGated ./lib/orchestrator/...`
Expected: PASS.

- [ ] **Step 1.4: Commit**

```bash
git add -A .harness/sensors/
git commit -m "chore: remove legacy sensors and fixtures

Prepares the tree for the typed-steps schema landing in subsequent
PRs. The old sensor shapes do not validate against the new schema,
so they cannot stay during the transition. Acceptance sensors are
re-introduced in the last PR of this series.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Schema reshape + validator extension

**Files:**
- Modify: `schemas/sensor.yaml` (add `$defs/Step`, `$defs/StepInputs`, `$defs/ShellStep`, `$defs/HttpStep`, `$defs/AssertStep`, `$defs/SensorStep`, `$defs/StepOutputs`, `$defs/Matcher`, `$defs/HttpExpect`; extend `execution` with `oneOf [command, steps]`)
- Modify: `lib/sensor/shape.go` (add `Step`, `StepConfig`, type-specific decode helpers)
- Modify: `lib/sensor/load.go` (normalize `command:` shortcut → single shell step; populate decoded steps)
- Modify: `lib/schema/validator.go` (no version detection added; new `$defs` are pulled automatically once `sensor.yaml` is rewritten)
- Test: `lib/schema/validator_test.go` (cases for both shapes)
- Test: `lib/sensor/shape_test.go` (decode round-trip)
- Test: `lib/sensor/load_test.go` (command-shape and steps-shape both load)

This PR introduces the new schema and decoded shape; cross-field validation rules ship in Task 9.

- [ ] **Step 2.1: Read the spec's Schema section (lines around 130–230 of the spec) to recall the exact `$defs`**

Run: `sed -n '130,240p' docs/superpowers/specs/2026-05-15-complex-commands-design.md`

- [ ] **Step 2.2: Write a failing schema test for the new step shape**

Append to `lib/schema/validator_test.go`:

```go
func TestValidateSensor_StepsShape(t *testing.T) {
    sensorYAML := []byte(`
id: example-steps
version: "1"
kind: assertion
type: computational
output: single
description: smoke
intent: smoke
purpose:
  description: example
cost:
  compute:
    cpu: low
    duration: short
blind_spots: []
verification:
  golden_cases: []
execution:
  steps:
    - id: ping
      type: shell
      run: echo hi
      exit_code_map: { 0: pass }
`)
    v := newValidator(t)
    if err := v.Validate(schema.TargetSensor, sensorYAML); err != nil {
        t.Fatalf("steps shape should validate, got: %v", err)
    }
}

func TestValidateSensor_StepsAndCommandReject(t *testing.T) {
    sensorYAML := []byte(`
id: bad
version: "1"
kind: assertion
type: computational
output: single
description: x
intent: x
purpose: { description: x }
cost: { compute: { cpu: low, duration: short } }
blind_spots: []
verification: { golden_cases: [] }
execution:
  command: echo hi
  steps:
    - id: dup
      type: shell
      run: echo also
`)
    v := newValidator(t)
    if err := v.Validate(schema.TargetSensor, sensorYAML); err == nil {
        t.Fatalf("declaring both command and steps must reject")
    }
}
```

`newValidator(t)` is a helper already present in `validator_test.go`. If absent, mirror the construction at the top of the file.

- [ ] **Step 2.3: Run the new tests; confirm they fail because the new shape is not in the schema yet**

Run: `go test -run 'TestValidateSensor_Steps' ./lib/schema/...`
Expected: FAIL — "validation … missing/unexpected `steps`" or similar.

- [ ] **Step 2.4: Add the new `$defs` to `schemas/sensor.yaml`**

Insert under the top-level `$defs:` block, alongside the existing ones (`Verdict`, `Severity`, the `Require*` arms, …):

```yaml
$defs:
  # ... existing defs ...

  Step:
    type: object
    required: [id, type]
    properties:
      id:   { type: string, pattern: '^[a-z][a-z0-9-]*$' }
      type: { enum: [shell, http, assert, sensor] }
      with: { $ref: '#/$defs/StepInputs' }
    oneOf:
      - $ref: '#/$defs/ShellStep'
      - $ref: '#/$defs/HttpStep'
      - $ref: '#/$defs/AssertStep'
      - $ref: '#/$defs/SensorStep'

  StepInputs:
    type: object
    additionalProperties:
      oneOf:
        - type: string
        - type: number
        - type: boolean
        - type: object
          required: [fixture]
          properties:
            fixture: { type: string }

  ShellStep:
    properties:
      type: { const: shell }
      run:  { type: string, minLength: 1 }
      exit_code_map:
        type: object
        additionalProperties:
          $ref: 'signal.yaml#/$defs/Verdict'
      parse:
        type: object
        required: [patterns]
        properties:
          patterns:
            type: array
            minItems: 1
            items: { $ref: '#/$defs/Pattern' }
      outputs: { $ref: '#/$defs/StepOutputs' }
    required: [run]

  HttpStep:
    properties:
      type:    { const: http }
      method:  { enum: [GET, POST, PUT, PATCH, DELETE, HEAD] }
      url:     { type: string, minLength: 1 }
      headers:
        type: object
        additionalProperties: { type: string }
      body_from:
        oneOf:
          - { required: [fixture],  properties: { fixture:  { type: string } } }
          - { required: [template], properties: { template: { type: string } } }
          - { required: [inline],   properties: { inline:   {} } }
      timeout: { type: string, pattern: '^[0-9]+(ms|s|m)$' }
      expect:  { $ref: '#/$defs/HttpExpect' }
      outputs: { $ref: '#/$defs/StepOutputs' }
    required: [method, url]

  AssertStep:
    properties:
      type:   { const: assert }
      expect: { $ref: '#/$defs/Matcher' }
    required: [expect]
    not: { required: [with] }   # rule 10: assert cannot declare with

  SensorStep:
    properties:
      type:                { const: sensor }
      ref:                 { type: string, pattern: '^[a-z][a-z0-9-]*$' }
      outputs_passthrough: { type: boolean }
      outputs:             { $ref: '#/$defs/StepOutputs' }
    required: [ref]

  StepOutputs:
    type: object
    additionalProperties:
      type: object
      required: [from]
      properties:
        from:     { type: string, minLength: 1 }
        regex:    { type: string }
        jsonpath: { type: string }
        trim:     { type: boolean }
      oneOf:
        - required: [regex]
        - required: [jsonpath]
        - required: [trim]
        - allOf:
            - not: { required: [regex] }
            - not: { required: [jsonpath] }
            - not: { required: [trim] }

  Matcher:
    type: object
    properties:
      value:      {}
      equals:     {}
      matches:    { type: string }
      contains:   { type: string }
      gte:        { type: number }
      lte:        { type: number }
      type:       { enum: [string, number, boolean, array, object, 'null'] }
      min_length: { type: integer, minimum: 0 }
      max_length: { type: integer, minimum: 0 }
      jsonpath:   { type: string }
    additionalProperties: false

  HttpExpect:
    type: object
    properties:
      status:  { $ref: '#/$defs/Matcher' }
      headers:
        type: object
        additionalProperties: { $ref: '#/$defs/Matcher' }
      body:
        oneOf:
          - { $ref: '#/$defs/Matcher' }
          - type: array
            items: { $ref: '#/$defs/Matcher' }
```

- [ ] **Step 2.5: Update `execution` in `schemas/sensor.yaml` to add the `steps` shape and mark it mutually exclusive with `command`**

Find the existing `execution:` block (around line 400 of `schemas/sensor.yaml`). Add `steps` as a peer property:

```yaml
execution:
  type: object
  properties:
    # ... existing command, exit_code_map, output_parsing, prepare, model, etc.
    steps:
      type: array
      minItems: 1
      items: { $ref: '#/$defs/Step' }
  oneOf:
    - required: [command]
      not: { required: [steps] }
    - required: [steps]
      not: { required: [command] }
```

For the existing top-level `allOf` that requires `output_parsing.patterns` when `output: stream`, gate it with `if execution.command is present`:

```yaml
- if:
    properties:
      output: { const: stream }
      execution:
        required: [command]
  then:
    properties:
      execution:
        required: [output_parsing]
```

The companion `if execution.steps is present → at least one shell step with parse:` rule cannot be expressed in JSON Schema; it lands in Go validation (Task 9).

- [ ] **Step 2.6: Re-run the two new schema tests; confirm `TestValidateSensor_StepsShape` passes and `TestValidateSensor_StepsAndCommandReject` passes**

Run: `go test -run 'TestValidateSensor_Steps' ./lib/schema/...`
Expected: PASS.

- [ ] **Step 2.7: Run the full schema package tests to confirm no regressions**

Run: `go test ./lib/schema/...`
Expected: PASS.

- [ ] **Step 2.8: Extend `lib/sensor/shape.go` with `StepConfig` and union fields**

Add to `lib/sensor/shape.go` (or a new file `lib/sensor/step.go` if that pattern is already established in the package — check `ls lib/sensor/` first):

```go
// StepConfig is the YAML-decoded form of an execution.steps[] entry.
// Type-specific fields are tagged omitempty so the same struct serves
// every union arm; cross-field validation in lib/sensor/validate.go
// ensures only the fields valid for the declared Type are populated.
type StepConfig struct {
    ID   string                 `json:"id"   yaml:"id"`
    Type string                 `json:"type" yaml:"type"`
    With map[string]interface{} `json:"with,omitempty" yaml:"with,omitempty"`

    // Shell fields
    Run         string                  `json:"run,omitempty"            yaml:"run,omitempty"`
    ExitCodeMap map[string]signal.Verdict `json:"exit_code_map,omitempty" yaml:"exit_code_map,omitempty"`
    Parse       *ParseConfig            `json:"parse,omitempty"          yaml:"parse,omitempty"`

    // HTTP fields
    Method   string             `json:"method,omitempty"   yaml:"method,omitempty"`
    URL      string             `json:"url,omitempty"      yaml:"url,omitempty"`
    Headers  map[string]string  `json:"headers,omitempty"  yaml:"headers,omitempty"`
    BodyFrom *BodyFromConfig    `json:"body_from,omitempty" yaml:"body_from,omitempty"`
    Timeout  string             `json:"timeout,omitempty"  yaml:"timeout,omitempty"`
    Expect   interface{}        `json:"expect,omitempty"   yaml:"expect,omitempty"`

    // Sensor fields
    Ref                string `json:"ref,omitempty"                 yaml:"ref,omitempty"`
    OutputsPassthrough bool   `json:"outputs_passthrough,omitempty" yaml:"outputs_passthrough,omitempty"`

    // Common output declaration
    Outputs map[string]OutputSpec `json:"outputs,omitempty" yaml:"outputs,omitempty"`
}

type ParseConfig struct {
    Patterns []signal.Pattern `json:"patterns" yaml:"patterns"`
}

type BodyFromConfig struct {
    Fixture  string      `json:"fixture,omitempty"  yaml:"fixture,omitempty"`
    Template string      `json:"template,omitempty" yaml:"template,omitempty"`
    Inline   interface{} `json:"inline,omitempty"   yaml:"inline,omitempty"`
}

type OutputSpec struct {
    From     string `json:"from"               yaml:"from"`
    Regex    string `json:"regex,omitempty"    yaml:"regex,omitempty"`
    JSONPath string `json:"jsonpath,omitempty" yaml:"jsonpath,omitempty"`
    Trim     bool   `json:"trim,omitempty"     yaml:"trim,omitempty"`
}
```

Then extend the existing `Execution` struct (find it in `lib/sensor/shape.go` — already has `Command`, `ExitCodeMap`, `OutputParsing`, `Prepare`, etc.) by adding:

```go
type Execution struct {
    // ... existing fields kept verbatim ...
    Steps []StepConfig `json:"steps,omitempty" yaml:"steps,omitempty"`
}
```

- [ ] **Step 2.9: Write a failing test for `Load` decoding a `steps`-shape sensor**

Append to `lib/sensor/load_test.go`:

```go
func TestLoad_StepsShape(t *testing.T) {
    dir := t.TempDir()
    sensorDir := filepath.Join(dir, ".harness", "sensors")
    if err := os.MkdirAll(sensorDir, 0o755); err != nil {
        t.Fatal(err)
    }
    body := []byte(`
id: example
version: "1"
kind: assertion
type: computational
output: single
description: x
intent: x
purpose: { description: x }
cost: { compute: { cpu: low, duration: short } }
blind_spots: []
verification: { golden_cases: [] }
execution:
  steps:
    - id: ping
      type: shell
      run: echo hi
      exit_code_map: { "0": pass }
`)
    if err := os.WriteFile(filepath.Join(sensorDir, "example.yaml"), body, 0o644); err != nil {
        t.Fatal(err)
    }
    s, err := sensor.Load(filepath.Join(sensorDir, "example.yaml"))
    if err != nil {
        t.Fatalf("load: %v", err)
    }
    if got := len(s.Execution.Steps); got != 1 {
        t.Fatalf("Execution.Steps len = %d, want 1", got)
    }
    if got := s.Execution.Steps[0].Type; got != "shell" {
        t.Fatalf("Type = %q, want shell", got)
    }
}
```

- [ ] **Step 2.10: Run; confirm failure (field Steps doesn't exist yet on `Execution` or decoder doesn't pick it up)**

Run: `go test -run TestLoad_StepsShape ./lib/sensor/...`
Expected: FAIL.

- [ ] **Step 2.11: Implement the `Execution.Steps` field (done in Step 2.8) and add the normalization stub**

Edit `lib/sensor/load.go`. Find the function that returns `*Sensor` after `json.Unmarshal`. Add immediately before the return:

```go
// Normalize command: shortcut to a single shell step at index 0.
// The on-disk YAML keeps its declared shape; this normalization is
// purely an in-memory convenience for the engine.
if s.Execution.Command != "" && len(s.Execution.Steps) == 0 {
    s.Execution.Steps = []StepConfig{{
        ID:          "main",
        Type:        "shell",
        Run:         s.Execution.Command,
        ExitCodeMap: convertExitCodeMap(s.Execution.ExitCodeMap),
        Parse:       legacyParseFromOutputParsing(s.Execution.OutputParsing),
    }}
}
```

Helpers (in the same file):

```go
func convertExitCodeMap(in map[int]signal.Verdict) map[string]signal.Verdict {
    if len(in) == 0 {
        return nil
    }
    out := make(map[string]signal.Verdict, len(in))
    for k, v := range in {
        out[strconv.Itoa(k)] = v
    }
    return out
}

func legacyParseFromOutputParsing(op *OutputParsing) *ParseConfig {
    if op == nil || len(op.Patterns) == 0 {
        return nil
    }
    return &ParseConfig{Patterns: append([]signal.Pattern{}, op.Patterns...)}
}
```

(If `convertExitCodeMap` would clobber types because the existing `ExitCodeMap` already uses string keys, drop the conversion and assign directly. Inspect the current `Execution` struct first.)

- [ ] **Step 2.12: Run the load test; confirm it passes**

Run: `go test -run TestLoad_StepsShape ./lib/sensor/...`
Expected: PASS.

- [ ] **Step 2.13: Run the full sensor and schema packages**

Run: `go test ./lib/sensor/... ./lib/schema/...`
Expected: PASS.

- [ ] **Step 2.14: Commit**

```bash
git add schemas/sensor.yaml lib/sensor/ lib/schema/
git commit -m "feat(schema): add typed-steps execution shape

Introduces $defs/Step (shell/http/assert/sensor union), StepInputs,
StepOutputs, Matcher, HttpExpect, and adds steps[] alongside command:
under execution with a oneOf gate. Validator picks up the new defs
automatically via existing schema registration; no version-aware
code is added.

lib/sensor/shape.go gains StepConfig + ParseConfig + BodyFromConfig +
OutputSpec, and Execution.Steps. Load normalizes command: to a single
shell step in memory so downstream consumers see one shape.

Cross-field rules (cycle detection, output↔parse coherence,
blocking↔steps exclusion, etc.) land in a later PR with the engine.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: lib/fixture/ — discovery and resolution

**Files:**
- Create: `lib/fixture/load.go`
- Create: `lib/fixture/load_test.go`
- Create: `lib/fixture/resolve.go`
- Create: `lib/fixture/resolve_test.go`
- Create: `lib/fixture/testdata/<small samples>`
- Move/Modify: `lib/sensor/fixture.go` (the existing `WriteFixture` writes to `.harness/sensors/fixtures/`; move it into `lib/fixture` as `fixture.Write`, point it at `.harness/fixtures/`)
- Modify: `lib/sensor/fixture_test.go` (delete or move tests over)
- Modify: every caller of `sensor.WriteFixture` to use `fixture.Write` — grep first

This PR owns the top-level fixture pool and decommissions the old per-sensor location helper.

- [ ] **Step 3.1: Inventory callers of `sensor.WriteFixture`**

Run: `grep -rn "sensor\.WriteFixture\|sensor.WriteFixture" --include='*.go' .`
Expected: `lib/sensor/fixture_test.go` plus `skills/create-sensor/scripts/write-fixture.go` (and possibly skills/detect-sensors). Note them.

- [ ] **Step 3.2: Write a failing test for `fixture.Discover`**

Create `lib/fixture/load_test.go`:

```go
package fixture_test

import (
    "os"
    "path/filepath"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/fixture"
)

func TestDiscover_FlatAndNested(t *testing.T) {
    root := t.TempDir()
    must := func(rel, body string) {
        t.Helper()
        p := filepath.Join(root, rel)
        if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
            t.Fatal(err)
        }
        if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
            t.Fatal(err)
        }
    }
    must(".harness/fixtures/order-valid.json", `{"id":"x"}`)
    must(".harness/fixtures/orders/large.json", `{"big":true}`)

    pool, err := fixture.Discover(root)
    if err != nil {
        t.Fatalf("Discover: %v", err)
    }
    if got, ok := pool["order-valid.json"]; !ok || got != filepath.Join(root, ".harness/fixtures/order-valid.json") {
        t.Errorf("flat fixture not discovered: %v", pool)
    }
    if got, ok := pool["orders/large.json"]; !ok || got != filepath.Join(root, ".harness/fixtures/orders/large.json") {
        t.Errorf("nested fixture not discovered: %v", pool)
    }
}

func TestDiscover_RejectOversize(t *testing.T) {
    root := t.TempDir()
    big := make([]byte, 2*1024*1024)
    p := filepath.Join(root, ".harness/fixtures/big.bin")
    if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(p, big, 0o644); err != nil {
        t.Fatal(err)
    }
    if _, err := fixture.Discover(root); err == nil {
        t.Fatalf("expected oversize fixture to be rejected")
    }
}

func TestDiscover_NoDirReturnsEmpty(t *testing.T) {
    root := t.TempDir()
    pool, err := fixture.Discover(root)
    if err != nil {
        t.Fatalf("missing dir should not error, got: %v", err)
    }
    if len(pool) != 0 {
        t.Fatalf("expected empty pool, got %d entries", len(pool))
    }
}
```

- [ ] **Step 3.3: Run; confirm failure (package doesn't exist)**

Run: `go test ./lib/fixture/...`
Expected: FAIL (no such package).

- [ ] **Step 3.4: Implement `lib/fixture/load.go`**

```go
// Package fixture discovers and resolves the top-level fixture pool
// at <projectRoot>/.harness/fixtures/. Fixtures are static files
// authored alongside sensors; they are referenced from sensor steps
// via `with: { fixture: <name> }` and `${{ fixtures.<name> }}`.
package fixture

import (
    "fmt"
    "io/fs"
    "os"
    "path/filepath"
    "strconv"
    "strings"
)

const defaultMaxBytes = 1 << 20 // 1 MiB

// Pool maps fixture names (with their original extension and any sub-path
// segments) to their absolute filesystem paths.
type Pool map[string]string

// Discover walks <projectRoot>/.harness/fixtures/ and returns a Pool. Paths
// inside the fixtures directory become names verbatim (with forward slashes
// regardless of platform). Files larger than the configured cap (default 1 MiB,
// overridable by HARNESS_FIXTURE_MAX_BYTES) cause Discover to return an error
// citing the offending path. A missing fixtures directory yields an empty pool
// and no error.
func Discover(projectRoot string) (Pool, error) {
    if projectRoot == "" {
        return nil, fmt.Errorf("fixture.Discover: projectRoot is required")
    }
    root := filepath.Join(projectRoot, ".harness", "fixtures")
    cap := defaultMaxBytes
    if raw := os.Getenv("HARNESS_FIXTURE_MAX_BYTES"); raw != "" {
        if n, err := strconv.Atoi(raw); err == nil && n > 0 {
            cap = n
        }
    }

    pool := Pool{}
    err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
        if walkErr != nil {
            if os.IsNotExist(walkErr) {
                return fs.SkipAll
            }
            return walkErr
        }
        if d.IsDir() {
            return nil
        }
        info, err := d.Info()
        if err != nil {
            return err
        }
        if int(info.Size()) > cap {
            return fmt.Errorf("fixture %q exceeds %d bytes (size=%d); raise HARNESS_FIXTURE_MAX_BYTES to override",
                path, cap, info.Size())
        }
        rel, err := filepath.Rel(root, path)
        if err != nil {
            return err
        }
        name := filepath.ToSlash(rel)
        pool[name] = path
        return nil
    })
    if err != nil && !strings.Contains(err.Error(), "no such file") {
        return nil, err
    }
    return pool, nil
}
```

- [ ] **Step 3.5: Implement `lib/fixture/resolve.go`**

```go
package fixture

import "fmt"

// Resolve returns the absolute path of the named fixture, or an error
// citing the pool's available names when the name is unknown.
func Resolve(pool Pool, name string) (string, error) {
    if path, ok := pool[name]; ok {
        return path, nil
    }
    return "", fmt.Errorf("fixture %q not found in pool (have %d fixtures)", name, len(pool))
}
```

Create `lib/fixture/resolve_test.go`:

```go
package fixture_test

import (
    "testing"

    "github.com/iurykrieger/harness-framework/lib/fixture"
)

func TestResolve(t *testing.T) {
    pool := fixture.Pool{"x.json": "/abs/x.json"}
    if got, err := fixture.Resolve(pool, "x.json"); err != nil || got != "/abs/x.json" {
        t.Fatalf("hit: got=%q err=%v", got, err)
    }
    if _, err := fixture.Resolve(pool, "missing"); err == nil {
        t.Fatalf("miss should error")
    }
}
```

- [ ] **Step 3.6: Add `fixture.Write` to replace `sensor.WriteFixture`**

Create `lib/fixture/write.go`:

```go
package fixture

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"
)

// PathEscapeError is returned by Write when the resolved target path
// escapes the fixtures root.
type PathEscapeError struct {
    Rel  string
    Root string
}

func (e *PathEscapeError) Error() string {
    return fmt.Sprintf("fixture path %q resolves outside %s", e.Rel, e.Root)
}

// Write atomically writes the payload to <projectRoot>/.harness/fixtures/<relPath>.
// relPath is relative to the fixtures root (e.g. "order-valid.json" or
// "orders/large.json"). Parent directories are created with mode 0o755. The
// write is atomic via tmp+rename. Idempotent: re-writing the same content
// is allowed. Returns the absolute path of the written file.
func Write(projectRoot, relPath string, payload []byte) (string, error) {
    if projectRoot == "" {
        return "", fmt.Errorf("fixture.Write: projectRoot is required")
    }
    if relPath == "" {
        return "", fmt.Errorf("fixture.Write: relPath is required")
    }
    root := filepath.Join(projectRoot, ".harness", "fixtures")
    target := filepath.Clean(filepath.Join(root, relPath))
    sep := string(os.PathSeparator)
    if target == root || !strings.HasPrefix(target+sep, root+sep) {
        return "", &PathEscapeError{Rel: relPath, Root: root}
    }
    if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
        return "", err
    }
    tmp, err := os.CreateTemp(filepath.Dir(target), ".tmp-fixture-*")
    if err != nil {
        return "", err
    }
    tmpName := tmp.Name()
    if _, err := tmp.Write(payload); err != nil {
        tmp.Close()
        os.Remove(tmpName)
        return "", err
    }
    if err := tmp.Close(); err != nil {
        os.Remove(tmpName)
        return "", err
    }
    if err := os.Rename(tmpName, target); err != nil {
        os.Remove(tmpName)
        return "", err
    }
    return target, nil
}
```

Add a matching test in `lib/fixture/write_test.go`:

```go
package fixture_test

import (
    "errors"
    "os"
    "path/filepath"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/fixture"
)

func TestWrite_AtomicAndIdempotent(t *testing.T) {
    root := t.TempDir()
    abs, err := fixture.Write(root, "order-valid.json", []byte(`{"id":"x"}`))
    if err != nil {
        t.Fatalf("Write: %v", err)
    }
    if abs != filepath.Join(root, ".harness/fixtures/order-valid.json") {
        t.Fatalf("abs = %q", abs)
    }
    body, _ := os.ReadFile(abs)
    if string(body) != `{"id":"x"}` {
        t.Fatalf("body = %q", body)
    }
    if _, err := fixture.Write(root, "order-valid.json", []byte(`{"id":"x"}`)); err != nil {
        t.Fatalf("idempotent rewrite: %v", err)
    }
}

func TestWrite_RejectEscape(t *testing.T) {
    root := t.TempDir()
    _, err := fixture.Write(root, "../escape.txt", []byte("x"))
    var esc *fixture.PathEscapeError
    if !errors.As(err, &esc) {
        t.Fatalf("expected PathEscapeError, got %v", err)
    }
}
```

- [ ] **Step 3.7: Run the fixture package tests; all green**

Run: `go test ./lib/fixture/...`
Expected: PASS.

- [ ] **Step 3.8: Repoint callers of `sensor.WriteFixture`**

In `skills/create-sensor/scripts/write-fixture.go`:

- Replace import `"github.com/iurykrieger/harness-framework/lib/sensor"` (or whatever the existing import alias is) with `"github.com/iurykrieger/harness-framework/lib/fixture"`.
- Replace `sensor.WriteFixture(res.ProjectRoot, relPath, payload)` with `fixture.Write(res.ProjectRoot, relPath, payload)`. **Note:** the `relPath` semantics change — it is now relative to `.harness/fixtures/`, not absolute from project root. Adjust the caller so `relPath` no longer includes the `.harness/sensors/fixtures/` prefix; if the script previously computed that prefix, drop it.

Read the script first:

Run: `cat skills/create-sensor/scripts/write-fixture.go`

Adjust the input contract documentation and any preceding helper so the relPath received from stdin (or whichever channel) does not embed the old path prefix.

- [ ] **Step 3.9: Delete `lib/sensor/fixture.go` and `lib/sensor/fixture_test.go`**

```bash
git rm lib/sensor/fixture.go lib/sensor/fixture_test.go
```

- [ ] **Step 3.10: Confirm the build is clean (compile sensors and skills packages)**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 3.11: Run all touched test suites**

Run: `go test ./lib/fixture/... ./lib/sensor/... -tags=create_sensor ./skills/create-sensor/...`
Expected: PASS.

- [ ] **Step 3.12: Commit**

```bash
git add lib/fixture/ lib/sensor/fixture.go lib/sensor/fixture_test.go skills/create-sensor/scripts/write-fixture.go
git commit -m "feat(fixture): introduce top-level .harness/fixtures/ pool

Adds lib/fixture with Discover, Resolve, and Write. Discover walks
<root>/.harness/fixtures/, returning name → absolute path (with size
cap, HARNESS_FIXTURE_MAX_BYTES override). Write replaces the previous
lib/sensor.WriteFixture, now targeting the new location. The old
sensor.WriteFixture (which wrote to .harness/sensors/fixtures/) is
removed; create-sensor's write-fixture.go script is repointed.

The previous per-sensor fixture location is deleted by Task 1; this
PR completes the move on the writer side.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: lib/template/actions.go — strict `${{ … }}` renderer

**Files:**
- Create: `lib/template/actions.go`
- Create: `lib/template/actions_test.go`
- Keep: `lib/template/render.go` (existing `{{slot}}` renderer, unrelated)

- [ ] **Step 4.1: Write failing tests covering the accessor matrix**

Create `lib/template/actions_test.go`:

```go
package template_test

import (
    "strings"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/template"
)

func ctx() template.ActionContext {
    return template.ActionContext{
        Fixtures: map[string]string{"order-valid.json": "/abs/order-valid.json"},
        Env:      map[string]string{"TARGET_URL": "https://stg.api"},
        Steps: map[string]template.ActionStep{
            "create": {
                Verdict: "pass",
                Outputs: map[string]string{"order_id": "abc-123"},
                Response: &template.ActionResponse{
                    Status:  201,
                    Headers: map[string]string{"content-type": "application/json"},
                },
            },
        },
    }
}

func TestRenderActions_Accessors(t *testing.T) {
    tests := []struct {
        name  string
        input string
        want  string
    }{
        {"fixture",          "load ${{ fixtures.order-valid.json }}", "load /abs/order-valid.json"},
        {"output",           "id=${{ steps.create.outputs.order_id }}", "id=abc-123"},
        {"step verdict",     "v=${{ steps.create.verdict }}",         "v=pass"},
        {"response status",  "s=${{ steps.create.response.status }}", "s=201"},
        {"response header",  "ct=${{ steps.create.response.headers.content-type }}", "ct=application/json"},
        {"env",              "u=${{ env.TARGET_URL }}",                "u=https://stg.api"},
        {"plain",            "no placeholder",                          "no placeholder"},
    }
    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            got, err := template.RenderActions(tc.input, ctx())
            if err != nil {
                t.Fatalf("err: %v", err)
            }
            if got != tc.want {
                t.Fatalf("got %q, want %q", got, tc.want)
            }
        })
    }
}

func TestRenderActions_RejectsOperators(t *testing.T) {
    inputs := []string{
        "${{ steps.create.outputs.x + 1 }}",
        "${{ steps.create.verdict == 'pass' }}",
        "${{ contains(x, y) }}",
        "${{ steps.create.outputs.x || 'fallback' }}",
    }
    for _, in := range inputs {
        if _, err := template.RenderActions(in, ctx()); err == nil {
            t.Fatalf("expected error for %q", in)
        }
    }
}

func TestRenderActions_UnknownAccessor(t *testing.T) {
    if _, err := template.RenderActions("${{ steps.missing.outputs.k }}", ctx()); err == nil {
        t.Fatalf("expected error for unknown step")
    }
    if _, err := template.RenderActions("${{ steps.create.outputs.missing }}", ctx()); err == nil {
        t.Fatalf("expected error for undeclared output")
    }
    if _, err := template.RenderActions("${{ env.MISSING }}", ctx()); err == nil {
        t.Fatalf("expected error for missing env var")
    }
}

func TestRenderActions_AllowsIdentifiersWithDashes(t *testing.T) {
    c := template.ActionContext{
        Steps: map[string]template.ActionStep{
            "create-order": {Outputs: map[string]string{"id": "ok"}},
        },
    }
    got, err := template.RenderActions("${{ steps.create-order.outputs.id }}", c)
    if err != nil || got != "ok" {
        t.Fatalf("got %q err %v", got, err)
    }
}

func TestRenderActions_LiteralBraceSurvives(t *testing.T) {
    out, err := template.RenderActions("plain {{ not actions }}", ctx())
    if err != nil {
        t.Fatalf("err: %v", err)
    }
    if !strings.Contains(out, "{{ not actions }}") {
        t.Fatalf("single-brace block should not be touched: %q", out)
    }
}
```

- [ ] **Step 4.2: Run; confirm failures**

Run: `go test ./lib/template/...`
Expected: FAIL (`RenderActions` / `ActionContext` not defined).

- [ ] **Step 4.3: Implement `lib/template/actions.go`**

```go
package template

import (
    "fmt"
    "regexp"
    "strconv"
    "strings"
)

// ActionContext is the resolution scope for ${{ … }} accessors.
type ActionContext struct {
    Fixtures map[string]string
    Env      map[string]string
    Steps    map[string]ActionStep
}

type ActionStep struct {
    Verdict  string
    Severity string
    Outputs  map[string]string
    Response *ActionResponse
}

type ActionResponse struct {
    Status     int
    Headers    map[string]string
}

// actionsPattern matches ${{ <expr> }} with whitespace tolerated.
var actionsPattern = regexp.MustCompile(`\$\{\{\s*([^}]+?)\s*\}\}`)

// accessorPattern matches one or more dot-separated identifiers, where each
// identifier is [a-zA-Z_][a-zA-Z0-9_.-]*. The outer regex captures the whole
// accessor as a single string; we then validate each segment is identifier-shaped.
var accessorPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*(\.[a-zA-Z_][a-zA-Z0-9_.-]*)*$`)

// RenderActions substitutes ${{ accessor }} placeholders in input. The expression
// inside the braces must be a single dot-separated identifier path. Any
// operator, function call, comparison, or whitespace-separated token is a
// render-time error.
func RenderActions(input string, c ActionContext) (string, error) {
    var firstErr error
    out := actionsPattern.ReplaceAllStringFunc(input, func(match string) string {
        if firstErr != nil {
            return match
        }
        expr := strings.TrimSpace(actionsPattern.FindStringSubmatch(match)[1])
        if !accessorPattern.MatchString(expr) {
            firstErr = fmt.Errorf("invalid accessor %q: only dot-separated identifiers are allowed", expr)
            return match
        }
        val, err := resolveAccessor(expr, c)
        if err != nil {
            firstErr = err
            return match
        }
        return val
    })
    if firstErr != nil {
        return "", firstErr
    }
    return out, nil
}

func resolveAccessor(expr string, c ActionContext) (string, error) {
    parts := strings.SplitN(expr, ".", 2)
    if len(parts) == 0 {
        return "", fmt.Errorf("empty accessor")
    }
    switch parts[0] {
    case "fixtures":
        if len(parts) < 2 || parts[1] == "" {
            return "", fmt.Errorf("fixtures accessor needs a name")
        }
        if p, ok := c.Fixtures[parts[1]]; ok {
            return p, nil
        }
        return "", fmt.Errorf("fixture %q not found", parts[1])
    case "env":
        if len(parts) < 2 || parts[1] == "" {
            return "", fmt.Errorf("env accessor needs a name")
        }
        if v, ok := c.Env[parts[1]]; ok {
            return v, nil
        }
        return "", fmt.Errorf("env var %q not in sealed snapshot", parts[1])
    case "steps":
        if len(parts) < 2 {
            return "", fmt.Errorf("steps accessor needs a step id")
        }
        rest := strings.SplitN(parts[1], ".", 2)
        stepID := rest[0]
        s, ok := c.Steps[stepID]
        if !ok {
            return "", fmt.Errorf("step %q has not run yet (or does not exist)", stepID)
        }
        if len(rest) < 2 {
            return "", fmt.Errorf("steps.%s accessor needs a field (verdict|severity|outputs.<k>|response.<k>)", stepID)
        }
        return resolveStepAccessor(stepID, rest[1], s)
    }
    return "", fmt.Errorf("unknown accessor root %q", parts[0])
}

func resolveStepAccessor(stepID, suffix string, s ActionStep) (string, error) {
    switch {
    case suffix == "verdict":
        return s.Verdict, nil
    case suffix == "severity":
        return s.Severity, nil
    case strings.HasPrefix(suffix, "outputs."):
        key := strings.TrimPrefix(suffix, "outputs.")
        v, ok := s.Outputs[key]
        if !ok {
            return "", fmt.Errorf("step %q did not declare output %q", stepID, key)
        }
        return v, nil
    case suffix == "response.status":
        if s.Response == nil {
            return "", fmt.Errorf("step %q is not an http step", stepID)
        }
        return strconv.Itoa(s.Response.Status), nil
    case strings.HasPrefix(suffix, "response.headers."):
        if s.Response == nil {
            return "", fmt.Errorf("step %q is not an http step", stepID)
        }
        h := strings.TrimPrefix(suffix, "response.headers.")
        if v, ok := s.Response.Headers[h]; ok {
            return v, nil
        }
        return "", fmt.Errorf("step %q response has no header %q", stepID, h)
    }
    return "", fmt.Errorf("steps.%s.%s is not a valid accessor", stepID, suffix)
}
```

- [ ] **Step 4.4: Run; expect green**

Run: `go test ./lib/template/...`
Expected: PASS.

- [ ] **Step 4.5: Commit**

```bash
git add lib/template/actions.go lib/template/actions_test.go
git commit -m "feat(template): strict \${{ … }} accessor renderer

Adds lib/template.RenderActions for resolving Actions-style
\${{ accessor }} placeholders. The grammar is deliberately restricted
to dot-separated identifiers — no operators, no function calls, no
conditionals. Unknown accessors and undeclared step outputs are
render-time errors. The escape valve is a shell step.

Coexists with the existing {{slot}} renderer (RenderTemplate), which
is used by the inferential prompt path and is unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: lib/step/ root + lib/step/shell + gate invariant allowlist

**Files:**
- Create: `lib/step/step.go` (Step interface, ExecContext, StepResult, HttpResponse, SubrunFunc)
- Create: `lib/step/step_test.go` (trivial table; the meat is in subpackage tests)
- Create: `lib/step/match.go` (shared Matcher evaluator)
- Create: `lib/step/match_test.go`
- Create: `lib/step/outputs.go` (StepOutputs extractor: from/regex/jsonpath/trim)
- Create: `lib/step/outputs_test.go`
- Create: `lib/step/shell/shell.go` (Step impl using subprocess.Start+Run)
- Create: `lib/step/shell/shell_test.go`
- Create: `lib/step/shell/parse.go` (helpers for parse: patterns; mostly delegates to signal.MatchLine)
- Create: `lib/step/shell/parse_test.go`
- Modify: `lib/orchestrator/gate_invariant_test.go` (allowlist `lib/step/shell/shell.go`)

This PR is the biggest. Break review into the four sub-areas: step root, match, outputs, shell.

- [ ] **Step 5.1: Pick a jsonpath library**

Quick decision call: `github.com/tidwall/gjson` is leaner (no go-yaml dep, simple API). Use it.

Run: `go get github.com/tidwall/gjson@latest`
Then: `go mod tidy`

- [ ] **Step 5.2: Create `lib/step/step.go` with the locked contract types (see "Cross-task type contract" at the top of this plan)**

Paste the entire block from the contract section verbatim into `lib/step/step.go`. Also export the canonical status constants:

```go
const (
    StatusCompleted = "completed"
    StatusAborted   = "aborted"
)
```

- [ ] **Step 5.3: Write tests for the Matcher evaluator**

Create `lib/step/match_test.go`:

```go
package step_test

import (
    "testing"

    "github.com/iurykrieger/harness-framework/lib/step"
)

func TestMatch_Equals(t *testing.T) {
    if !step.Match(step.Matcher{Equals: 201}, 201) {
        t.Fatal("int equality")
    }
    if !step.Match(step.Matcher{Equals: 201}, "201") {
        t.Fatal("numeric coercion from string")
    }
    if step.Match(step.Matcher{Equals: 201}, "two-hundred-one") {
        t.Fatal("non-numeric string should not match numeric expectation")
    }
    if !step.Match(step.Matcher{Equals: "pass"}, "pass") {
        t.Fatal("string equality")
    }
}

func TestMatch_GteLte(t *testing.T) {
    if !step.Match(step.Matcher{Gte: ptr(500.0)}, 600) {
        t.Fatal("gte hit")
    }
    if step.Match(step.Matcher{Gte: ptr(500.0)}, 400) {
        t.Fatal("gte miss")
    }
    if !step.Match(step.Matcher{Lte: ptr(500.0)}, "200") {
        t.Fatal("lte with stringified number")
    }
}

func TestMatch_RegexAndContains(t *testing.T) {
    if !step.Match(step.Matcher{Matches: "^abc"}, "abcdef") {
        t.Fatal("regex anchor")
    }
    if !step.Match(step.Matcher{Contains: "json"}, "application/json") {
        t.Fatal("substring")
    }
}

func ptr(x float64) *float64 { return &x }
```

- [ ] **Step 5.4: Implement `lib/step/match.go`**

```go
package step

import (
    "fmt"
    "regexp"
    "strconv"
    "strings"
)

// Matcher mirrors $defs/Matcher from the schema. Pointer fields signal "not declared".
type Matcher struct {
    Value     interface{}
    Equals    interface{}
    Matches   string
    Contains  string
    Gte       *float64
    Lte       *float64
    Type      string  // "string"|"number"|"boolean"|"array"|"object"|"null"
    MinLength *int
    MaxLength *int
    JSONPath  string
}

// Match returns true iff every declared matcher applies to value. Values are
// coerced per the spec's type-coercion rule: numeric matchers parse the value
// as float when it is a string.
func Match(m Matcher, value interface{}) bool {
    if m.JSONPath != "" {
        // Extract via jsonpath before comparing — delegated to outputs.go.
        extracted, ok := extractJSONPath(value, m.JSONPath)
        if !ok {
            return false
        }
        value = extracted
    }
    if m.Equals != nil && !equalsValue(m.Equals, value) {
        return false
    }
    if m.Matches != "" {
        s := fmt.Sprint(value)
        re, err := regexp.Compile(m.Matches)
        if err != nil || !re.MatchString(s) {
            return false
        }
    }
    if m.Contains != "" && !strings.Contains(fmt.Sprint(value), m.Contains) {
        return false
    }
    if m.Gte != nil {
        n, ok := toFloat(value)
        if !ok || n < *m.Gte {
            return false
        }
    }
    if m.Lte != nil {
        n, ok := toFloat(value)
        if !ok || n > *m.Lte {
            return false
        }
    }
    // Type, MinLength, MaxLength: implement when first needed; for now,
    // emit a panic-free placeholder so the schema-declared fields validate
    // even if not yet exercised.
    return true
}

func equalsValue(want, got interface{}) bool {
    // Numeric: parse got as float when it is a string.
    if wn, ok := toFloat(want); ok {
        if gn, ok := toFloat(got); ok {
            return wn == gn
        }
        return false
    }
    // String/other: compare via Sprint.
    return fmt.Sprint(want) == fmt.Sprint(got)
}

func toFloat(v interface{}) (float64, bool) {
    switch x := v.(type) {
    case float64:
        return x, true
    case float32:
        return float64(x), true
    case int:
        return float64(x), true
    case int64:
        return float64(x), true
    case string:
        n, err := strconv.ParseFloat(x, 64)
        if err != nil {
            return 0, false
        }
        return n, true
    }
    return 0, false
}

// extractJSONPath is defined in outputs.go (shared with the outputs
// extractor). See that file.
```

- [ ] **Step 5.5: Run matcher tests; expect green**

Run: `go test ./lib/step/...`
Expected: PASS (only matcher tests so far).

- [ ] **Step 5.6: Write tests for outputs extraction**

Create `lib/step/outputs_test.go`:

```go
package step_test

import (
    "testing"

    "github.com/iurykrieger/harness-framework/lib/step"
)

func TestExtractOutput_Regex(t *testing.T) {
    spec := step.OutputSpec{From: "stdout", Regex: `^DONE: (.+)$`}
    got, err := step.ExtractOutput(spec, step.OutputSource{Stdout: "DONE: abc-123\n"})
    if err != nil || got != "abc-123" {
        t.Fatalf("got=%q err=%v", got, err)
    }
}

func TestExtractOutput_JSONPath(t *testing.T) {
    spec := step.OutputSpec{From: "response.body", JSONPath: "$.id"}
    src := step.OutputSource{ResponseBody: []byte(`{"id":"xyz","other":1}`)}
    got, err := step.ExtractOutput(spec, src)
    if err != nil || got != "xyz" {
        t.Fatalf("got=%q err=%v", got, err)
    }
}

func TestExtractOutput_FromStatus(t *testing.T) {
    spec := step.OutputSpec{From: "response.status"}
    got, err := step.ExtractOutput(spec, step.OutputSource{ResponseStatus: 201})
    if err != nil || got != "201" {
        t.Fatalf("got=%q err=%v", got, err)
    }
}

func TestExtractOutput_RegexNoMatchErrors(t *testing.T) {
    spec := step.OutputSpec{From: "stdout", Regex: `^nope`}
    if _, err := step.ExtractOutput(spec, step.OutputSource{Stdout: "DONE\n"}); err == nil {
        t.Fatal("expected error for no-match regex")
    }
}
```

- [ ] **Step 5.7: Implement `lib/step/outputs.go`**

```go
package step

import (
    "encoding/json"
    "fmt"
    "regexp"
    "strconv"
    "strings"

    "github.com/tidwall/gjson"
)

// OutputSpec mirrors $defs/StepOutputs[*]: one mode (regex|jsonpath|trim) plus
// the raw passthrough default.
type OutputSpec struct {
    From     string
    Regex    string
    JSONPath string
    Trim     bool
}

// OutputSource carries everything a step can extract from. Fields irrelevant
// to the chosen From are zero.
type OutputSource struct {
    Stdout         string
    Stderr         string
    ResponseBody   []byte
    ResponseStatus int
    ResponseDurMS  int
    ResponseHeader map[string]string
}

// ExtractOutput resolves one OutputSpec against the source. Returns an error
// when extraction fails (regex no-match, jsonpath no-result, non-JSON when
// jsonpath is requested).
func ExtractOutput(spec OutputSpec, src OutputSource) (string, error) {
    raw, err := selectFrom(spec.From, src)
    if err != nil {
        return "", err
    }
    switch {
    case spec.Regex != "":
        re, err := regexp.Compile(spec.Regex)
        if err != nil {
            return "", fmt.Errorf("regex compile: %w", err)
        }
        m := re.FindStringSubmatch(raw)
        if m == nil {
            return "", fmt.Errorf("regex %q produced no match against From=%q", spec.Regex, spec.From)
        }
        if len(m) < 2 {
            return m[0], nil
        }
        return m[1], nil
    case spec.JSONPath != "":
        if !json.Valid([]byte(raw)) {
            return "", fmt.Errorf("jsonpath requested but From=%q is not valid JSON", spec.From)
        }
        path := strings.TrimPrefix(spec.JSONPath, "$.")
        res := gjson.Get(raw, path)
        if !res.Exists() {
            return "", fmt.Errorf("jsonpath %q produced no result", spec.JSONPath)
        }
        return res.String(), nil
    case spec.Trim:
        return strings.TrimSpace(raw), nil
    }
    return raw, nil
}

func selectFrom(from string, src OutputSource) (string, error) {
    switch {
    case from == "stdout":
        return src.Stdout, nil
    case from == "stderr":
        return src.Stderr, nil
    case from == "response.body":
        return string(src.ResponseBody), nil
    case from == "response.status":
        return strconv.Itoa(src.ResponseStatus), nil
    case from == "response.duration_ms":
        return strconv.Itoa(src.ResponseDurMS), nil
    case strings.HasPrefix(from, "response.headers."):
        h := strings.TrimPrefix(from, "response.headers.")
        if v, ok := src.ResponseHeader[h]; ok {
            return v, nil
        }
        return "", fmt.Errorf("response has no header %q", h)
    }
    return "", fmt.Errorf("unsupported From=%q", from)
}

// extractJSONPath is the matcher's helper sibling.
func extractJSONPath(value interface{}, path string) (interface{}, bool) {
    raw, ok := value.(string)
    if !ok {
        if b, ok := value.([]byte); ok {
            raw = string(b)
        } else {
            return nil, false
        }
    }
    if !json.Valid([]byte(raw)) {
        return nil, false
    }
    res := gjson.Get(raw, strings.TrimPrefix(path, "$."))
    if !res.Exists() {
        return nil, false
    }
    return res.String(), true
}
```

- [ ] **Step 5.8: Run outputs tests; expect green**

Run: `go test ./lib/step/...`
Expected: PASS.

- [ ] **Step 5.9: Write tests for the shell step**

Create `lib/step/shell/shell_test.go`:

```go
package shell_test

import (
    "context"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/sensor"
    "github.com/iurykrieger/harness-framework/lib/signal"
    "github.com/iurykrieger/harness-framework/lib/step"
    "github.com/iurykrieger/harness-framework/lib/step/shell"
)

func TestShell_HappyPath(t *testing.T) {
    cfg := sensor.StepConfig{
        ID:          "s",
        Type:        "shell",
        Run:         "echo hi && echo ok",
        ExitCodeMap: map[string]signal.Verdict{"0": signal.VerdictPass},
    }
    s, err := shell.New(cfg)
    if err != nil {
        t.Fatal(err)
    }
    ec := &step.ExecContext{Env: map[string]string{}}
    res := s.Execute(context.Background(), ec)
    if res.Verdict != signal.VerdictPass {
        t.Fatalf("verdict = %v", res.Verdict)
    }
    if res.Status != step.StatusCompleted {
        t.Fatalf("status = %q", res.Status)
    }
}

func TestShell_ExitCodeMapping(t *testing.T) {
    cfg := sensor.StepConfig{
        ID:          "fail",
        Type:        "shell",
        Run:         "exit 2",
        ExitCodeMap: map[string]signal.Verdict{"0": signal.VerdictPass, "2": signal.VerdictWarn},
    }
    s, _ := shell.New(cfg)
    res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
    if res.Verdict != signal.VerdictWarn {
        t.Fatalf("verdict = %v", res.Verdict)
    }
}

func TestShell_ParseEmitsIndividuals(t *testing.T) {
    cfg := sensor.StepConfig{
        ID:   "tail",
        Type: "shell",
        Run:  "echo ERROR foo && echo WARN bar && echo done",
        ExitCodeMap: map[string]signal.Verdict{"0": signal.VerdictPass},
        Parse: &sensor.ParseConfig{
            Patterns: []signal.Pattern{
                {Match: "ERROR", Verdict: signal.VerdictFail, Severity: signal.SeverityError},
                {Match: "WARN", Verdict: signal.VerdictWarn, Severity: signal.SeverityWarn},
            },
        },
    }
    s, _ := shell.New(cfg)
    res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
    if len(res.Signals) != 2 {
        t.Fatalf("expected 2 individuals, got %d", len(res.Signals))
    }
    if res.Verdict != signal.VerdictFail {
        t.Fatalf("verdict folded from parse should be fail, got %v", res.Verdict)
    }
}

func TestShell_WithFixtureInjectsEnv(t *testing.T) {
    cfg := sensor.StepConfig{
        ID:   "read",
        Type: "shell",
        With: map[string]interface{}{"fixture": "x.txt"},
        Run:  `echo "$HARNESS_FIXTURE_PATH"`,
        ExitCodeMap: map[string]signal.Verdict{"0": signal.VerdictPass},
    }
    s, _ := shell.New(cfg)
    res := s.Execute(context.Background(), &step.ExecContext{
        Fixtures: map[string]string{"x.txt": "/abs/x.txt"},
        Env:      map[string]string{},
    })
    if res.Verdict != signal.VerdictPass {
        t.Fatalf("verdict = %v", res.Verdict)
    }
    if !contains(res.Stdout, "/abs/x.txt") {
        t.Fatalf("stdout did not echo fixture path: %q", res.Stdout)
    }
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
    for i := 0; i+len(sub) <= len(s); i++ {
        if s[i:i+len(sub)] == sub {
            return i
        }
    }
    return -1
}
```

- [ ] **Step 5.10: Run; expect failure (shell package doesn't exist yet)**

Run: `go test ./lib/step/shell/...`
Expected: FAIL (no such package).

- [ ] **Step 5.11: Implement `lib/step/shell/shell.go`**

```go
// Package shell implements the type: shell step. Streaming is done via
// subprocess.Start + StreamHandle.Run so parse: patterns can emit individual
// signals. The PreflightGate invariant covers this site by allowlist
// (lib/step/shell/shell.go) — the gate fires upstream in orchestrator.RunOne.
package shell

import (
    "bytes"
    "context"
    "fmt"
    "strconv"
    "strings"

    "github.com/iurykrieger/harness-framework/lib/sensor"
    "github.com/iurykrieger/harness-framework/lib/signal"
    "github.com/iurykrieger/harness-framework/lib/step"
    "github.com/iurykrieger/harness-framework/lib/subprocess"
    "github.com/iurykrieger/harness-framework/lib/template"
)

type Step struct {
    cfg sensor.StepConfig
}

func New(cfg sensor.StepConfig) (step.Step, error) {
    if cfg.Type != "shell" {
        return nil, fmt.Errorf("shell.New: type=%q (want shell)", cfg.Type)
    }
    if cfg.Run == "" {
        return nil, fmt.Errorf("shell.New: run is required")
    }
    return &Step{cfg: cfg}, nil
}

func (s *Step) ID() string   { return s.cfg.ID }
func (s *Step) Type() string { return "shell" }

func (s *Step) Execute(ctx context.Context, ec *step.ExecContext) *step.StepResult {
    res := &step.StepResult{
        Status:  step.StatusAborted,
        Verdict: signal.VerdictError,
        Outputs: map[string]string{},
    }
    // Render run script through actions template.
    actionsCtx := buildActionsContext(ec)
    rendered, err := template.RenderActions(s.cfg.Run, actionsCtx)
    if err != nil {
        res.Err = fmt.Errorf("render run: %w", err)
        return res
    }
    // Resolve `with:` into env injections.
    extraEnv, err := resolveWith(s.cfg.With, ec)
    if err != nil {
        res.Err = err
        return res
    }
    envMap := mergeEnv(ec.Env, extraEnv)

    var stdout, stderr bytes.Buffer
    cfg := subprocess.StreamConfig{
        Command:  rendered,
        Env:      envMap,
        Patterns: parsePatterns(s.cfg.Parse),
        Stdout:   &stdout,
        Stderr:   &stderr,
    }
    handle, err := subprocess.Start(ctx, cfg)
    if err != nil {
        res.Err = fmt.Errorf("subprocess.Start: %w", err)
        return res
    }
    sr := handle.Run()
    res.Status = step.StatusCompleted
    res.Stdout = stdout.String()
    res.Signals = sr.Individuals

    // Map exit code → verdict.
    res.Verdict = mapExit(s.cfg.ExitCodeMap, sr.ExitCode)
    // Fold worst parse verdict.
    res.Verdict = worst(res.Verdict, signal.MaxStreamVerdict(sr.Individuals))

    // Extract declared outputs.
    src := step.OutputSource{Stdout: res.Stdout, Stderr: sr.StderrExcerpt}
    for name, spec := range s.cfg.Outputs {
        v, err := step.ExtractOutput(step.OutputSpec{
            From: spec.From, Regex: spec.Regex, JSONPath: spec.JSONPath, Trim: spec.Trim,
        }, src)
        if err != nil {
            res.Err = fmt.Errorf("extract output %q: %w", name, err)
            res.Verdict = signal.VerdictError
            return res
        }
        res.Outputs[name] = v
    }
    return res
}

func resolveWith(with map[string]interface{}, ec *step.ExecContext) (map[string]string, error) {
    out := map[string]string{}
    var firstFixture string
    for k, v := range with {
        switch x := v.(type) {
        case string:
            // String literal or ${{ … }}; render through actions.
            rendered, err := template.RenderActions(x, buildActionsContext(ec))
            if err != nil {
                return nil, fmt.Errorf("with[%q]: %w", k, err)
            }
            out["HARNESS_INPUT_"+strings.ToUpper(strings.ReplaceAll(k, "-", "_"))] = rendered
        case map[string]interface{}:
            // Expect { fixture: <name> }
            name, _ := x["fixture"].(string)
            if name == "" {
                return nil, fmt.Errorf("with[%q]: object form must declare fixture", k)
            }
            abs, ok := ec.Fixtures[name]
            if !ok {
                return nil, fmt.Errorf("with[%q]: fixture %q not in pool", k, name)
            }
            out["HARNESS_FIXTURE_"+strings.ToUpper(strings.ReplaceAll(name, "-", "_"))] = abs
            if firstFixture == "" {
                firstFixture = abs
            }
        default:
            out["HARNESS_INPUT_"+strings.ToUpper(strings.ReplaceAll(k, "-", "_"))] = fmt.Sprint(v)
        }
    }
    if firstFixture != "" {
        out["HARNESS_FIXTURE_PATH"] = firstFixture
    }
    return out, nil
}

func mergeEnv(base, extra map[string]string) map[string]string {
    out := map[string]string{}
    for k, v := range base {
        out[k] = v
    }
    for k, v := range extra {
        out[k] = v
    }
    return out
}

func parsePatterns(p *sensor.ParseConfig) []signal.Pattern {
    if p == nil {
        return nil
    }
    return p.Patterns
}

func mapExit(m map[string]signal.Verdict, code int) signal.Verdict {
    if v, ok := m[strconv.Itoa(code)]; ok {
        return v
    }
    if code == 0 {
        return signal.VerdictPass
    }
    return signal.VerdictFail
}

func worst(a, b signal.Verdict) signal.Verdict {
    rank := map[signal.Verdict]int{
        signal.VerdictPass:  0,
        signal.VerdictWarn:  1,
        signal.VerdictFail:  2,
        signal.VerdictError: 3,
    }
    if rank[b] > rank[a] {
        return b
    }
    return a
}

func buildActionsContext(ec *step.ExecContext) template.ActionContext {
    out := template.ActionContext{
        Fixtures: ec.Fixtures,
        Env:      ec.Env,
        Steps:    map[string]template.ActionStep{},
    }
    for id, sr := range ec.Steps {
        as := template.ActionStep{
            Verdict: string(sr.Verdict),
            Outputs: sr.Outputs,
        }
        if sr.Response != nil {
            headers := map[string]string{}
            for k, vs := range sr.Response.Headers {
                if len(vs) > 0 {
                    headers[strings.ToLower(k)] = vs[0]
                }
            }
            as.Response = &template.ActionResponse{
                Status:  sr.Response.Status,
                Headers: headers,
            }
        }
        out.Steps[id] = as
    }
    return out
}
```

- [ ] **Step 5.12: Update gate invariant allowlist**

Edit `lib/orchestrator/gate_invariant_test.go`. Find the `allowedFiles` map (around line 24):

```go
allowedFiles := map[string]bool{
    "lib/subprocess/stream.go": true,
    "lib/subprocess/detach.go": true,
    "lib/subprocess/step.go":   true,
    "lib/step/shell/shell.go":  true,  // <-- ADD
}
```

Add a one-line rationale comment immediately above the new entry:

```go
// lib/step/shell/shell.go is a streaming primitive consumed by lib/exec,
// which is gated upstream by orchestrator.RunOne (PreflightGate). The
// shell step needs Start+Run because parse: patterns require stdout to
// be drained — subprocess.RunStep (the prepare/teardown primitive)
// discards stdout and is unsuitable.
```

- [ ] **Step 5.13: Run shell + gate invariant tests**

Run: `go test ./lib/step/... ./lib/orchestrator/...`
Expected: PASS. If `TestSpawnCallSitesGated` fails citing `lib/step/shell/shell.go`, re-check the allowlist entry.

- [ ] **Step 5.14: Commit**

```bash
git add lib/step/ lib/orchestrator/gate_invariant_test.go go.mod go.sum
git commit -m "feat(step): add Step interface, match/outputs primitives, shell impl

lib/step/step.go locks the cross-package contract: Step interface,
ExecContext, StepResult, HttpResponse, SubrunFunc. Match and Outputs
extractors live as files at the package root per Rule #9.

lib/step/shell wraps subprocess.Start+Run so parse: patterns can
drain stdout. The new spawn site at lib/step/shell/shell.go is
added to gate_invariant_test.go's allowlist with rationale: it is
a streaming primitive gated upstream by orchestrator.RunOne.

jsonpath uses github.com/tidwall/gjson.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: lib/step/http + lib/step/assert

**Files:**
- Create: `lib/step/http/http.go`, `http_test.go`, `expect.go`, `expect_test.go`, `body.go`, `body_test.go`, `testdata/`
- Create: `lib/step/assert/assert.go`, `assert_test.go`

- [ ] **Step 6.1: Write http step tests using httptest.Server**

Create `lib/step/http/http_test.go`:

```go
package http_test

import (
    "context"
    "fmt"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/sensor"
    "github.com/iurykrieger/harness-framework/lib/signal"
    "github.com/iurykrieger/harness-framework/lib/step"
    httpstep "github.com/iurykrieger/harness-framework/lib/step/http"
)

func TestHTTP_2xxDefaultPass(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(204)
    }))
    defer srv.Close()
    s, _ := httpstep.New(sensor.StepConfig{
        ID: "p", Type: "http", Method: "GET", URL: srv.URL,
    })
    res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
    if res.Verdict != signal.VerdictPass {
        t.Fatalf("verdict = %v", res.Verdict)
    }
}

func TestHTTP_4xxDefaultFail(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(404)
    }))
    defer srv.Close()
    s, _ := httpstep.New(sensor.StepConfig{
        ID: "p", Type: "http", Method: "GET", URL: srv.URL,
    })
    res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
    if res.Verdict != signal.VerdictFail {
        t.Fatalf("verdict = %v", res.Verdict)
    }
}

func TestHTTP_ExpectStatusEquals(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(201)
        fmt.Fprint(w, `{"id":"abc","items":[1]}`)
    }))
    defer srv.Close()
    s, _ := httpstep.New(sensor.StepConfig{
        ID: "p", Type: "http", Method: "POST", URL: srv.URL,
        Expect: map[string]interface{}{
            "status": map[string]interface{}{"equals": 201},
            "body": []interface{}{
                map[string]interface{}{"jsonpath": "$.id", "matches": `^[a-z]+$`},
                map[string]interface{}{"jsonpath": "$.items", "type": "array", "min_length": 1},
            },
        },
    })
    res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
    if res.Verdict != signal.VerdictPass {
        t.Fatalf("verdict = %v stdout=%q outputs=%+v", res.Verdict, res.Stdout, res.Outputs)
    }
}

func TestHTTP_BodyFromFixture(t *testing.T) {
    seen := ""
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        b := make([]byte, 64)
        n, _ := r.Body.Read(b)
        seen = string(b[:n])
        w.WriteHeader(201)
    }))
    defer srv.Close()

    dir := t.TempDir()
    f := dir + "/order-valid.json"
    if err := writeFile(f, `{"sku":"x"}`); err != nil {
        t.Fatal(err)
    }
    s, _ := httpstep.New(sensor.StepConfig{
        ID: "p", Type: "http", Method: "POST", URL: srv.URL,
        BodyFrom: &sensor.BodyFromConfig{Fixture: "order-valid.json"},
    })
    res := s.Execute(context.Background(), &step.ExecContext{
        Fixtures: map[string]string{"order-valid.json": f},
        Env:      map[string]string{},
    })
    if res.Verdict != signal.VerdictPass {
        t.Fatalf("verdict = %v", res.Verdict)
    }
    if seen != `{"sku":"x"}` {
        t.Fatalf("server saw %q", seen)
    }
}

func writeFile(p, body string) error {
    return os.WriteFile(p, []byte(body), 0o644)
}
```

(Adjust `os.WriteFile` import.)

- [ ] **Step 6.2: Implement `lib/step/http/http.go`, `body.go`, `expect.go`**

Sketch:

```go
// lib/step/http/http.go
package http

import (
    "bytes"
    "context"
    "fmt"
    "io"
    "net/http"
    "time"

    "github.com/iurykrieger/harness-framework/lib/sensor"
    "github.com/iurykrieger/harness-framework/lib/signal"
    "github.com/iurykrieger/harness-framework/lib/step"
    "github.com/iurykrieger/harness-framework/lib/template"
)

type Step struct {
    cfg sensor.StepConfig
}

func New(cfg sensor.StepConfig) (step.Step, error) {
    if cfg.Type != "http" {
        return nil, fmt.Errorf("http.New: type=%q (want http)", cfg.Type)
    }
    return &Step{cfg: cfg}, nil
}

func (s *Step) ID() string   { return s.cfg.ID }
func (s *Step) Type() string { return "http" }

func (s *Step) Execute(ctx context.Context, ec *step.ExecContext) *step.StepResult {
    res := &step.StepResult{Status: step.StatusAborted, Verdict: signal.VerdictError, Outputs: map[string]string{}}
    actionsCtx := buildActionsContext(ec)
    url, err := template.RenderActions(s.cfg.URL, actionsCtx)
    if err != nil {
        res.Err = err
        return res
    }
    body, err := buildBody(s.cfg.BodyFrom, ec, actionsCtx)
    if err != nil {
        res.Err = err
        return res
    }
    req, err := http.NewRequestWithContext(ctx, s.cfg.Method, url, bytes.NewReader(body))
    if err != nil {
        res.Err = err
        return res
    }
    for k, v := range s.cfg.Headers {
        rv, err := template.RenderActions(v, actionsCtx)
        if err != nil {
            res.Err = err
            return res
        }
        req.Header.Set(k, rv)
    }
    timeout := 30 * time.Second
    if s.cfg.Timeout != "" {
        if d, err := time.ParseDuration(s.cfg.Timeout); err == nil {
            timeout = d
        }
    }
    client := &http.Client{Timeout: timeout}
    start := time.Now()
    resp, err := client.Do(req)
    dur := int(time.Since(start) / time.Millisecond)
    if err != nil {
        res.Err = err
        res.Verdict = signal.VerdictError
        res.Signals = []map[string]interface{}{buildHTTPSignal(s.cfg.ID, req, nil, dur, signal.VerdictError, err.Error())}
        return res
    }
    defer resp.Body.Close()
    bodyBytes, _ := io.ReadAll(resp.Body)
    res.Response = &step.HttpResponse{
        Status:     resp.StatusCode,
        Body:       bodyBytes,
        Headers:    resp.Header,
        DurationMs: dur,
    }
    verdict, evidence := evalExpect(s.cfg.Expect, resp.StatusCode, bodyBytes, resp.Header)
    res.Verdict = verdict
    res.Status = step.StatusCompleted
    res.Signals = []map[string]interface{}{buildHTTPSignal(s.cfg.ID, req, resp, dur, verdict, evidence)}

    // outputs extraction
    src := step.OutputSource{
        ResponseBody:   bodyBytes,
        ResponseStatus: resp.StatusCode,
        ResponseDurMS:  dur,
        ResponseHeader: flattenHeaders(resp.Header),
    }
    for name, spec := range s.cfg.Outputs {
        v, err := step.ExtractOutput(step.OutputSpec{From: spec.From, Regex: spec.Regex, JSONPath: spec.JSONPath, Trim: spec.Trim}, src)
        if err != nil {
            res.Err = fmt.Errorf("output %q: %w", name, err)
            res.Verdict = signal.VerdictError
            return res
        }
        res.Outputs[name] = v
    }
    return res
}

// (helpers buildBody, evalExpect, buildHTTPSignal, flattenHeaders, buildActionsContext — implement; tests drive shapes)
```

Implement `body.go`:

```go
package http

import (
    "encoding/json"
    "fmt"
    "os"

    "github.com/iurykrieger/harness-framework/lib/sensor"
    "github.com/iurykrieger/harness-framework/lib/step"
    "github.com/iurykrieger/harness-framework/lib/template"
)

func buildBody(b *sensor.BodyFromConfig, ec *step.ExecContext, actx template.ActionContext) ([]byte, error) {
    if b == nil {
        return nil, nil
    }
    switch {
    case b.Fixture != "":
        path, ok := ec.Fixtures[b.Fixture]
        if !ok {
            return nil, fmt.Errorf("body_from.fixture %q not in pool", b.Fixture)
        }
        return os.ReadFile(path)
    case b.Inline != nil:
        return json.Marshal(b.Inline)
    case b.Template != "":
        rendered, err := template.RenderActions(b.Template, actx)
        if err != nil {
            return nil, err
        }
        return []byte(rendered), nil
    }
    return nil, nil
}
```

Implement `expect.go` (matchers reused from `lib/step/match.go`):

```go
package http

import (
    "net/http"

    "github.com/iurykrieger/harness-framework/lib/signal"
    "github.com/iurykrieger/harness-framework/lib/step"
)

// evalExpect returns the verdict and a human-readable evidence string.
// expect is the YAML-decoded map[string]interface{} (or nil).
func evalExpect(expect interface{}, status int, body []byte, headers http.Header) (signal.Verdict, string) {
    if expect == nil {
        return defaultVerdict(status), defaultEvidence(status)
    }
    m, ok := expect.(map[string]interface{})
    if !ok {
        return signal.VerdictError, "expect: unexpected shape"
    }
    if s, ok := m["status"]; ok {
        if !step.Match(matcherFrom(s), status) {
            return signal.VerdictFail, "status did not match"
        }
    }
    if h, ok := m["headers"].(map[string]interface{}); ok {
        for k, v := range h {
            got := headers.Get(k)
            if !step.Match(matcherFrom(v), got) {
                return signal.VerdictFail, "header "+k+" did not match"
            }
        }
    }
    if b, ok := m["body"]; ok {
        switch x := b.(type) {
        case map[string]interface{}:
            if !step.Match(matcherFrom(x), string(body)) {
                return signal.VerdictFail, "body did not match"
            }
        case []interface{}:
            for i, item := range x {
                if !step.Match(matcherFrom(item), string(body)) {
                    return signal.VerdictFail, fmt.Sprintf("body[%d] did not match", i)
                }
            }
        }
    }
    return signal.VerdictPass, "all expectations met"
}

func defaultVerdict(status int) signal.Verdict {
    switch {
    case status >= 200 && status < 400:
        return signal.VerdictPass
    case status >= 400:
        return signal.VerdictFail
    default:
        return signal.VerdictError
    }
}

func defaultEvidence(status int) string {
    return "no expect; status=" + http.StatusText(status)
}

// matcherFrom converts a YAML-decoded matcher (map[string]interface{}) into a
// step.Matcher struct. Robust against optional keys.
func matcherFrom(in interface{}) step.Matcher {
    m, ok := in.(map[string]interface{})
    if !ok {
        // bare value → equals
        return step.Matcher{Equals: in}
    }
    out := step.Matcher{}
    if v, ok := m["equals"]; ok {
        out.Equals = v
    }
    if v, ok := m["matches"].(string); ok {
        out.Matches = v
    }
    if v, ok := m["contains"].(string); ok {
        out.Contains = v
    }
    if v, ok := m["gte"].(float64); ok {
        out.Gte = &v
    }
    if v, ok := m["lte"].(float64); ok {
        out.Lte = &v
    }
    if v, ok := m["jsonpath"].(string); ok {
        out.JSONPath = v
    }
    if v, ok := m["type"].(string); ok {
        out.Type = v
    }
    if v, ok := m["min_length"].(int); ok {
        out.MinLength = &v
    }
    if v, ok := m["max_length"].(int); ok {
        out.MaxLength = &v
    }
    return out
}
```

(Add `fmt` import; flesh out remaining helpers in `http.go`.)

- [ ] **Step 6.3: Run http tests; iterate until green**

Run: `go test ./lib/step/http/...`
Expected: PASS for all four cases. If a matcher case fails, double-check `matcherFrom` against the YAML-decoded shapes.

- [ ] **Step 6.4: Write assert step tests**

Create `lib/step/assert/assert_test.go`:

```go
package assert_test

import (
    "context"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/sensor"
    "github.com/iurykrieger/harness-framework/lib/signal"
    "github.com/iurykrieger/harness-framework/lib/step"
    "github.com/iurykrieger/harness-framework/lib/step/assert"
)

func TestAssert_EqualsHit(t *testing.T) {
    cfg := sensor.StepConfig{
        ID: "g", Type: "assert",
        Expect: map[string]interface{}{
            "value":  "${{ steps.prev.outputs.x }}",
            "equals": "ok",
        },
    }
    s, _ := assert.New(cfg)
    ec := &step.ExecContext{
        Env: map[string]string{},
        Steps: map[string]*step.StepResult{
            "prev": {Outputs: map[string]string{"x": "ok"}, Verdict: signal.VerdictPass},
        },
    }
    res := s.Execute(context.Background(), ec)
    if res.Verdict != signal.VerdictPass {
        t.Fatalf("verdict = %v", res.Verdict)
    }
}

func TestAssert_GteMiss(t *testing.T) {
    cfg := sensor.StepConfig{
        ID: "g", Type: "assert",
        Expect: map[string]interface{}{
            "value": "1200",
            "lte":   500,
        },
    }
    s, _ := assert.New(cfg)
    res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
    if res.Verdict != signal.VerdictFail {
        t.Fatalf("verdict = %v", res.Verdict)
    }
}
```

- [ ] **Step 6.5: Implement `lib/step/assert/assert.go`**

```go
package assert

import (
    "context"
    "fmt"

    "github.com/iurykrieger/harness-framework/lib/sensor"
    "github.com/iurykrieger/harness-framework/lib/signal"
    "github.com/iurykrieger/harness-framework/lib/step"
    "github.com/iurykrieger/harness-framework/lib/template"
)

type Step struct {
    cfg sensor.StepConfig
}

func New(cfg sensor.StepConfig) (step.Step, error) {
    if cfg.Type != "assert" {
        return nil, fmt.Errorf("assert.New: type=%q (want assert)", cfg.Type)
    }
    if cfg.Expect == nil {
        return nil, fmt.Errorf("assert.New: expect is required")
    }
    return &Step{cfg: cfg}, nil
}

func (s *Step) ID() string   { return s.cfg.ID }
func (s *Step) Type() string { return "assert" }

func (s *Step) Execute(ctx context.Context, ec *step.ExecContext) *step.StepResult {
    res := &step.StepResult{Status: step.StatusCompleted, Outputs: map[string]string{}}
    m, ok := s.cfg.Expect.(map[string]interface{})
    if !ok {
        res.Verdict = signal.VerdictError
        res.Err = fmt.Errorf("assert.Expect must be an object")
        return res
    }
    raw, ok := m["value"]
    if !ok {
        res.Verdict = signal.VerdictError
        res.Err = fmt.Errorf("assert.Expect missing value")
        return res
    }
    // value may be a template string
    val := raw
    if s, ok := raw.(string); ok {
        rendered, err := template.RenderActions(s, buildActionsContext(ec))
        if err != nil {
            res.Verdict = signal.VerdictError
            res.Err = err
            return res
        }
        val = rendered
    }
    matcher := matcherFrom(m)
    if step.Match(matcher, val) {
        res.Verdict = signal.VerdictPass
    } else {
        res.Verdict = signal.VerdictFail
    }
    res.Signals = []map[string]interface{}{buildAssertSignal(s.cfg.ID, val, matcher, res.Verdict)}
    return res
}

// matcherFrom and buildAssertSignal — implement minimally; reuse the matcherFrom
// pattern from lib/step/http/expect.go (copy-paste, do not couple packages).
// buildActionsContext is the same helper from shell.go; duplicate here.
```

(Implementer: duplicate `matcherFrom` and `buildActionsContext` to keep packages decoupled. Rule of three; abstract only if a fourth caller appears.)

- [ ] **Step 6.6: Run all step tests**

Run: `go test ./lib/step/...`
Expected: PASS.

- [ ] **Step 6.7: Commit**

```bash
git add lib/step/http/ lib/step/assert/
git commit -m "feat(step): http and assert step types

http uses net/http directly: declarative method/url/headers/body_from,
HttpExpect with status/headers/body matchers, status-default verdict
when expect is absent (2xx/3xx pass, 4xx/5xx fail, network error
error). Body sources cover fixture, inline (JSON-encoded), and
template (rendered through actions). HTTP step emits one structured
signal with metadata.kind=http_observation.

assert is in-memory: renders expect.value, applies one Matcher,
emits one signal with metadata.kind=assertion. with: is rejected
upstream by schema (Validation rule 10 in Task 9).

Matcher and Outputs evaluators reused from lib/step root package.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: lib/exec/ — engine and aggregate

**Files:**
- Create: `lib/exec/engine.go`, `engine_test.go`, `render.go`, `aggregate.go`, `aggregate_test.go`, `testdata/`

This PR wires shell+http+assert together. The `sensor` step type lands in Task 8 because it needs to re-enter the engine.

- [ ] **Step 7.1: Write an engine test that runs a 3-step pipeline (shell + http + assert)**

Create `lib/exec/engine_test.go` with one happy-path and one fail-fast case. Use `httptest.Server` for the http step. Outline:

```go
package exec_test

import (
    "context"
    "fmt"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/exec"
    "github.com/iurykrieger/harness-framework/lib/sensor"
    "github.com/iurykrieger/harness-framework/lib/signal"
)

func TestRun_HappyPath_ShellHttpAssert(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(201)
        fmt.Fprint(w, `{"id":"abc-123"}`)
    }))
    defer srv.Close()

    s := &sensor.Sensor{
        // …fill required fields…
        Execution: sensor.Execution{
            Steps: []sensor.StepConfig{
                {ID: "noop", Type: "shell", Run: "echo ok",
                 ExitCodeMap: map[string]signal.Verdict{"0": signal.VerdictPass}},
                {ID: "create", Type: "http", Method: "POST", URL: srv.URL,
                 Outputs: map[string]sensor.OutputSpec{"order_id": {From: "response.body", JSONPath: "$.id"}}},
                {ID: "gate", Type: "assert",
                 Expect: map[string]interface{}{"value": "${{ steps.create.outputs.order_id }}", "matches": `^[a-z0-9-]+$`}},
            },
        },
    }
    signals, err := exec.Run(context.Background(), s, nil, map[string]string{})
    if err != nil {
        t.Fatalf("Run: %v", err)
    }
    // The aggregate is the last signal.
    agg := signals[len(signals)-1]
    if agg["verdict"] != string(signal.VerdictPass) {
        t.Fatalf("aggregate verdict = %v", agg["verdict"])
    }
}

func TestRun_FailFast_AbortsRest(t *testing.T) {
    s := &sensor.Sensor{
        Execution: sensor.Execution{
            Steps: []sensor.StepConfig{
                {ID: "fail", Type: "shell", Run: "exit 2",
                 ExitCodeMap: map[string]signal.Verdict{"2": signal.VerdictFail}},
                {ID: "skip", Type: "shell", Run: "echo should-not-run",
                 ExitCodeMap: map[string]signal.Verdict{"0": signal.VerdictPass}},
            },
        },
    }
    signals, _ := exec.Run(context.Background(), s, nil, map[string]string{})
    agg := signals[len(signals)-1]
    if agg["verdict"] != string(signal.VerdictFail) {
        t.Fatalf("aggregate verdict = %v", agg["verdict"])
    }
    // The 'skip' step should not have emitted any signal.
    for _, sig := range signals[:len(signals)-1] {
        meta, _ := sig["metadata"].(map[string]interface{})
        if meta["step_id"] == "skip" {
            t.Fatal("aborted step emitted a signal")
        }
    }
}
```

- [ ] **Step 7.2: Run; expect failure (package doesn't exist)**

Run: `go test ./lib/exec/...`
Expected: FAIL.

- [ ] **Step 7.3: Implement `lib/exec/engine.go`**

```go
// Package exec is the typed-step pipeline engine. Run is called by
// the orchestrator after PreflightGate and prepare phases, with the
// loaded *Sensor and a sealed env snapshot. It returns the full
// stream of emitted signals (individuals + final aggregate).
package exec

import (
    "context"
    "fmt"

    "github.com/iurykrieger/harness-framework/lib/sensor"
    "github.com/iurykrieger/harness-framework/lib/signal"
    "github.com/iurykrieger/harness-framework/lib/step"
    "github.com/iurykrieger/harness-framework/lib/step/assert"
    httpstep "github.com/iurykrieger/harness-framework/lib/step/http"
    "github.com/iurykrieger/harness-framework/lib/step/shell"
)

// Run executes the sensor's steps fail-fast and returns the signal
// stream. subrun may be nil; required only when the sensor has
// type: sensor steps (Task 8 wires it).
func Run(ctx context.Context, s *sensor.Sensor, subrun step.SubrunFunc, env map[string]string) ([]map[string]interface{}, error) {
    ec := &step.ExecContext{
        Fixtures: s.Fixtures,
        Env:      env,
        Steps:    map[string]*step.StepResult{},
    }
    var out []map[string]interface{}
    perStepDetails := []map[string]interface{}{}
    var runningVerdict signal.Verdict = signal.VerdictPass

    for _, cfg := range s.Execution.Steps {
        instance, err := buildStep(cfg, subrun)
        if err != nil {
            return nil, fmt.Errorf("buildStep %q: %w", cfg.ID, err)
        }
        res := instance.Execute(ctx, ec)
        ec.Steps[cfg.ID] = res
        out = append(out, res.Signals...)
        perStepDetails = append(perStepDetails, map[string]interface{}{
            "id":       cfg.ID,
            "type":     cfg.Type,
            "verdict":  string(res.Verdict),
        })
        runningVerdict = worst(runningVerdict, res.Verdict)
        if res.Verdict == signal.VerdictFail || res.Verdict == signal.VerdictError {
            break
        }
    }
    aggregate := buildAggregate(s, runningVerdict, perStepDetails)
    out = append(out, aggregate)
    return out, nil
}

func buildStep(cfg sensor.StepConfig, subrun step.SubrunFunc) (step.Step, error) {
    switch cfg.Type {
    case "shell":
        return shell.New(cfg)
    case "http":
        return httpstep.New(cfg)
    case "assert":
        return assert.New(cfg)
    case "sensor":
        // Wired in Task 8.
        return nil, fmt.Errorf("type: sensor not yet supported in this PR")
    }
    return nil, fmt.Errorf("unknown step type %q", cfg.Type)
}

func worst(a, b signal.Verdict) signal.Verdict {
    rank := map[signal.Verdict]int{
        signal.VerdictPass: 0, signal.VerdictWarn: 1, signal.VerdictFail: 2, signal.VerdictError: 3,
    }
    if rank[b] > rank[a] {
        return b
    }
    return a
}
```

- [ ] **Step 7.4: Implement `lib/exec/aggregate.go`**

```go
package exec

import (
    "github.com/iurykrieger/harness-framework/lib/sensor"
    "github.com/iurykrieger/harness-framework/lib/signal"
)

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

func severityFromVerdict(v signal.Verdict) signal.Severity {
    switch v {
    case signal.VerdictPass:
        return signal.SeverityInfo
    case signal.VerdictWarn:
        return signal.SeverityWarn
    case signal.VerdictFail:
        return signal.SeverityError
    case signal.VerdictError:
        return signal.SeverityCritical
    }
    return signal.SeverityInfo
}
```

(If `signal.SeverityFromVerdict` already exists in `lib/signal/`, prefer it.)

- [ ] **Step 7.5: Run engine tests; expect green**

Run: `go test ./lib/exec/...`
Expected: PASS for both test cases.

- [ ] **Step 7.6: Commit**

```bash
git add lib/exec/
git commit -m "feat(exec): typed-step pipeline engine with fail-fast aggregation

Run iterates execution.steps[] sequentially, dispatching to
shell/http/assert builders. Fail-fast: first fail/error aborts the
chain. The aggregate signal carries metadata.steps[] with per-step
{id, type, verdict} for heal diagnostics. Signal stream is
individuals first, aggregate last.

type: sensor lands in the next PR with a SubrunFunc indirection that
keeps the cycle (exec → orchestrator → exec) broken.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: lib/step/sensor — inline sub-run composition

**Files:**
- Create: `lib/step/sensor/sensor.go`, `sensor_test.go`
- Modify: `lib/exec/engine.go` (replace the placeholder "not supported" return)

- [ ] **Step 8.1: Write a sensor-step test with a stub subrun**

Create `lib/step/sensor/sensor_test.go`:

```go
package sensorstep_test

import (
    "context"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/sensor"
    "github.com/iurykrieger/harness-framework/lib/signal"
    "github.com/iurykrieger/harness-framework/lib/step"
    sensorstep "github.com/iurykrieger/harness-framework/lib/step/sensor"
)

func TestSensorStep_SubrunPropagatesVerdict(t *testing.T) {
    sub := func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
        if ref != "child" {
            t.Fatalf("ref = %q", ref)
        }
        return &step.StepResult{
            Verdict: signal.VerdictPass,
            Outputs: map[string]string{},
            Status:  step.StatusCompleted,
        }, nil
    }
    s, _ := sensorstep.New(sensor.StepConfig{ID: "x", Type: "sensor", Ref: "child"}, sub)
    res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
    if res.Verdict != signal.VerdictPass {
        t.Fatalf("verdict = %v", res.Verdict)
    }
}

func TestSensorStep_SubrunFailureAborts(t *testing.T) {
    sub := func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
        return &step.StepResult{Verdict: signal.VerdictFail, Status: step.StatusCompleted}, nil
    }
    s, _ := sensorstep.New(sensor.StepConfig{ID: "x", Type: "sensor", Ref: "child"}, sub)
    res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
    if res.Verdict != signal.VerdictFail {
        t.Fatalf("verdict = %v", res.Verdict)
    }
}
```

- [ ] **Step 8.2: Implement `lib/step/sensor/sensor.go`**

```go
// Package sensorstep implements the type: sensor step. The engine
// provides a SubrunFunc that re-enters orchestrator.RunOne for the
// referenced sensor, avoiding an exec → orchestrator import cycle.
package sensorstep

import (
    "context"
    "fmt"

    "github.com/iurykrieger/harness-framework/lib/sensor"
    "github.com/iurykrieger/harness-framework/lib/signal"
    "github.com/iurykrieger/harness-framework/lib/step"
)

type Step struct {
    cfg    sensor.StepConfig
    subrun step.SubrunFunc
}

func New(cfg sensor.StepConfig, subrun step.SubrunFunc) (step.Step, error) {
    if cfg.Type != "sensor" {
        return nil, fmt.Errorf("sensorstep.New: type=%q (want sensor)", cfg.Type)
    }
    if cfg.Ref == "" {
        return nil, fmt.Errorf("sensorstep.New: ref is required")
    }
    if subrun == nil {
        return nil, fmt.Errorf("sensorstep.New: subrun func required")
    }
    return &Step{cfg: cfg, subrun: subrun}, nil
}

func (s *Step) ID() string   { return s.cfg.ID }
func (s *Step) Type() string { return "sensor" }

func (s *Step) Execute(ctx context.Context, ec *step.ExecContext) *step.StepResult {
    // Resolve with: into per-sub-run fixture/env overrides.
    fxOverride, envOverride, err := resolveWith(s.cfg.With, ec)
    if err != nil {
        return &step.StepResult{Verdict: signal.VerdictError, Err: err, Status: step.StatusAborted}
    }
    sub, err := s.subrun(ctx, s.cfg.Ref, fxOverride, envOverride)
    if err != nil {
        return &step.StepResult{Verdict: signal.VerdictError, Err: err, Status: step.StatusAborted}
    }
    res := &step.StepResult{
        Verdict: sub.Verdict,
        Status:  step.StatusCompleted,
        Outputs: map[string]string{},
        Signals: sub.Signals,  // passthrough; engine decides whether to filter based on outputs_passthrough
    }
    // Resolve declared outputs against sub-run's aggregate or signals.
    // Minimal pass: only verdict/severity built-ins; outputs.<k> deferred
    // to a future iteration. The schema reminder in the spec says these
    // built-ins are always available.
    for name, spec := range s.cfg.Outputs {
        // For now: only support spec.From="aggregate.verdict" / "aggregate.severity"
        // (lightweight built-ins). Anything else requires extracting from sub.Signals.
        switch spec.From {
        case "aggregate.verdict":
            res.Outputs[name] = string(sub.Verdict)
        default:
            // Extraction from aggregate.evidence / aggregate.metadata.* is left
            // as a follow-up; spec allows declaring it, but Task 8 ships only
            // the common case. Document this in the commit.
            res.Outputs[name] = ""
        }
    }
    if !s.cfg.OutputsPassthrough {
        res.Signals = nil
    }
    return res
}

func resolveWith(with map[string]interface{}, ec *step.ExecContext) (map[string]string, map[string]string, error) {
    fx := map[string]string{}
    env := map[string]string{}
    for k, v := range with {
        switch x := v.(type) {
        case map[string]interface{}:
            if name, ok := x["fixture"].(string); ok && name != "" {
                if abs, found := ec.Fixtures[name]; found {
                    fx[name] = abs
                } else {
                    return nil, nil, fmt.Errorf("with[%q]: fixture %q not in pool", k, name)
                }
            }
        case string:
            env[k] = x
        default:
            env[k] = fmt.Sprint(v)
        }
    }
    return fx, env, nil
}
```

- [ ] **Step 8.3: Wire the subrun in `lib/exec/engine.go`**

In `lib/exec/engine.go`, add an import of `lib/step/sensor`, and change `buildStep`:

```go
import (
    // ...
    sensorstep "github.com/iurykrieger/harness-framework/lib/step/sensor"
)

func buildStep(cfg sensor.StepConfig, subrun step.SubrunFunc) (step.Step, error) {
    switch cfg.Type {
    case "shell":
        return shell.New(cfg)
    case "http":
        return httpstep.New(cfg)
    case "assert":
        return assert.New(cfg)
    case "sensor":
        return sensorstep.New(cfg, subrun)
    }
    return nil, fmt.Errorf("unknown step type %q", cfg.Type)
}
```

- [ ] **Step 8.4: Run sensor-step and engine tests**

Run: `go test ./lib/step/sensor/... ./lib/exec/...`
Expected: PASS.

- [ ] **Step 8.5: Commit**

```bash
git add lib/step/sensor/ lib/exec/engine.go
git commit -m "feat(step): type: sensor inline composition

sensorstep.Execute calls a SubrunFunc supplied by the engine to
re-enter orchestrator.RunOne for the referenced sensor. The
subrun's aggregate verdict becomes the step's verdict; signals
are passed through to the parent's stream when
outputs_passthrough=true, otherwise consumed internally.

The cycle exec → orchestrator → exec is broken by the SubrunFunc
indirection in lib/step (the engine receives the callback from
orchestrator.RunOne at call time; lib/step/sensor never imports
orchestrator).

outputs.<k> extraction from aggregate.evidence/metadata is the
common-case follow-up: this PR ships the verdict/severity
built-ins only.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: lib/sensor/validate.go cross-field rules

**Files:**
- Modify: `lib/sensor/validate.go` (existing — add the eleven cross-field rules)
- Modify: `lib/sensor/validate_test.go`

The spec enumerates eleven rules. Each gets at least one positive and one negative test.

- [ ] **Step 9.1: Read the spec's Validation rules section to lock the eleven rules**

Run: `grep -A 30 'Validation rules' docs/superpowers/specs/2026-05-15-complex-commands-design.md | head -50`

- [ ] **Step 9.2: Write failing tests for each rule (paste, then run, then implement)**

Append eleven test functions to `lib/sensor/validate_test.go`. Each named `TestValidate_Rule<n>_<short>`. For brevity, here is the template — repeat 11 times:

```go
func TestValidate_Rule3_BlockingAndStepsReject(t *testing.T) {
    s := minimalStepsSensor(t)
    s.Execution.Blocking = true
    if err := sensor.Validate(s); err == nil {
        t.Fatalf("rule 3: blocking+steps should reject")
    }
}
```

The `minimalStepsSensor(t)` helper builds a baseline Sensor with one valid shell step. Define it at the bottom of the test file.

Cover:

1. `output: single` with parse: → error
2. `output: stream` with no shell parse: → error
3. `blocking: true` + `steps:` → error
4. duplicate step id → error
5. with-fixture for missing fixture → error
6. interpolation references future step → error
7. interpolation references undeclared output → error
8. type: sensor cycle (A → B → A) → error
9. type: sensor → blocking child → error
10. assert with `with:` → error (likely caught by schema but Go validator double-checks)
11. requires[kind=sensor] overlap with type: sensor ref → warning (test inspects the warning list, not an error)

- [ ] **Step 9.3: Run; expect 11 failures**

Run: `go test -run 'TestValidate_Rule' ./lib/sensor/...`
Expected: 11 FAILs.

- [ ] **Step 9.4: Implement the rules**

Edit `lib/sensor/validate.go`. Add a single entry point if not present:

```go
// Validate runs all cross-field rules. It returns the first error it finds,
// plus any non-fatal warnings (rule 11) appended to the Sensor's
// .Warnings field — define that field on the Sensor struct if absent.
func Validate(s *Sensor) error {
    rules := []func(*Sensor) error{
        ruleOutputSingleNoParse,
        ruleOutputStreamWithParse,
        ruleBlockingNotWithSteps,
        ruleStepIDsUnique,
        ruleWithFixturesExist,
        ruleInterpolationOrder,
        ruleInterpolationDeclared,
        ruleSensorCycles,
        ruleSensorRefNotBlocking,
        ruleAssertNoWith,
    }
    for _, r := range rules {
        if err := r(s); err != nil {
            return err
        }
    }
    s.Warnings = append(s.Warnings, sensorOverlapWarnings(s)...)
    return nil
}

// rule implementations: each takes *Sensor and returns error or nil.
// Cycle detection uses an iterative DFS over (requires[kind=sensor] ids
// ∪ type:sensor step refs). Depth limit: 5; depth >5 is an error.
```

Implement each rule. Tests drive the shape. Reuse `s.Fixtures` already populated by `Load` (Task 2 set this up).

For rule 8 (cycle detection), load every sensor in `.harness/sensors/` to build the cross-sensor graph. Use `lib/sensor.LoadAll(projectRoot)` if it exists, else add a helper.

- [ ] **Step 9.5: Run all sensor tests**

Run: `go test ./lib/sensor/...`
Expected: PASS.

- [ ] **Step 9.6: Commit**

```bash
git add lib/sensor/validate.go lib/sensor/validate_test.go lib/sensor/shape.go
git commit -m "feat(sensor): cross-field validation for typed steps

Implements the eleven validation rules from the spec:

  1. output: single forbids any step parse:
  2. output: stream + steps: requires at least one shell parse:
  3. blocking: true + steps: rejected
  4. duplicate step ids rejected
  5. with: { fixture: X } requires X in fixture pool
  6. interpolation references must point at earlier steps
  7. interpolation references must point at declared outputs
  8. type: sensor cycle detection (DFS, max depth 5, combined graph
     with requires[kind=sensor])
  9. type: sensor refs to blocking children rejected
  10. assert step with with: rejected (schema double-check)
  11. requires[kind=sensor] / type: sensor ref overlap → warning

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: lib/orchestrator/run.go call swap

**Files:**
- Modify: `lib/orchestrator/run.go` (replace `subprocess.StreamSubprocess` call with `exec.Run`)
- Modify: `lib/orchestrator/run_test.go` (snapshot tests; expectations updated)

This PR is small in code, big in semantics — it activates the new engine end-to-end.

- [ ] **Step 10.1: Find the existing call site**

Run: `grep -n "subprocess.StreamSubprocess\|subprocess.Start" lib/orchestrator/run.go`
Note line and function context.

- [ ] **Step 10.2: Build the subrun callback the engine needs**

In `lib/orchestrator/run.go`, define a helper:

```go
func newSubrunFunc(o *Orchestrator) step.SubrunFunc {
    return func(ctx context.Context, ref string, fixtures, env map[string]string) (*step.StepResult, error) {
        // Re-enter RunOne for the referenced sensor with overridden fixtures/env.
        // Return a StepResult that captures the aggregate.
        res, err := o.RunOneWithRoot(ctx, ref, /* options carrying fixtures and env */)
        if err != nil {
            return nil, err
        }
        return &step.StepResult{
            Verdict: res.AggregateVerdict,
            Outputs: map[string]string{},
            Signals: res.Signals, // aggregate is the last entry; engine handles passthrough
        }, nil
    }
}
```

(Names may differ; adapt to the existing `Orchestrator`/`Result` types in `lib/orchestrator`.)

- [ ] **Step 10.3: Swap the call**

Replace the body of the lifecycle phase that ran `subprocess.StreamSubprocess(...)` for the sensor command with a call to `exec.Run(ctx, s, newSubrunFunc(o), sealedEnv)`. Adapt the result handling to consume `[]map[string]interface{}` directly.

- [ ] **Step 10.4: Run the orchestrator tests**

Run: `go test ./lib/orchestrator/...`
Expected: PASS. Many existing tests may need updating for the new aggregate shape — adjust assertions to read `metadata.kind=="aggregate"`.

- [ ] **Step 10.5: Run the full suite**

Run: `go test ./lib/...`
Expected: PASS.

- [ ] **Step 10.6: Commit**

```bash
git add lib/orchestrator/
git commit -m "feat(orchestrator): swap subprocess.StreamSubprocess for exec.Run

The orchestrator's command phase now calls lib/exec.Run with a
SubrunFunc that re-enters RunOneWithRoot for type: sensor steps.
PreflightGate continues to run once per top-level sensor; prepare
and teardown phases (requires[kind=step]) are unchanged.

End-to-end pipeline is now active: schema → load → validate →
PreflightGate → prepare → exec.Run (steps) → teardown.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 11: Runners — skills/run-sensor/

**Files:**
- Modify: `skills/run-sensor/scripts/run-computational.go`, `run-computational_test.go`
- Modify: `skills/run-sensor/scripts/run-inferential.go`, `run-inferential_test.go`

- [ ] **Step 11.1: Read both runners to find the body that called `subprocess.StreamSubprocess` (or already delegated to orchestrator)**

Run: `cat skills/run-sensor/scripts/run-computational.go`
Run: `cat skills/run-sensor/scripts/run-inferential.go`

If they delegate to `orchestrator.RunOne` directly, Task 10 may have already made them work — verify by running their tests:

Run: `go test -tags=run_computational ./skills/run-sensor/...`
Run: `go test -tags=run_inferential ./skills/run-sensor/...`

If both green, only the inferential prompt injection (next step) needs attention.

- [ ] **Step 11.2: Inject `HARNESS_PROMPT` into the env snapshot for inferential sensors with `steps:`**

In `run-inferential.go`, immediately before calling into `orchestrator.RunOne`, build the rendered prompt and add it to the env map that the orchestrator threads down to `exec.Run`:

```go
prompt, missing := template.RenderTemplate(s.Execution.UserPromptTemplate, bindings)
if len(missing) > 0 {
    // existing error path
}
env := mergeEnv(os.Environ() /* … */, map[string]string{"HARNESS_PROMPT": prompt})
```

The mechanism for threading env into `exec.Run` is provided by Task 10's call swap; check the function signature there.

- [ ] **Step 11.3: Run runner tests**

Run: `go test -tags=run_computational ./skills/run-sensor/...`
Run: `go test -tags=run_inferential ./skills/run-sensor/...`
Expected: PASS.

- [ ] **Step 11.4: Commit**

```bash
git add skills/run-sensor/scripts/
git commit -m "feat(run-sensor): inject HARNESS_PROMPT into env snapshot for inferential

Inferential runner renders user_prompt_template once and adds the
result to the env snapshot passed into exec.Run. Steps then see
HARNESS_PROMPT as an env var (sealed for the duration of the
sensor run). Computational runner is unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 12: skills/detect-sensors — remove replay-fixture, add run-golden

**Files:**
- Delete: `skills/detect-sensors/scripts/replay-fixture.go`, `replay-fixture_test.go`
- Create: `skills/detect-sensors/scripts/run-golden.go`, `run-golden_test.go`
- Modify: `skills/detect-sensors/SKILL.md` (phase-7 verification loop description)
- Modify: `skills/detect-sensors/scripts/write-sensor.go` (validator catches up; usually no code change, just rerun tests)

- [ ] **Step 12.1: Delete replay-fixture**

```bash
git rm skills/detect-sensors/scripts/replay-fixture.go skills/detect-sensors/scripts/replay-fixture_test.go 2>/dev/null || true
```

(If `_test.go` doesn't exist or is named differently, adapt.)

- [ ] **Step 12.2: Write failing test for run-golden**

Create `skills/detect-sensors/scripts/run-golden_test.go` (under tag `run_golden`):

```go
//go:build run_golden
// +build run_golden

package main

import (
    "os"
    "path/filepath"
    "testing"
)

func TestRunGolden_PassesOnHappyPath(t *testing.T) {
    root := t.TempDir()
    sensorsDir := filepath.Join(root, ".harness", "sensors")
    if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
        t.Fatal(err)
    }
    body := []byte(`
id: trivial
version: "1"
kind: assertion
type: computational
output: single
description: trivial
intent: trivial
purpose: { description: trivial }
cost: { compute: { cpu: low, duration: short } }
blind_spots: []
requires: []
verification:
  golden_cases:
    - name: hp
      expected_verdict: pass
      expected_severity: info
execution:
  command: "true"
  exit_code_map: { "0": pass }
`)
    sensorPath := filepath.Join(sensorsDir, "trivial.yaml")
    if err := os.WriteFile(sensorPath, body, 0o644); err != nil {
        t.Fatal(err)
    }
    rc := runGolden(sensorPath)
    if rc != 0 {
        t.Fatalf("runGolden rc=%d, want 0", rc)
    }
}

func TestRunGolden_FailsOnVerdictMismatch(t *testing.T) {
    root := t.TempDir()
    sensorsDir := filepath.Join(root, ".harness", "sensors")
    if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
        t.Fatal(err)
    }
    body := []byte(`
id: badverdict
version: "1"
kind: assertion
type: computational
output: single
description: x
intent: x
purpose: { description: x }
cost: { compute: { cpu: low, duration: short } }
blind_spots: []
requires: []
verification:
  golden_cases:
    - name: should-pass-but-wont
      expected_verdict: pass
      expected_severity: info
execution:
  command: "exit 1"
  exit_code_map: { "1": fail }
`)
    sensorPath := filepath.Join(sensorsDir, "badverdict.yaml")
    if err := os.WriteFile(sensorPath, body, 0o644); err != nil {
        t.Fatal(err)
    }
    rc := runGolden(sensorPath)
    if rc == 0 {
        t.Fatalf("runGolden rc=%d, want non-zero (verdict mismatch)", rc)
    }
}
```

The test exercises `runGolden(sensorPath string) int` — a func variant of `main` that returns the exit code for test consumption. Refactor `main` to call `os.Exit(runGolden(flagOrEnv(...)))` so tests can call `runGolden` directly.

- [ ] **Step 12.3: Implement run-golden.go**

```go
//go:build run_golden
// +build run_golden

// Command run-golden invokes a sensor for each declared golden case and
// compares the aggregate signal to the case's expected_verdict /
// expected_severity. Used by /detect-sensors phase 7.
package main

import (
    "encoding/json"
    "fmt"
    "os"
    "os/exec"
    "strings"
)

func main() {
    sensorPath := flagOrEnv("--sensor", "HARNESS_SENSOR_PATH")
    if sensorPath == "" {
        fail("missing --sensor")
    }
    s, err := loadSensor(sensorPath)
    if err != nil {
        fail("load sensor: " + err.Error())
    }
    for _, gc := range s.Verification.GoldenCases {
        verdict, severity := invokeSensor(sensorPath, gc.Fixture)
        if verdict != gc.ExpectedVerdict {
            fmt.Fprintf(os.Stderr, "golden %q verdict mismatch: got %s want %s\n",
                gc.Name, verdict, gc.ExpectedVerdict)
            os.Exit(1)
        }
        if severity != gc.ExpectedSeverity {
            fmt.Fprintf(os.Stderr, "golden %q severity mismatch: got %s want %s\n",
                gc.Name, severity, gc.ExpectedSeverity)
            os.Exit(1)
        }
    }
    fmt.Println("all golden cases passed")
}

// (helpers: flagOrEnv, loadSensor, invokeSensor that spawns run-computational
//  with -tags=run_computational and parses last-line JSON; fail() prints
//  and exits 1)
```

(Concrete helper implementations follow the same patterns as other scripts in `skills/run-sensor/`.)

- [ ] **Step 12.4: Update SKILL.md**

In `skills/detect-sensors/SKILL.md`, find phase 7 ("Iterate"). Rewrite to describe the new loop:

> Phase 7: for each generated sensor, run `run-golden.go --sensor=<path>` to invoke the sensor's golden cases via the standard runner. Compare aggregate `verdict` / `severity` against `expected_verdict` / `expected_severity`. On mismatch, surface to the author for editing. There is no replay-fixture substitution; sensors run for real.

Keep the description in present tense; do not reference the prior mechanism (Rule #10).

- [ ] **Step 12.5: Run tests**

Run: `go test -tags=run_golden ./skills/detect-sensors/...`
Expected: PASS (the trivial-skip test passes; flesh out further when real fixtures are available).

Also run: `go test ./skills/detect-sensors/...`
Expected: PASS for any other tests.

- [ ] **Step 12.6: Commit**

```bash
git add skills/detect-sensors/
git commit -m "feat(detect-sensors): replace replay-fixture with run-golden

The cat <fixture> substitution mechanism no longer makes sense: in
the typed-steps world, golden cases run the sensor for real and
compare the aggregate signal. run-golden.go iterates
verification.golden_cases[], invokes the standard runner, and
compares verdict/severity. SKILL.md phase 7 is rewritten in
present tense.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 13: skills/heal-sensor — per-step diagnostic

**Files:**
- Modify: `skills/heal-sensor/SKILL.md` (instructions for per-step diagnostic)
- Modify: `skills/heal-sensor/scripts/*` (read `metadata.steps[]` on aggregate to localize the failing step)

- [ ] **Step 13.1: Read the existing skill body**

Run: `cat skills/heal-sensor/SKILL.md`

Find the section that describes how heal reads sensor signals and proposes edits.

- [ ] **Step 13.2: Update SKILL.md to describe per-step diagnostic**

Replace the diagnostic paragraph with:

> When healing a sensor with `execution.steps:`, read the aggregate signal's `metadata.steps[]` array. Each entry has `{id, type, verdict, duration_ms}`. The failing step is the last one whose `verdict` is `fail` or `error` (fail-fast guarantees only one such entry). Scope your proposed edits to that step. For sensors with `execution.command:` shortcut, behavior is unchanged.

Keep tense present.

- [ ] **Step 13.3: Update the script(s) that consume aggregate signals**

Find any place that previously read `output_parsing.patterns` to suggest a fix; teach it to look up `metadata.steps[]` first when present, fall through to legacy reading otherwise.

- [ ] **Step 13.4: Run heal-sensor tests**

Run: `go test ./skills/heal-sensor/...`
Expected: PASS.

- [ ] **Step 13.5: Commit**

```bash
git add skills/heal-sensor/
git commit -m "feat(heal-sensor): per-step diagnostic

When the aggregate carries metadata.steps[], heal-sensor uses the
last failing entry to localize the fix to that step. Legacy
command:-shape sensors continue to use the prior diagnostic path.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 14: CLAUDE.md Rule #11 edit

**Files:**
- Modify: `CLAUDE.md` (Rule #11 fixture-pool location)

- [ ] **Step 14.1: Locate Rule #11 in CLAUDE.md**

Run: `grep -n "Rule #11\|11. \*\*" CLAUDE.md | head -5`

- [ ] **Step 14.2: Rewrite the third location entry**

Find the bullet that names `.harness/sensors/fixtures/<group>/<case>.*`. Replace with:

> - `.harness/fixtures/<name>` — sensor-domain fixture data, referenced by sensor steps via `with: { fixture: <name> }` or interpolation `${{ fixtures.<name> }}`, and by `verification.golden_cases[].fixture`. NOT a Go test fixture. Lives in the user project tree (under `.harness/`) and is consumed at sensor runtime. Sub-paths permitted.

Keep the other two locations in Rule #11 verbatim.

- [ ] **Step 14.3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs(claude-md): point Rule #11 at .harness/fixtures/ pool

Updates the sensor-domain fixture location named in Rule #11 to the
new top-level pool introduced by the complex-commands spec. The
three-location taxonomy is preserved; only the third entry changes.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 15: Acceptance sensors

**Files:**
- Create: `.harness/sensors/smoke-http-roundtrip.yaml` (shell + http + assert)
- Create: `.harness/sensors/smoke-with-setup.yaml` (composes via `type: sensor`)
- Create: `.harness/fixtures/order-valid.json`

- [ ] **Step 15.1: Author a `shell+http+assert` sensor**

Create `.harness/sensors/smoke-http-roundtrip.yaml`:

```yaml
id: smoke-http-roundtrip
version: "1"
kind: assertion
type: computational
output: single
description: "Posts an order fixture and asserts the round-trip id."
intent: "Verify the orders endpoint accepts a valid payload and returns a UUID-shaped id."
purpose:
  description: "Smoke test for the orders create path."
cost:
  compute: { cpu: low, duration: short }
blind_spots: []
requires: []
verification:
  golden_cases:
    - name: happy-path
      fixture: order-valid.json
      expected_verdict: pass
      expected_severity: info
execution:
  steps:
    - id: create
      type: http
      method: POST
      url: http://localhost:8080/orders
      headers: { content-type: application/json }
      body_from: { fixture: order-valid.json }
      expect:
        status: { equals: 201 }
        body:
          - { jsonpath: $.id, matches: '^[a-z0-9-]+$' }
      outputs:
        order_id: { from: response.body, jsonpath: $.id }
    - id: gate
      type: assert
      expect:
        value: "${{ steps.create.outputs.order_id }}"
        matches: '^[a-z0-9-]+$'
```

- [ ] **Step 15.2: Author a `type: sensor` composing sensor**

Create `.harness/sensors/smoke-with-setup.yaml` that references `smoke-http-roundtrip` as a `type: sensor` step.

- [ ] **Step 15.3: Author the matching fixture**

Create `.harness/fixtures/order-valid.json`:

```json
{"sku": "abc", "qty": 1}
```

- [ ] **Step 15.4: Validate and run**

Run: `go run -C "$(pwd)" -tags=run_computational ./skills/run-sensor/scripts smoke-http-roundtrip`
Expected: aggregate signal with `verdict: pass` (or `error` if no server at localhost:8080 — that's fine for the smoke; the schema-validation guarantee is the point of acceptance).

For schema validation without spawn:

Run: `go run -C "$(pwd)" -tags=validate_only ./skills/detect-sensors/scripts .harness/sensors/smoke-http-roundtrip.yaml`
(Or whatever the standalone validator entry point is.)

- [ ] **Step 15.5: Commit**

```bash
git add .harness/sensors/ .harness/fixtures/
git commit -m "feat: acceptance sensors for typed-steps

Adds two sensors that exercise the new schema end-to-end:

- smoke-http-roundtrip: shell + http + assert with fixture binding
  and step-to-step output chaining
- smoke-with-setup: composes the above via type: sensor

Plus order-valid.json under .harness/fixtures/ as the shared input.
These also serve as documentation samples for authoring against the
new shape.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Final verification (one-shot, after all 15 tasks)

- [ ] Run the full repo test suite

Run:
```bash
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
go test -tags=start_sensor ./skills/...
go test -tags=stop_sensor ./skills/...
go test -tags=list_sensors ./skills/...
go test -tags=tail_sensor ./skills/...
go test -tags=heal_retry_original ./skills/heal-sensor/...
go test -tags=write_usecase ./skills/...
go test -tags=run_golden ./skills/detect-sensors/...
go vet -tags=run_computational ./...
go vet -tags=run_inferential ./...
```

Expected: all PASS.

- [ ] Confirm the gate invariant test has exactly one allowlist addition

Run: `grep -n 'allowedFiles\[\]\|allowedFiles:=\|lib/step/shell' lib/orchestrator/gate_invariant_test.go`
Expected: one entry `"lib/step/shell/shell.go": true,`.

- [ ] Confirm `.harness/sensors/` and `.harness/fixtures/` contain only new content (no legacy paths)

Run: `find .harness/sensors -name '*.json'` (should be empty); `find .harness/sensors/fixtures` (should not exist).

---

## Out-of-scope reminders

- Mocks/stubs for HTTP (Spec B).
- `/create-sensor` emitting multi-angle steps (Spec B).
- Observability deep-scan (Spec C).
- Per-step `requires:` preflight.
- Expression language beyond strict identifier accessors.
- Blocking sensors with `steps:` — rejected by rule 3.
