# UseCase Entity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce a fourth entity (UseCase) to the harness with its own JSON Schema, extend `stack.json` with `purpose`/`archetypes`/`journeys`, and ship `/detect-usecases` — the LLM-driven skill that scans a project and persists one descriptive UseCase per observable journey variation.

**Architecture:** Same trilayer pattern as the existing sensor and stack subsystems — JSON Schema in `schemas/`, typed Go shape + load/persist/cross-check helpers in `lib/<context>/`, thin CLI wrapper script under `skills/<name>/scripts/`. UseCase is purely descriptive: it carries `trigger`/`behavior`/`expected_outcome` as narrative prose + freeform fixtures (no executor in this scope; an out-of-scope future `/create-sensor` reads UseCases to synthesize regression sensors). `journey_id` cross-references `stack.journeys[].id`, validated in Go (not JSON Schema).

**Tech Stack:** Go 1.25 (single module `github.com/iurykrieger/harness-framework`), JSON Schema Draft 2020-12, `github.com/santhosh-tekuri/jsonschema/v5`, standard `testing` package with table-driven tests, `runtime.Caller`-based fixture discovery (no `testing/iotest`-style network/IO mocking — tests touch real testdata files), atomic file writes via tempfile + rename.

**Spec:** `docs/superpowers/specs/2026-05-14-usecase-entity-design.md`

---

## File Inventory

**New files:**
- `schemas/usecase.json`
- `lib/usecase/shape.go`, `shape_test.go`
- `lib/usecase/load.go`, `load_test.go`
- `lib/usecase/persist.go`, `persist_test.go`
- `lib/usecase/evidence.go`, `evidence_test.go`
- `lib/usecase/cross_check.go`, `cross_check_test.go`
- `lib/usecase/usecasetest/canonical.go`
- `lib/usecase/testdata/canonical-usecase.json`
- `lib/usecase/testdata/invalid-missing-journey-id.json`
- `lib/usecase/testdata/invalid-empty-evidence.json`
- `lib/usecase/testdata/invalid-bad-id-pattern.json`
- `lib/usecase/testdata/invalid-bad-version-format.json`
- `lib/usecase/testdata/invalid-missing-trigger-fixture.json`
- `lib/stack/cross_check.go`, `cross_check_test.go`
- `lib/stack/testdata/golden-stack-with-journeys.json`
- `lib/stack/testdata/invalid-journey-archetype-orphan.json`
- `skills/detect-usecases/SKILL.md`
- `skills/detect-usecases/scripts/write-usecase.go`
- `skills/detect-usecases/scripts/write-usecase_test.go`

**Modified files:**
- `schemas/stack.json` — add `purpose`, `archetypes`, `journeys` + `$defs/{Archetype,EntryPointKind,Journey,EntryPoint}`
- `lib/schema/validator.go` — add `TargetUseCase`, `usecaseURL`, compile usecase schema; `NewValidator` reads `usecase.json` from `schemasDir`
- `lib/schema/discover.go` — `FindSchemasDir` requires `usecase.json` alongside the existing three
- `lib/schema/validator_test.go`, `lib/schema/discover_test.go` — cover new target + new required file
- `lib/stack/shape.go` — add `Purpose`, `Archetypes`, `Journeys` to `Stack`; add `Journey`, `EntryPoint`, `Archetype`, `EntryPointKind` types
- `lib/stack/shape_test.go` — legacy decode + full-stack-with-journeys decode
- `lib/stack/persist.go` — call `cross_check` helpers after schema validation
- `skills/detect-sensors/scripts/write-stack.go` — remove local `crossCheckProducedBy` (migrated into `lib/stack`)
- `skills/detect-sensors/scripts/write-stack_test.go` — same coverage, now exercised through `stack.ValidateAndPersist`
- `CLAUDE.md` — schema enumeration becomes "four schemas"; mention `.harness/usecases/` and the new skill

---

## Task 1: Add `usecase.json` schema and reserve the validator slot

**Files:**
- Create: `schemas/usecase.json`
- Modify: `lib/schema/validator.go`
- Modify: `lib/schema/discover.go`
- Modify: `lib/schema/validator_test.go`
- Modify: `lib/schema/discover_test.go`

This task introduces the schema file and makes the validator package aware of a fourth target, so subsequent tasks (and tests) can load it. The schema itself does NOT yet validate every nuance — that comes via the canonical fixture in Task 7. Here we lock in the contract.

- [ ] **Step 1: Write `schemas/usecase.json`**

Create the file at `schemas/usecase.json` with content:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://harness-framework/schemas/usecase.json",
  "title": "UseCase",
  "description": "An observable journey variation of the project: one concrete input shape, one expected behavior, one expected outcome. Produced by /detect-usecases; consumed by /create-sensor to synthesize deterministic regression sensors.",

  "$defs": {
    "Evidence": {
      "type": "object",
      "additionalProperties": false,
      "required": ["file", "rationale"],
      "properties": {
        "file":       { "type": "string" },
        "line_start": { "type": ["integer", "null"], "minimum": 1 },
        "line_end":   { "type": ["integer", "null"], "minimum": 1 },
        "rationale":  { "type": "string" }
      }
    },
    "Trigger": {
      "type": "object",
      "additionalProperties": false,
      "required": ["summary", "shape", "fixture"],
      "properties": {
        "summary":       { "type": "string" },
        "shape":         { "type": "string" },
        "fixture":       { "description": "Concrete example input. Free shape; depends on the trigger kind." },
        "preconditions": { "type": "array", "items": { "type": "string" } }
      }
    },
    "Behavior": {
      "type": "object",
      "additionalProperties": false,
      "required": ["summary"],
      "properties": {
        "summary":        { "type": "string" },
        "business_rules": { "type": "array", "items": { "type": "string" } }
      }
    },
    "ExpectedOutcome": {
      "type": "object",
      "additionalProperties": false,
      "required": ["summary", "shape", "fixture"],
      "properties": {
        "summary":      { "type": "string" },
        "shape":        { "type": "string" },
        "fixture":      { "description": "Concrete example output." },
        "invariants":   { "type": "array", "items": { "type": "string" } },
        "side_effects": { "type": "array", "items": { "type": "string" } }
      }
    }
  },

  "type": "object",
  "additionalProperties": false,
  "required": [
    "id", "version", "name", "description",
    "journey_id",
    "trigger", "behavior", "expected_outcome",
    "evidence"
  ],
  "properties": {
    "id": {
      "type": "string",
      "pattern": "^[a-z][a-z0-9-]*$"
    },
    "version": {
      "type": "string",
      "pattern": "^\\d+\\.\\d+\\.\\d+(-[A-Za-z0-9.-]+)?(\\+[A-Za-z0-9.-]+)?$"
    },
    "name":        { "type": "string" },
    "description": { "type": "string" },
    "journey_id": {
      "type": "string",
      "pattern": "^[a-z][a-z0-9-]*$"
    },
    "trigger":          { "$ref": "#/$defs/Trigger" },
    "behavior":         { "$ref": "#/$defs/Behavior" },
    "expected_outcome": { "$ref": "#/$defs/ExpectedOutcome" },
    "evidence": {
      "type": "array",
      "minItems": 1,
      "items": { "$ref": "#/$defs/Evidence" }
    },
    "regression_priority": {
      "type": "string",
      "enum": ["critical", "high", "medium", "low"]
    },
    "blind_spots": { "type": "array", "items": { "type": "string" } },
    "tags":        { "type": "array", "items": { "type": "string" } },
    "references":  { "type": "array", "items": { "type": "string" } }
  }
}
```

- [ ] **Step 2: Modify `lib/schema/validator.go`**

Edit the top of the file to add the new URL constant and the new Target:

```go
const (
    schemaBaseURL = "https://harness-framework/schemas/"
    sensorURL     = schemaBaseURL + "sensor.json"
    signalURL     = schemaBaseURL + "signal.json"
    stackURL      = schemaBaseURL + "stack.json"
    usecaseURL    = schemaBaseURL + "usecase.json"
)

type Target string

const (
    TargetSensor  Target = "sensor"
    TargetSignal  Target = "signal"
    TargetStack   Target = "stack"
    TargetUseCase Target = "usecase"
)

type Validator struct {
    sensor  *jsonschema.Schema
    signal  *jsonschema.Schema
    stack   *jsonschema.Schema
    usecase *jsonschema.Schema
}
```

In `NewValidator`, after the existing `stackBytes` read and `AddResource` block, add usecase loading and compilation:

```go
    usecaseBytes, err := os.ReadFile(filepath.Join(schemasDir, "usecase.json"))
    if err != nil {
        return nil, fmt.Errorf("read usecase.json: %w", err)
    }
    if err := c.AddResource(usecaseURL, strings.NewReader(string(usecaseBytes))); err != nil {
        return nil, fmt.Errorf("register usecase schema: %w", err)
    }
```

After the existing `Compile(stackURL)` block:

```go
    usecase, err := c.Compile(usecaseURL)
    if err != nil {
        return nil, fmt.Errorf("compile usecase schema: %w", err)
    }
    return &Validator{sensor: sensor, signal: signal, stack: stack, usecase: usecase}, nil
```

In `Validate`, add the new case before `default`:

```go
    case TargetUseCase:
        return v.usecase.Validate(instance)
```

- [ ] **Step 3: Modify `lib/schema/discover.go`**

Update `FindSchemasDir` to require `usecase.json` alongside the existing three:

```go
        if hasFile(filepath.Join(candidate, "sensor.json")) &&
            hasFile(filepath.Join(candidate, "signal.json")) &&
            hasFile(filepath.Join(candidate, "stack.json")) &&
            hasFile(filepath.Join(candidate, "usecase.json")) {
            return candidate, nil
        }
```

- [ ] **Step 4: Write a failing test for the new target**

Edit `lib/schema/validator_test.go`. Add a new test:

```go
func TestValidator_UseCaseTarget(t *testing.T) {
    schemasDir := schematest.RepoSchemasDir(t)
    v, err := NewValidator(schemasDir)
    if err != nil {
        t.Fatal(err)
    }
    bad := map[string]interface{}{"id": "x"} // missing every other required field
    if err := v.Validate(TargetUseCase, bad); err == nil {
        t.Fatal("expected schema rejection for empty usecase, got nil")
    }
}
```

- [ ] **Step 5: Run the test (it should pass — schema and validator are in place)**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/usecases && go test ./lib/schema/...`
Expected: PASS for all tests including the new one. If `NewValidator` errors with "read usecase.json: …", the schema file was not written in Step 1 — fix and re-run.

- [ ] **Step 6: Commit**

```bash
git add schemas/usecase.json lib/schema/validator.go lib/schema/discover.go lib/schema/validator_test.go
git commit -m "$(cat <<'EOF'
feat(schema): add usecase.json schema and validator target

Introduces the fourth entity-schema and wires it through NewValidator,
TargetUseCase, and FindSchemasDir. Subsequent tasks build on this base.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Extend `stack.json` with `purpose`/`archetypes`/`journeys`

**Files:**
- Modify: `schemas/stack.json`
- Modify: `lib/schema/validator.go` (only if a `detectStackLegacyShape` is needed — usually not)
- Create: `lib/stack/testdata/golden-stack-with-journeys.json`
- Modify: `lib/stack/load_test.go`

The three new fields are additive and optional — existing stacks remain valid.

- [ ] **Step 1: Add new `$defs` and properties in `schemas/stack.json`**

Open `schemas/stack.json`. In the existing `$defs` object, add four new defs after `LogShape`:

```json
    ,
    "Archetype": {
      "type": "string",
      "enum": [
        "http-api",
        "http-spa",
        "http-ssr",
        "queue-consumer",
        "queue-producer",
        "cli-tool",
        "library",
        "iac",
        "data-pipeline",
        "scheduler",
        "event-driven-service",
        "db-bound-service"
      ]
    },
    "EntryPointKind": {
      "type": "string",
      "enum": [
        "http-route",
        "queue-subscription",
        "cli-command",
        "scheduled-job",
        "event-handler",
        "grpc-method"
      ]
    },
    "EntryPoint": {
      "type": "object",
      "additionalProperties": false,
      "required": ["kind", "evidence"],
      "properties": {
        "kind":     { "$ref": "#/$defs/EntryPointKind" },
        "method":   { "type": "string" },
        "path":     { "type": "string" },
        "topic":    { "type": "string" },
        "command":  { "type": "string" },
        "schedule": { "type": "string" },
        "evidence": { "$ref": "#/$defs/Evidence" }
      }
    },
    "Journey": {
      "type": "object",
      "additionalProperties": false,
      "required": ["id", "name", "summary", "archetype", "entry_points"],
      "properties": {
        "id":        { "type": "string", "pattern": "^[a-z][a-z0-9-]*$" },
        "name":      { "type": "string" },
        "summary":   { "type": "string" },
        "archetype": { "$ref": "#/$defs/Archetype" },
        "entry_points": {
          "type": "array",
          "minItems": 1,
          "items": { "$ref": "#/$defs/EntryPoint" }
        }
      }
    }
```

In the top-level `properties` object, add (after `log_shapes`):

```json
    ,
    "purpose": {
      "type": "string",
      "description": "One-sentence declarative description of what the application does in the world."
    },
    "archetypes": {
      "type": "array",
      "items": { "$ref": "#/$defs/Archetype" },
      "description": "Closed-enum archetype labels. Multiple values valid for hybrid apps."
    },
    "journeys": {
      "type": "array",
      "items": { "$ref": "#/$defs/Journey" },
      "description": "Aggregation layer that UseCases reference via journey_id."
    }
```

The top-level `required` array stays unchanged — the new fields are all optional.

- [ ] **Step 2: Create the golden full-stack fixture**

Create `lib/stack/testdata/golden-stack-with-journeys.json`:

```json
{
  "version": "0.2.0",
  "detected_at": "2026-05-14T10:00:00Z",
  "detected_by": "manual",
  "languages": [{ "name": "go", "version": "1.25" }],
  "components": [
    {
      "role": "http-server",
      "name": "net/http",
      "evidence": [
        { "file": "cmd/server/main.go", "line_start": 10, "rationale": "http.ListenAndServe call site" }
      ]
    }
  ],
  "log_shapes": [
    {
      "id": "plain-stdout",
      "produced_by": ["net/http"],
      "format": "plain",
      "sample": "starting server on :8080"
    }
  ],
  "purpose": "HTTP API for managing user accounts.",
  "archetypes": ["http-api"],
  "journeys": [
    {
      "id": "user-registration",
      "name": "User registration",
      "summary": "POST /users creates an account.",
      "archetype": "http-api",
      "entry_points": [
        {
          "kind": "http-route",
          "method": "POST",
          "path": "/users",
          "evidence": {
            "file": "cmd/server/main.go",
            "line_start": 25,
            "rationale": "registration handler"
          }
        }
      ]
    }
  ]
}
```

- [ ] **Step 3: Add a passing test that the full stack decodes**

Edit `lib/stack/load_test.go`. Extend the existing `TestLoadStackFile` table with a new case:

```go
        {name: "with journeys", fixture: "golden-stack-with-journeys.json", wantCode: 0},
```

(Append it inside the `cases := []struct{…}{…}` literal alongside the existing entries.)

- [ ] **Step 4: Run the test to confirm full stack passes schema**

Run: `go test ./lib/stack/...`
Expected: PASS including the new `with journeys` case.

- [ ] **Step 5: Add a failing test that legacy stack still passes**

Inside the same table, add:

```go
        {name: "legacy stack without new fields", fixture: "golden-stack.json", wantCode: 0},
```

Run: `go test ./lib/stack/...`
Expected: PASS (proves retrocompat — the new schema fields are optional).

- [ ] **Step 6: Commit**

```bash
git add schemas/stack.json lib/stack/testdata/golden-stack-with-journeys.json lib/stack/load_test.go
git commit -m "$(cat <<'EOF'
feat(stack): add optional purpose/archetypes/journeys to stack schema

Three additive top-level fields plus four $defs (Archetype, EntryPointKind,
EntryPoint, Journey). Existing stack.json files remain valid; the new fields
are populated by the upcoming /detect-usecases skill.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Extend `lib/stack/shape.go` with Journey, EntryPoint, and new top-level fields

**Files:**
- Modify: `lib/stack/shape.go`
- Create: `lib/stack/shape_test.go` (or append if it exists; this plan assumes the package has none yet — verify)

- [ ] **Step 1: Write a failing round-trip test for Journey + EntryPoint**

Check if `lib/stack/shape_test.go` exists:

```bash
ls /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/usecases/lib/stack/shape_test.go
```

If missing, create it. If present, append the test below.

Create or extend `lib/stack/shape_test.go`:

```go
package stack

import (
    "encoding/json"
    "testing"
)

func TestStack_FullRoundTrip(t *testing.T) {
    lineStart := 25
    in := Stack{
        Version: "0.2.0",
        Languages: []Language{{Name: "go", Version: "1.25"}},
        Components: []Component{{
            Role: RoleHTTPServer,
            Name: "net/http",
            Evidence: []Evidence{{File: "cmd/server/main.go", LineStart: &lineStart, Rationale: "x"}},
        }},
        LogShapes: []LogShape{{ID: "x", ProducedBy: []string{"net/http"}, Format: FormatPlain, Sample: "x"}},
        Purpose:   "HTTP API",
        Archetypes: []Archetype{ArchetypeHTTPAPI},
        Journeys: []Journey{{
            ID:        "user-registration",
            Name:      "User registration",
            Summary:   "POST /users",
            Archetype: ArchetypeHTTPAPI,
            EntryPoints: []EntryPoint{{
                Kind:     EntryPointHTTPRoute,
                Method:   "POST",
                Path:     "/users",
                Evidence: Evidence{File: "cmd/server/main.go", LineStart: &lineStart, Rationale: "handler"},
            }},
        }},
    }
    body, err := json.Marshal(in)
    if err != nil {
        t.Fatal(err)
    }
    var out Stack
    if err := json.Unmarshal(body, &out); err != nil {
        t.Fatal(err)
    }
    if out.Purpose != "HTTP API" {
        t.Errorf("purpose = %q", out.Purpose)
    }
    if len(out.Archetypes) != 1 || out.Archetypes[0] != ArchetypeHTTPAPI {
        t.Errorf("archetypes = %v", out.Archetypes)
    }
    if len(out.Journeys) != 1 || out.Journeys[0].ID != "user-registration" {
        t.Errorf("journeys = %v", out.Journeys)
    }
}

func TestStack_OptionalFieldsOmitted(t *testing.T) {
    in := Stack{
        Version: "0.1.0",
        Languages: []Language{{Name: "go"}},
        Components: []Component{{Role: RoleLogger, Name: "x", Evidence: []Evidence{{File: "x.go", Rationale: "x"}}}},
        LogShapes: []LogShape{{ID: "x", ProducedBy: []string{"x"}, Format: FormatPlain, Sample: "x"}},
    }
    body, err := json.Marshal(in)
    if err != nil {
        t.Fatal(err)
    }
    s := string(body)
    for _, key := range []string{`"purpose"`, `"archetypes"`, `"journeys"`} {
        if containsKey(s, key) {
            t.Errorf("expected %s to be omitted, got %s", key, s)
        }
    }
}

func containsKey(haystack, needle string) bool {
    // simple substring check; the JSON keys we look for have surrounding quotes
    for i := 0; i+len(needle) <= len(haystack); i++ {
        if haystack[i:i+len(needle)] == needle {
            return true
        }
    }
    return false
}
```

- [ ] **Step 2: Run the test (it should fail — types don't exist yet)**

Run: `go test ./lib/stack/...`
Expected: FAIL with `undefined: Archetype`, `undefined: ArchetypeHTTPAPI`, `undefined: Journey`, `undefined: EntryPoint`, `undefined: EntryPointHTTPRoute`, and `s.Purpose`/`s.Archetypes`/`s.Journeys` undefined on `Stack`.

- [ ] **Step 3: Extend `Stack` and add new types in `lib/stack/shape.go`**

Open `lib/stack/shape.go`. Modify the `Stack` struct to add three new fields after `LogShapes`:

```go
type Stack struct {
    Version    string      `json:"version"`
    DetectedAt string      `json:"detected_at"`
    DetectedBy string      `json:"detected_by"`
    Languages  []Language  `json:"languages"`
    Components []Component `json:"components"`
    LogShapes  []LogShape  `json:"log_shapes"`

    Purpose    string      `json:"purpose,omitempty"`
    Archetypes []Archetype `json:"archetypes,omitempty"`
    Journeys   []Journey   `json:"journeys,omitempty"`
}
```

Append (after the existing `FieldMeaning` block, before `ShapesByRole`):

```go
// Archetype is the enum from $defs/Archetype in stack.json.
type Archetype string

const (
    ArchetypeHTTPAPI            Archetype = "http-api"
    ArchetypeHTTPSPA            Archetype = "http-spa"
    ArchetypeHTTPSSR            Archetype = "http-ssr"
    ArchetypeQueueConsumer      Archetype = "queue-consumer"
    ArchetypeQueueProducer      Archetype = "queue-producer"
    ArchetypeCLITool            Archetype = "cli-tool"
    ArchetypeLibrary            Archetype = "library"
    ArchetypeIaC                Archetype = "iac"
    ArchetypeDataPipeline       Archetype = "data-pipeline"
    ArchetypeScheduler          Archetype = "scheduler"
    ArchetypeEventDrivenService Archetype = "event-driven-service"
    ArchetypeDBBoundService     Archetype = "db-bound-service"
)

// EntryPointKind is the enum from $defs/EntryPointKind.
type EntryPointKind string

const (
    EntryPointHTTPRoute         EntryPointKind = "http-route"
    EntryPointQueueSubscription EntryPointKind = "queue-subscription"
    EntryPointCLICommand        EntryPointKind = "cli-command"
    EntryPointScheduledJob      EntryPointKind = "scheduled-job"
    EntryPointEventHandler      EntryPointKind = "event-handler"
    EntryPointGRPCMethod        EntryPointKind = "grpc-method"
)

// Journey groups one or more UseCases under a shared concept.
type Journey struct {
    ID          string       `json:"id"`
    Name        string       `json:"name"`
    Summary     string       `json:"summary"`
    Archetype   Archetype    `json:"archetype"`
    EntryPoints []EntryPoint `json:"entry_points"`
}

// EntryPoint identifies where a journey enters the system.
type EntryPoint struct {
    Kind     EntryPointKind `json:"kind"`
    Method   string         `json:"method,omitempty"`
    Path     string         `json:"path,omitempty"`
    Topic    string         `json:"topic,omitempty"`
    Command  string         `json:"command,omitempty"`
    Schedule string         `json:"schedule,omitempty"`
    Evidence Evidence       `json:"evidence"`
}
```

- [ ] **Step 4: Run tests to confirm green**

Run: `go test ./lib/stack/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/stack/shape.go lib/stack/shape_test.go
git commit -m "$(cat <<'EOF'
feat(stack): typed Journey/EntryPoint + Archetype/EntryPointKind enums

Adds Purpose, Archetypes, and Journeys to the Stack struct with full
omitempty semantics. Existing stack.json files still decode unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Move `crossCheckProducedBy` into `lib/stack/cross_check.go` and add journey cross-checks

**Files:**
- Create: `lib/stack/cross_check.go`
- Create: `lib/stack/cross_check_test.go`
- Create: `lib/stack/testdata/invalid-journey-archetype-orphan.json`
- Modify: `lib/stack/persist.go`
- Modify: `skills/detect-sensors/scripts/write-stack.go`
- Modify: `skills/detect-sensors/scripts/write-stack_test.go` (only if assertions break)

Pulls the existing cross-check into the shared library so the new `/detect-usecases` flow (which also calls `stack.ValidateAndPersist`) benefits, and adds the two new checks promised by the spec.

- [ ] **Step 1: Write the failing test**

Create `lib/stack/cross_check_test.go`:

```go
package stack

import (
    "strings"
    "testing"
)

func TestCheckJourneyArchetypes_OK(t *testing.T) {
    s := &Stack{
        Archetypes: []Archetype{ArchetypeHTTPAPI},
        Journeys: []Journey{
            {ID: "j1", Archetype: ArchetypeHTTPAPI},
        },
    }
    if err := CheckJourneyArchetypes(s); err != nil {
        t.Fatalf("unexpected: %v", err)
    }
}

func TestCheckJourneyArchetypes_Orphan(t *testing.T) {
    s := &Stack{
        Archetypes: []Archetype{ArchetypeHTTPAPI},
        Journeys: []Journey{
            {ID: "j1", Archetype: ArchetypeQueueConsumer},
        },
    }
    err := CheckJourneyArchetypes(s)
    if err == nil {
        t.Fatal("expected error for orphan archetype, got nil")
    }
    if !strings.Contains(err.Error(), "queue-consumer") || !strings.Contains(err.Error(), "j1") {
        t.Errorf("error %q must name both the journey id and the archetype", err)
    }
}

func TestCheckProducedBy_Orphan(t *testing.T) {
    s := &Stack{
        Components: []Component{{Name: "real"}},
        LogShapes:  []LogShape{{ID: "lost", ProducedBy: []string{"ghost"}}},
    }
    err := CheckProducedBy(s)
    if err == nil {
        t.Fatal("expected error, got nil")
    }
    if !strings.Contains(err.Error(), "lost") || !strings.Contains(err.Error(), "ghost") {
        t.Errorf("error %q must name both the log_shape id and the orphan component", err)
    }
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./lib/stack/...`
Expected: FAIL with `undefined: CheckJourneyArchetypes`, `undefined: CheckProducedBy`.

- [ ] **Step 3: Implement `lib/stack/cross_check.go`**

```go
package stack

import "fmt"

// CheckProducedBy verifies every log_shapes[].produced_by[] entry matches
// some components[].name. Replaces the per-script implementation that
// previously lived in skills/detect-sensors/scripts/write-stack.go.
func CheckProducedBy(s *Stack) error {
    names := map[string]struct{}{}
    for _, c := range s.Components {
        names[c.Name] = struct{}{}
    }
    for _, sh := range s.LogShapes {
        for _, pb := range sh.ProducedBy {
            if _, ok := names[pb]; !ok {
                return fmt.Errorf("log_shape %q references unknown component %q", sh.ID, pb)
            }
        }
    }
    return nil
}

// CheckJourneyArchetypes verifies every journeys[].archetype is a value
// present in archetypes[]. Returns nil when both arrays are empty.
func CheckJourneyArchetypes(s *Stack) error {
    if len(s.Journeys) == 0 {
        return nil
    }
    known := map[Archetype]struct{}{}
    for _, a := range s.Archetypes {
        known[a] = struct{}{}
    }
    for _, j := range s.Journeys {
        if _, ok := known[j.Archetype]; !ok {
            return fmt.Errorf("journey %q declares archetype %q not listed in archetypes[]", j.ID, j.Archetype)
        }
    }
    return nil
}
```

- [ ] **Step 4: Run to confirm pass**

Run: `go test ./lib/stack/...`
Expected: PASS for the three new cases.

- [ ] **Step 5: Wire cross-checks into `lib/stack/ValidateAndPersist`**

Open `lib/stack/persist.go`. After the existing schema-validation block:

```go
    if err := v.Validate(schema.TargetStack, m); err != nil {
        return "", err
    }
```

Add (immediately after that `Validate` call):

```go
    body, _ := json.Marshal(m)
    var typed Stack
    if err := json.Unmarshal(body, &typed); err != nil {
        return "", fmt.Errorf("decode after schema validation: %w", err)
    }
    if err := CheckProducedBy(&typed); err != nil {
        return "", err
    }
    if err := CheckJourneyArchetypes(&typed); err != nil {
        return "", err
    }
```

(The re-marshal/unmarshal converts `map[string]interface{}` to the typed `Stack`. The map is what the schema validates; the struct is what the cross-checks consume.)

- [ ] **Step 6: Remove the local `crossCheckProducedBy` from `write-stack.go`**

Open `skills/detect-sensors/scripts/write-stack.go`. Delete the `crossCheckProducedBy(body)` call (it sits between `os.ReadFile` and `stack.ValidateAndPersist`). Delete the `crossCheckProducedBy` function definition at the bottom of the file. Also remove the now-unused `"encoding/json"` import if there are no other references in this file (`grep "json\." skills/detect-sensors/scripts/write-stack.go` after the edit — if the count is zero, drop the import).

- [ ] **Step 7: Verify existing `write-stack_test.go` still passes**

Run: `go test -tags=write_stack ./skills/detect-sensors/scripts/...`
Expected: PASS. The orphan-produced-by case now flows through `stack.ValidateAndPersist` instead of the local helper, but the observable behavior — exit code 1, stderr mentions the orphan — is identical.

If a test now reports the orphan error via `error:` prefix instead of `error: stack_produced_by_orphan:`, update the assertion to match the new error string OR accept that the test is now slightly less specific.

Read `skills/detect-sensors/scripts/write-stack_test.go` and adjust the assertion strings to match the new stderr (which is `"error: log_shape \"lost\" references unknown component \"ghost\"\n"` from `cross_check.go` formatted by `schema.PrintValidationOrPlain` if it falls through that path, or directly from `ValidateAndPersist`'s returned error).

- [ ] **Step 8: Run the broader test suite**

Run: `go test ./lib/... && go test -tags=write_stack ./skills/...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add lib/stack/cross_check.go lib/stack/cross_check_test.go lib/stack/persist.go \
        skills/detect-sensors/scripts/write-stack.go skills/detect-sensors/scripts/write-stack_test.go
git commit -m "$(cat <<'EOF'
refactor(stack): centralize cross-checks in lib/stack; add journey archetype check

Moves crossCheckProducedBy out of the write-stack script and into
lib/stack/cross_check.go so every caller of ValidateAndPersist benefits.
Adds CheckJourneyArchetypes per the UseCase design — journeys[].archetype
must be present in the top-level archetypes[] array.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Add `lib/usecase/shape.go` with the typed UseCase and a canonical fixture

**Files:**
- Create: `lib/usecase/shape.go`
- Create: `lib/usecase/shape_test.go`
- Create: `lib/usecase/testdata/canonical-usecase.json`

- [ ] **Step 1: Create the canonical fixture**

Create `lib/usecase/testdata/canonical-usecase.json`:

```json
{
  "id": "create-user-with-email",
  "version": "0.1.0",
  "name": "Create user with valid email",
  "description": "POST /users with a valid email creates an account and returns 201.",
  "journey_id": "user-registration",
  "trigger": {
    "summary": "POST request to /users carrying a JSON body with a valid email.",
    "shape": "HTTP request",
    "fixture": {
      "method": "POST",
      "path": "/users",
      "body": { "email": "alice@example.com" }
    },
    "preconditions": ["No user with email 'alice@example.com' exists."]
  },
  "behavior": {
    "summary": "Validates, persists, emits user.created.",
    "business_rules": ["Email must be unique."]
  },
  "expected_outcome": {
    "summary": "HTTP 201 with the user object.",
    "shape": "HTTP response",
    "fixture": {
      "status": 201,
      "body": { "id": "uuid", "email": "alice@example.com" }
    },
    "invariants": ["Response.body.id is UUID v4."],
    "side_effects": ["Row inserted in users."]
  },
  "evidence": [
    {
      "file": "src/users/users.controller.ts",
      "line_start": 42,
      "line_end": 68,
      "rationale": "POST /users handler"
    }
  ],
  "regression_priority": "critical",
  "tags": ["happy-path", "registration"]
}
```

- [ ] **Step 2: Write a failing round-trip test**

Create `lib/usecase/shape_test.go`:

```go
package usecase

import (
    "encoding/json"
    "os"
    "path/filepath"
    "runtime"
    "strings"
    "testing"
)

func TestUseCase_RoundTrip(t *testing.T) {
    body := readTestdata(t, "canonical-usecase.json")
    var uc UseCase
    if err := json.Unmarshal(body, &uc); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if uc.ID != "create-user-with-email" {
        t.Errorf("id = %q", uc.ID)
    }
    if uc.JourneyID != "user-registration" {
        t.Errorf("journey_id = %q", uc.JourneyID)
    }
    if uc.Trigger.Summary == "" || uc.Trigger.Shape == "" || uc.Trigger.Fixture == nil {
        t.Errorf("trigger decoded incomplete: %+v", uc.Trigger)
    }
    if uc.Behavior.Summary == "" {
        t.Errorf("behavior summary empty")
    }
    if len(uc.ExpectedOutcome.Invariants) == 0 {
        t.Errorf("invariants lost")
    }
    if len(uc.Evidence) != 1 || uc.Evidence[0].File == "" {
        t.Errorf("evidence lost: %+v", uc.Evidence)
    }
    out, err := json.Marshal(uc)
    if err != nil {
        t.Fatal(err)
    }
    var back UseCase
    if err := json.Unmarshal(out, &back); err != nil {
        t.Fatalf("re-unmarshal: %v", err)
    }
    if back.ID != uc.ID {
        t.Errorf("round-trip id mismatch")
    }
}

func TestUseCase_OptionalFieldsOmitted(t *testing.T) {
    uc := UseCase{ID: "x", Version: "0.1.0", JourneyID: "j"}
    body, err := json.Marshal(uc)
    if err != nil {
        t.Fatal(err)
    }
    s := string(body)
    for _, k := range []string{`"regression_priority"`, `"blind_spots"`, `"tags"`, `"references"`} {
        if strings.Contains(s, k) {
            t.Errorf("expected %s omitted, got %s", k, s)
        }
    }
}

func readTestdata(t *testing.T, name string) []byte {
    t.Helper()
    _, this, _, _ := runtime.Caller(0)
    p := filepath.Clean(filepath.Join(filepath.Dir(this), "testdata", name))
    b, err := os.ReadFile(p)
    if err != nil {
        t.Fatal(err)
    }
    return b
}
```

- [ ] **Step 3: Run the test (must fail — types don't exist yet)**

Run: `go test ./lib/usecase/...`
Expected: FAIL with `package usecase is not in std (no Go files)` or `undefined: UseCase`.

- [ ] **Step 4: Implement `lib/usecase/shape.go`**

```go
// Package usecase owns the project-level UseCase artifact: a descriptive
// snapshot of one observable journey variation (input, behavior, expected
// outcome) used by /create-sensor to synthesize a regression sensor.
package usecase

import "github.com/iurykrieger/harness-framework/lib/stack"

// UseCase is the typed view of a usecase.json file.
type UseCase struct {
    ID                 string          `json:"id"`
    Version            string          `json:"version"`
    Name               string          `json:"name,omitempty"`
    Description        string          `json:"description,omitempty"`
    JourneyID          string          `json:"journey_id"`
    Trigger            Trigger         `json:"trigger,omitempty"`
    Behavior           Behavior        `json:"behavior,omitempty"`
    ExpectedOutcome    ExpectedOutcome `json:"expected_outcome,omitempty"`
    Evidence           []stack.Evidence `json:"evidence,omitempty"`
    RegressionPriority RegressionPriority `json:"regression_priority,omitempty"`
    BlindSpots         []string        `json:"blind_spots,omitempty"`
    Tags               []string        `json:"tags,omitempty"`
    References         []string        `json:"references,omitempty"`
}

type Trigger struct {
    Summary       string   `json:"summary,omitempty"`
    Shape         string   `json:"shape,omitempty"`
    Fixture       any      `json:"fixture,omitempty"`
    Preconditions []string `json:"preconditions,omitempty"`
}

type Behavior struct {
    Summary       string   `json:"summary,omitempty"`
    BusinessRules []string `json:"business_rules,omitempty"`
}

type ExpectedOutcome struct {
    Summary     string   `json:"summary,omitempty"`
    Shape       string   `json:"shape,omitempty"`
    Fixture     any      `json:"fixture,omitempty"`
    Invariants  []string `json:"invariants,omitempty"`
    SideEffects []string `json:"side_effects,omitempty"`
}

type RegressionPriority string

const (
    PriorityCritical RegressionPriority = "critical"
    PriorityHigh     RegressionPriority = "high"
    PriorityMedium   RegressionPriority = "medium"
    PriorityLow      RegressionPriority = "low"
)
```

(The schema requires `name`, `description`, `trigger`, `behavior`, `expected_outcome`, and `evidence` — but on the Go side they have `omitempty` so partial structs round-trip cleanly through `Marshal`. Schema validation enforces presence; the struct does not.)

- [ ] **Step 5: Run tests to confirm pass**

Run: `go test ./lib/usecase/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add lib/usecase/shape.go lib/usecase/shape_test.go lib/usecase/testdata/canonical-usecase.json
git commit -m "$(cat <<'EOF'
feat(usecase): introduce typed UseCase struct + canonical fixture

Defines the Go types backing schemas/usecase.json — UseCase, Trigger,
Behavior, ExpectedOutcome, RegressionPriority — and ships the canonical
testdata fixture (create-user-with-email) used by every other test in
the lib/usecase package.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Add `lib/usecase/load.go`

**Files:**
- Create: `lib/usecase/load.go`
- Create: `lib/usecase/load_test.go`
- Create: `lib/usecase/testdata/invalid-missing-journey-id.json`
- Create: `lib/usecase/testdata/invalid-empty-evidence.json`
- Create: `lib/usecase/testdata/invalid-bad-id-pattern.json`
- Create: `lib/usecase/testdata/invalid-bad-version-format.json`
- Create: `lib/usecase/testdata/invalid-missing-trigger-fixture.json`

- [ ] **Step 1: Create the invalid fixtures**

`lib/usecase/testdata/invalid-missing-journey-id.json`:

```json
{
  "id": "x",
  "version": "0.1.0",
  "name": "x",
  "description": "x",
  "trigger":          { "summary": "x", "shape": "x", "fixture": {} },
  "behavior":         { "summary": "x" },
  "expected_outcome": { "summary": "x", "shape": "x", "fixture": {} },
  "evidence": [{ "file": "x.go", "rationale": "x" }]
}
```

`lib/usecase/testdata/invalid-empty-evidence.json`:

```json
{
  "id": "x",
  "version": "0.1.0",
  "name": "x",
  "description": "x",
  "journey_id": "y",
  "trigger":          { "summary": "x", "shape": "x", "fixture": {} },
  "behavior":         { "summary": "x" },
  "expected_outcome": { "summary": "x", "shape": "x", "fixture": {} },
  "evidence": []
}
```

`lib/usecase/testdata/invalid-bad-id-pattern.json`:

```json
{
  "id": "Bad-ID",
  "version": "0.1.0",
  "name": "x",
  "description": "x",
  "journey_id": "y",
  "trigger":          { "summary": "x", "shape": "x", "fixture": {} },
  "behavior":         { "summary": "x" },
  "expected_outcome": { "summary": "x", "shape": "x", "fixture": {} },
  "evidence": [{ "file": "x.go", "rationale": "x" }]
}
```

`lib/usecase/testdata/invalid-bad-version-format.json`:

```json
{
  "id": "x",
  "version": "v1",
  "name": "x",
  "description": "x",
  "journey_id": "y",
  "trigger":          { "summary": "x", "shape": "x", "fixture": {} },
  "behavior":         { "summary": "x" },
  "expected_outcome": { "summary": "x", "shape": "x", "fixture": {} },
  "evidence": [{ "file": "x.go", "rationale": "x" }]
}
```

`lib/usecase/testdata/invalid-missing-trigger-fixture.json`:

```json
{
  "id": "x",
  "version": "0.1.0",
  "name": "x",
  "description": "x",
  "journey_id": "y",
  "trigger":          { "summary": "x", "shape": "x" },
  "behavior":         { "summary": "x" },
  "expected_outcome": { "summary": "x", "shape": "x", "fixture": {} },
  "evidence": [{ "file": "x.go", "rationale": "x" }]
}
```

- [ ] **Step 2: Write the failing test**

Create `lib/usecase/load_test.go`:

```go
package usecase

import (
    "bytes"
    "path/filepath"
    "testing"
)

func TestLoadUseCaseFile(t *testing.T) {
    cases := []struct {
        name       string
        fixture    string
        wantCode   int
        wantSubstr string
    }{
        {name: "canonical", fixture: "canonical-usecase.json", wantCode: 0},
        {name: "missing journey_id", fixture: "invalid-missing-journey-id.json", wantCode: 1, wantSubstr: "journey_id"},
        {name: "empty evidence", fixture: "invalid-empty-evidence.json", wantCode: 1, wantSubstr: "evidence"},
        {name: "bad id pattern", fixture: "invalid-bad-id-pattern.json", wantCode: 1, wantSubstr: "id"},
        {name: "bad version", fixture: "invalid-bad-version-format.json", wantCode: 1, wantSubstr: "version"},
        {name: "missing trigger fixture", fixture: "invalid-missing-trigger-fixture.json", wantCode: 1, wantSubstr: "fixture"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            var stderr bytes.Buffer
            _, _, code := LoadUseCaseFile(filepath.Join("testdata", tc.fixture), "", &stderr)
            if code != tc.wantCode {
                t.Fatalf("code=%d want=%d stderr=%s", code, tc.wantCode, stderr.String())
            }
            if tc.wantSubstr != "" && !bytes.Contains(stderr.Bytes(), []byte(tc.wantSubstr)) {
                t.Errorf("stderr %q missing %q", stderr.String(), tc.wantSubstr)
            }
        })
    }
}
```

- [ ] **Step 3: Run the test (must fail — function does not exist)**

Run: `go test ./lib/usecase/...`
Expected: FAIL with `undefined: LoadUseCaseFile`.

- [ ] **Step 4: Implement `lib/usecase/load.go`**

```go
package usecase

import (
    "encoding/json"
    "fmt"
    "io"
    "os"
    "path/filepath"

    "github.com/iurykrieger/harness-framework/lib/schema"
)

// LoadUseCaseFile reads, parses, and schema-validates a usecase JSON file
// at path. Returns the decoded map, the resolved absolute path, and an
// exit code: 0 success, 1 schema validation failure, 2 I/O or parse failure.
func LoadUseCaseFile(path, schemasDir string, stderr io.Writer) (map[string]interface{}, string, int) {
    if path == "" {
        fmt.Fprintln(stderr, "error: empty path")
        return nil, "", 2
    }
    abs, err := filepath.Abs(path)
    if err != nil {
        fmt.Fprintln(stderr, "error: resolve:", err)
        return nil, "", 2
    }
    v, code := schema.LoadValidator(schemasDir, stderr)
    if code != 0 {
        return nil, "", code
    }
    body, err := os.ReadFile(abs)
    if err != nil {
        fmt.Fprintln(stderr, "error: read:", err)
        return nil, "", 2
    }
    var u map[string]interface{}
    if err := json.Unmarshal(body, &u); err != nil {
        fmt.Fprintln(stderr, "error: parse:", err)
        return nil, "", 2
    }
    if err := v.Validate(schema.TargetUseCase, u); err != nil {
        schema.PrintValidationOrPlain(err, stderr)
        return nil, "", 1
    }
    return u, abs, 0
}
```

- [ ] **Step 5: Run tests to confirm pass**

Run: `go test ./lib/usecase/...`
Expected: PASS. If the `wantSubstr` for a case doesn't appear in stderr, refine the substring (the underlying validator phrasing may differ — check the actual stderr output in the failure message and adjust the substring to match a stable token).

- [ ] **Step 6: Commit**

```bash
git add lib/usecase/load.go lib/usecase/load_test.go lib/usecase/testdata/invalid-*.json
git commit -m "$(cat <<'EOF'
feat(usecase): LoadUseCaseFile mirrors the stack loader pattern

Provides the read+validate primitive that downstream callers (write-usecase
script, /detect-usecases skill, future /create-sensor) rely on. Five
invalid fixtures cover the most common authoring mistakes.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Add `lib/usecase/evidence.go` (file-existence check)

**Files:**
- Create: `lib/usecase/evidence.go`
- Create: `lib/usecase/evidence_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/usecase/evidence_test.go`:

```go
package usecase

import (
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/stack"
)

func TestCheckEvidenceFiles_OK(t *testing.T) {
    root := t.TempDir()
    if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("//"), 0o644); err != nil {
        t.Fatal(err)
    }
    uc := &UseCase{Evidence: []stack.Evidence{{File: "a.go", Rationale: "x"}}}
    if err := CheckEvidenceFiles(uc, root); err != nil {
        t.Fatalf("unexpected: %v", err)
    }
}

func TestCheckEvidenceFiles_Missing(t *testing.T) {
    root := t.TempDir()
    uc := &UseCase{Evidence: []stack.Evidence{
        {File: "a.go", Rationale: "x"},
        {File: "b.go", Rationale: "x"},
    }}
    err := CheckEvidenceFiles(uc, root)
    if err == nil {
        t.Fatal("expected error")
    }
    if !strings.Contains(err.Error(), "a.go") || !strings.Contains(err.Error(), "b.go") {
        t.Errorf("error should list both missing files; got %q", err)
    }
}

func TestCheckEvidenceFiles_Empty(t *testing.T) {
    uc := &UseCase{}
    if err := CheckEvidenceFiles(uc, "/no/such"); err != nil {
        t.Errorf("empty evidence should be OK at this layer (schema validates minItems)")
    }
}
```

- [ ] **Step 2: Run (must fail — function undefined)**

Run: `go test ./lib/usecase/...`
Expected: FAIL with `undefined: CheckEvidenceFiles`.

- [ ] **Step 3: Implement `lib/usecase/evidence.go`**

```go
package usecase

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"
)

// CheckEvidenceFiles verifies every UseCase.Evidence[].File exists on
// disk relative to projectRoot. Returns a single error listing every
// missing file when any are absent.
func CheckEvidenceFiles(uc *UseCase, projectRoot string) error {
    var missing []string
    for _, ev := range uc.Evidence {
        full := filepath.Join(projectRoot, ev.File)
        if _, err := os.Stat(full); err != nil {
            missing = append(missing, ev.File)
        }
    }
    if len(missing) == 0 {
        return nil
    }
    return fmt.Errorf("evidence files not found under %s: %s", projectRoot, strings.Join(missing, ", "))
}
```

- [ ] **Step 4: Run to confirm pass**

Run: `go test ./lib/usecase/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/usecase/evidence.go lib/usecase/evidence_test.go
git commit -m "$(cat <<'EOF'
feat(usecase): CheckEvidenceFiles validates on-disk pointers

Catches stale or typo'd evidence.file paths at persist time rather than
letting them propagate to /create-sensor where they'd surface as
"file not found" mid-synthesis.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Add `lib/usecase/cross_check.go` (journey_id ↔ stack)

**Files:**
- Create: `lib/usecase/cross_check.go`
- Create: `lib/usecase/cross_check_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/usecase/cross_check_test.go`:

```go
package usecase

import (
    "strings"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/stack"
)

func TestCheckJourneyReference_OK(t *testing.T) {
    s := &stack.Stack{Journeys: []stack.Journey{{ID: "user-registration"}}}
    uc := &UseCase{JourneyID: "user-registration"}
    if err := CheckJourneyReference(uc, s); err != nil {
        t.Fatalf("unexpected: %v", err)
    }
}

func TestCheckJourneyReference_Missing(t *testing.T) {
    s := &stack.Stack{Journeys: []stack.Journey{{ID: "user-registration"}}}
    uc := &UseCase{JourneyID: "ghost"}
    err := CheckJourneyReference(uc, s)
    if err == nil {
        t.Fatal("expected error")
    }
    if !strings.Contains(err.Error(), "ghost") {
        t.Errorf("error must name the bad id; got %q", err)
    }
}

func TestCheckJourneyReference_EmptyStackJourneys(t *testing.T) {
    s := &stack.Stack{}
    uc := &UseCase{JourneyID: "anything"}
    err := CheckJourneyReference(uc, s)
    if err == nil {
        t.Fatal("expected error: stack has no journeys, so any journey_id is unresolved")
    }
}
```

- [ ] **Step 2: Run (must fail — function undefined)**

Run: `go test ./lib/usecase/...`
Expected: FAIL with `undefined: CheckJourneyReference`.

- [ ] **Step 3: Implement `lib/usecase/cross_check.go`**

```go
package usecase

import (
    "fmt"

    "github.com/iurykrieger/harness-framework/lib/stack"
)

// CheckJourneyReference verifies UseCase.JourneyID matches some
// stack.Journeys[].ID. Strict by design: an empty stack.Journeys with
// any non-empty journey_id is rejected (stale UseCase pointing at a
// deleted journey).
func CheckJourneyReference(uc *UseCase, s *stack.Stack) error {
    for _, j := range s.Journeys {
        if j.ID == uc.JourneyID {
            return nil
        }
    }
    return fmt.Errorf("usecase %q references journey_id %q absent from stack.journeys[]", uc.ID, uc.JourneyID)
}
```

- [ ] **Step 4: Run to confirm pass**

Run: `go test ./lib/usecase/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/usecase/cross_check.go lib/usecase/cross_check_test.go
git commit -m "$(cat <<'EOF'
feat(usecase): CheckJourneyReference enforces stack-side referential integrity

A UseCase pointing at a deleted or never-declared journey is rejected at
persist time so /create-sensor never has to reason about dangling refs.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Add `lib/usecase/persist.go` and `usecasetest` helper

**Files:**
- Create: `lib/usecase/persist.go`
- Create: `lib/usecase/persist_test.go`
- Create: `lib/usecase/usecasetest/canonical.go`

- [ ] **Step 1: Write the `usecasetest` helper**

Create `lib/usecase/usecasetest/canonical.go`:

```go
// Package usecasetest exposes test helpers that load canonical UseCase
// fixtures from lib/usecase/testdata/. Production code MUST NOT import it.
package usecasetest

import (
    "encoding/json"
    "os"
    "path/filepath"
    "runtime"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/usecase"
)

// LoadCanonical returns the canonical UseCase fixture.
func LoadCanonical(t *testing.T) *usecase.UseCase {
    t.Helper()
    _, this, _, _ := runtime.Caller(0)
    p := filepath.Clean(filepath.Join(filepath.Dir(this), "..", "testdata", "canonical-usecase.json"))
    body, err := os.ReadFile(p)
    if err != nil {
        t.Fatalf("read %s: %v", p, err)
    }
    var uc usecase.UseCase
    if err := json.Unmarshal(body, &uc); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    return &uc
}

// CanonicalBody returns the raw JSON bytes of the canonical fixture.
func CanonicalBody(t *testing.T) []byte {
    t.Helper()
    _, this, _, _ := runtime.Caller(0)
    p := filepath.Clean(filepath.Join(filepath.Dir(this), "..", "testdata", "canonical-usecase.json"))
    body, err := os.ReadFile(p)
    if err != nil {
        t.Fatalf("read %s: %v", p, err)
    }
    return body
}
```

- [ ] **Step 2: Write the failing persist test**

Create `lib/usecase/persist_test.go`:

```go
package usecase_test

import (
    "encoding/json"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/schema/schematest"
    "github.com/iurykrieger/harness-framework/lib/stack"
    "github.com/iurykrieger/harness-framework/lib/usecase"
    "github.com/iurykrieger/harness-framework/lib/usecase/usecasetest"
)

// minimalStack returns a stack with the journey the canonical UseCase
// references, so cross-check passes.
func minimalStack() *stack.Stack {
    return &stack.Stack{
        Archetypes: []stack.Archetype{stack.ArchetypeHTTPAPI},
        Journeys: []stack.Journey{
            {ID: "user-registration", Archetype: stack.ArchetypeHTTPAPI},
        },
    }
}

// projectRootWithEvidence creates a temp dir, writes the file the
// canonical UseCase points to, and returns the dir.
func projectRootWithEvidence(t *testing.T) string {
    t.Helper()
    root := t.TempDir()
    target := filepath.Join(root, "src", "users", "users.controller.ts")
    if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(target, []byte("//"), 0o644); err != nil {
        t.Fatal(err)
    }
    return root
}

func TestValidateAndPersist_Happy(t *testing.T) {
    schemasDir := schematest.RepoSchemasDir(t)
    outDir := t.TempDir()
    projectRoot := projectRootWithEvidence(t)
    body := usecasetest.CanonicalBody(t)

    path, err := usecase.ValidateAndPersist(body, outDir, projectRoot, minimalStack(), schemasDir)
    if err != nil {
        t.Fatalf("unexpected: %v", err)
    }
    if !strings.HasSuffix(path, "create-user-with-email.json") {
        t.Errorf("path = %q, want suffix create-user-with-email.json", path)
    }
    if _, err := os.Stat(path); err != nil {
        t.Errorf("file not written: %v", err)
    }
}

func TestValidateAndPersist_RejectsBadJourney(t *testing.T) {
    schemasDir := schematest.RepoSchemasDir(t)
    outDir := t.TempDir()
    projectRoot := projectRootWithEvidence(t)
    body := usecasetest.CanonicalBody(t)

    // Strip the matching journey so cross-check fails.
    bad := &stack.Stack{Archetypes: []stack.Archetype{stack.ArchetypeHTTPAPI}}
    if _, err := usecase.ValidateAndPersist(body, outDir, projectRoot, bad, schemasDir); err == nil {
        t.Fatal("expected journey cross-check error")
    }
    files, _ := os.ReadDir(outDir)
    if len(files) != 0 {
        t.Errorf("expected nothing written on validation failure, got %d files", len(files))
    }
}

func TestValidateAndPersist_RejectsMissingEvidence(t *testing.T) {
    schemasDir := schematest.RepoSchemasDir(t)
    outDir := t.TempDir()
    body := usecasetest.CanonicalBody(t)

    // projectRoot WITHOUT the evidence file
    if _, err := usecase.ValidateAndPersist(body, outDir, t.TempDir(), minimalStack(), schemasDir); err == nil {
        t.Fatal("expected evidence cross-check error")
    }
}

func TestValidateAndPersist_RejectsSchemaViolation(t *testing.T) {
    schemasDir := schematest.RepoSchemasDir(t)
    outDir := t.TempDir()
    projectRoot := projectRootWithEvidence(t)

    var doc map[string]interface{}
    if err := json.Unmarshal(usecasetest.CanonicalBody(t), &doc); err != nil {
        t.Fatal(err)
    }
    delete(doc, "journey_id")
    body, _ := json.Marshal(doc)

    if _, err := usecase.ValidateAndPersist(body, outDir, projectRoot, minimalStack(), schemasDir); err == nil {
        t.Fatal("expected schema validation error")
    }
}

func TestValidateAndPersist_OverwritesAtomically(t *testing.T) {
    schemasDir := schematest.RepoSchemasDir(t)
    outDir := t.TempDir()
    projectRoot := projectRootWithEvidence(t)

    target := filepath.Join(outDir, "create-user-with-email.json")
    if err := os.WriteFile(target, []byte("STALE"), 0o644); err != nil {
        t.Fatal(err)
    }
    body := usecasetest.CanonicalBody(t)

    if _, err := usecase.ValidateAndPersist(body, outDir, projectRoot, minimalStack(), schemasDir); err != nil {
        t.Fatal(err)
    }
    data, err := os.ReadFile(target)
    if err != nil {
        t.Fatal(err)
    }
    if strings.Contains(string(data), "STALE") {
        t.Errorf("expected target to be overwritten")
    }
}
```

(Test file uses `package usecase_test` so it can import `usecasetest` without circular deps.)

- [ ] **Step 3: Run (must fail — function undefined)**

Run: `go test ./lib/usecase/...`
Expected: FAIL with `undefined: usecase.ValidateAndPersist`.

- [ ] **Step 4: Implement `lib/usecase/persist.go`**

```go
package usecase

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"

    "github.com/iurykrieger/harness-framework/lib/schema"
    "github.com/iurykrieger/harness-framework/lib/stack"
)

// ValidateAndPersist validates draftJSON against schemas/usecase.json,
// cross-checks the journey_id reference against stk, verifies every
// evidence file exists under projectRoot, then writes a canonicalised
// copy (2-space indent) to <outDir>/<id>.json. Returns the absolute path.
//
// Idempotent: re-persisting the same body produces a byte-identical file.
// Does NOT mutate draftJSON.
func ValidateAndPersist(
    draftJSON []byte,
    outDir string,
    projectRoot string,
    stk *stack.Stack,
    schemasDir string,
) (string, error) {
    var doc map[string]interface{}
    if err := json.Unmarshal(draftJSON, &doc); err != nil {
        return "", fmt.Errorf("parse usecase JSON: %w", err)
    }

    dir := schemasDir
    if dir == "" {
        cwd, _ := os.Getwd()
        found, ferr := schema.FindSchemasDir(cwd)
        if ferr != nil {
            return "", fmt.Errorf("locate schemas: %w", ferr)
        }
        dir = found
    }
    v, err := schema.NewValidator(dir)
    if err != nil {
        return "", fmt.Errorf("load schemas: %w", err)
    }
    if err := v.Validate(schema.TargetUseCase, doc); err != nil {
        return "", err
    }

    // Decode the typed view for cross-checks. The map carries the
    // canonical bytes for write; the struct is just for validation.
    var uc UseCase
    body, _ := json.Marshal(doc)
    if err := json.Unmarshal(body, &uc); err != nil {
        return "", fmt.Errorf("decode after schema validation: %w", err)
    }
    if err := CheckJourneyReference(&uc, stk); err != nil {
        return "", err
    }
    if err := CheckEvidenceFiles(&uc, projectRoot); err != nil {
        return "", err
    }

    id, ok := doc["id"].(string)
    if !ok || id == "" {
        return "", fmt.Errorf("usecase.id missing after validation")
    }
    if err := os.MkdirAll(outDir, 0o755); err != nil {
        return "", fmt.Errorf("mkdir: %w", err)
    }
    target := filepath.Join(outDir, id+".json")
    if err := writeCanonical(target, doc); err != nil {
        return "", fmt.Errorf("write: %w", err)
    }
    abs, err := filepath.Abs(target)
    if err != nil {
        return target, nil
    }
    return abs, nil
}

func writeCanonical(path string, doc map[string]interface{}) error {
    tmp, err := os.CreateTemp(filepath.Dir(path), ".persist-*")
    if err != nil {
        return err
    }
    tmpPath := tmp.Name()
    enc := json.NewEncoder(tmp)
    enc.SetIndent("", "  ")
    if err := enc.Encode(doc); err != nil {
        tmp.Close()
        os.Remove(tmpPath)
        return err
    }
    if err := tmp.Close(); err != nil {
        os.Remove(tmpPath)
        return err
    }
    if err := os.Chmod(tmpPath, 0o644); err != nil {
        os.Remove(tmpPath)
        return err
    }
    return os.Rename(tmpPath, path)
}
```

- [ ] **Step 5: Run tests to confirm pass**

Run: `go test ./lib/usecase/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add lib/usecase/persist.go lib/usecase/persist_test.go lib/usecase/usecasetest/canonical.go
git commit -m "$(cat <<'EOF'
feat(usecase): ValidateAndPersist with schema + journey + evidence checks

Single entry point combining JSON Schema validation, journey_id reference
check against stack.Stack, evidence-file existence under projectRoot, and
canonical atomic write to <outDir>/<id>.json. Tests cover happy, schema
violation, bad journey, missing evidence file, and overwrite.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Add `write-usecase.go` CLI script

**Files:**
- Create: `skills/detect-usecases/scripts/write-usecase.go`
- Create: `skills/detect-usecases/scripts/write-usecase_test.go`

- [ ] **Step 1: Write the failing CLI test**

Create `skills/detect-usecases/scripts/write-usecase_test.go`:

```go
//go:build write_usecase

package main

import (
    "bytes"
    "encoding/json"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/schema/schematest"
    "github.com/iurykrieger/harness-framework/lib/usecase/usecasetest"
)

func writeDraftAt(t *testing.T, dir string, doc []byte) string {
    t.Helper()
    p := filepath.Join(dir, "draft.json")
    if err := os.WriteFile(p, doc, 0o644); err != nil {
        t.Fatal(err)
    }
    return p
}

func writeStackJSON(t *testing.T, projectRoot string) {
    t.Helper()
    harness := filepath.Join(projectRoot, ".harness")
    if err := os.MkdirAll(harness, 0o755); err != nil {
        t.Fatal(err)
    }
    body := []byte(`{
  "version": "0.2.0",
  "detected_at": "2026-05-14T10:00:00Z",
  "detected_by": "manual",
  "languages": [{"name":"go"}],
  "components": [{"role":"http-server","name":"net/http","evidence":[{"file":"x.go","rationale":"x"}]}],
  "log_shapes": [{"id":"x","produced_by":["net/http"],"format":"plain","sample":"x"}],
  "archetypes": ["http-api"],
  "journeys": [{
    "id": "user-registration",
    "name": "x",
    "summary": "x",
    "archetype": "http-api",
    "entry_points": [{
      "kind": "http-route",
      "method": "POST",
      "path": "/users",
      "evidence": {"file":"x.go","rationale":"x"}
    }]
  }]
}`)
    if err := os.WriteFile(filepath.Join(harness, "stack.json"), body, 0o644); err != nil {
        t.Fatal(err)
    }
}

func writeEvidenceFile(t *testing.T, projectRoot string) {
    t.Helper()
    target := filepath.Join(projectRoot, "src", "users", "users.controller.ts")
    if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(target, []byte("//"), 0o644); err != nil {
        t.Fatal(err)
    }
}

func TestRun_Happy(t *testing.T) {
    projectRoot := t.TempDir()
    writeStackJSON(t, projectRoot)
    writeEvidenceFile(t, projectRoot)
    schemasDir := schematest.RepoSchemasDir(t)
    out := filepath.Join(projectRoot, ".harness", "usecases")
    draft := writeDraftAt(t, t.TempDir(), usecasetest.CanonicalBody(t))

    var stdout, stderr bytes.Buffer
    code := run([]string{
        "--out", out,
        "--project-root", projectRoot,
        "--schemas-dir", schemasDir,
        draft,
    }, &stdout, &stderr)
    if code != 0 {
        t.Fatalf("code=%d stderr=%s", code, stderr.String())
    }
    expected := filepath.Join(out, "create-user-with-email.json")
    if !strings.Contains(stdout.String(), "create-user-with-email.json") {
        t.Fatalf("stdout %q missing expected filename", stdout.String())
    }
    if _, err := os.Stat(expected); err != nil {
        t.Fatalf("file not written: %v", err)
    }
}

func TestRun_StackMissing(t *testing.T) {
    projectRoot := t.TempDir() // NO .harness/stack.json
    schemasDir := schematest.RepoSchemasDir(t)
    out := filepath.Join(projectRoot, ".harness", "usecases")
    draft := writeDraftAt(t, t.TempDir(), usecasetest.CanonicalBody(t))

    var stdout, stderr bytes.Buffer
    code := run([]string{
        "--out", out,
        "--project-root", projectRoot,
        "--schemas-dir", schemasDir,
        draft,
    }, &stdout, &stderr)
    if code != 2 {
        t.Fatalf("code=%d want 2 (setup error)", code)
    }
    if !strings.Contains(stderr.String(), "stack") {
        t.Errorf("stderr %q should mention stack", stderr.String())
    }
}

func TestRun_SchemaViolation(t *testing.T) {
    projectRoot := t.TempDir()
    writeStackJSON(t, projectRoot)
    writeEvidenceFile(t, projectRoot)
    schemasDir := schematest.RepoSchemasDir(t)
    out := filepath.Join(projectRoot, ".harness", "usecases")

    var doc map[string]interface{}
    if err := json.Unmarshal(usecasetest.CanonicalBody(t), &doc); err != nil {
        t.Fatal(err)
    }
    delete(doc, "journey_id")
    bad, _ := json.Marshal(doc)
    draft := writeDraftAt(t, t.TempDir(), bad)

    var stdout, stderr bytes.Buffer
    code := run([]string{
        "--out", out,
        "--project-root", projectRoot,
        "--schemas-dir", schemasDir,
        draft,
    }, &stdout, &stderr)
    if code != 1 {
        t.Fatalf("code=%d want 1 (schema fail)", code)
    }
}

func TestRun_JourneyOrphan(t *testing.T) {
    projectRoot := t.TempDir()
    writeStackJSON(t, projectRoot) // declares journey "user-registration"
    writeEvidenceFile(t, projectRoot)
    schemasDir := schematest.RepoSchemasDir(t)
    out := filepath.Join(projectRoot, ".harness", "usecases")

    var doc map[string]interface{}
    json.Unmarshal(usecasetest.CanonicalBody(t), &doc)
    doc["journey_id"] = "ghost"
    bad, _ := json.Marshal(doc)
    draft := writeDraftAt(t, t.TempDir(), bad)

    var stdout, stderr bytes.Buffer
    code := run([]string{
        "--out", out,
        "--project-root", projectRoot,
        "--schemas-dir", schemasDir,
        draft,
    }, &stdout, &stderr)
    if code != 1 {
        t.Fatalf("code=%d want 1 (cross-check fail)", code)
    }
    if !strings.Contains(stderr.String(), "ghost") {
        t.Errorf("stderr %q must name the bad journey", stderr.String())
    }
}

func TestRun_EvidenceMissing(t *testing.T) {
    projectRoot := t.TempDir()
    writeStackJSON(t, projectRoot)
    // do NOT writeEvidenceFile
    schemasDir := schematest.RepoSchemasDir(t)
    out := filepath.Join(projectRoot, ".harness", "usecases")
    draft := writeDraftAt(t, t.TempDir(), usecasetest.CanonicalBody(t))

    var stdout, stderr bytes.Buffer
    code := run([]string{
        "--out", out,
        "--project-root", projectRoot,
        "--schemas-dir", schemasDir,
        draft,
    }, &stdout, &stderr)
    if code != 1 {
        t.Fatalf("code=%d want 1 (evidence missing)", code)
    }
    if !strings.Contains(stderr.String(), "users.controller.ts") {
        t.Errorf("stderr should name the missing evidence file; got %q", stderr.String())
    }
}

func TestRun_MissingOut(t *testing.T) {
    var stdout, stderr bytes.Buffer
    if code := run([]string{"draft.json"}, &stdout, &stderr); code != 2 {
        t.Fatalf("code=%d want 2", code)
    }
}

func TestRun_MissingProjectRoot(t *testing.T) {
    var stdout, stderr bytes.Buffer
    if code := run([]string{"--out", t.TempDir(), "draft.json"}, &stdout, &stderr); code != 2 {
        t.Fatalf("code=%d want 2", code)
    }
}

func TestRun_NoPositional(t *testing.T) {
    var stdout, stderr bytes.Buffer
    code := run([]string{"--out", t.TempDir(), "--project-root", t.TempDir()}, &stdout, &stderr)
    if code != 2 {
        t.Fatalf("code=%d want 2", code)
    }
}
```

- [ ] **Step 2: Run (must fail — file doesn't exist yet)**

Run: `go test -tags=write_usecase ./skills/detect-usecases/scripts/...`
Expected: FAIL with `no Go files` or `undefined: run`.

- [ ] **Step 3: Implement `skills/detect-usecases/scripts/write-usecase.go`**

```go
//go:build write_usecase

// Command write-usecase reads a draft UseCase JSON, validates it via
// lib/usecase.ValidateAndPersist (schema + journey_id cross-check +
// evidence file existence), and writes <out>/<id>.json.
//
// Usage:
//
//	go run -tags=write_usecase ./skills/detect-usecases/scripts \
//	  --out=<dir> --project-root=<dir> [--schemas-dir=<dir>] <draft.json>
//
// Exit codes: 0 written, 1 validation failed (schema or cross-check),
// 2 usage or I/O error.
package main

import (
    "encoding/json"
    "errors"
    "flag"
    "fmt"
    "io"
    "os"
    "path/filepath"

    "github.com/santhosh-tekuri/jsonschema/v5"

    "github.com/iurykrieger/harness-framework/lib/schema"
    "github.com/iurykrieger/harness-framework/lib/stack"
    "github.com/iurykrieger/harness-framework/lib/usecase"
)

func main() {
    os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
    fs := flag.NewFlagSet("write-usecase", flag.ContinueOnError)
    fs.SetOutput(stderr)
    var outDir, projectRoot, schemasDir string
    fs.StringVar(&outDir, "out", "", "directory to write the usecase file into (required)")
    fs.StringVar(&projectRoot, "project-root", "", "project root (required, holds .harness/stack.json)")
    fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
    if err := fs.Parse(args); err != nil {
        return 2
    }
    if outDir == "" {
        fmt.Fprintln(stderr, "error: --out is required")
        return 2
    }
    if projectRoot == "" {
        fmt.Fprintln(stderr, "error: --project-root is required")
        return 2
    }
    if fs.NArg() != 1 {
        fmt.Fprintln(stderr, "usage: write-usecase --out=DIR --project-root=DIR [--schemas-dir=DIR] <draft.json>")
        return 2
    }
    draftPath := fs.Arg(0)

    body, err := os.ReadFile(draftPath)
    if err != nil {
        fmt.Fprintln(stderr, "error: read draft:", err)
        return 2
    }

    stk, code := loadStack(projectRoot, schemasDir, stderr)
    if code != 0 {
        return code
    }

    path, err := usecase.ValidateAndPersist(body, outDir, projectRoot, stk, schemasDir)
    if err != nil {
        var ve *jsonschema.ValidationError
        if errors.As(err, &ve) {
            schema.PrintValidationOrPlain(err, stderr)
            return 1
        }
        // Cross-check errors are plain errors — also exit 1.
        fmt.Fprintln(stderr, "error:", err)
        return 1
    }
    fmt.Fprintln(stdout, path)
    return 0
}

func loadStack(projectRoot, schemasDir string, stderr io.Writer) (*stack.Stack, int) {
    stackPath := filepath.Join(projectRoot, ".harness", "stack.json")
    if _, err := os.Stat(stackPath); err != nil {
        fmt.Fprintf(stderr, "error: stack_missing: %s — run /detect-sensors first\n", stackPath)
        return nil, 2
    }
    body, err := os.ReadFile(stackPath)
    if err != nil {
        fmt.Fprintln(stderr, "error: read stack:", err)
        return nil, 2
    }
    var s stack.Stack
    if err := json.Unmarshal(body, &s); err != nil {
        fmt.Fprintln(stderr, "error: parse stack:", err)
        return nil, 2
    }
    return &s, 0
}
```

- [ ] **Step 4: Run tests to confirm pass**

Run: `go test -tags=write_usecase ./skills/detect-usecases/scripts/...`
Expected: PASS for all eight cases.

- [ ] **Step 5: Smoke-run the binary against the canonical fixture**

```bash
# from the worktree root
PROJ=$(mktemp -d)
mkdir -p "$PROJ/.harness" "$PROJ/src/users"
cp lib/stack/testdata/golden-stack-with-journeys.json "$PROJ/.harness/stack.json"
touch "$PROJ/src/users/users.controller.ts"

HARNESS_REGISTRY_ROOT="$PROJ" GOWORK=off CLAUDE_PLUGIN_ROOT="$(pwd)" \
  go run -tags=write_usecase ./skills/detect-usecases/scripts \
  --out="$PROJ/.harness/usecases" \
  --project-root="$PROJ" \
  --schemas-dir="$(pwd)/schemas" \
  lib/usecase/testdata/canonical-usecase.json
```

Expected stdout: an absolute path ending in `/.harness/usecases/create-user-with-email.json`. The file at that path should be canonical JSON.

If the smoke run fails because `golden-stack-with-journeys.json` declares only one journey (`user-registration`), that journey matches the canonical UseCase fixture — the run should succeed.

- [ ] **Step 6: Commit**

```bash
git add skills/detect-usecases/scripts/write-usecase.go skills/detect-usecases/scripts/write-usecase_test.go
git commit -m "$(cat <<'EOF'
feat(detect-usecases): write-usecase CLI script

Thin CLI wrapper around usecase.ValidateAndPersist. Reads the project's
.harness/stack.json to thread the typed Stack into the cross-check;
emits stack_missing remediation when the stack artifact is absent
(per spec — /detect-sensors must run first).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Write the `/detect-usecases` skill prose

**Files:**
- Create: `skills/detect-usecases/SKILL.md`

- [ ] **Step 1: Create `skills/detect-usecases/SKILL.md`**

```markdown
---
name: detect-usecases
description: Use when the user invokes /detect-usecases or asks to scan a project for its use cases and persist them under .harness/usecases/. Reads .harness/stack.json (errors out if absent — /detect-sensors must run first), augments it with purpose/archetypes/journeys when those fields are missing, then enumerates variations per journey from validation schemas, code branches, pre-condition states, conditionally-emitted events, and the project's tests/OpenAPI as oracle. Drafts one UseCase JSON per variation and persists each via skills/detect-usecases/scripts/write-usecase.go.
---

# detect-usecases

Scan a project, identify the journeys that compose its purpose, enumerate the variations within each journey, and persist one descriptive `UseCase` per variation as JSON under `<project>/.harness/usecases/`. Each `UseCase` carries trigger/behavior/expected_outcome as narrative prose plus a concrete fixture; a future `/create-sensor` skill reads the persisted UseCases and synthesizes deterministic regression sensors.

## Invocation

```
/detect-usecases [project-path]
```

If the user supplies no argument, scan cwd. The output directory is always `<project>/.harness/usecases/`.

Optional flags:
- `--refresh-stack` — force Phase 0.5 to regenerate `purpose`/`archetypes`/`journeys` even when already populated.
- `--journey=<id>` — limit Phase 1 to a single journey by id (for iteration).

## Procedure

### Phase 0 — Stack precheck

Read `<project>/.harness/stack.json`.

- File absent → abort with `verdict=error`, `metadata.kind=stack_missing`. Remediation: *"Run /detect-sensors first to produce .harness/stack.json"*.
- File present, no `purpose`/`archetypes`/`journeys` → continue to Phase 0.5.
- File present and all three fields populated → continue to Phase 1.

### Phase 0.5 — Stack augmentation (when needed)

Infer the three top-level fields and persist the augmented `stack.json` via the existing `stack.ValidateAndPersist` primitive.

1. **Purpose** — triangulate `languages` + `components` + top-level docs (`README.md`, `CLAUDE.md`, `AGENTS.md`). One declarative sentence.
2. **Archetypes** — derive from component roles:
   - Component `http-server` or `http-router` → `http-api` (or `http-spa`/`http-ssr` when a frontend framework is among the components).
   - Component `queue-consumer` → `queue-consumer`.
   - Component `queue-producer` → `queue-producer`.
   - `bin/` or `cmd/` and no server → `cli-tool`.
   - Library manifest with no server entrypoint → `library`.
   - `*.tf`, `Pulumi.yaml`, `Chart.yaml` → `iac`.
   - Cron declarations (`@Cron`, `cron.yaml`) → `scheduler`.
   - Hybrid apps get multiple values.
3. **Journeys** — per archetype, scan entry-point declarations:
   - `http-api` → controllers, route files (`@Controller`, `app.post()`, `router.HandleFunc`, Flask/FastAPI decorators). Group routes serving one domain concept under one journey.
   - `queue-consumer` → consumer registrations (`@KafkaListener`, `sqs.consume`, `EventBridge.handler`). One topic/queue = one journey.
   - `cli-tool` → top-level commands (`@Command`, Cobra `cmd.AddCommand`).
   - `scheduler` → each scheduled job.
   - Record each journey's `entry_points[]` with file:line evidence pointing at the registration site.

### Phase 1 — Per journey, enumerate variations

For each `journey` in `stack.journeys[]`:

1. **Read the source** pointed to by `entry_points[].evidence` — the handler, the service it delegates to, the use-case/domain layer below it.
2. **Identify variation sources**:
   - **Input validation** — schemas declared in Zod/Joi/class-validator/Pydantic/struct tags. Each rule that can fail is a variation (`missing-required-field`, `invalid-format`, `out-of-range`, `wrong-type`).
   - **Branches in handler/service** — `if (existing)`, `if (!user)`, `try/catch`, domain-error returns. Each branch is a distinct observable path.
   - **Pre-condition states** — existing vs absent records, feature flags, authorization (authenticated vs anonymous, role-gated).
   - **Conditionally-emitted events** — `if (orderTotal > 100) emit('high-value-order')`. A side-effect that only fires under specific conditions deserves its own UseCase.
   - **Existing tests** (`*.spec.ts`, `*_test.go`, `test_*.py`) and OpenAPI/Swagger files in the entry-point's neighborhood — *used as oracle for what variations the team considers important*. The UseCase does **not** reference the test or the spec file in its `evidence[]` — evidence points at the implementation, not the spec.
3. **Draft a UseCase per variation**:
   - `id`: kebab-case, `<verb>-<entity>-<discriminator>` pattern (`create-user-with-email`, `create-user-duplicate-email-conflict`, `login-with-wrong-password`).
   - `journey_id`: the `journey.id` from `stack.journeys[]`.
   - `trigger`: prose summary + free-form `shape` label (`HTTP request`, `Kafka message`, `CLI invocation`, `scheduled tick`) + concrete fixture.
   - `behavior`: prose summary + extracted business rules.
   - `expected_outcome`: prose summary + free-form `shape` + concrete fixture + `invariants[]` (verifiable rules in prose) + `side_effects[]`.
   - `evidence[]`: pointers to handler and service code that implements the variation. Minimum one entry.
   - `regression_priority`: heuristic — `critical` for happy-path nuclear journeys; `high` for error variations with side-effects; `medium` for common validation; `low` for obscure edges.
   - `tags`: stable convention — `happy-path`, `error-handling`, `validation`, `authz`, `idempotent`, `side-effects`.

### Phase 2 — Persist each draft

Write each draft to a temp file, then run the validator-and-writer:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_usecase \
  ./skills/detect-usecases/scripts \
  --out=<project>/.harness/usecases \
  --project-root=<project> \
  --schemas-dir=<plugin>/schemas \
  /tmp/<draft-name>.json
```

The script reads `<project>/.harness/stack.json`, validates the draft against `schemas/usecase.json`, cross-checks `journey_id` against `stack.journeys[].id`, verifies every `evidence[].file` exists, then writes canonical JSON to `<out>/<id>.json` atomically.

Exit codes:
- `0` — written.
- `1` — schema validation or cross-check failed; nothing written.
- `2` — usage / I/O / setup error (missing flag, draft unreadable, stack_missing, schemas not found).

### Phase 3 — Report back

Surface a grouped list per journey:

```
Generated 14 use cases at /repo/.harness/usecases/:

journey: user-registration (5 use cases)
  - create-user-with-email.json                 — critical · happy-path
  - create-user-duplicate-email-conflict.json   — high · error-handling
  - create-user-invalid-email-format.json       — medium · validation
  - create-user-missing-password.json           — medium · validation
  - create-user-with-disposable-email.json      — low · edge-case

journey: user-login (4 use cases)
  - ...

Next: /create-sensor <use-case-id> to generate a deterministic regression sensor for each.
```

## Behavior in projects with weak oracles

The skill never refuses to produce UseCases for lack of tests or docs. When the oracle is weak:

- Reduce to variations inferred from code (branches, validations, conditional returns).
- Mark `regression_priority: low` for inferred-without-fixture-confirmation variations.
- Annotate `blind_spots[]`: *"Fixture inferred from types; no test or payload example found in the repo."*

## Safety notes

- The script never executes the implementation code. It only validates JSON and writes files.
- Existing files at `<out>/<id>.json` are overwritten atomically by `os.Create` + `os.Rename`. Commit `.harness/usecases/` before re-running so diffs are reviewable.
- Drafts staged in `/tmp/` are the user's to clean up; the script does not touch them.
- Schemas are resolved by walking up from cwd; invoke from inside the harness-framework checkout (or pass `--schemas-dir=<plugin>/schemas`) so the validator sees the right contract.
```

- [ ] **Step 2: Sanity-check the frontmatter parses**

```bash
head -3 skills/detect-usecases/SKILL.md
```

Expected: three lines — `---`, the `name:` and `description:` block, then `---`. The body follows.

- [ ] **Step 3: Commit**

```bash
git add skills/detect-usecases/SKILL.md
git commit -m "$(cat <<'EOF'
feat(detect-usecases): SKILL.md procedural prose

Documents the four-phase procedure (stack precheck → augment → enumerate
variations per journey → persist) and how the LLM-driven detection
interacts with the deterministic write-usecase.go script.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: Update `CLAUDE.md` with the new schema and skill

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update the "three schemas" section**

Open `CLAUDE.md`. Find the section beginning `### The three schemas`. Change its heading to `### The four schemas` and append a fourth bullet after `stack.json` describing usecase.json:

```markdown
- `schemas/usecase.json` — the use-case contract. Describes one observable journey variation of the project (trigger as narrative + fixture, behavior, expected_outcome with invariants and side_effects, file:line evidence pointing at the implementation). Produced by `/detect-usecases`; consumed by a future `/create-sensor` skill to synthesize deterministic regression sensors. References `stack.json` indirectly via `journey_id` (validated in Go, not JSON Schema).
```

- [ ] **Step 2: Add the new skill to the Skills section**

Find the paragraph describing each `skills/<name>/SKILL.md`. After the run-sensor / detect-sensors mentions (or in alphabetical order — match existing convention), add a one-line reference:

```markdown
`skills/detect-usecases/` scans the project, augments `stack.json` with `purpose`/`archetypes`/`journeys` when missing, then drafts one descriptive UseCase per observable journey variation and persists each via `skills/detect-usecases/scripts/write-usecase.go` to `<project>/.harness/usecases/<id>.json`.
```

- [ ] **Step 3: Update Build/validate/test if a new test tag is worth listing**

Inside the `### Local verification` block, after the existing `go test -tags=…` lines, add:

```bash
go test -tags=write_usecase   ./skills/...          # the write-usecase script
```

- [ ] **Step 4: Run a full smoke**

Run:

```bash
go test ./lib/... && \
go test -tags=write_sensor ./skills/... && \
go test -tags=write_stack ./skills/... && \
go test -tags=write_usecase ./skills/... && \
go vet ./...
```

Expected: PASS across the board.

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
docs(claude): document the fourth schema (usecase) and /detect-usecases

Promotes the schema enumeration from three to four entities, adds a
paragraph to the Skills section pointing at /detect-usecases, and lists
the write_usecase test tag in Local verification.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review

**Spec coverage check** (every section of `2026-05-14-usecase-entity-design.md` must map to at least one task):

| Spec section | Task(s) |
|---|---|
| Schema `schemas/usecase.json` | Task 1 |
| Extension of `stack.json` (`purpose`, `archetypes`, `journeys`, `$defs/{Archetype,EntryPointKind,Journey,EntryPoint}`) | Task 2 |
| Schema validator awareness of usecase | Task 1 |
| `lib/stack/shape.go` extension | Task 3 |
| `lib/stack` semantic cross-checks (archetype membership) | Task 4 |
| `lib/usecase/shape.go` | Task 5 |
| `lib/usecase/load.go` | Task 6 |
| `lib/usecase/evidence.go` (file existence) | Task 7 |
| `lib/usecase/cross_check.go` (journey_id ↔ stack) | Task 8 |
| `lib/usecase/persist.go` orchestration | Task 9 |
| `lib/usecase/usecasetest/` helpers | Task 9 |
| `lib/usecase/testdata/` (canonical + 5 invalid) | Tasks 5 & 6 |
| `skills/detect-usecases/scripts/write-usecase.go` + test | Task 10 |
| `skills/detect-usecases/SKILL.md` (4-phase procedure) | Task 11 |
| `CLAUDE.md` update (four schemas + skill mention) | Task 12 |
| `--refresh-stack`, `--journey=<id>` flags | Task 11 (skill prose only; flags consumed by the LLM, not the script) |

No spec section unmapped.

**Placeholder scan:** every step has either a concrete command or a complete code block. No "TBD", "implement later", or "similar to Task N" without showing the code.

**Type consistency:**
- `UseCase.Evidence` is `[]stack.Evidence` everywhere (Tasks 5, 7, 9) — no divergent shape.
- `CheckEvidenceFiles(uc *UseCase, projectRoot string) error` — same signature in Tasks 7 and 9.
- `CheckJourneyReference(uc *UseCase, s *stack.Stack) error` — same in Tasks 8 and 9.
- `ValidateAndPersist(draftJSON []byte, outDir, projectRoot string, stk *stack.Stack, schemasDir string) (string, error)` — same in Tasks 9 and 10.
- `TargetUseCase` and `usecaseURL` introduced in Task 1, consumed in Task 6 (`schema.TargetUseCase`) and indirectly via `ValidateAndPersist` in Task 9.
- `Archetype` and `EntryPointKind` enum constants prefixed `Archetype*` / `EntryPoint*` consistently across Tasks 3, 4, and 9.

No inconsistencies found.

**Scope check:** Tasks 1–12 form one cohesive subsystem (UseCase entity + detect skill + cross-checks). `/create-sensor`, diffing, and replay are explicitly out of scope (called out in the spec's "What changes" section and in "Future work"). This plan ships a working, testable subsystem on its own.

---

## Plan complete

Plan saved to `docs/superpowers/plans/2026-05-14-usecase-entity.md`.

Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
