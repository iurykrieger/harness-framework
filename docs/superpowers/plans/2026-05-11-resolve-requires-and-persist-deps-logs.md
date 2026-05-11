# Resolve Full `requires[]` Set and Persist Per-Dep Logs — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `RunOne` honour every `requires[]` kind (tool, context, env; ignore permission) via a unified Phase 0 gate, and make every sensor invocation (root, non-blocking dep, blocking dep) write `.runtime/sensors/<id>/<run_id>/{raw,signals}.log` with `signals.log == stdout JSONL` byte-for-byte.

**Architecture:** Two converging changes inside `lib/orchestrator/lifecycle.go::RunOne` and `lib/orchestrator/live_deps.go::startBlockingDep`. New library code in `lib/sensor/requires.go`, `lib/watcher/spawn.go`, `lib/heal/rules/missing_context.go`. `lib/subprocess/stream.go` gains an optional `RawLogPath` tee. All emissions in the orchestrator go through a new `emitSignalWithPersistence` helper. No schema bump, no auto-fix, no permission parser.

**Tech Stack:** Go 1.25, standard library only for new code (`os`, `os/exec`, `io`, `filepath`, `regexp`, `encoding/json`, `testing`). The watcher extraction inherits `github.com/google/uuid` from the existing `start.go`. JSON Schema Draft 2020-12 unchanged via `github.com/santhosh-tekuri/jsonschema/v5`.

**Spec:** `docs/superpowers/specs/2026-05-11-resolve-requires-and-persist-deps-logs-design.md`

---

## File map

### Created

- `lib/heal/rules/missing_context.go` — heal rule matching `Required context path "X" does not exist`
- `lib/heal/rules/missing_context_test.go`
- `lib/sensor/requires.go` — `Failure`, `Gate`, `GateOpts`, `CheckRequiresGate`, `BuildRequiresGateSignal`, `checkTool`, `checkContext`, `checkEnv`
- `lib/sensor/requires_test.go`
- `lib/watcher/spawn.go` — `SpawnOpts`, `Spawn`, `BinaryPath` (extracted from start.go)
- `lib/watcher/spawn_test.go`
- `lib/watcher/spawn_unix.go` — `sysProcAttr` + `killGroup` (extracted from start_unix.go)
- `lib/orchestrator/persistence.go` — `prepareRuntimeDir`, `emitSignalWithPersistence`
- `lib/orchestrator/persistence_test.go`
- `sensors/fixtures/requires-tool-missing.json` — golden fixture
- `sensors/fixtures/requires-context-missing.json` — golden fixture
- `scripts/smoke-requires-deps-logs.sh` — end-to-end smoke test

### Modified

- `lib/heal/classify.go` — add `ShapeMissingContext` constant; extend `IsKnown` switch
- `lib/heal/classify_test.go` — `TestShape_IsKnown` table gets new entry
- `lib/heal/rules/registry.go` — register `missingContext{}` in `Registered()`
- `lib/heal/rules/stderr_pattern.go` — extend `binary-not-found` regex to match the new `Required tool "X" is not on PATH` rationale (or add a new rule line — see Task 1.3)
- `lib/sensor/env.go` — `BuildMissingEnvSignal` becomes wrapper around `BuildRequiresGateSignal`
- `lib/subprocess/stream.go` — `StreamConfig.RawLogPath` optional field; tee implementation
- `lib/subprocess/stream_test.go` — new cases for empty and populated `RawLogPath`
- `lib/orchestrator/lifecycle.go` — Phase 0 swap; runtime-dir + tee wiring; use `emitSignalWithPersistence`
- `lib/orchestrator/lifecycle_test.go` — gate failure cases + persistence assertions
- `lib/orchestrator/live_deps.go` — `startBlockingDep` calls `watcher.Spawn`; all emissions via `emitSignalWithPersistence`
- `lib/orchestrator/live_deps_test.go` — new cases for watcher spawn + log files
- `lib/orchestrator/preflight.go` — cascade emissions via `emitSignalWithPersistence`
- `lib/orchestrator/preflight_test.go` — assertions on cascade signal persisted
- `skills/start-sensor/scripts/start.go` — call `watcher.Spawn` instead of inline `os.StartProcess`
- `skills/start-sensor/scripts/start_unix.go` — drop `watcherSysProcAttr` and `watcherBinaryPath` (moved to `lib/watcher`); keep `killPID`/`killGroup` if still used locally

### Untouched (per spec §3, §5)

- `schemas/sensor.json`, `schemas/signal.json`
- `skills/run-sensor/scripts/run-computational.go`, `run-inferential.go`
- `skills/start-sensor/scripts/watcher.go` (the watcher binary itself)
- `skills/start-sensor/scripts/watcher_test.go`

---

## Working principles

1. **One commit per task.** Each numbered task ends in a commit; the test it adds must pass at commit time.
2. **Run before commit.** Every task runs `go test` and `go vet` for the touched packages before staging.
3. **No `go build` for runners.** Tests use build tags (`go test -tags=run_computational`, `-tags=start_sensor`).
4. **Worktree path.** All file paths in this plan are relative to `/Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/install-sensor-requirements/`.
5. **Schemas dir.** Tests that load schemas pass `--schemas-dir=<absolute>` or rely on `schema.LoadValidator` walking up.

---

## Task 1: `lib/heal/rules/missing_context.go` + register

**Files:**
- Create: `lib/heal/rules/missing_context.go`
- Create: `lib/heal/rules/missing_context_test.go`
- Modify: `lib/heal/classify.go`
- Modify: `lib/heal/classify_test.go`
- Modify: `lib/heal/rules/registry.go`

### Task 1.1 — Add `ShapeMissingContext` constant

- [ ] **Step 1.1.1: Write the failing test**

Edit `lib/heal/classify_test.go` — find `TestShape_IsKnown` cases map and add an entry for `missing-context`:

```go
func TestShape_IsKnown(t *testing.T) {
    cases := map[heal.Shape]bool{
        heal.ShapeMissingEnv:         true,
        heal.ShapeBinaryNotFound:     true,
        heal.ShapeEnvFileAbsent:      true,
        heal.ShapeServiceUnavailable: true,
        heal.ShapeMissingContext:     true, // NEW
        heal.Shape("nonsense"):       false,
        heal.Shape(""):               false,
    }
    for s, want := range cases {
        if got := s.IsKnown(); got != want {
            t.Errorf("Shape(%q).IsKnown() = %v, want %v", s, got, want)
        }
    }
}
```

- [ ] **Step 1.1.2: Run test to verify it fails**

Run: `go test ./lib/heal/ -run TestShape_IsKnown -v`
Expected: FAIL — `undefined: heal.ShapeMissingContext`

- [ ] **Step 1.1.3: Implement the constant**

Edit `lib/heal/classify.go` to add the constant and update the switch:

```go
const (
    ShapeMissingEnv         Shape = "missing-env"
    ShapeBinaryNotFound     Shape = "binary-not-found"
    ShapeEnvFileAbsent      Shape = "env-file-absent"
    ShapeServiceUnavailable Shape = "service-unavailable"
    ShapeMissingContext     Shape = "missing-context"
)

func (s Shape) IsKnown() bool {
    switch s {
    case ShapeMissingEnv, ShapeBinaryNotFound, ShapeEnvFileAbsent, ShapeServiceUnavailable, ShapeMissingContext:
        return true
    }
    return false
}
```

- [ ] **Step 1.1.4: Run test to verify it passes**

Run: `go test ./lib/heal/ -run TestShape_IsKnown -v`
Expected: PASS

### Task 1.2 — Write the `missing_context` rule

- [ ] **Step 1.2.1: Write the failing test**

Create `lib/heal/rules/missing_context_test.go`:

```go
package rules

import (
    "testing"

    "github.com/iurykrieger/harness-framework/lib/heal"
)

func TestMissingContextRule_Match(t *testing.T) {
    cases := []struct {
        name      string
        sig       heal.Signal
        wantOK    bool
        wantShape heal.Shape
        wantDetail string
    }{
        {
            name: "matches formatted rationale",
            sig: heal.Signal{
                Verdict: "error",
                Evidence: []heal.SignalEvidence{
                    {Rationale: `Required context path "./.env" does not exist`},
                },
            },
            wantOK:     true,
            wantShape:  heal.ShapeMissingContext,
            wantDetail: "./.env",
        },
        {
            name: "matches with surrounding text",
            sig: heal.Signal{
                Verdict: "error",
                Evidence: []heal.SignalEvidence{
                    {Rationale: `something Required context path "/abs/path/file.yaml" does not exist trailing`},
                },
            },
            wantOK:     true,
            wantShape:  heal.ShapeMissingContext,
            wantDetail: "/abs/path/file.yaml",
        },
        {
            name: "no match when verdict not error",
            sig: heal.Signal{
                Verdict: "pass",
                Evidence: []heal.SignalEvidence{
                    {Rationale: `Required context path "./x" does not exist`},
                },
            },
            wantOK: false,
        },
        {
            name: "no match when rationale shape different",
            sig: heal.Signal{
                Verdict: "error",
                Evidence: []heal.SignalEvidence{
                    {Rationale: "some other failure"},
                },
            },
            wantOK: false,
        },
        {
            name: "scans all evidence entries",
            sig: heal.Signal{
                Verdict: "error",
                Evidence: []heal.SignalEvidence{
                    {Rationale: "unrelated"},
                    {Rationale: `Required context path "second" does not exist`},
                },
            },
            wantOK:     true,
            wantShape:  heal.ShapeMissingContext,
            wantDetail: "second",
        },
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            ok, shape, detail := missingContext{}.Match(tc.sig, heal.FailedSensor{})
            if ok != tc.wantOK {
                t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
            }
            if !ok {
                return
            }
            if shape != tc.wantShape {
                t.Errorf("shape = %v, want %v", shape, tc.wantShape)
            }
            if detail != tc.wantDetail {
                t.Errorf("detail = %q, want %q", detail, tc.wantDetail)
            }
        })
    }
}

func TestMissingContextRule_Name(t *testing.T) {
    if got := (missingContext{}).Name(); got != "missing-context" {
        t.Fatalf("Name() = %q, want %q", got, "missing-context")
    }
}
```

- [ ] **Step 1.2.2: Run test to verify it fails**

Run: `go test ./lib/heal/rules/ -run TestMissingContextRule -v`
Expected: FAIL — `undefined: missingContext`

- [ ] **Step 1.2.3: Implement the rule**

Create `lib/heal/rules/missing_context.go`:

```go
// lib/heal/rules/missing_context.go
package rules

import (
    "regexp"

    "github.com/iurykrieger/harness-framework/lib/heal"
)

// missingContext fires when verdict=error AND an evidence rationale
// matches `Required context path "<PATH>" does not exist`. The PATH
// is returned as the rule's detail string so /heal-sensor can decide
// remediation (e.g. propose `mkdir -p <path>` or `touch <path>`).
type missingContext struct{}

var missingContextRegex = regexp.MustCompile(`Required context path "([^"]+)" does not exist`)

func (missingContext) Name() string { return "missing-context" }

func (missingContext) Match(signal heal.Signal, _ heal.FailedSensor) (bool, heal.Shape, string) {
    if signal.Verdict != "error" {
        return false, "", ""
    }
    for _, ev := range signal.Evidence {
        if m := missingContextRegex.FindStringSubmatch(ev.Rationale); m != nil {
            return true, heal.ShapeMissingContext, m[1]
        }
    }
    return false, "", ""
}
```

- [ ] **Step 1.2.4: Run test to verify it passes**

Run: `go test ./lib/heal/rules/ -run TestMissingContextRule -v`
Expected: PASS

### Task 1.3 — Extend the existing `binary-not-found` regex for the new tool rationale

Look at `lib/heal/rules/stderr_pattern.go` first to find where `command not found` is matched. The spec (§6 "Per-check rationale format") says the existing `binary-not-found` shape is reused for `tool`; we extend the matcher to also accept `Required tool "X" is not on PATH`.

- [ ] **Step 1.3.1: Read the existing rule**

Run: `cat lib/heal/rules/stderr_pattern.go`

Identify the regex that currently matches `\bcommand not found\b` and the rule struct.

- [ ] **Step 1.3.2: Write the failing test**

Add this test to `lib/heal/rules/stderr_pattern_test.go` (append to the existing test cases, or add a new test function if the structure prefers):

```go
func TestStderrPatternRule_MatchesRequiredToolNotOnPath(t *testing.T) {
    rule := stderrPatternRule{}
    sig := heal.Signal{
        Verdict: "error",
        Evidence: []heal.SignalEvidence{
            {Rationale: `Required tool "docker" is not on PATH`},
        },
    }
    ok, shape, detail := rule.Match(sig, heal.FailedSensor{Tools: []string{"docker"}})
    if !ok {
        t.Fatal("expected match")
    }
    if shape != heal.ShapeBinaryNotFound {
        t.Errorf("shape = %v, want %v", shape, heal.ShapeBinaryNotFound)
    }
    if detail != "docker" {
        t.Errorf("detail = %q, want %q", detail, "docker")
    }
}
```

- [ ] **Step 1.3.3: Run test to verify it fails**

Run: `go test ./lib/heal/rules/ -run TestStderrPatternRule_MatchesRequiredToolNotOnPath -v`
Expected: FAIL — the existing regex doesn't match the new phrasing.

- [ ] **Step 1.3.4: Extend the regex**

In `lib/heal/rules/stderr_pattern.go`, find the binary-not-found regex declaration (likely a `regexp.MustCompile(...)`). Add an alternation to also match `Required tool "X" is not on PATH` and capture the tool name.

Pattern to add (depending on existing pattern shape — adapt):

```go
// existing: `\bcommand not found\b`
// add alternative: `Required tool "([^"]+)" is not on PATH`
// combined approach: use TWO patterns and pick whichever matched, OR extend
// the regex with alternation and a named capture.
```

Concrete implementation depends on the file's current shape. If the rule has a single regex, refactor to keep a slice of patterns each with its own extractor — see how `lib/heal/patterns.go::stderrPatterns` already uses a slice of `{re, shape}`. Mirror that structure.

After the edit, the test from Step 1.3.2 must pass.

- [ ] **Step 1.3.5: Run all heal/rules tests to verify no regression**

Run: `go test ./lib/heal/...`
Expected: PASS (all existing tests + the two new ones)

### Task 1.4 — Register `missingContext` in `rules.Registered`

- [ ] **Step 1.4.1: Modify the registry**

Edit `lib/heal/rules/registry.go`:

```go
func Registered() []heal.Rule {
    return []heal.Rule{
        missingEnv{},
        missingContext{}, // NEW
        healHint{},
        exitCode127{},
        prepareTemplateCopy{},
        stderrPatternRule{},
    }
}
```

(Place `missingContext` right after `missingEnv` — both are setup-shape rules that match formatted rationales emitted by the gate.)

- [ ] **Step 1.4.2: Verify no test regression**

Run: `go test ./lib/heal/...`
Expected: PASS

### Task 1.5 — Commit Task 1

- [ ] **Step 1.5.1: Commit**

```bash
git add lib/heal/classify.go lib/heal/classify_test.go \
        lib/heal/rules/missing_context.go lib/heal/rules/missing_context_test.go \
        lib/heal/rules/stderr_pattern.go lib/heal/rules/stderr_pattern_test.go \
        lib/heal/rules/registry.go
git commit -m "$(cat <<'EOF'
feat(heal): add missing-context shape + rule; extend binary-not-found phrasing

ShapeMissingContext joins the closed enum of setup-shape failures. The new
missing_context rule matches the rationale emitted by the upcoming
requires[] gate (Task 2) for tool/context/env preconditions:

  Required context path "X" does not exist

stderr_pattern.go's binary-not-found matcher gains a second phrasing —
Required tool "X" is not on PATH — so the same shape covers both the
pre-flight gate output and post-execution stderr archaeology.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: `lib/sensor/requires.go` — Gate API + checks + signal builder

**Files:**
- Create: `lib/sensor/requires.go`
- Create: `lib/sensor/requires_test.go`
- Modify: `lib/sensor/env.go` (`BuildMissingEnvSignal` becomes wrapper)

### Task 2.1 — Types and skeleton

- [ ] **Step 2.1.1: Write the failing test (types + zero value)**

Create `lib/sensor/requires_test.go`:

```go
package sensor

import (
    "errors"
    "os"
    "reflect"
    "testing"
)

func TestGate_FailedZeroValue(t *testing.T) {
    var g Gate
    if g.Failed() {
        t.Fatal("zero-value Gate should not be Failed")
    }
}

func TestGate_FailedWhenNonEmpty(t *testing.T) {
    g := Gate{Failures: []Failure{{Kind: "tool"}}}
    if !g.Failed() {
        t.Fatal("non-empty Failures should make Failed() true")
    }
}

func TestFailureFields(t *testing.T) {
    f := Failure{
        Kind:       "tool",
        Identifier: "docker",
        Rationale:  `Required tool "docker" is not on PATH`,
        HealShape:  "binary-not-found",
    }
    // Smoke test: all fields settable and readable via reflect.
    v := reflect.ValueOf(f)
    if v.NumField() != 4 {
        t.Fatalf("Failure should have 4 fields, got %d", v.NumField())
    }
}
```

- [ ] **Step 2.1.2: Run test to verify it fails**

Run: `go test ./lib/sensor/ -run TestGate -v && go test ./lib/sensor/ -run TestFailureFields -v`
Expected: FAIL — `undefined: Gate` etc.

- [ ] **Step 2.1.3: Create the skeleton**

Create `lib/sensor/requires.go`:

```go
package sensor

import (
    "errors"
    "fmt"
    "os"
    "os/exec"
    "strings"
)

// Failure describes one unsatisfied precondition. Produced by
// CheckRequiresGate; consumed by BuildRequiresGateSignal.
type Failure struct {
    Kind       string // "tool" | "context" | "env"
    Identifier string // tool: name; context: path; env: var name
    Rationale  string // formatted text for evidence[].rationale
    HealShape  string // value of lib/heal.Shape encoded as string to avoid import cycle
}

// Gate aggregates all preconditions failures emitted by CheckRequiresGate.
type Gate struct {
    Failures []Failure
}

// Failed reports whether the gate found at least one missing precondition.
func (g Gate) Failed() bool { return len(g.Failures) > 0 }

// GateOpts injects external dependencies for testability.
type GateOpts struct {
    LookupEnv func(string) (string, bool) // default: LookupEnvFn
    LookPath  func(string) (string, error) // default: exec.LookPath
    Stat      func(string) error           // default: statHelper
}

// CheckRequiresGate runs the precondition checks in fixed order —
// tool → context → env — and collects all failures (no fail-fast).
// Within a kind, failures appear in the same order as the corresponding
// entries appear in requires[]. kind=sensor / kind=step / kind=permission
// entries are ignored: sensor is handled by the orchestrator DAG, step by
// the prepare phase, permission by Claude Code's permission engine.
func CheckRequiresGate(sensorJSON map[string]interface{}, opts GateOpts) Gate {
    return Gate{} // TODO Task 2.2
}

// BuildRequiresGateSignal constructs the verdict=error Signal emitted
// when CheckRequiresGate returns a non-empty Gate. One evidence[] entry
// per failure, preserving Gate.Failures order. metadata.heal_hint =
// "<HealShape>:<Identifier>" derived from the first failure.
// remediation.instructions aggregates a human summary listing every failure.
func BuildRequiresGateSignal(env Envelope, outputMode string, gate Gate) map[string]interface{} {
    return nil // TODO Task 2.5
}

func statHelper(path string) error {
    _, err := os.Stat(path)
    return err
}

func resolveOpts(opts GateOpts) GateOpts {
    if opts.LookupEnv == nil {
        opts.LookupEnv = LookupEnvFn
    }
    if opts.LookPath == nil {
        opts.LookPath = exec.LookPath
    }
    if opts.Stat == nil {
        opts.Stat = statHelper
    }
    return opts
}

// Silence unused import warnings until the real implementation lands.
var _ = errors.New
var _ = fmt.Sprintf
var _ = strings.TrimSpace
```

- [ ] **Step 2.1.4: Run test to verify it passes**

Run: `go test ./lib/sensor/ -run "TestGate|TestFailureFields" -v`
Expected: PASS

### Task 2.2 — `checkTool`

- [ ] **Step 2.2.1: Write the failing test**

Append to `lib/sensor/requires_test.go`:

```go
func TestCheckTool_MissingFromPATH(t *testing.T) {
    opts := GateOpts{
        LookPath: func(name string) (string, error) {
            return "", exec.ErrNotFound
        },
    }
    s := map[string]interface{}{
        "requires": []interface{}{
            map[string]interface{}{"kind": "tool", "name": "docker"},
        },
    }
    g := CheckRequiresGate(s, opts)
    if !g.Failed() {
        t.Fatalf("expected failure, got %+v", g)
    }
    if len(g.Failures) != 1 {
        t.Fatalf("want 1 failure, got %d", len(g.Failures))
    }
    f := g.Failures[0]
    if f.Kind != "tool" {
        t.Errorf("Kind = %q, want %q", f.Kind, "tool")
    }
    if f.Identifier != "docker" {
        t.Errorf("Identifier = %q, want %q", f.Identifier, "docker")
    }
    if f.Rationale != `Required tool "docker" is not on PATH` {
        t.Errorf("Rationale = %q, want %q", f.Rationale, `Required tool "docker" is not on PATH`)
    }
    if f.HealShape != "binary-not-found" {
        t.Errorf("HealShape = %q, want %q", f.HealShape, "binary-not-found")
    }
}

func TestCheckTool_PresentOnPATH(t *testing.T) {
    opts := GateOpts{
        LookPath: func(name string) (string, error) {
            return "/usr/bin/" + name, nil
        },
    }
    s := map[string]interface{}{
        "requires": []interface{}{
            map[string]interface{}{"kind": "tool", "name": "docker"},
        },
    }
    g := CheckRequiresGate(s, opts)
    if g.Failed() {
        t.Fatalf("expected no failure, got %+v", g.Failures)
    }
}

func TestCheckTool_OtherLookPathError(t *testing.T) {
    opts := GateOpts{
        LookPath: func(name string) (string, error) {
            return "", errors.New("some other error")
        },
    }
    s := map[string]interface{}{
        "requires": []interface{}{
            map[string]interface{}{"kind": "tool", "name": "docker"},
        },
    }
    g := CheckRequiresGate(s, opts)
    if !g.Failed() {
        t.Fatal("non-ErrNotFound LookPath error should still register a failure")
    }
}

func TestCheckTool_MalformedEntriesIgnored(t *testing.T) {
    opts := GateOpts{
        LookPath: func(name string) (string, error) { return "", exec.ErrNotFound },
    }
    s := map[string]interface{}{
        "requires": []interface{}{
            map[string]interface{}{"kind": "tool"},                  // missing name
            map[string]interface{}{"kind": "tool", "name": ""},      // empty name
            map[string]interface{}{"kind": "tool", "name": "real"},  // counted
        },
    }
    g := CheckRequiresGate(s, opts)
    if len(g.Failures) != 1 || g.Failures[0].Identifier != "real" {
        t.Fatalf("expected only 'real', got %+v", g.Failures)
    }
}
```

- [ ] **Step 2.2.2: Run test to verify it fails**

Run: `go test ./lib/sensor/ -run TestCheckTool -v`
Expected: FAIL — `CheckRequiresGate` returns empty Gate.

- [ ] **Step 2.2.3: Implement `checkTool`**

Replace the stub `CheckRequiresGate` body in `lib/sensor/requires.go`:

```go
func CheckRequiresGate(sensorJSON map[string]interface{}, opts GateOpts) Gate {
    opts = resolveOpts(opts)
    var g Gate
    for _, f := range checkTool(sensorJSON, opts) {
        g.Failures = append(g.Failures, f)
    }
    // TODO Task 2.3: checkContext
    // TODO Task 2.4: checkEnv
    return g
}

func checkTool(sensorJSON map[string]interface{}, opts GateOpts) []Failure {
    entries := Project(sensorJSON, "tool")
    var out []Failure
    for _, entry := range entries {
        name, _ := entry["name"].(string)
        if name == "" {
            continue
        }
        if _, err := opts.LookPath(name); err == nil {
            continue
        }
        out = append(out, Failure{
            Kind:       "tool",
            Identifier: name,
            Rationale:  fmt.Sprintf(`Required tool %q is not on PATH`, name),
            HealShape:  "binary-not-found",
        })
    }
    return out
}
```

Remove the silencing `var _ = ...` lines for `errors` / `fmt` / `strings` once the real usage covers them (keep imports clean — only those actually used).

- [ ] **Step 2.2.4: Run test to verify it passes**

Run: `go test ./lib/sensor/ -run TestCheckTool -v`
Expected: PASS

### Task 2.3 — `checkContext`

- [ ] **Step 2.3.1: Write the failing test**

Append to `lib/sensor/requires_test.go`:

```go
func TestCheckContext_MissingPath(t *testing.T) {
    opts := GateOpts{
        LookPath: func(string) (string, error) { return "ok", nil },
        Stat: func(path string) error {
            return os.ErrNotExist
        },
    }
    s := map[string]interface{}{
        "requires": []interface{}{
            map[string]interface{}{"kind": "context", "path": "./.env"},
        },
    }
    g := CheckRequiresGate(s, opts)
    if !g.Failed() || len(g.Failures) != 1 {
        t.Fatalf("expected 1 failure, got %+v", g.Failures)
    }
    f := g.Failures[0]
    if f.Kind != "context" {
        t.Errorf("Kind = %q", f.Kind)
    }
    if f.Identifier != "./.env" {
        t.Errorf("Identifier = %q", f.Identifier)
    }
    if f.Rationale != `Required context path "./.env" does not exist` {
        t.Errorf("Rationale = %q", f.Rationale)
    }
    if f.HealShape != "missing-context" {
        t.Errorf("HealShape = %q", f.HealShape)
    }
}

func TestCheckContext_PathExists(t *testing.T) {
    opts := GateOpts{
        LookPath: func(string) (string, error) { return "ok", nil },
        Stat:     func(path string) error { return nil },
    }
    s := map[string]interface{}{
        "requires": []interface{}{
            map[string]interface{}{"kind": "context", "path": "./.env"},
        },
    }
    g := CheckRequiresGate(s, opts)
    if g.Failed() {
        t.Fatalf("expected no failure, got %+v", g.Failures)
    }
}

func TestCheckContext_StatNonNotExistError(t *testing.T) {
    opts := GateOpts{
        LookPath: func(string) (string, error) { return "ok", nil },
        Stat:     func(path string) error { return errors.New("permission denied") },
    }
    s := map[string]interface{}{
        "requires": []interface{}{
            map[string]interface{}{"kind": "context", "path": "./.env"},
        },
    }
    g := CheckRequiresGate(s, opts)
    if !g.Failed() || len(g.Failures) != 1 {
        t.Fatalf("expected 1 failure for non-NotExist error, got %+v", g.Failures)
    }
    if !strings.Contains(g.Failures[0].Rationale, "cannot stat") {
        t.Errorf("expected rationale to mention 'cannot stat', got %q", g.Failures[0].Rationale)
    }
    if g.Failures[0].HealShape != "missing-context" {
        t.Errorf("HealShape should remain missing-context, got %q", g.Failures[0].HealShape)
    }
}
```

- [ ] **Step 2.3.2: Run test to verify it fails**

Run: `go test ./lib/sensor/ -run TestCheckContext -v`
Expected: FAIL — `checkContext` not implemented.

- [ ] **Step 2.3.3: Implement `checkContext`**

Edit `lib/sensor/requires.go` — add to `CheckRequiresGate`:

```go
func CheckRequiresGate(sensorJSON map[string]interface{}, opts GateOpts) Gate {
    opts = resolveOpts(opts)
    var g Gate
    g.Failures = append(g.Failures, checkTool(sensorJSON, opts)...)
    g.Failures = append(g.Failures, checkContext(sensorJSON, opts)...)
    // TODO Task 2.4: checkEnv
    return g
}

func checkContext(sensorJSON map[string]interface{}, opts GateOpts) []Failure {
    entries := Project(sensorJSON, "context")
    var out []Failure
    for _, entry := range entries {
        path, _ := entry["path"].(string)
        if path == "" {
            continue
        }
        err := opts.Stat(path)
        if err == nil {
            continue
        }
        rationale := fmt.Sprintf(`Required context path %q does not exist`, path)
        if !errors.Is(err, os.ErrNotExist) {
            rationale = fmt.Sprintf(`Required context path %q: cannot stat: %v`, path, err)
        }
        out = append(out, Failure{
            Kind:       "context",
            Identifier: path,
            Rationale:  rationale,
            HealShape:  "missing-context",
        })
    }
    return out
}
```

- [ ] **Step 2.3.4: Run test to verify it passes**

Run: `go test ./lib/sensor/ -run TestCheckContext -v`
Expected: PASS

### Task 2.4 — `checkEnv`

- [ ] **Step 2.4.1: Write the failing test**

Append to `lib/sensor/requires_test.go`:

```go
func TestCheckEnv_MissingNonOptional(t *testing.T) {
    opts := GateOpts{
        LookPath: func(string) (string, error) { return "ok", nil },
        Stat:     func(string) error { return nil },
        LookupEnv: func(name string) (string, bool) {
            return "", false
        },
    }
    s := map[string]interface{}{
        "requires": []interface{}{
            map[string]interface{}{"kind": "env", "name": "GITHUB_TOKEN", "description": "PAT"},
            map[string]interface{}{"kind": "env", "name": "DEBUG", "optional": true},
        },
    }
    g := CheckRequiresGate(s, opts)
    if len(g.Failures) != 1 {
        t.Fatalf("expected 1 failure, got %+v", g.Failures)
    }
    f := g.Failures[0]
    if f.Kind != "env" || f.Identifier != "GITHUB_TOKEN" {
        t.Errorf("got %+v", f)
    }
    if f.Rationale != "Required environment variable GITHUB_TOKEN is not set: PAT" {
        t.Errorf("Rationale = %q", f.Rationale)
    }
    if f.HealShape != "missing-env" {
        t.Errorf("HealShape = %q", f.HealShape)
    }
}

func TestCheckEnv_RationaleWithoutDescription(t *testing.T) {
    opts := GateOpts{
        LookPath: func(string) (string, error) { return "ok", nil },
        Stat:     func(string) error { return nil },
        LookupEnv: func(string) (string, bool) { return "", false },
    }
    s := map[string]interface{}{
        "requires": []interface{}{
            map[string]interface{}{"kind": "env", "name": "REGION"},
        },
    }
    g := CheckRequiresGate(s, opts)
    if g.Failures[0].Rationale != "Required environment variable REGION is not set" {
        t.Errorf("Rationale = %q", g.Failures[0].Rationale)
    }
}
```

- [ ] **Step 2.4.2: Run test to verify it fails**

Run: `go test ./lib/sensor/ -run TestCheckEnv -v`
Expected: FAIL.

- [ ] **Step 2.4.3: Implement `checkEnv`**

Edit `lib/sensor/requires.go`:

```go
func CheckRequiresGate(sensorJSON map[string]interface{}, opts GateOpts) Gate {
    opts = resolveOpts(opts)
    var g Gate
    g.Failures = append(g.Failures, checkTool(sensorJSON, opts)...)
    g.Failures = append(g.Failures, checkContext(sensorJSON, opts)...)
    g.Failures = append(g.Failures, checkEnv(sensorJSON, opts)...)
    return g
}

func checkEnv(sensorJSON map[string]interface{}, opts GateOpts) []Failure {
    entries := Project(sensorJSON, "env")
    var out []Failure
    for _, entry := range entries {
        name, _ := entry["name"].(string)
        if name == "" {
            continue
        }
        optional, _ := entry["optional"].(bool)
        if optional {
            continue
        }
        if _, set := opts.LookupEnv(name); set {
            continue
        }
        description, _ := entry["description"].(string)
        rationale := fmt.Sprintf("Required environment variable %s is not set", name)
        if description != "" {
            rationale = rationale + ": " + description
        }
        out = append(out, Failure{
            Kind:       "env",
            Identifier: name,
            Rationale:  rationale,
            HealShape:  "missing-env",
        })
    }
    return out
}
```

- [ ] **Step 2.4.4: Run test to verify it passes**

Run: `go test ./lib/sensor/ -run TestCheckEnv -v`
Expected: PASS

### Task 2.5 — Cross-kind ordering, ignored kinds, edge cases

- [ ] **Step 2.5.1: Write the failing test**

Append to `lib/sensor/requires_test.go`:

```go
func TestCheckRequiresGate_OrderingToolContextEnv(t *testing.T) {
    opts := GateOpts{
        LookPath:  func(string) (string, error) { return "", exec.ErrNotFound },
        Stat:      func(string) error { return os.ErrNotExist },
        LookupEnv: func(string) (string, bool) { return "", false },
    }
    s := map[string]interface{}{
        "requires": []interface{}{
            // Note: declared out of group order on purpose.
            map[string]interface{}{"kind": "env", "name": "VAR_A"},
            map[string]interface{}{"kind": "context", "path": "/a"},
            map[string]interface{}{"kind": "tool", "name": "tool_a"},
            map[string]interface{}{"kind": "env", "name": "VAR_B"},
            map[string]interface{}{"kind": "tool", "name": "tool_b"},
            map[string]interface{}{"kind": "context", "path": "/b"},
        },
    }
    g := CheckRequiresGate(s, opts)
    if len(g.Failures) != 6 {
        t.Fatalf("expected 6 failures, got %d (%+v)", len(g.Failures), g.Failures)
    }
    wantOrder := []string{"tool_a", "tool_b", "/a", "/b", "VAR_A", "VAR_B"}
    for i, w := range wantOrder {
        if g.Failures[i].Identifier != w {
            t.Errorf("Failures[%d].Identifier = %q, want %q", i, g.Failures[i].Identifier, w)
        }
    }
}

func TestCheckRequiresGate_PermissionIgnored(t *testing.T) {
    opts := GateOpts{
        LookPath:  func(string) (string, error) { return "ok", nil },
        Stat:      func(string) error { return nil },
        LookupEnv: func(string) (string, bool) { return "ok", true },
    }
    s := map[string]interface{}{
        "requires": []interface{}{
            map[string]interface{}{"kind": "permission", "scope": "Bash(go test:*)"},
            map[string]interface{}{"kind": "sensor", "id": "some-dep"},
            map[string]interface{}{"kind": "step", "command": "echo prep"},
        },
    }
    g := CheckRequiresGate(s, opts)
    if g.Failed() {
        t.Fatalf("permission/sensor/step entries should be ignored, got %+v", g.Failures)
    }
}

func TestCheckRequiresGate_NoRequiresField(t *testing.T) {
    opts := GateOpts{
        LookPath:  func(string) (string, error) { return "ok", nil },
        Stat:      func(string) error { return nil },
        LookupEnv: func(string) (string, bool) { return "", true },
    }
    g := CheckRequiresGate(map[string]interface{}{}, opts)
    if g.Failed() {
        t.Fatalf("missing requires[] should produce no failures, got %+v", g.Failures)
    }
}

func TestCheckRequiresGate_EmptyRequiresArray(t *testing.T) {
    g := CheckRequiresGate(map[string]interface{}{"requires": []interface{}{}}, GateOpts{})
    if g.Failed() {
        t.Fatalf("empty requires[] should produce no failures, got %+v", g.Failures)
    }
}
```

- [ ] **Step 2.5.2: Run test to verify it passes**

Run: `go test ./lib/sensor/ -run TestCheckRequiresGate -v`
Expected: PASS (the kind-by-kind iteration already enforces tool → context → env across kinds, and `sensor.Project` already filters by kind).

### Task 2.6 — `BuildRequiresGateSignal`

- [ ] **Step 2.6.1: Write the failing test**

Append to `lib/sensor/requires_test.go`:

```go
func TestBuildRequiresGateSignal_Shape(t *testing.T) {
    prev := NowFn
    defer func() { NowFn = prev }()
    NowFn = stableNow

    env := Envelope{
        SensorID: "e2e-tests", Version: "0.1.0", RunID: "r1",
        StartedAt: "2026-05-08T00:00:00Z", SensorType: "computational",
    }
    gate := Gate{Failures: []Failure{
        {Kind: "tool", Identifier: "docker", Rationale: `Required tool "docker" is not on PATH`, HealShape: "binary-not-found"},
        {Kind: "context", Identifier: "./.env", Rationale: `Required context path "./.env" does not exist`, HealShape: "missing-context"},
        {Kind: "env", Identifier: "GH_TOKEN", Rationale: "Required environment variable GH_TOKEN is not set", HealShape: "missing-env"},
    }}
    sig := BuildRequiresGateSignal(env, "stream", gate)

    if sig["verdict"] != "error" || sig["severity"] != "high" {
        t.Fatalf("verdict/severity = %v/%v", sig["verdict"], sig["severity"])
    }
    ev := sig["evidence"].([]interface{})
    if len(ev) != 3 {
        t.Fatalf("evidence length = %d, want 3", len(ev))
    }
    if ev[0].(map[string]interface{})["rationale"] != `Required tool "docker" is not on PATH` {
        t.Errorf("evidence[0].rationale = %v", ev[0])
    }
    md := sig["metadata"].(map[string]interface{})
    if md["kind"] != "aggregate" || md["output_mode"] != "stream" {
        t.Errorf("metadata = %+v", md)
    }
    if md["heal_hint"] != "binary-not-found:docker" {
        t.Errorf("heal_hint = %v, want %q", md["heal_hint"], "binary-not-found:docker")
    }
    rem := sig["remediation"].(map[string]interface{})
    if !strings.Contains(rem["instructions"].(string), "docker") {
        t.Errorf("remediation should mention docker; got %v", rem["instructions"])
    }
    if !strings.Contains(rem["instructions"].(string), "GH_TOKEN") {
        t.Errorf("remediation should mention GH_TOKEN; got %v", rem["instructions"])
    }
}

func TestBuildRequiresGateSignal_EmptyGate(t *testing.T) {
    env := Envelope{SensorID: "x", Version: "0.1.0", RunID: "r", StartedAt: "2026-05-08T00:00:00Z"}
    sig := BuildRequiresGateSignal(env, "single", Gate{})
    // Empty gate should still produce a well-formed signal with no evidence
    // and no heal_hint. Callers shouldn't reach this — but it shouldn't panic.
    if sig["verdict"] != "error" {
        t.Errorf("verdict = %v", sig["verdict"])
    }
    md := sig["metadata"].(map[string]interface{})
    if _, hasHint := md["heal_hint"]; hasHint {
        t.Errorf("empty gate should produce no heal_hint")
    }
}
```

- [ ] **Step 2.6.2: Run test to verify it fails**

Run: `go test ./lib/sensor/ -run TestBuildRequiresGateSignal -v`
Expected: FAIL — `BuildRequiresGateSignal` still returns nil.

- [ ] **Step 2.6.3: Implement `BuildRequiresGateSignal`**

Replace the stub in `lib/sensor/requires.go`:

```go
func BuildRequiresGateSignal(env Envelope, outputMode string, gate Gate) map[string]interface{} {
    finished := NowFn().Format("2006-01-02T15:04:05Z")
    evidence := make([]interface{}, 0, len(gate.Failures))
    for _, f := range gate.Failures {
        evidence = append(evidence, map[string]interface{}{
            "rationale": f.Rationale,
        })
    }
    md := map[string]interface{}{
        "kind":        "aggregate",
        "output_mode": outputMode,
    }
    if len(gate.Failures) > 0 {
        first := gate.Failures[0]
        md["heal_hint"] = first.HealShape + ":" + first.Identifier
    }
    sig := map[string]interface{}{
        "sensor_id":   env.SensorID,
        "version":     env.Version,
        "run_id":      env.RunID,
        "started_at":  env.StartedAt,
        "finished_at": finished,
        "verdict":     "error",
        "severity":    "high",
        "confidence":  1.0,
        "evidence":    evidence,
        "cost_actual": map[string]interface{}{"latency_ms": 0},
        "metadata":    md,
    }
    if rem := buildRequiresGateRemediation(gate); rem != "" {
        sig["remediation"] = map[string]interface{}{"instructions": rem}
    }
    return sig
}

func buildRequiresGateRemediation(gate Gate) string {
    if len(gate.Failures) == 0 {
        return ""
    }
    parts := make([]string, 0, len(gate.Failures))
    for _, f := range gate.Failures {
        switch f.Kind {
        case "tool":
            parts = append(parts, fmt.Sprintf(`install or expose %q on PATH`, f.Identifier))
        case "context":
            parts = append(parts, fmt.Sprintf(`create the required path %q`, f.Identifier))
        case "env":
            parts = append(parts, fmt.Sprintf(`set env %s`, f.Identifier))
        }
    }
    return "Resolve the following preconditions before re-running: " + strings.Join(parts, "; ") + "."
}
```

- [ ] **Step 2.6.4: Run test to verify it passes**

Run: `go test ./lib/sensor/ -run TestBuildRequiresGateSignal -v`
Expected: PASS

### Task 2.7 — `BuildMissingEnvSignal` becomes wrapper

- [ ] **Step 2.7.1: Verify existing tests still pass with current implementation**

Run: `go test ./lib/sensor/ -run TestCheckRequiredEnv -v && go test ./lib/sensor/ -run TestBuildErrorSignal -v`
Expected: PASS (we haven't touched `env.go` yet).

There is currently no direct test for `BuildMissingEnvSignal` in `env_test.go` — `TestBuildErrorSignal` is on a different function. We add one before refactoring so we have a regression net.

- [ ] **Step 2.7.2: Write a regression test for `BuildMissingEnvSignal`**

Append to `lib/sensor/env_test.go`:

```go
func TestBuildMissingEnvSignal_ShapeWrappedToGate(t *testing.T) {
    prev := NowFn
    defer func() { NowFn = prev }()
    NowFn = stableNow

    env := Envelope{SensorID: "x", Version: "0.1.0", RunID: "r1", StartedAt: "2026-05-08T00:00:00Z"}
    missing := []MissingEnv{
        {Name: "GH_TOKEN", Description: "PAT"},
        {Name: "REGION"},
    }
    sig := BuildMissingEnvSignal(env, "stream", missing)

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

Note: this test asserts the **new** shape (with `heal_hint`) — it will FAIL against the current `BuildMissingEnvSignal` (which doesn't emit `heal_hint`).

- [ ] **Step 2.7.3: Run test to verify it fails**

Run: `go test ./lib/sensor/ -run TestBuildMissingEnvSignal_ShapeWrappedToGate -v`
Expected: FAIL (`heal_hint` missing).

- [ ] **Step 2.7.4: Refactor `BuildMissingEnvSignal` to wrap `BuildRequiresGateSignal`**

Edit `lib/sensor/env.go` — replace the body of `BuildMissingEnvSignal`:

```go
// BuildMissingEnvSignal is a thin wrapper around BuildRequiresGateSignal
// kept for backwards compatibility with call sites that still produce
// []MissingEnv. New code should call CheckRequiresGate + BuildRequiresGateSignal
// directly.
func BuildMissingEnvSignal(env Envelope, outputMode string, missing []MissingEnv) map[string]interface{} {
    gate := Gate{Failures: make([]Failure, 0, len(missing))}
    for _, m := range missing {
        gate.Failures = append(gate.Failures, Failure{
            Kind:       "env",
            Identifier: m.Name,
            Rationale:  missingEnvRationale(m),
            HealShape:  "missing-env",
        })
    }
    return BuildRequiresGateSignal(env, outputMode, gate)
}
```

Remove `missingEnvRemediation` if no longer referenced. Keep `missingEnvRationale` (used by the wrapper). `CheckRequiredEnv` remains unchanged.

- [ ] **Step 2.7.5: Run all sensor tests**

Run: `go test ./lib/sensor/...`
Expected: PASS (all existing env tests + the new wrapper test + all requires tests).

### Task 2.8 — Vet and commit

- [ ] **Step 2.8.1: Run vet**

Run: `go vet ./lib/sensor/...`
Expected: clean.

- [ ] **Step 2.8.2: Commit**

```bash
git add lib/sensor/requires.go lib/sensor/requires_test.go \
        lib/sensor/env.go lib/sensor/env_test.go
git commit -m "$(cat <<'EOF'
feat(sensor): unified requires[] gate with tool/context/env checks

CheckRequiresGate replaces CheckRequiredEnv as the canonical pre-execution
precondition check. It collects failures across tool/context/env in fixed
order (tool → context → env, with within-kind order matching requires[]
position) and emits a single verdict=error Signal via BuildRequiresGateSignal
with one evidence[] entry per failure and metadata.heal_hint shaped to drive
/heal-sensor.

kind=sensor, kind=step, kind=permission entries are ignored: sensor is
handled by the orchestrator DAG, step by the prepare phase, permission by
Claude Code's permission engine.

BuildMissingEnvSignal becomes a thin wrapper over BuildRequiresGateSignal
to preserve existing call sites that still produce []MissingEnv.

No call-site changes yet — Task 3 wires it into RunOne.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Phase 0 swap in `RunOne`

**Files:**
- Modify: `lib/orchestrator/lifecycle.go`
- Modify: `lib/orchestrator/lifecycle_test.go`

### Task 3.1 — Identify the current Phase 0 block

The current Phase 0 in `lib/orchestrator/lifecycle.go::RunOne` is the block starting at the comment `// Phase 0: enforce requires[kind=env] BEFORE prepare runs.` and ending after the `BuildMissingEnvSignal` emission (lines 49–66 in the file you previously read).

- [ ] **Step 3.1.1: Read the current Phase 0**

Run: `sed -n '45,70p' lib/orchestrator/lifecycle.go`
(Use Bash, then visually confirm the block matches the description above. If line numbers drifted, locate by content.)

### Task 3.2 — Test: tool gate failure routes through RunOne

- [ ] **Step 3.2.1: Write the failing test**

Append to `lib/orchestrator/lifecycle_test.go` (adapt imports and existing test helpers):

```go
func TestRunOne_GateFailure_Tool(t *testing.T) {
    // Stub LookPath to force "docker" missing.
    prevLookup := sensor.LookupEnvFn
    sensor.LookupEnvFn = func(string) (string, bool) { return "", true }
    t.Cleanup(func() { sensor.LookupEnvFn = prevLookup })

    // Build a minimal sensor that declares requires[kind=tool].name=docker.
    s := Sensor{
        ID:   "needs-docker",
        Path: "/tmp/needs-docker.json",
        JSON: map[string]interface{}{
            "id":      "needs-docker",
            "version": "0.1.0",
            "type":    "computational",
            "output":  "stream",
            "requires": []interface{}{
                map[string]interface{}{"kind": "tool", "name": "definitely-not-on-PATH-xyz-1234"},
            },
            "execution": map[string]interface{}{
                "command":       "echo should-not-run",
                "exit_code_map": []interface{}{},
                "output_parsing": map[string]interface{}{
                    "patterns": []interface{}{
                        map[string]interface{}{"regex": "x", "verdict": "pass", "severity": "info"},
                    },
                },
            },
        },
    }

    var stdout, stderr bytes.Buffer
    sig, code := RunOne(context.Background(), s, "", nil, &stdout, &stderr)
    if code != 0 {
        t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
    }
    if sig["verdict"] != "error" {
        t.Fatalf("verdict = %v", sig["verdict"])
    }
    md := sig["metadata"].(map[string]interface{})
    if hh, _ := md["heal_hint"].(string); !strings.HasPrefix(hh, "binary-not-found:") {
        t.Errorf("heal_hint = %v, want binary-not-found:* prefix", md["heal_hint"])
    }
    // Stdout should contain exactly one JSONL signal.
    if strings.Count(stdout.String(), "\n") != 1 {
        t.Errorf("expected 1 stdout line, got %d (%s)", strings.Count(stdout.String(), "\n"), stdout.String())
    }
}
```

(Reuse existing helpers — `Sensor` is already exported from `lib/orchestrator`; `bytes`, `context`, `strings`, `sensor` imports may need to be added.)

- [ ] **Step 3.2.2: Run test to verify it fails**

Run: `go test ./lib/orchestrator/ -run TestRunOne_GateFailure_Tool -v`
Expected: FAIL — current Phase 0 only checks env, so the tool requirement is ignored and the command runs.

### Task 3.3 — Replace Phase 0 with `CheckRequiresGate`

- [ ] **Step 3.3.1: Edit `lifecycle.go`**

Replace the Phase 0 block in `RunOne`:

```go
// ANTES (current):
if missing := sensor.CheckRequiredEnv(s.JSON); len(missing) > 0 {
    sig := sensor.BuildMissingEnvSignal(envelope, output, missing)
    if v != nil {
        if err := v.Validate(schema.TargetSignal, sig); err != nil {
            schema.PrintValidationOrPlain(err, stderr)
            return nil, 1
        }
    }
    _ = json.NewEncoder(stdout).Encode(sig)
    return sig, 0
}

// DEPOIS:
gate := sensor.CheckRequiresGate(s.JSON, sensor.GateOpts{
    LookupEnv: sensor.LookupEnvFn,
})
if gate.Failed() {
    sig := sensor.BuildRequiresGateSignal(envelope, output, gate)
    if v != nil {
        if err := v.Validate(schema.TargetSignal, sig); err != nil {
            schema.PrintValidationOrPlain(err, stderr)
            return nil, 1
        }
    }
    _ = json.NewEncoder(stdout).Encode(sig)
    return sig, 0
}
```

Note: persistence wiring (`emitSignalWithPersistence`) is added in Task 8, not here. Stdout is the only sink for now.

- [ ] **Step 3.3.2: Run the new test**

Run: `go test ./lib/orchestrator/ -run TestRunOne_GateFailure_Tool -v`
Expected: PASS

### Task 3.4 — Add coverage for context and env via RunOne

- [ ] **Step 3.4.1: Append two more tests to `lifecycle_test.go`**

```go
func TestRunOne_GateFailure_Context(t *testing.T) {
    s := Sensor{
        ID:   "needs-context",
        Path: "/tmp/needs-context.json",
        JSON: map[string]interface{}{
            "id":      "needs-context",
            "version": "0.1.0",
            "type":    "computational",
            "output":  "stream",
            "requires": []interface{}{
                map[string]interface{}{"kind": "context", "path": "/this/path/does/not/exist/12345"},
            },
            "execution": map[string]interface{}{
                "command":       "echo should-not-run",
                "exit_code_map": []interface{}{},
                "output_parsing": map[string]interface{}{
                    "patterns": []interface{}{
                        map[string]interface{}{"regex": "x", "verdict": "pass", "severity": "info"},
                    },
                },
            },
        },
    }
    var stdout, stderr bytes.Buffer
    sig, _ := RunOne(context.Background(), s, "", nil, &stdout, &stderr)
    if sig["verdict"] != "error" {
        t.Fatalf("verdict = %v", sig["verdict"])
    }
    md := sig["metadata"].(map[string]interface{})
    if hh, _ := md["heal_hint"].(string); !strings.HasPrefix(hh, "missing-context:") {
        t.Errorf("heal_hint = %v", md["heal_hint"])
    }
}

func TestRunOne_GateFailure_Env(t *testing.T) {
    prev := sensor.LookupEnvFn
    sensor.LookupEnvFn = func(string) (string, bool) { return "", false }
    t.Cleanup(func() { sensor.LookupEnvFn = prev })

    s := Sensor{
        ID:   "needs-env",
        Path: "/tmp/needs-env.json",
        JSON: map[string]interface{}{
            "id":      "needs-env",
            "version": "0.1.0",
            "type":    "computational",
            "output":  "stream",
            "requires": []interface{}{
                map[string]interface{}{"kind": "env", "name": "DEFINITELY_UNSET_VAR_XYZ"},
            },
            "execution": map[string]interface{}{
                "command":       "echo should-not-run",
                "exit_code_map": []interface{}{},
                "output_parsing": map[string]interface{}{
                    "patterns": []interface{}{
                        map[string]interface{}{"regex": "x", "verdict": "pass", "severity": "info"},
                    },
                },
            },
        },
    }
    var stdout, stderr bytes.Buffer
    sig, _ := RunOne(context.Background(), s, "", nil, &stdout, &stderr)
    if sig["verdict"] != "error" {
        t.Fatalf("verdict = %v", sig["verdict"])
    }
    md := sig["metadata"].(map[string]interface{})
    if hh, _ := md["heal_hint"].(string); !strings.HasPrefix(hh, "missing-env:") {
        t.Errorf("heal_hint = %v", md["heal_hint"])
    }
}
```

- [ ] **Step 3.4.2: Run tests**

Run: `go test ./lib/orchestrator/ -run "TestRunOne_GateFailure" -v`
Expected: PASS for all three.

- [ ] **Step 3.4.3: Run full lifecycle tests**

Run: `go test ./lib/orchestrator/...`
Expected: PASS (existing tests must not regress).

### Task 3.5 — Vet and commit

- [ ] **Step 3.5.1: Vet**

Run: `go vet ./lib/orchestrator/...`
Expected: clean.

- [ ] **Step 3.5.2: Commit**

```bash
git add lib/orchestrator/lifecycle.go lib/orchestrator/lifecycle_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): RunOne Phase 0 swaps env-only check for requires gate

CheckRequiresGate replaces CheckRequiredEnv. RunOne now fails fast on
missing tool / missing context / missing env in a single Signal, emitting
metadata.heal_hint shaped for /heal-sensor regardless of which kind
triggered the failure. Stdout protocol is unchanged.

The Phase 0 block remains the only point that emits and returns early;
prepare/command/teardown are skipped on gate failure as before.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Extract watcher launcher into `lib/watcher`

**Files:**
- Create: `lib/watcher/spawn.go`
- Create: `lib/watcher/spawn_unix.go`
- Create: `lib/watcher/spawn_test.go`
- Modify: `skills/start-sensor/scripts/start.go`
- Modify: `skills/start-sensor/scripts/start_unix.go`

### Task 4.1 — Write the failing test for `BinaryPath`

- [ ] **Step 4.1.1: Create the test file**

Create `lib/watcher/spawn_test.go`:

```go
package watcher

import (
    "os"
    "path/filepath"
    "testing"
)

func TestBinaryPath_NeighbourOfExecutable(t *testing.T) {
    got, err := BinaryPath()
    if err != nil {
        t.Fatalf("BinaryPath: %v", err)
    }
    exe, _ := os.Executable()
    want := filepath.Join(filepath.Dir(exe), "watcher")
    if got != want {
        t.Errorf("got %q, want %q", got, want)
    }
}
```

- [ ] **Step 4.1.2: Run test to verify it fails**

Run: `go test ./lib/watcher/ -v`
Expected: FAIL — package does not exist.

### Task 4.2 — Create `lib/watcher/spawn.go` skeleton

- [ ] **Step 4.2.1: Create the file**

Create `lib/watcher/spawn.go`:

```go
// Package watcher launches a watcher subprocess that tails a sensor's
// raw stdout log file, applies the sensor's output_parsing patterns, and
// writes parsed Signals to signals.log. Extracted from
// skills/start-sensor/scripts/start.go so both /start-sensor and the
// orchestrator's startBlockingDep can spawn watchers via the same code path.
package watcher

import (
    "fmt"
    "os"
    "path/filepath"
)

// SpawnOpts captures everything needed to launch a watcher subprocess.
type SpawnOpts struct {
    ProjectRoot    string
    SensorID       string
    RunID          string
    RawLogPath     string
    SignalsLogPath string
    EnvelopeJSON   []byte
    PatternsJSON   []byte
    SubprocessPID  int
    // WatcherLogPath is where the watcher's own stderr is appended.
    // Defaults to <dir of SignalsLogPath>/watcher.log when empty.
    WatcherLogPath string
}

// Spawn launches the watcher binary detached. Returns the watcher's PID
// (captured before Release, so the registry's non-negativity invariant
// is preserved on Unix). On error the returned PID is 0.
func Spawn(opts SpawnOpts) (int, error) {
    bin, err := BinaryPath()
    if err != nil {
        return 0, fmt.Errorf("watcher binary path: %w", err)
    }
    logPath := opts.WatcherLogPath
    if logPath == "" {
        logPath = filepath.Join(filepath.Dir(opts.SignalsLogPath), "watcher.log")
    }
    logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
    if err != nil {
        return 0, fmt.Errorf("open watcher.log: %w", err)
    }
    proc, err := os.StartProcess(bin, []string{bin}, &os.ProcAttr{
        Env: []string{
            fmt.Sprintf("HARNESS_WATCHER_RAW=%s", opts.RawLogPath),
            fmt.Sprintf("HARNESS_WATCHER_SIGNALS=%s", opts.SignalsLogPath),
            fmt.Sprintf("HARNESS_WATCHER_PATTERNS=%s", string(opts.PatternsJSON)),
            fmt.Sprintf("HARNESS_WATCHER_ENVELOPE=%s", string(opts.EnvelopeJSON)),
            fmt.Sprintf("HARNESS_WATCHER_SUBPROCESS_PID=%d", opts.SubprocessPID),
            fmt.Sprintf("HARNESS_WATCHER_REGISTRY_ROOT=%s", opts.ProjectRoot),
            fmt.Sprintf("HARNESS_WATCHER_SENSOR_ID=%s", opts.SensorID),
        },
        Files: []*os.File{nil, nil, logFile},
        Sys:   &sysProcAttr,
    })
    if err != nil {
        _ = logFile.Close()
        return 0, fmt.Errorf("start watcher: %w", err)
    }
    pid := proc.Pid
    _ = proc.Release()
    _ = logFile.Close() // parent's handle; child keeps its own fd open
    return pid, nil
}

// BinaryPath returns the absolute path of the watcher binary, which is
// expected to live alongside the caller's executable in production.
func BinaryPath() (string, error) {
    exe, err := os.Executable()
    if err != nil {
        return "", err
    }
    return filepath.Join(filepath.Dir(exe), "watcher"), nil
}
```

Create `lib/watcher/spawn_unix.go`:

```go
//go:build darwin || linux

package watcher

import "syscall"

var sysProcAttr = syscall.SysProcAttr{Setsid: true}
```

- [ ] **Step 4.2.2: Run `BinaryPath` test**

Run: `go test ./lib/watcher/ -v`
Expected: PASS for `TestBinaryPath_NeighbourOfExecutable`.

### Task 4.3 — Test: Spawn returns error when binary missing

- [ ] **Step 4.3.1: Add the test**

Append to `lib/watcher/spawn_test.go`:

```go
func TestSpawn_ErrorWhenBinaryAbsent(t *testing.T) {
    // Point WatcherLogPath into a real temp dir but the binary path will
    // be derived from os.Executable() which is the test binary itself —
    // there is no neighbouring "watcher" file, so os.StartProcess fails.
    tmp := t.TempDir()
    rawLog := filepath.Join(tmp, "raw.log")
    sigLog := filepath.Join(tmp, "signals.log")
    if err := os.WriteFile(rawLog, nil, 0o644); err != nil {
        t.Fatal(err)
    }

    pid, err := Spawn(SpawnOpts{
        ProjectRoot:    tmp,
        SensorID:       "x",
        RunID:          "r1",
        RawLogPath:     rawLog,
        SignalsLogPath: sigLog,
        EnvelopeJSON:   []byte(`{}`),
        PatternsJSON:   []byte(`[]`),
        SubprocessPID:  os.Getpid(),
    })
    if err == nil {
        t.Fatalf("expected error when watcher binary missing, got pid=%d", pid)
    }
    if pid != 0 {
        t.Errorf("pid = %d, want 0", pid)
    }
}
```

- [ ] **Step 4.3.2: Run test**

Run: `go test ./lib/watcher/ -v`
Expected: PASS for both tests (the test asserts both the happy path of `BinaryPath` derivation AND the failure mode of `Spawn` when no binary exists at that path).

### Task 4.4 — Refactor `start.go` to call `watcher.Spawn`

- [ ] **Step 4.4.1: Locate the inline watcher block in `start.go`**

Read `skills/start-sensor/scripts/start.go` lines 165–248 (the block that calls `watcherBinaryPath`, opens `watcher.log`, builds env, calls `os.StartProcess`, captures pid before Release).

- [ ] **Step 4.4.2: Replace it with a call to `watcher.Spawn`**

In `start.go::runStart`, inside the `registry.WithFileLock` callback, replace the inline watcher block with:

```go
// Build watcher inputs.
envelope := libsensor.Envelope{
    SensorID:   id,
    Version:    stringField(sensorJSON, "version"),
    RunID:      uuid.NewString(),
    StartedAt:  time.Now().UTC().Format("2006-01-02T15:04:05Z"),
    SensorType: stringField(sensorJSON, "type"),
}
patterns := []interface{}{}
if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
    if raw, ok := op["patterns"].([]interface{}); ok {
        patterns = raw
    }
}
patternsJSON, _ := json.Marshal(patterns)
envelopeJSON, _ := json.Marshal(envelope)

watcherPID, err := watcher.Spawn(watcher.SpawnOpts{
    ProjectRoot:    projectRoot,
    SensorID:       id,
    RunID:          envelope.RunID,
    RawLogPath:     r.RawLog(id),
    SignalsLogPath: r.SignalsLog(id),
    EnvelopeJSON:   envelopeJSON,
    PatternsJSON:   patternsJSON,
    SubprocessPID:  det.PID,
})
if err != nil {
    if det.PGID > 0 {
        _ = killGroup(det.PGID)
    }
    return fmt.Errorf("start watcher: %w", err)
}
```

Add the import: `"github.com/iurykrieger/harness-framework/lib/watcher"`.

- [ ] **Step 4.4.3: Drop `watcherBinaryPath` and `watcherSysProcAttr` from `start_unix.go`**

These two symbols are now in `lib/watcher/`. Keep `killGroup` and `killPID` — they are still used locally.

Final `start_unix.go`:

```go
//go:build start_sensor && (darwin || linux)

package main

import "syscall"

// killGroup sends SIGKILL to the entire process group identified by pgid.
// Used to undo a just-spawned root subprocess when the watcher spawn
// fails inside the flock callback.
func killGroup(pgid int) error {
    return syscall.Kill(-pgid, syscall.SIGKILL)
}

// killPID sends SIGKILL to a single process.
func killPID(pid int) error {
    return syscall.Kill(pid, syscall.SIGKILL)
}
```

Note: drop the `os` and `path/filepath` imports — they're no longer used.

- [ ] **Step 4.4.4: Run start-sensor tests**

Run: `go test -tags=start_sensor ./skills/start-sensor/...`
Expected: PASS (the refactor is behaviour-preserving).

- [ ] **Step 4.4.5: Vet**

Run: `go vet -tags=start_sensor ./skills/start-sensor/... && go vet ./lib/watcher/...`
Expected: clean.

### Task 4.5 — Commit Task 4

- [ ] **Step 4.5.1: Commit**

```bash
git add lib/watcher/spawn.go lib/watcher/spawn_unix.go lib/watcher/spawn_test.go \
        skills/start-sensor/scripts/start.go skills/start-sensor/scripts/start_unix.go
git commit -m "$(cat <<'EOF'
refactor(watcher): extract watcher launcher to lib/watcher

watcher.Spawn is the single producer of detached watcher subprocesses.
Behaviour is bit-identical to the inline block previously living in
skills/start-sensor/scripts/start.go: opens watcher.log in append mode,
builds the HARNESS_WATCHER_* env vars, calls os.StartProcess with
Setsid, captures pid BEFORE Release (preserving the registry's
non-negativity invariant), closes the parent log handle.

The new package will be reused by lib/orchestrator/live_deps.go in a
later task so blocking deps started by the orchestrator stop having
empty signals.log.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: `lib/subprocess/stream.go` — tee hook

**Files:**
- Modify: `lib/subprocess/stream.go`
- Modify: `lib/subprocess/stream_test.go`

### Task 5.1 — Test: empty `RawLogPath` is compat

- [ ] **Step 5.1.1: Verify existing tests still describe current behaviour**

Run: `go test ./lib/subprocess/...`
Expected: PASS (baseline).

### Task 5.2 — Test: populated `RawLogPath` writes file

- [ ] **Step 5.2.1: Write the failing test**

Append to `lib/subprocess/stream_test.go`:

```go
func TestStreamSubprocess_RawLogPathPopulated(t *testing.T) {
    tmp := t.TempDir()
    rawLogPath := filepath.Join(tmp, "raw.log")

    cfg := StreamConfig{
        Command:   `printf "line-1\nline-2\nline-3\n"`,
        TimeoutMS: 5000,
        Envelope:  sensor.Envelope{SensorID: "x", Version: "0.0.1", RunID: "r", StartedAt: "2026-05-08T00:00:00Z"},
        Stdout:    io.Discard,
        Stderr:    io.Discard,
        RawLogPath: rawLogPath,
    }
    if _, err := StreamSubprocess(context.Background(), cfg); err != nil {
        t.Fatalf("StreamSubprocess: %v", err)
    }
    got, err := os.ReadFile(rawLogPath)
    if err != nil {
        t.Fatalf("ReadFile: %v", err)
    }
    if !strings.Contains(string(got), "line-1") || !strings.Contains(string(got), "line-3") {
        t.Errorf("raw.log content unexpected: %q", string(got))
    }
}

func TestStreamSubprocess_RawLogPathEmpty_NoFileCreated(t *testing.T) {
    tmp := t.TempDir()
    nonexistent := filepath.Join(tmp, "nope.log")

    cfg := StreamConfig{
        Command:   `echo hello`,
        TimeoutMS: 5000,
        Envelope:  sensor.Envelope{SensorID: "x", Version: "0.0.1", RunID: "r", StartedAt: "2026-05-08T00:00:00Z"},
        Stdout:    io.Discard,
        Stderr:    io.Discard,
        // RawLogPath intentionally empty
    }
    if _, err := StreamSubprocess(context.Background(), cfg); err != nil {
        t.Fatalf("StreamSubprocess: %v", err)
    }
    if _, err := os.Stat(nonexistent); !os.IsNotExist(err) {
        t.Errorf("expected nope.log to NOT exist, stat err = %v", err)
    }
}
```

Add imports if not present: `"io"`, `"os"`, `"path/filepath"`, `"strings"`.

- [ ] **Step 5.2.2: Run test to verify it fails**

Run: `go test ./lib/subprocess/ -run TestStreamSubprocess_RawLogPath -v`
Expected: FAIL — `RawLogPath` field does not exist.

### Task 5.3 — Implement the tee

- [ ] **Step 5.3.1: Add the field**

Edit `lib/subprocess/stream.go`:

```go
type StreamConfig struct {
    Command   string
    Env       map[string]string
    TimeoutMS int
    Patterns  []signal.Pattern
    Envelope  sensor.Envelope
    Validator *schema.Validator
    Stdout    io.Writer
    Stderr    io.Writer

    // RawLogPath is optional. When non-empty, stdout+stderr of the
    // subprocess are tee-written to this file in O_TRUNC mode (a fresh
    // run overwrites the previous content). On write errors the streamer
    // logs to cfg.Stderr and keeps streaming — never aborts.
    RawLogPath string
}
```

- [ ] **Step 5.3.2: Wire the tee in `StreamSubprocess`**

In `StreamSubprocess`, after `cmd.Start()`, add:

```go
var rawLogFile *os.File
if cfg.RawLogPath != "" {
    var openErr error
    rawLogFile, openErr = os.OpenFile(cfg.RawLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
    if openErr != nil {
        fmt.Fprintf(cfg.Stderr, "stream: cannot open RawLogPath %q: %v\n", cfg.RawLogPath, openErr)
        rawLogFile = nil
    } else {
        defer rawLogFile.Close()
    }
}
```

Then modify the `scan` function to tee. Replace its definition:

```go
scan := func(r io.Reader, captureStderr bool) {
    defer wg.Done()
    sc := bufio.NewScanner(r)
    sc.Buffer(make([]byte, 64*1024), 1024*1024)
    for sc.Scan() {
        line := sc.Text()
        if rawLogFile != nil {
            // Best-effort tee. On write error: log and keep going.
            if _, werr := rawLogFile.WriteString(line + "\n"); werr != nil {
                fmt.Fprintf(cfg.Stderr, "stream: raw.log write failed: %v\n", werr)
                _ = rawLogFile.Close()
                rawLogFile = nil
            }
        }
        if captureStderr {
            stderrMu.Lock()
            if remaining := streamStderrExcerptCap - stderrBuf.Len(); remaining > 0 {
                if len(line)+1 <= remaining {
                    stderrBuf.WriteString(line)
                    stderrBuf.WriteByte('\n')
                } else {
                    stderrBuf.WriteString(line[:remaining])
                }
            }
            stderrMu.Unlock()
        }
        m, ok := signal.MatchLine(line, cfg.Patterns)
        if !ok {
            continue
        }
        emits <- emit{sig: buildIndividualSignal(cfg.Envelope, m)}
    }
}
```

Note: there's a race condition if both goroutines write to the same file. Wrap the tee in a small `sync.Mutex` or use `rawLogFile.WriteString` which is safe via the OS only on small writes — for safety, add a `sync.Mutex rawLogMu`:

```go
var rawLogMu sync.Mutex
// inside scan:
if rawLogFile != nil {
    rawLogMu.Lock()
    if _, werr := rawLogFile.WriteString(line + "\n"); werr != nil {
        fmt.Fprintf(cfg.Stderr, "stream: raw.log write failed: %v\n", werr)
        _ = rawLogFile.Close()
        rawLogFile = nil
    }
    rawLogMu.Unlock()
}
```

- [ ] **Step 5.3.3: Run tests**

Run: `go test ./lib/subprocess/ -v`
Expected: PASS (both new tests + existing).

### Task 5.4 — Vet and commit

- [ ] **Step 5.4.1: Vet**

Run: `go vet ./lib/subprocess/...`

- [ ] **Step 5.4.2: Commit**

```bash
git add lib/subprocess/stream.go lib/subprocess/stream_test.go
git commit -m "$(cat <<'EOF'
feat(subprocess): StreamConfig.RawLogPath tees stdout+stderr to a file

When RawLogPath is set, StreamSubprocess opens the path with O_TRUNC and
writes each scanned line (from both stdout and stderr drainers) to the
file in parallel with the existing pattern-matching pipeline. On write
errors the streamer logs to cfg.Stderr and disables the tee — never
aborts the stream.

Empty RawLogPath preserves current behaviour bit-for-bit. The orchestrator
will use this in Task 6 to persist raw.log alongside the JSONL signals.log.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Runtime dir + tee wiring in `RunOne` + `emitSignalWithPersistence`

**Files:**
- Create: `lib/orchestrator/persistence.go`
- Create: `lib/orchestrator/persistence_test.go`
- Modify: `lib/orchestrator/lifecycle.go`
- Modify: `lib/orchestrator/lifecycle_test.go`

### Task 6.1 — Test: `prepareRuntimeDir` happy path

- [ ] **Step 6.1.1: Write the failing test**

Create `lib/orchestrator/persistence_test.go`:

```go
package orchestrator

import (
    "bytes"
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
)

func TestPrepareRuntimeDir_CreatesNestedPath(t *testing.T) {
    tmp := t.TempDir()
    rawLog, sigLog, err := prepareRuntimeDir(tmp, "my-sensor", "run-abc")
    if err != nil {
        t.Fatalf("prepareRuntimeDir: %v", err)
    }
    wantDir := filepath.Join(tmp, ".runtime", "sensors", "my-sensor", "run-abc")
    if filepath.Dir(rawLog) != wantDir {
        t.Errorf("raw log dir = %q, want %q", filepath.Dir(rawLog), wantDir)
    }
    if filepath.Base(rawLog) != "raw.log" {
        t.Errorf("raw log base = %q, want raw.log", filepath.Base(rawLog))
    }
    if filepath.Base(sigLog) != "signals.log" {
        t.Errorf("signals log base = %q", filepath.Base(sigLog))
    }
    if _, err := os.Stat(wantDir); err != nil {
        t.Errorf("dir not created: %v", err)
    }
}

func TestPrepareRuntimeDir_FailsOnNonexistentParent(t *testing.T) {
    _, _, err := prepareRuntimeDir("/dev/null/cannot-mkdir-under-this", "x", "r")
    if err == nil {
        t.Fatal("expected error, got nil")
    }
}
```

- [ ] **Step 6.1.2: Run test to verify it fails**

Run: `go test ./lib/orchestrator/ -run TestPrepareRuntimeDir -v`
Expected: FAIL — function undefined.

### Task 6.2 — Implement `prepareRuntimeDir`

- [ ] **Step 6.2.1: Create `persistence.go`**

Create `lib/orchestrator/persistence.go`:

```go
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
        // Persist to stdout only; do not abort.
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
```

- [ ] **Step 6.2.2: Run tests**

Run: `go test ./lib/orchestrator/ -run TestPrepareRuntimeDir -v`
Expected: PASS

### Task 6.3 — Test: `emitSignalWithPersistence` writes both sinks

- [ ] **Step 6.3.1: Append the test**

```go
func TestEmitSignalWithPersistence_WritesBothSinks(t *testing.T) {
    tmp := t.TempDir()
    sig := map[string]interface{}{
        "sensor_id": "my-sensor",
        "run_id":    "run-xyz",
        "verdict":   "pass",
    }
    var stdout, stderr bytes.Buffer
    if err := emitSignalWithPersistence(sig, &stdout, tmp, "my-sensor", &stderr); err != nil {
        t.Fatalf("emit: %v", err)
    }
    if stdout.Len() == 0 {
        t.Fatal("stdout empty")
    }
    sigLog := filepath.Join(tmp, ".runtime", "sensors", "my-sensor", "run-xyz", "signals.log")
    fileBytes, err := os.ReadFile(sigLog)
    if err != nil {
        t.Fatalf("read signals.log: %v", err)
    }
    if !bytes.Equal(stdout.Bytes(), fileBytes) {
        t.Errorf("stdout vs signals.log differ:\nstdout=%q\nfile=%q", stdout.String(), string(fileBytes))
    }
    // Sanity: the file is valid JSONL.
    var out map[string]interface{}
    if err := json.Unmarshal(bytes.TrimRight(fileBytes, "\n"), &out); err != nil {
        t.Errorf("signals.log not valid JSON: %v", err)
    }
    if out["run_id"] != "run-xyz" {
        t.Errorf("run_id round-trip mismatch")
    }
}

func TestEmitSignalWithPersistence_MissingRunID_UsesFallback(t *testing.T) {
    tmp := t.TempDir()
    sig := map[string]interface{}{
        "sensor_id": "my-sensor",
        "verdict":   "pass",
    }
    var stdout, stderr bytes.Buffer
    if err := emitSignalWithPersistence(sig, &stdout, tmp, "my-sensor", &stderr); err != nil {
        t.Fatalf("emit: %v", err)
    }
    if !bytes.Contains(stderr.Bytes(), []byte("missing run_id")) {
        t.Errorf("expected warning about missing run_id, stderr=%q", stderr.String())
    }
    // A fallback dir should exist under .runtime/sensors/my-sensor/ts-*.
    parent := filepath.Join(tmp, ".runtime", "sensors", "my-sensor")
    entries, _ := os.ReadDir(parent)
    if len(entries) != 1 {
        t.Fatalf("expected one fallback dir, got %d entries", len(entries))
    }
    if !strings.HasPrefix(entries[0].Name(), "ts-") {
        t.Errorf("fallback dir name = %q, want ts-* prefix", entries[0].Name())
    }
}
```

Add `"strings"` to the imports if not present.

- [ ] **Step 6.3.2: Run tests**

Run: `go test ./lib/orchestrator/ -run TestEmitSignalWithPersistence -v`
Expected: PASS

### Task 6.4 — Wire `RunOne` to use `prepareRuntimeDir` and `emitSignalWithPersistence`

- [ ] **Step 6.4.1: Write the failing assertion**

Append to `lib/orchestrator/lifecycle_test.go`:

```go
func TestRunOne_PersistsAggregateAndStdoutMatchesSignalsLog(t *testing.T) {
    tmp := t.TempDir()
    // Place sensor at <tmp>/sensors/echo.json so projectRoot resolves to <tmp>.
    sensorsDir := filepath.Join(tmp, "sensors")
    if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
        t.Fatal(err)
    }
    sensorPath := filepath.Join(sensorsDir, "echo-stream.json")
    s := Sensor{
        ID:   "echo-stream",
        Path: sensorPath,
        JSON: map[string]interface{}{
            "id":      "echo-stream",
            "version": "0.0.1",
            "type":    "computational",
            "output":  "stream",
            "execution": map[string]interface{}{
                "command": `printf "PASS line\n"`,
                "exit_code_map": []interface{}{
                    map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
                },
                "output_parsing": map[string]interface{}{
                    "patterns": []interface{}{
                        map[string]interface{}{"regex": "PASS", "verdict": "pass", "severity": "info"},
                    },
                },
            },
            "cost": map[string]interface{}{
                "class": "cheap",
                "latency": map[string]interface{}{"p50_ms": 1, "p95_ms": 5, "timeout_ms": 5000},
                "compute": map[string]interface{}{"cpu": "low", "memory_mb": 1},
            },
        },
    }
    var stdout, stderr bytes.Buffer
    _, code := RunOne(context.Background(), s, "", nil, &stdout, &stderr)
    if code != 0 {
        t.Fatalf("exit=%d stderr=%s", code, stderr.String())
    }
    // Find <tmp>/.runtime/sensors/echo-stream/<run_id>/signals.log
    parent := filepath.Join(tmp, ".runtime", "sensors", "echo-stream")
    entries, err := os.ReadDir(parent)
    if err != nil || len(entries) == 0 {
        t.Fatalf("no .runtime entry: err=%v entries=%v", err, entries)
    }
    sigLog := filepath.Join(parent, entries[0].Name(), "signals.log")
    fileBytes, err := os.ReadFile(sigLog)
    if err != nil {
        t.Fatalf("read signals.log: %v", err)
    }
    if !bytes.Equal(stdout.Bytes(), fileBytes) {
        t.Errorf("stdout != signals.log\nstdout=%q\nfile=%q", stdout.String(), string(fileBytes))
    }
    // raw.log should contain the printf output.
    rawLog := filepath.Join(parent, entries[0].Name(), "raw.log")
    rawBytes, _ := os.ReadFile(rawLog)
    if !strings.Contains(string(rawBytes), "PASS line") {
        t.Errorf("raw.log missing subprocess output: %q", string(rawBytes))
    }
}
```

Note: this test assumes `RunOne` resolves the project root from `s.Path` (`filepath.Dir(filepath.Dir(absPath))`). If `RunOne`'s current signature does not give it the path, see Task 6.4.2 below.

- [ ] **Step 6.4.2: Run test to verify it fails**

Run: `go test ./lib/orchestrator/ -run TestRunOne_PersistsAggregateAndStdoutMatchesSignalsLog -v`
Expected: FAIL — `RunOne` does not currently write to `.runtime`.

- [ ] **Step 6.4.3: Wire `RunOne` to write logs**

Edit `lib/orchestrator/lifecycle.go::RunOne`. Add to the very top, right after `envelope, err := sensor.BuildEnvelope(s.JSON)`:

```go
projectRoot := filepath.Dir(filepath.Dir(s.Path))
rawLogPath, signalsLogPath, dirErr := prepareRuntimeDir(projectRoot, s.ID, envelope.RunID)
if dirErr != nil {
    // Hard fail: emit a runtime_dir_failed signal to stdout only and return.
    finished := sensor.NowFn().Format("2006-01-02T15:04:05Z")
    sig := map[string]interface{}{
        "sensor_id":   envelope.SensorID,
        "version":     envelope.Version,
        "run_id":      envelope.RunID,
        "started_at":  envelope.StartedAt,
        "finished_at": finished,
        "verdict":     "error",
        "severity":    "high",
        "confidence":  1.0,
        "evidence": []interface{}{
            map[string]interface{}{"rationale": fmt.Sprintf("cannot create runtime dir: %v", dirErr)},
        },
        "cost_actual": map[string]interface{}{"latency_ms": 0},
        "metadata": map[string]interface{}{
            "kind":         "runtime_dir_failed",
            "output_mode":  output,
            "error_excerpt": dirErr.Error(),
        },
    }
    if v != nil {
        if vErr := v.Validate(schema.TargetSignal, sig); vErr != nil {
            schema.PrintValidationOrPlain(vErr, stderr)
            return nil, 1
        }
    }
    _ = json.NewEncoder(stdout).Encode(sig)
    return sig, 0
}
signalsLogFile, openErr := os.OpenFile(signalsLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
if openErr != nil {
    fmt.Fprintf(stderr, "RunOne: cannot open signals.log %q: %v\n", signalsLogPath, openErr)
} else {
    defer signalsLogFile.Close()
    stdout = io.MultiWriter(stdout, signalsLogFile)
}
```

Then update the `subprocess.StreamSubprocess` call to pass `RawLogPath: rawLogPath`:

```go
res, _ := subprocess.StreamSubprocess(ctx, subprocess.StreamConfig{
    Command:    command,
    Env:        envExtra,
    TimeoutMS:  timeoutMS,
    Patterns:   patterns,
    Envelope:   envelope,
    Validator:  v,
    Stdout:     stdout,
    Stderr:     stderr,
    RawLogPath: rawLogPath, // NEW
})
```

Add imports to `lifecycle.go`:

```go
import (
    // ... existing ...
    "io"
    "os"
    "path/filepath"
)
```

(`fmt` is already imported.)

- [ ] **Step 6.4.4: Run all orchestrator tests**

Run: `go test ./lib/orchestrator/...`
Expected: PASS

Note: existing tests that used `s.Path = "/tmp/foo.json"` may now try to create `.runtime` under `/tmp/` and write there. This is acceptable in tests but verify no tests depend on `.runtime` being absent. If they do, set `s.Path` to `<TempDir>/sensors/<id>.json`.

If a pre-existing test fails because of this, fix it by giving the sensor a real on-disk path under `t.TempDir()`.

### Task 6.5 — Vet and commit

- [ ] **Step 6.5.1: Vet**

Run: `go vet ./lib/orchestrator/...`

- [ ] **Step 6.5.2: Commit**

```bash
git add lib/orchestrator/persistence.go lib/orchestrator/persistence_test.go \
        lib/orchestrator/lifecycle.go lib/orchestrator/lifecycle_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): RunOne persists raw.log and signals.log per run

Every RunOne invocation now creates .runtime/sensors/<id>/<run_id>/ and
tees:
  - subprocess stdout+stderr → raw.log (via subprocess.StreamConfig.RawLogPath)
  - emitted JSONL signals     → signals.log (via io.MultiWriter wrapping stdout)

The invariant "signals.log == stdout JSONL" is byte-for-byte enforced
because both sinks share the same MultiWriter.

prepareRuntimeDir failure emits a verdict=error signal with
metadata.kind = "runtime_dir_failed" to stdout only — the only path
that breaks the invariant, accepted because the alternative is silent
degradation.

emitSignalWithPersistence is now available for cascade and dep
emissions in preflight.go / live_deps.go (Task 8).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: `startBlockingDep` calls `watcher.Spawn`

**Files:**
- Modify: `lib/orchestrator/live_deps.go`
- Modify: `lib/orchestrator/live_deps_test.go`

### Task 7.1 — Read the current `startBlockingDep`

- [ ] **Step 7.1.1: Read for orientation**

Run: `sed -n '155,195p' lib/orchestrator/live_deps.go`

Identify the lines that:
1. Create `raw.log` / `signals.log` files (lines 167–171).
2. Call `subprocess.SpawnDetached`.
3. Write the registry entry with `WatcherPID: 0`.

### Task 7.2 — Test: blocking dep gets a watcher

- [ ] **Step 7.2.1: Write the failing test**

Append to `lib/orchestrator/live_deps_test.go`:

```go
func TestStartBlockingDep_SpawnsWatcherAndPopulatesPID(t *testing.T) {
    // This test cannot easily exercise the real watcher binary, so we
    // assert that startBlockingDep records a non-zero WatcherPID OR
    // returns an error mentioning "watcher" (when the binary is absent
    // in the test environment).

    tmp := t.TempDir()
    sensorsDir := filepath.Join(tmp, "sensors")
    _ = os.MkdirAll(sensorsDir, 0o755)

    r := registry.NewRoot(tmp)
    rs := registry.RunningSensors{}
    dep := Sensor{
        ID:   "fake-blocking",
        Path: filepath.Join(sensorsDir, "fake-blocking.json"),
        JSON: map[string]interface{}{
            "id":      "fake-blocking",
            "version": "0.0.1",
            "type":    "computational",
            "output":  "stream",
            "execution": map[string]interface{}{
                "command":  "sleep 30",
                "blocking": true,
                "output_parsing": map[string]interface{}{
                    "patterns": []interface{}{
                        map[string]interface{}{"regex": "x", "verdict": "pass", "severity": "info"},
                    },
                },
            },
        },
    }
    holder := registry.HeldByEntry{Kind: "sensor", ID: "root", PID: os.Getpid(), AttachedAt: "2026-05-11T00:00:00Z"}

    err := startBlockingDep(&rs, r, dep, holder)
    if err != nil {
        // Acceptable in CI when the watcher binary isn't sitting next to
        // the test binary: surface the failure but check it mentions
        // "watcher" so we know the new code path ran.
        if !strings.Contains(err.Error(), "watcher") {
            t.Fatalf("unexpected error: %v", err)
        }
        // Cleanup the dangling subprocess if it was started.
        for _, e := range rs.Entries {
            if e.SensorID == dep.ID && e.PGID > 0 {
                _ = killGroupHelper(e.PGID)
            }
        }
        return
    }
    defer func() {
        for _, e := range rs.Entries {
            if e.SensorID == dep.ID && e.PGID > 0 {
                _ = killGroupHelper(e.PGID)
            }
        }
    }()
    entry := rs.FindEntry(dep.ID)
    if entry == nil {
        t.Fatal("entry not in rs")
    }
    if entry.WatcherPID == 0 {
        t.Errorf("WatcherPID = 0, expected > 0")
    }
    // raw.log and signals.log should exist under <tmp>/.runtime/sensors/<dep>/<run_id>/
    parent := filepath.Join(tmp, ".runtime", "sensors", dep.ID)
    sub, _ := os.ReadDir(parent)
    if len(sub) == 0 {
        t.Fatalf("expected run_id subdir under %q", parent)
    }
    if _, err := os.Stat(filepath.Join(parent, sub[0].Name(), "raw.log")); err != nil {
        t.Errorf("raw.log missing: %v", err)
    }
    if _, err := os.Stat(filepath.Join(parent, sub[0].Name(), "signals.log")); err != nil {
        t.Errorf("signals.log missing: %v", err)
    }
}

// killGroupHelper is a test helper to clean up dangling subprocesses
// spawned by startBlockingDep when the test exits.
func killGroupHelper(pgid int) error {
    return syscall.Kill(-pgid, syscall.SIGKILL)
}
```

Add imports: `"syscall"`, `"github.com/iurykrieger/harness-framework/lib/registry"`.

- [ ] **Step 7.2.2: Run test to verify it fails**

Run: `go test ./lib/orchestrator/ -run TestStartBlockingDep_SpawnsWatcherAndPopulatesPID -v`
Expected: FAIL — current `startBlockingDep` sets `WatcherPID: 0` and `.runtime` path lacks `<run_id>`.

### Task 7.3 — Implement watcher.Spawn in `startBlockingDep`

- [ ] **Step 7.3.1: Edit `startBlockingDep`**

Replace the body of `startBlockingDep` in `lib/orchestrator/live_deps.go`:

```go
func startBlockingDep(rs *registry.RunningSensors, r registry.Root, dep Sensor, holder registry.HeldByEntry) error {
    execMap, _ := dep.JSON["execution"].(map[string]interface{})
    command, _ := execMap["command"].(string)

    runID := uuid.NewString()
    rawLogPath, signalsLogPath, dirErr := prepareRuntimeDir(r.ProjectRoot, dep.ID, runID)
    if dirErr != nil {
        return fmt.Errorf("runtime dir: %w", dirErr)
    }
    // Create the empty log files (watcher needs them to exist for its
    // O_APPEND opens; raw.log will be the SpawnDetached LogFile).
    if err := os.WriteFile(signalsLogPath, nil, 0o644); err != nil {
        return fmt.Errorf("create signals.log: %w", err)
    }

    det, err := subprocess.SpawnDetached(subprocess.DetachConfig{Command: command, LogFile: rawLogPath})
    if err != nil {
        return fmt.Errorf("spawn: %w", err)
    }

    envelope := libsensor.Envelope{
        SensorID:   dep.ID,
        Version:    stringField(dep.JSON, "version"),
        RunID:      runID,
        StartedAt:  time.Now().UTC().Format("2006-01-02T15:04:05Z"),
        SensorType: stringField(dep.JSON, "type"),
    }
    envelopeJSON, _ := json.Marshal(envelope)
    patterns := []interface{}{}
    if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
        if raw, ok := op["patterns"].([]interface{}); ok {
            patterns = raw
        }
    }
    patternsJSON, _ := json.Marshal(patterns)

    watcherPID, werr := watcher.Spawn(watcher.SpawnOpts{
        ProjectRoot:    r.ProjectRoot,
        SensorID:       dep.ID,
        RunID:          runID,
        RawLogPath:     rawLogPath,
        SignalsLogPath: signalsLogPath,
        EnvelopeJSON:   envelopeJSON,
        PatternsJSON:   patternsJSON,
        SubprocessPID:  det.PID,
    })
    if werr != nil {
        if det.PGID > 0 {
            _ = syscall.Kill(-det.PGID, syscall.SIGKILL)
        }
        return fmt.Errorf("watcher: %w", werr)
    }

    now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
    rs.RemoveEntry(dep.ID)
    rs.Entries = append(rs.Entries, registry.RunningSensorEntry{
        SensorID:   dep.ID,
        PID:        det.PID,
        PGID:       det.PGID,
        WatcherPID: watcherPID,
        StartedAt:  now,
        Command:    command,
        LogDir:     filepath.Join(".runtime", "sensors", dep.ID, runID),
        HeldBy:     []registry.HeldByEntry{holder},
    })
    return registry.Save(r, *rs)
}

// stringField is duplicated from start.go because the orchestrator
// can't reach package main; keep it small and local.
func stringField(m map[string]interface{}, key string) string {
    s, _ := m[key].(string)
    return s
}
```

Note: `stringField` may already exist in `live_deps.go`. If so, reuse — don't redeclare.

Add imports to `live_deps.go`:

```go
import (
    // ... existing ...
    "github.com/iurykrieger/harness-framework/lib/watcher"
    libsensor "github.com/iurykrieger/harness-framework/lib/sensor"
)
```

Remove the comment block that said "No watcher process is spawned for orchestrator-managed deps".

Also note `r.ProjectRoot` — confirm this is the field name in `registry.Root` (it should be, per the registry package). If the field is unexported, add a method or use the public path. Per `lib/registry/paths.go:11-16`, `Root` has the project root accessible as `r.ProjectRoot` (or an equivalent method) — verify with: `grep -n "ProjectRoot\|projectRoot" lib/registry/paths.go`.

If `Root.ProjectRoot` is unexported, expose via `(r Root) Project() string` and use that. Add the method in this commit.

- [ ] **Step 7.3.2: Run tests**

Run: `go test ./lib/orchestrator/ -run TestStartBlockingDep -v`
Expected: PASS (or the "watcher binary missing" branch fires — both are acceptable per the test).

- [ ] **Step 7.3.3: Run full orchestrator suite**

Run: `go test ./lib/orchestrator/...`
Expected: PASS

### Task 7.4 — Commit

- [ ] **Step 7.4.1: Vet**

Run: `go vet ./lib/orchestrator/...`

- [ ] **Step 7.4.2: Commit**

```bash
git add lib/orchestrator/live_deps.go lib/orchestrator/live_deps_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): startBlockingDep spawns a watcher per dep

Blocking deps brought up by the orchestrator no longer have empty
signals.log files. startBlockingDep now:

1. Creates .runtime/sensors/<dep>/<run_id>/ via prepareRuntimeDir.
2. Spawns the dep's command detached with raw.log as SpawnDetached.LogFile.
3. Calls watcher.Spawn so the watcher tails raw.log and writes signals.log.
4. Records WatcherPID in the registry entry (was 0).
5. Records LogDir with the run_id segment.

If watcher.Spawn fails, the subprocess group is SIGKILLed to avoid
orphans — AttachLiveDep's existing dep_start_failed cascade path then
surfaces the error to the caller.

The previous inline comment "No watcher process is spawned for
orchestrator-managed deps" is deleted along with the behaviour it
described.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Apply `emitSignalWithPersistence` to cascade and dep_attached/detached emissions

**Files:**
- Modify: `lib/orchestrator/preflight.go`
- Modify: `lib/orchestrator/live_deps.go`
- Modify: `lib/orchestrator/preflight_test.go`
- Modify: `lib/orchestrator/live_deps_test.go`

### Task 8.1 — Identify every `json.NewEncoder(stdout).Encode(...)` in the two files

- [ ] **Step 8.1.1: List them**

Run: `grep -n "json.NewEncoder(stdout).Encode" lib/orchestrator/preflight.go lib/orchestrator/live_deps.go`

You should see emissions for:
- `preflight.go`: cascade signal at AttachLiveDep failure path; cascade signal inside RunDeps loop; cascade at root cascade.
- `live_deps.go`: dep_attached / dep_started in AttachLiveDep; dep_detached in DetachLiveDep; aggregate in stopBlockingDep.

### Task 8.2 — Test: cascade signal persists to signals.log

- [ ] **Step 8.2.1: Write a focused test**

Append to `lib/orchestrator/preflight_test.go`:

```go
func TestRunDeps_CascadeSignalPersistedToSignalsLog(t *testing.T) {
    tmp := t.TempDir()
    sensorsDir := filepath.Join(tmp, "sensors")
    _ = os.MkdirAll(sensorsDir, 0o755)

    // failing-dep: prints to stderr, exits non-zero.
    failingDep := map[string]interface{}{
        "id":      "failing-dep",
        "version": "0.0.1",
        "type":    "computational",
        "output":  "stream",
        "execution": map[string]interface{}{
            "command": `false`,
            "exit_code_map": []interface{}{
                map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
                map[string]interface{}{"exit_code": "*", "verdict": "fail", "severity": "high"},
            },
            "output_parsing": map[string]interface{}{
                "patterns": []interface{}{
                    map[string]interface{}{"regex": "x", "verdict": "pass", "severity": "info"},
                },
            },
        },
        "cost": map[string]interface{}{
            "class": "cheap",
            "latency": map[string]interface{}{"p50_ms": 1, "p95_ms": 5, "timeout_ms": 5000},
            "compute": map[string]interface{}{"cpu": "low", "memory_mb": 1},
        },
    }
    failingDepJSON, _ := json.Marshal(failingDep)
    _ = os.WriteFile(filepath.Join(sensorsDir, "failing-dep.json"), failingDepJSON, 0o644)

    // root: depends on failing-dep.
    rootSensor := map[string]interface{}{
        "id":      "root",
        "version": "0.0.1",
        "type":    "computational",
        "output":  "stream",
        "requires": []interface{}{
            map[string]interface{}{"kind": "sensor", "id": "failing-dep"},
        },
        "execution": map[string]interface{}{
            "command": `echo should-not-run`,
            "exit_code_map": []interface{}{},
            "output_parsing": map[string]interface{}{
                "patterns": []interface{}{
                    map[string]interface{}{"regex": "x", "verdict": "pass", "severity": "info"},
                },
            },
        },
        "cost": map[string]interface{}{
            "class": "cheap",
            "latency": map[string]interface{}{"p50_ms": 1, "p95_ms": 5, "timeout_ms": 5000},
            "compute": map[string]interface{}{"cpu": "low", "memory_mb": 1},
        },
    }
    rootJSON, _ := json.Marshal(rootSensor)
    _ = os.WriteFile(filepath.Join(sensorsDir, "root.json"), rootJSON, 0o644)

    var stdout, stderr bytes.Buffer
    code := RunWithDepsRoot(context.Background(), "root", tmp, "", &stdout, &stderr)
    if code == 0 {
        t.Errorf("expected non-zero exit because root cascades")
    }
    // The cascade signal for root must be in signals.log under
    // .runtime/sensors/root/<run_id>/signals.log.
    parent := filepath.Join(tmp, ".runtime", "sensors", "root")
    sub, err := os.ReadDir(parent)
    if err != nil || len(sub) == 0 {
        t.Fatalf("no .runtime/sensors/root entry: err=%v", err)
    }
    sigLog := filepath.Join(parent, sub[0].Name(), "signals.log")
    fileBytes, err := os.ReadFile(sigLog)
    if err != nil {
        t.Fatalf("read signals.log: %v", err)
    }
    if len(fileBytes) == 0 {
        t.Fatal("signals.log is empty; cascade signal not persisted")
    }
    if !bytes.Contains(fileBytes, []byte("cascade")) {
        t.Errorf("signals.log missing 'cascade' marker: %q", string(fileBytes))
    }
}
```

- [ ] **Step 8.2.2: Run test to verify it fails**

Run: `go test ./lib/orchestrator/ -run TestRunDeps_CascadeSignalPersistedToSignalsLog -v`
Expected: FAIL — cascade signals are currently emitted only to stdout.

### Task 8.3 — Replace stdout emissions with `emitSignalWithPersistence`

- [ ] **Step 8.3.1: Refactor `preflight.go`**

In `lib/orchestrator/preflight.go::RunDeps`, replace each:

```go
_ = json.NewEncoder(stdout).Encode(cascade)
```

with:

```go
_ = emitSignalWithPersistence(cascade, stdout, projectRoot, s.ID, stderr)
```

Where:
- For the intermediate cascade inside the loop, `s.ID` is the dep that cascades.
- For the `dep_start_failed` cascade at line ~90, the sensorID should be `targetID` (the root) — that's the sensor whose `.runtime` the failure-of-a-dep-blocks-the-root signal belongs to.

Pass `stderr` through: add `stderr io.Writer` to the helper signature already done in Task 6.

- [ ] **Step 8.3.2: Refactor `live_deps.go`**

In `live_deps.go`:

- `AttachLiveDep` — `dep_attached`/`dep_started` signals: target is `dep.ID`.
- `DetachLiveDep` — `dep_detached`: target is `depID`.
- `stopBlockingDep` — final aggregate: target is `entry.SensorID`.

Each becomes `emitSignalWithPersistence(sig, stdout, projectRoot, <targetID>, stderr)`.

`AttachLiveDep` and `DetachLiveDep` need a `projectRoot` parameter, which they can derive from `r.ProjectRoot`. Update call sites in `preflight.go` accordingly.

- [ ] **Step 8.3.3: Update call sites that don't currently pass stderr**

Look at every caller of `AttachLiveDep` / `DetachLiveDep` to ensure `stderr` flows through. They already accept `stderr` in their signature — verify.

- [ ] **Step 8.3.4: Run the cascade persistence test**

Run: `go test ./lib/orchestrator/ -run TestRunDeps_CascadeSignalPersistedToSignalsLog -v`
Expected: PASS

- [ ] **Step 8.3.5: Run full orchestrator suite**

Run: `go test ./lib/orchestrator/...`
Expected: PASS

### Task 8.4 — Vet and commit

- [ ] **Step 8.4.1: Vet**

Run: `go vet ./lib/orchestrator/...`

- [ ] **Step 8.4.2: Commit**

```bash
git add lib/orchestrator/preflight.go lib/orchestrator/preflight_test.go \
        lib/orchestrator/live_deps.go lib/orchestrator/live_deps_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): cascade and dep_* signals land in signals.log

Every emission point in the orchestrator now routes through
emitSignalWithPersistence:
  - RunDeps cascade signals (intermediate dep cascades and root cascade)
  - AttachLiveDep dep_attached / dep_started
  - DetachLiveDep dep_detached
  - stopBlockingDep final aggregate

The signal goes to stdout JSONL AND to
.runtime/sensors/<targetID>/<run_id>/signals.log byte-for-byte, closing
the diagnostic gap where a failed dep cascade was visible at runtime but
left no trace on disk for /tail-sensor or post-mortem analysis.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Golden fixtures + end-to-end smoke test

**Files:**
- Create: `sensors/fixtures/requires-tool-missing.json`
- Create: `sensors/fixtures/requires-context-missing.json`
- Create: `scripts/smoke-requires-deps-logs.sh`

### Task 9.1 — `requires-tool-missing.json`

- [ ] **Step 9.1.1: Create the fixture**

Create `sensors/fixtures/requires-tool-missing.json`:

```json
{
  "id": "requires-tool-missing",
  "version": "0.1.0",
  "name": "Requires Tool Missing (fixture)",
  "description": "Asserts that the requires[] gate fails when a declared tool is not on PATH.",
  "kind": "assertion",
  "type": "computational",
  "regulation": "maintainability",
  "phase": "on-demand",
  "determinism": "high",
  "output": "stream",
  "cost": {
    "class": "cheap",
    "latency": { "p50_ms": 1, "p95_ms": 5, "timeout_ms": 5000 },
    "compute": { "cpu": "low", "memory_mb": 1 }
  },
  "triggers": [{ "on": "manual" }],
  "requires": [
    { "kind": "tool", "name": "definitely-not-on-PATH-xyz-1234" }
  ],
  "execution": {
    "command": "echo this command should never run",
    "exit_code_map": [
      { "exit_code": 0, "verdict": "pass", "severity": "info" }
    ],
    "output_parsing": {
      "patterns": [
        { "regex": "never", "verdict": "pass", "severity": "info" }
      ]
    }
  },
  "verification": {
    "golden_cases": [
      {
        "fixture": "self",
        "expected_verdict": "error",
        "expected_severity": "high",
        "notes": "Tool 'definitely-not-on-PATH-xyz-1234' will never be on PATH."
      }
    ]
  }
}
```

- [ ] **Step 9.1.2: Validate it against the schema**

Run: `go run -tags=run_computational ./skills/run-sensor/scripts requires-tool-missing` from inside `sensors/fixtures/` directory? No — `run-sensor` resolves via `sensors/<id>.json`. Instead, validate manually by running the existing schema-validate sensor.

Run from worktree root: `go test ./lib/schema/...`
Expected: PASS (no schema test should regress — we just added a fixture).

### Task 9.2 — `requires-context-missing.json`

- [ ] **Step 9.2.1: Create the fixture**

Create `sensors/fixtures/requires-context-missing.json`:

```json
{
  "id": "requires-context-missing",
  "version": "0.1.0",
  "name": "Requires Context Missing (fixture)",
  "description": "Asserts that the requires[] gate fails when a declared context path does not exist.",
  "kind": "assertion",
  "type": "computational",
  "regulation": "maintainability",
  "phase": "on-demand",
  "determinism": "high",
  "output": "stream",
  "cost": {
    "class": "cheap",
    "latency": { "p50_ms": 1, "p95_ms": 5, "timeout_ms": 5000 },
    "compute": { "cpu": "low", "memory_mb": 1 }
  },
  "triggers": [{ "on": "manual" }],
  "requires": [
    { "kind": "context", "path": "./this/path/does/not/exist/12345-fixture" }
  ],
  "execution": {
    "command": "echo this command should never run",
    "exit_code_map": [
      { "exit_code": 0, "verdict": "pass", "severity": "info" }
    ],
    "output_parsing": {
      "patterns": [
        { "regex": "never", "verdict": "pass", "severity": "info" }
      ]
    }
  },
  "verification": {
    "golden_cases": [
      {
        "fixture": "self",
        "expected_verdict": "error",
        "expected_severity": "high",
        "notes": "Path is a guaranteed non-existent fixture under the worktree."
      }
    ]
  }
}
```

### Task 9.3 — Smoke test script

- [ ] **Step 9.3.1: Create the script**

Create `scripts/smoke-requires-deps-logs.sh`:

```bash
#!/usr/bin/env bash
# Smoke: requires[] gate + .runtime persistence end-to-end.
# Run from worktree root. Exits 0 on success, non-zero on the first
# unmet assertion. No CI integration — run by hand after major changes.

set -euo pipefail

WORKTREE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$WORKTREE_ROOT"

cleanup() {
  rm -rf .runtime/sensors/requires-tool-missing 2>/dev/null || true
  rm -rf .runtime/sensors/requires-context-missing 2>/dev/null || true
}
trap cleanup EXIT

# Symlink the fixtures into sensors/ so run-sensor can find them.
ln -sf "$WORKTREE_ROOT/sensors/fixtures/requires-tool-missing.json" \
       "$WORKTREE_ROOT/sensors/requires-tool-missing.json"
ln -sf "$WORKTREE_ROOT/sensors/fixtures/requires-context-missing.json" \
       "$WORKTREE_ROOT/sensors/requires-context-missing.json"

cleanup_symlinks() {
  rm -f "$WORKTREE_ROOT/sensors/requires-tool-missing.json"
  rm -f "$WORKTREE_ROOT/sensors/requires-context-missing.json"
}
trap 'cleanup_symlinks; cleanup' EXIT

echo "=== Smoke 1: requires-tool-missing should fail with verdict=error and heal_hint=binary-not-found:* ==="
OUTPUT="$(go run -tags=run_computational ./skills/run-sensor/scripts requires-tool-missing || true)"
echo "$OUTPUT" | grep -q '"verdict":"error"'      || { echo "FAIL: no verdict=error"; exit 1; }
echo "$OUTPUT" | grep -q '"heal_hint":"binary-not-found' || { echo "FAIL: no binary-not-found heal_hint"; exit 1; }

# .runtime/sensors/requires-tool-missing/<run_id>/signals.log must exist and match stdout.
RID_DIR="$(ls .runtime/sensors/requires-tool-missing 2>/dev/null | head -n1 || true)"
[ -n "$RID_DIR" ] || { echo "FAIL: no run_id dir for requires-tool-missing"; exit 1; }
[ -s ".runtime/sensors/requires-tool-missing/$RID_DIR/signals.log" ] || { echo "FAIL: signals.log empty"; exit 1; }
diff <(echo -n "$OUTPUT") ".runtime/sensors/requires-tool-missing/$RID_DIR/signals.log" \
  || { echo "FAIL: stdout != signals.log"; exit 1; }

echo "=== Smoke 2: requires-context-missing should fail with verdict=error and heal_hint=missing-context:* ==="
OUTPUT="$(go run -tags=run_computational ./skills/run-sensor/scripts requires-context-missing || true)"
echo "$OUTPUT" | grep -q '"verdict":"error"'         || { echo "FAIL: no verdict=error"; exit 1; }
echo "$OUTPUT" | grep -q '"heal_hint":"missing-context' || { echo "FAIL: no missing-context heal_hint"; exit 1; }

RID_DIR="$(ls .runtime/sensors/requires-context-missing 2>/dev/null | head -n1 || true)"
[ -n "$RID_DIR" ] || { echo "FAIL: no run_id dir for requires-context-missing"; exit 1; }
[ -s ".runtime/sensors/requires-context-missing/$RID_DIR/signals.log" ] || { echo "FAIL: signals.log empty"; exit 1; }

echo "OK"
```

- [ ] **Step 9.3.2: Make executable**

Run: `chmod +x scripts/smoke-requires-deps-logs.sh`

- [ ] **Step 9.3.3: Run it**

Run: `./scripts/smoke-requires-deps-logs.sh`
Expected: `=== Smoke 1: ... ===`, `=== Smoke 2: ... ===`, `OK`

### Task 9.4 — Run the full DoD checklist

Each item in spec §14:

- [ ] **DoD 1:** `go test ./lib/sensor/...` — PASS
- [ ] **DoD 2:** `go test ./lib/heal/...` — PASS
- [ ] **DoD 3:** `go test ./lib/watcher/...` — PASS
- [ ] **DoD 4:** `go test ./lib/subprocess/...` — PASS
- [ ] **DoD 5:** `go test -tags=start_sensor ./skills/start-sensor/...` — PASS
- [ ] **DoD 6:** `go test ./lib/orchestrator/...` — PASS
- [ ] **DoD 7:** fixtures exist under `sensors/fixtures/`
- [ ] **DoD 8:** smoke script PASS
- [ ] **DoD 9:** `go vet -tags=run_computational ./...` and `go vet -tags=run_inferential ./...` — clean

If any item fails, fix in a follow-up commit on this same branch.

### Task 9.5 — Commit

- [ ] **Step 9.5.1: Commit**

```bash
git add sensors/fixtures/requires-tool-missing.json \
        sensors/fixtures/requires-context-missing.json \
        scripts/smoke-requires-deps-logs.sh
git commit -m "$(cat <<'EOF'
test(sensors): golden fixtures and smoke for requires[] gate + .runtime logs

Adds two fixture sensors exercising the new requires[] gate paths
(tool / context) and a shell smoke that verifies:
  - verdict=error + heal_hint shape per kind
  - .runtime/sensors/<id>/<run_id>/signals.log exists and matches stdout

Run manually via scripts/smoke-requires-deps-logs.sh from worktree root
after substantive changes to the orchestrator or sensor gate.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review

### Spec coverage check

| Spec section | Task | Notes |
|---|---|---|
| §6 `Failure`, `Gate`, `GateOpts` | Task 2.1 | Types match spec |
| §6 `CheckRequiresGate` (tool/context/env, ignore others) | Tasks 2.2, 2.3, 2.4, 2.5 | Per-kind tests; ordering verified |
| §6 `BuildRequiresGateSignal` | Task 2.6 | One evidence per failure; heal_hint from first |
| §6 `BuildMissingEnvSignal` wrapper | Task 2.7 | Refactored to delegate |
| §7 `ShapeMissingContext` + IsKnown | Task 1.1 | |
| §7 `missing_context` rule | Tasks 1.2, 1.4 | Rule + registration |
| §7 extend `binary-not-found` for "Required tool" rationale | Task 1.3 | |
| §8 `lib/watcher` extraction | Task 4 | Spawn, BinaryPath, unix sys attr |
| §8 `start.go` refactor to call `watcher.Spawn` | Task 4.4 | |
| §9 `StreamConfig.RawLogPath` | Task 5 | Tee + best-effort error handling |
| §10 Phase 0 swap in `RunOne` | Task 3 | |
| §10 `prepareRuntimeDir` | Task 6.1, 6.2 | |
| §10 `RunOne` runtime-dir + tee wiring | Task 6.4 | |
| §10 `runtime_dir_failed` signal | Task 6.4.3 | Inline in `RunOne` |
| §11 `emitSignalWithPersistence` | Task 6.2 | Run_id from sig only |
| §11 Apply at every emission point | Task 8 | Cascade, dep_attached, dep_detached, aggregate |
| §12 `startBlockingDep` → `watcher.Spawn` | Task 7 | WatcherPID populated; logDir with run_id |
| §13 error handling matrix | Tasks 2.2, 2.3, 4.4, 5.3, 6.4 | Per-row coverage |
| §14 DoD 1–9 | Task 9.4 | Checklist run |
| §15 implementation order | Tasks 1–9 | Mirrors spec ordering |

No gaps detected.

### Placeholder scan

Searched for "TBD", "TODO", "fill in", "appropriate error handling", "similar to". None found in real plan text. Two `// TODO Task X.Y` comments exist inside Task 2.1 / 2.2 skeleton code — they are intentional incremental scaffolding that the same task removes by Task 2.4. Acceptable.

### Type / API consistency

- `Failure` fields used the same way in Task 2 tests and in `BuildRequiresGateSignal` — ✅
- `emitSignalWithPersistence` signature: `(sig, stdout, projectRoot, sensorID, stderr)`. Used identically in Task 6 helper and Task 8 call sites. ✅
- `watcher.SpawnOpts` field names match between Task 4 definition and Task 7 call site (`ProjectRoot`, `SensorID`, `RunID`, `RawLogPath`, `SignalsLogPath`, `EnvelopeJSON`, `PatternsJSON`, `SubprocessPID`). ✅
- `prepareRuntimeDir` returns `(rawLogPath, signalsLogPath, err)` consistently in Task 6, 7, 8. ✅

### Open caveats for the implementer

1. **`registry.Root.ProjectRoot` field name.** Task 7 assumes it's exported. Verify at start of Task 7.3 via `grep -n "ProjectRoot" lib/registry/paths.go`. If it's unexported (e.g. `projectRoot`), the helper accessor must be added or you must use an accessor method like `r.Project()`.
2. **`stderr_pattern.go` regex shape.** Task 1.3 says "the file's current shape" determines how to extend. Read the file before editing; if it uses a single regex, the cleanest change is to mirror the slice-of-patterns idiom in `lib/heal/patterns.go::stderrPatterns`.
3. **Existing orchestrator tests with `s.Path = "/tmp/..."`.** Task 6.4.4 warns that pre-existing tests may now write `.runtime` to unintended paths. Fix incrementally if you see test regressions; do not skip them.

---

## Plan complete

**Plan saved to:** `docs/superpowers/plans/2026-05-11-resolve-requires-and-persist-deps-logs.md`

Two execution options:

**1. Subagent-Driven (recommended)** — Dispatch a fresh subagent per Task, review between tasks, fast iteration. Each task is self-contained with its own commit.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
