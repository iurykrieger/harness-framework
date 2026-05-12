# Structural Refactor — Test Reorganization, Typed Shapes, Fixture Taxonomy

**Date:** 2026-05-12
**Status:** Approved for implementation
**Branch:** `worktree-refactor-2` (worktree)
**Distinct from:** `2026-05-12-lib-refactor-design.md` (skill-script deduplication — already merged in #29). This spec is a separate, sequential effort focused on filesystem layout, typed entity shapes, and fixture conventions.

## Objective

A pure structural refactor that:

1. Relocates root-level `test/` files into the context-owning packages.
2. Deletes the root-level `scripts/` directory (rule 4 violation — migration scripts and a broken smoke).
3. Introduces typed Go struct mirrors of `schemas/sensor.json` and `schemas/signal.json` (the third schema, `stack.json`, already has `lib/stack/shape.go`).
4. Dissolves `lib/testfixtures/` into per-entity `<pkg>test` helper packages and `testdata/` JSON files.
5. Records the fixture taxonomy as a durable rule in `CLAUDE.md`.

**No behavior change.** All Signals continue to validate against `schemas/signal.json`. All existing tests pass after their import paths are updated. Schemas are not modified.

## Non-objectives

- Schemas (`schemas/*.json`) are not edited.
- The map-based APIs in `lib/sensor/{requires,project,env,persist}.go` are not converted to typed `*Sensor`. The typed shapes are *introduced* and used in mechanically obvious places (envelope helper, fixtures); progressive migration of remaining call sites is a follow-up.
- File-header comments of the form `// lib/<pkg>/<file>.go` at the top of existing files are not removed. ~10 such headers exist; that cleanup is independent.
- `.harness/sensors/fixtures/` is not touched. It is sensor-domain fixture data referenced by `verification.golden_cases[].fixture` in each sensor JSON — distinct from Go test fixtures.
- `hooks/testdata/`, `lib/stack/testdata/` are already correct (per-package, Go convention). No changes.
- No new top-level packages outside what this spec specifies.
- No third-party dependency additions.

## Findings — verified against the worktree at `35d6d0a`

### A. Root-level `test/` directory (rule-9 violation)

| Path | Package | Concern |
|---|---|---|
| `test/integration_runtime_logs_test.go` | `package integration` (build tag `integration`) | Tests two concurrent `/run-sensor` invocations — orchestrator territory. |
| `test/heal-e2e/heal_e2e_test.go` | `package healE2E_test` | Black-box test of the `heal-sensor` skill binary. |
| `test/registry-discovery-e2e/registry_discovery_e2e_test.go` | `package registryDiscoveryE2E_test` | Tests `lib/registry.Lookup` invariant across the four registry-touching skills. |
| `test/fixtures/stack-discovery/` (with `go.mod`, `go.sum`, `cmd/server/main.go`, `expected-stack.json`, `expected-stdout.log`) | sub-module (own `go.mod`) | Consumed by `lib/stack/e2e_fixture_test.go:162` via `filepath.Join(dir, "test", "fixtures", "stack-discovery")`. |

### B. Root-level `scripts/` directory (rule-4 violation)

| Path | Status | Action |
|---|---|---|
| `scripts/migrate-requires.go` | One-shot v1→v2 `requires[]` migration, complete (zero v1 sensors remain in the repo) | Delete |
| `scripts/migrate-requires_test.go` | Test for the above | Delete |
| `scripts/smoke-requires-deps-logs.sh` | Broken (references `sensors/...` — the move to `.harness/sensors/` in commit `35d6d0a` invalidated all paths). Coverage is redundant with `lib/sensor/requires_test.go`. | Delete |

### C. `lib/sensor/` and `lib/signal/` lack typed schema mirrors

- `lib/stack/shape.go` (reference pattern): typed structs with `json:` tags + string-typed enums + `const` blocks.
- `lib/sensor/`: zero schema mirror. `Sensor` shape lives only as `map[string]interface{}` across `envelope.go`, `requires.go`, `project.go`, `env.go`, `persist.go`. Auxiliary structs exist (`Envelope`, `Gate`, `MissingEnv`, `Failure`) but none mirror the schema.
- `lib/signal/`: zero schema mirror. `Builder.Build()` returns `map[string]interface{}`. `AggregateInput`/`AggregateResult` are runtime helpers, not Signal mirrors.

### D. `lib/testfixtures/` is a god-package

| Symbol | LoC | Call sites | Real domain |
|---|---|---|---|
| `FreezeClock(t) func()` | 8 | 3, all in `lib/subprocess/stream_test.go` | mutates `sensor.NowFn`/`NewRunIDFn` |
| `WithRunDir(t, sensorID, seed) (Root, runID, runDir)` | 20 | 2, both in `lib/subprocess/stream_test.go` | builds a `registry.Root` + run directory |
| `RepoSchemasDir(t) string` | 13 | ~50, spread across `lib/cli`, `lib/schema`, `lib/sensor`, `lib/subprocess`, `lib/orchestrator`, `lib/registry`, `test/heal-e2e` | resolves `<repo>/schemas/` via `runtime.Caller` |
| `ValidSensor{Computational,Inferential,Setup}() map[string]interface{}` | ~90 | ~35, weighted in `lib/schema/validator_test.go` | canonical sensor fixtures |

Four responsibilities, four consumer profiles. Unification under one package is incidental ("they are tests"), not principled.

### E. Fixture-location taxonomy is undocumented

Five locations carry "fixture" semantics today, each serving a distinct purpose:

| Path | Tier | Purpose |
|---|---|---|
| `hooks/testdata/*.txt` | per-package static data (Go convention) | hooks tests |
| `lib/stack/testdata/*.json` | per-package static data | stack tests |
| `lib/testfixtures/*.go` | cross-package Go helpers | builders / time stubs |
| `test/fixtures/stack-discovery/` | cross-module Go sub-module | stack e2e harness |
| `.harness/sensors/fixtures/*` | sensor-domain data | `verification.golden_cases[].fixture` |

Today's CLAUDE.md rule 9 only mentions `lib/testfixtures/`. The remaining four are de-facto conventions that newcomers have to reverse-engineer.

### F. Minor consolidations

| Spot | LoC | Action |
|---|---|---|
| `lib/sensor/error.go` (single function `BuildErrorSignal`) | 17 | Fold into `lib/sensor/envelope.go` (cohesive: error signal is constructed from an `Envelope`). |
| `lib/cli/flag.go` (single type `MultiFlag`) | 10 | Fold into `lib/cli/bootstrap.go`. |
| `lib/sensor/env_test.go` | white-box `package sensor` | Split into `env_test.go` + `error_test.go` + `missing_env_signal_test.go`; migrate all three to `package sensor_test` to align with the other tests in the package. |
| `lib/orchestrator/main_test.go` (copies `/usr/bin/true` to disk to provide a fake watcher) | Fragile in Alpine / minimal Linux images | Replace with a portable shell stub: `os.WriteFile(watcherPath, []byte("#!/bin/sh\nexit 0\n"), 0o755)`. |

---

## Design

### 1. Relocate `test/` into context-owning packages

| From | To | Notes |
|---|---|---|
| `test/integration_runtime_logs_test.go` | `lib/orchestrator/integration_runtime_logs_test.go` | Keep `//go:build integration`. Package becomes `orchestrator_test` (black-box). |
| `test/heal-e2e/heal_e2e_test.go` | `skills/heal-sensor/scripts/heal_e2e_test.go` | Package `healSensor_test`. No new build tag. |
| `test/registry-discovery-e2e/registry_discovery_e2e_test.go` | `lib/registry/discovery_e2e_test.go` | Package `registry_test`. No new build tag. |
| `test/fixtures/stack-discovery/` (entire subtree) | `lib/stack/testdata/stack-discovery/` | Go convention: `testdata/` is ignored by `go list ./...` even with a nested `go.mod`. |

**Consequential edits:**
- `lib/stack/e2e_fixture_test.go:162` — replace `filepath.Join(dir, "test", "fixtures", "stack-discovery")` with `filepath.Join(dir, "testdata", "stack-discovery")`.
- Error message on line 172 — replace `"test/fixtures/stack-discovery not found"` with `"lib/stack/testdata/stack-discovery not found"`.
- The two e2e tests that today reference `testfixtures.ValidSensorComputational()` and `testfixtures.RepoSchemasDir(t)` switch to the new helpers introduced in section 4 below.

After this section: `test/` directory does not exist.

### 2. Delete root `scripts/`

Delete (no preservation):
- `scripts/migrate-requires.go`
- `scripts/migrate-requires_test.go`
- `scripts/smoke-requires-deps-logs.sh`
- `scripts/` (directory)

Recovery: `git log -- scripts/` preserves history if the script is ever needed again. The plan document at `docs/superpowers/plans/2026-05-11-requires-discriminated-union.md` references these files; that reference becomes a historical artifact — the plan stays untouched.

After this section: `find . -type d -name scripts -maxdepth 2 -not -path "./.git/*"` returns only `./skills/*/scripts`.

### 3. Typed shapes for `Sensor` and `Signal`

#### 3.1 `lib/sensor/shape.go`

New file mirroring `schemas/sensor.json` one-to-one. Pattern: structs with `json:` tags, string-typed enums, `const` blocks. Mirrors the `lib/stack/shape.go` pattern.

Top-level type:
```go
type Sensor struct {
    ID           string        `json:"id"`
    Version      string        `json:"version"`
    Name         string        `json:"name"`
    Description  string        `json:"description"`
    Kind         Kind          `json:"kind"`
    Type         Type          `json:"type"`
    Regulation   Regulation    `json:"regulation"`
    Phase        Phase         `json:"phase"`
    Determinism  Determinism   `json:"determinism"`
    Output       Output        `json:"output"`
    Cost         Cost          `json:"cost"`
    Triggers     []Trigger     `json:"triggers"`
    Requires     []Requirement `json:"requires,omitempty"`
    Execution    Execution     `json:"execution"`
    Verification Verification  `json:"verification"`
    Calibration  *Calibration  `json:"calibration,omitempty"`
}
```

Aux types (defined in `shape.go`, no separate files):
- `Cost`, `Latency`, `Compute`, `Tokens`
- `Trigger` (one field: `On TriggerOn` with enum)
- `Requirement` (discriminated union — `Kind` field plus all possible payload fields as pointers/zero-value; alternative: separate `RequirementSensor`, `RequirementTool`, etc. with `Requirement` as an interface). **Decision: flat struct with `Kind` discriminator + nullable fields**, matching how `requires.go` reads the array today.
- `Execution`, `ExitCodeMapEntry`, `OutputParsing`, `Pattern`, `Decoding`
- `Verification`, `GoldenCase`
- `Calibration`

Enums (`type Foo string` + `const` block):
- `Kind` (`observation`, `assertion`, `setup`)
- `Type` (`computational`, `inferential`)
- `Regulation` (`behaviour`, `maintainability`, ...)
- `Phase` (`on-demand`, `pre-integration`, `post-integration`, `pull-request`)
- `Determinism` (`high`, `medium`, `low`)
- `Output` (`single`, `stream`)
- `TriggerOn`, `CostClass`, `RequirementKind`

Method:
```go
func (s *Sensor) AsMap() map[string]interface{}
```

Implements JSON round-trip via `json.Marshal` → `json.Unmarshal`. Used as a bridge for call sites that still expect maps.

#### 3.2 `lib/sensor/shape_test.go`

Round-trip tests:
- Each canonical fixture (`canonical-computational.json`, `canonical-inferential.json`, `canonical-setup.json` — see section 4.2) unmarshals into `Sensor` without loss; re-marshalling produces equivalent JSON modulo key order.
- `Sensor.AsMap()` is structurally equal (deep equal) to the original `map[string]interface{}` parsing.

#### 3.3 `lib/signal/shape.go`

Mirrors `schemas/signal.json`:
```go
type Signal struct {
    SensorID   string                 `json:"sensor_id"`
    Version    string                 `json:"version"`
    RunID      string                 `json:"run_id"`
    StartedAt  string                 `json:"started_at"`
    SensorType string                 `json:"sensor_type"`
    Verdict    Verdict                `json:"verdict"`
    Severity   Severity               `json:"severity"`
    Rationale  string                 `json:"rationale,omitempty"`
    Evidence   []Evidence             `json:"evidence,omitempty"`
    Suggestion *Suggestion            `json:"suggestion,omitempty"`
    Metadata   map[string]interface{} `json:"metadata,omitempty"`
}
```

Aux types: `Evidence`, `Suggestion`.

Enums: `Verdict` (`pass`, `warn`, `fail`, `error`), `Severity` (`info`, `low`, `medium`, `high`, `critical`).

Method:
```go
func (s *Signal) AsMap() map[string]interface{}
```

Companion to `signal.Builder`:
```go
func (b *Builder) BuildTyped() Signal
```
Internal: build the map as today, JSON-round-trip into `Signal`. The existing `Build() map[string]interface{}` is preserved.

#### 3.4 `lib/signal/shape_test.go`

Round-trip tests + a parity test asserting `b.Build()` and `b.BuildTyped().AsMap()` are deep-equal for the same input.

#### 3.5 Apply `Sensor` in `BuildEnvelopeTyped`

Add to `lib/sensor/envelope.go`:
```go
func BuildEnvelopeTyped(s *Sensor) Envelope
```
Equivalent to `BuildEnvelope` but takes typed input. The existing `BuildEnvelope(map[string]interface{})` is preserved.

### 4. Dissolve `lib/testfixtures/`

#### 4.1 `FreezeClock` and `WithRunDir` — inline

Both are used only inside `lib/subprocess/stream_test.go` (3 and 2 call sites respectively). Move both functions verbatim into that file as unexported helpers (`freezeClock`, `withRunDir`).

#### 4.2 Canonical sensor fixtures — JSON + loader

Create:
- `lib/sensor/testdata/canonical-computational.json`
- `lib/sensor/testdata/canonical-inferential.json`
- `lib/sensor/testdata/canonical-setup.json`

Content: the JSON shape of `ValidSensorComputational()`, `ValidSensorInferential()`, `ValidSensorSetup()` from today's `lib/testfixtures/sensor.go`. Each file is the byte-for-byte JSON serialization, indented for readability.

Create `lib/sensor/sensortest/canonical.go` (new package `sensortest`):
```go
package sensortest

import (
    "encoding/json"
    "os"
    "path/filepath"
    "runtime"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/sensor"
)

func LoadComputational(t *testing.T) *sensor.Sensor { return load(t, "canonical-computational.json") }
func LoadInferential(t *testing.T)  *sensor.Sensor { return load(t, "canonical-inferential.json") }
func LoadSetup(t *testing.T)         *sensor.Sensor { return load(t, "canonical-setup.json") }

func load(t *testing.T, name string) *sensor.Sensor {
    t.Helper()
    _, thisFile, _, _ := runtime.Caller(0)
    p := filepath.Join(filepath.Dir(thisFile), "..", "testdata", name)
    body, err := os.ReadFile(p)
    if err != nil { t.Fatal(err) }
    var s sensor.Sensor
    if err := json.Unmarshal(body, &s); err != nil { t.Fatal(err) }
    return &s
}
```

Create `lib/sensor/sensortest/canonical_test.go`:
- Asserts the three canonical JSON files pass `schema.NewValidator(...).Validate(TargetSensor, *.AsMap())`.
- Asserts `LoadComputational`, `LoadInferential`, `LoadSetup` return non-nil `*Sensor` with non-empty `ID`.

#### 4.3 `RepoSchemasDir` — `schematest` package

Create `lib/schema/schematest/repodir.go` (new package `schematest`):
```go
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
    if !ok { t.Fatal("runtime.Caller failed") }
    // .../lib/schema/schematest/repodir.go → 3 levels up to repo root.
    repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
    dir := filepath.Join(repoRoot, "schemas")
    if _, err := os.Stat(filepath.Join(dir, "sensor.json")); err != nil {
        t.Fatalf("schemas dir not where expected (%s): %v", dir, err)
    }
    return dir
}
```

Note the path depth changes from `..` `..` (2 levels) to `..` `..` `..` (3 levels) because the file moves one directory deeper.

#### 4.4 Update all call sites

| Old import & usage | New import & usage |
|---|---|
| `testfixtures.FreezeClock(t)` | inline `freezeClock(t)` in `lib/subprocess/stream_test.go` |
| `testfixtures.WithRunDir(t, …)` | inline `withRunDir(t, …)` in `lib/subprocess/stream_test.go` |
| `testfixtures.RepoSchemasDir(t)` | `schematest.RepoSchemasDir(t)` |
| `testfixtures.ValidSensorComputational()` → `map` | `sensortest.LoadComputational(t).AsMap()` |
| `testfixtures.ValidSensorInferential()` → `map` | `sensortest.LoadInferential(t).AsMap()` |
| `testfixtures.ValidSensorSetup()` → `map` | `sensortest.LoadSetup(t).AsMap()` |

All ~120 call sites (across `lib/cli`, `lib/schema`, `lib/sensor`, `lib/subprocess`, `lib/orchestrator`, `lib/registry`, and the heal-sensor e2e moved in section 1) are updated. After this section: `lib/testfixtures/` is deleted.

#### 4.5 Delete `lib/testfixtures/`

Remove the directory and its files (`clock.go`, `paths.go`, `sensor.go`) after all imports are migrated.

### 5. CLAUDE.md rule 10 — fixture taxonomy

Add a new rule (after rule 9) — concise prose, no examples:

```markdown
10. **Test data and test helpers are split by purpose.** Three locations, three purposes:
    - `<pkg>/testdata/` — Go-convention static fixtures (JSON, txt, jsonl, nested go.mod sub-modules). Per-package; consumed by `_test.go` of the same package or by another package's `<pkg>test` helper via relative path. Ignored by `go build`.
    - `lib/<pkg>/<pkg>test/` — Go test helpers (functions taking `*testing.T`) that load/decorate testdata for cross-package use. Each `<pkg>test` package is owned by exactly one `<pkg>` and depends only on `<pkg>` and the standard library / testing. Convention follows `net/http/httptest` and `testing/iotest`. The package is importable from production code in principle; do not do so.
    - `.harness/sensors/fixtures/` — sensor-domain fixture data referenced by `verification.golden_cases[].fixture` in sensor JSON. NOT a Go test fixture. Lives in the user project tree (under `.harness/`) and is consumed at sensor runtime.

    A single shared "fixtures" or "testhelpers" package across the whole `lib/` tree is explicitly disallowed.
```

Update rule 9's closing sentence (currently `Cross-package test fixtures live in lib/testfixtures/...`) to: `Cross-package test fixtures follow the taxonomy in rule 10.`

### 6. Minor consolidations

#### 6.1 `lib/sensor/error.go` → fold into `lib/sensor/envelope.go`

Move `BuildErrorSignal(env Envelope, outputMode, rationale, remediation string) map[string]interface{}` to `envelope.go`. Delete `error.go`. The function uses `Envelope`; co-location is natural.

#### 6.2 `lib/cli/flag.go` → fold into `lib/cli/bootstrap.go`

Move `MultiFlag` type + its `String()` / `Set(string) error` methods to `bootstrap.go`. Delete `flag.go`.

#### 6.3 Split `lib/sensor/env_test.go`

Today the file is in `package sensor` (white-box) and tests three concerns (env, error, missing-env-signal). Split into:
- `lib/sensor/env_test.go` — tests for `CheckRequiredEnv`. Package becomes `sensor_test`.
- `lib/sensor/error_test.go` (formerly inside env_test.go) — tests for `BuildErrorSignal`. Package `sensor_test`.
- `lib/sensor/missing_env_signal_test.go` — tests for `BuildMissingEnvSignal`. Package `sensor_test`.

Any access to unexported symbols becomes either an exported test helper in a sibling `_test.go` (`package sensor`) or is dropped if not load-bearing.

#### 6.4 `lib/orchestrator/main_test.go` — portable watcher stub

Replace the body of `TestMain` such that the synthetic watcher binary written to disk is the literal shell content `#!/bin/sh\nexit 0\n` (with `0o755` perms). Drop the `os.ReadFile("/usr/bin/true")` path. The stub exits with status 0 regardless of platform.

---

## Commit plan (single PR, 8 commits, each CI-green)

1. **chore: relocate root-level test/ into context-owning packages.**
   Moves the four `test/*` paths to their new homes. Edits `lib/stack/e2e_fixture_test.go:162` for the new fixture path. Does NOT delete `lib/testfixtures/` yet — the moved tests still import it. After this commit, `test/` directory is empty.
2. **chore: remove completed migration scripts and broken smoke.**
   Deletes `scripts/migrate-requires*.{go,go}` and `scripts/smoke-requires-deps-logs.sh`. Removes the `scripts/` directory.
3. **refactor(sensor,signal): add typed shape.go mirroring JSON schemas.**
   Adds `lib/sensor/shape.go` + `lib/sensor/shape_test.go`, `lib/signal/shape.go` + `lib/signal/shape_test.go`, `BuildEnvelopeTyped` in `envelope.go`, `BuildTyped` in `signal/builder.go`. No call site changes yet.
4. **refactor(sensor): canonical fixtures as testdata JSON + sensortest loader.**
   Creates `lib/sensor/testdata/canonical-*.json`, `lib/sensor/sensortest/canonical.go` + `_test.go`. Migrates all `testfixtures.ValidSensor*()` call sites to `sensortest.Load*(t).AsMap()`.
5. **refactor(schema): introduce schematest package for RepoSchemasDir.**
   Creates `lib/schema/schematest/repodir.go`. Migrates all `testfixtures.RepoSchemasDir(t)` call sites to `schematest.RepoSchemasDir(t)`.
6. **refactor: inline FreezeClock and WithRunDir; delete lib/testfixtures.**
   Inlines the two helpers into `lib/subprocess/stream_test.go`. Deletes `lib/testfixtures/` directory (and its three files). Updates CLAUDE.md rule 9 closing sentence and adds rule 10.
7. **refactor: misc consolidations (cli/flag, sensor/error, env_test split).**
   Sections 6.1, 6.2, 6.3 above.
8. **test(orchestrator): replace /usr/bin/true copy with portable stub.**
   Section 6.4 above.

---

## Definition of Done

Each item is a binary check.

1. `go test ./... -tags=integration` passes.
2. `go test -tags=run_computational ./...` passes.
3. `go test -tags=run_inferential ./...` passes.
4. `go vet -tags=run_computational ./...` and `go vet -tags=run_inferential ./...` are clean.
5. `find . -type d -name scripts -maxdepth 2 -not -path "./.git/*"` returns exactly `./skills/*/scripts` (one path per skill, none at the repo root).
6. `find . -type d -name test -maxdepth 2 -not -path "./.git/*"` returns nothing (the root `test/` directory does not exist).
7. `lib/sensor/shape.go` and `lib/signal/shape.go` exist. The three canonical sensor fixtures round-trip through `json.Unmarshal` → `Sensor` → `AsMap()` and back, producing structurally equal `map[string]interface{}` values (asserted by `lib/sensor/shape_test.go`). A canonical Signal map round-trips through `json.Unmarshal` → `Signal` → `AsMap()` to a structurally equal map, and `Builder.Build()` is deep-equal to `Builder.BuildTyped().AsMap()` for the same input (asserted by `lib/signal/shape_test.go`).
8. `lib/testfixtures/` directory does not exist; `grep -rn 'testfixtures' --include='*.go' .` returns nothing.
9. `lib/schema/schematest/repodir.go` and `lib/sensor/sensortest/canonical.go` exist and are the only two `<pkg>test` packages under `lib/`.
10. `lib/sensor/testdata/canonical-{computational,inferential,setup}.json` exist and each passes `schema.NewValidator(...).Validate(TargetSensor, ...)` (asserted by `lib/sensor/sensortest/canonical_test.go`).
11. CLAUDE.md contains a rule 10 documenting the three-tier fixture taxonomy, and rule 9's closing sentence cross-references it.

---

## Out-of-scope follow-ups (recorded for traceability, NOT part of this PR)

- Migrate `lib/sensor/{requires,project,env,persist}.go` and `lib/heal/` to consume typed `*Sensor` instead of `map[string]interface{}`.
- Migrate `lib/signal/` aggregate/pattern helpers to operate on `[]Signal` instead of `[]map[string]interface{}`.
- Remove `Sensor.AsMap()` and `Signal.AsMap()` once no caller needs them.
- Strip `// path/to/file.go` headers from existing files.
- Audit `lib/heal/` for the same shape-mirror gap (the heal-rule registry might benefit from a typed `Plan` shape).

These are tracked in this section so that follow-up planners do not have to rediscover them.
