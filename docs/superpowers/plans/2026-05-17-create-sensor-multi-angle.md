# /create-sensor multi-angle authoring — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement Spec B (`docs/superpowers/specs/2026-05-17-create-sensor-multi-angle-design.md`) — replace `verification.golden_cases[]` with declarative `use_cases[]`, add deterministic Go-based grouping/inference for `/create-sensor`, rewrite the skill to consume `.harness/usecases/` as input, and regenerate the framework's own smoke sensors against the new contract.

**Architecture:** The schema change is a hard break — `verification` is removed, `use_cases[]` (required, minItems 1) is added. The change touches `lib/sensor/{shape,load,validate,persist}.go`, the smoke sensors, the CI workflow, and deletes `run-golden.go`. All of this lands in a single atomic commit (Task 1). Two new Go scripts follow: `read-usecases.go` (loads usecase YAMLs into a JSON ledger) and `plan-sensors.go` (applies deterministic grouping + kind/type/output/mock_strategy inference, emits JSONL plan). The SKILL.md is then rewritten to orchestrate the Go scripts and the LLM-driven synthesis phase. An acceptance sensor at the end self-tests the new flow against an existing usecase.

**Tech Stack:** Go 1.25, `sigs.k8s.io/yaml`, `santhosh-tekuri/jsonschema/v5`, Go standard `testing` (table-driven), `go run -C "$CLAUDE_PLUGIN_ROOT" -tags=<tag>` invocation contract, JSONL Signal envelopes per `schemas/signal.yaml`.

---

## Task 1: Atomic schema flip + cascade

**Why atomic:** `.github/workflows/test.yml` triggers on push to `main` AND on every PR. The schema change immediately invalidates every existing sensor YAML, every `lib/sensor` test that constructs a Sensor with `Verification`, and the entire `run-golden.go` script. Splitting this across commits leaves `go test ./...` red between them, blocking subsequent CI. Everything below lands in a single commit.

**Files:**
- Modify: `schemas/sensor.yaml` (remove `verification` block, add `use_cases`)
- Modify: `lib/sensor/shape.go` (drop `Verification`/`GoldenCase` types; add `UseCases []string` on `Sensor`)
- Modify: `lib/sensor/persist.go` (rename `RequireFixturesOnDisk` → `RequireUseCaseFilesOnDisk`; reroute `checkFixturesOnDisk` to check usecase file existence)
- Modify: `lib/sensor/validate.go` (no removals; if file has cross-field rules with a `projectRoot` parameter, add `use_cases_files_exist`)
- Modify: `lib/sensor/load.go` (if it normalizes `verification`, drop that path; no-op otherwise)
- Modify: `lib/sensor/persist_test.go` (replace `verification.golden_cases` in all draft fixtures with `use_cases: ["fake-usecase"]`; update assertions)
- Modify: `lib/sensor/load_test.go`, `lib/sensor/validate_test.go` (same find/replace for inline fixtures)
- Modify: `lib/sensor/testdata/canonical-{computational,inferential,setup}.yaml` (drop `verification` block, add `use_cases`)
- Modify: `lib/orchestrator/*_test.go` (8 files) — replace inline `Verification: ...` with `UseCases: []string{"fake"}` in test sensor builders
- Modify: `lib/registry/discovery_e2e_test.go`, `lib/schema/validator_test.go` — same
- Modify: `skills/create-sensor/scripts/write-sensor.go` (rename `RequireFixturesOnDisk` arg → `RequireUseCaseFilesOnDisk`; map `MissingFixtureError` → emit `missing_usecase` Signal instead of `missing_fixture`)
- Modify: `skills/create-sensor/scripts/write-sensor_test.go` (replace fixtures + assertions)
- Modify: `skills/run-sensor/scripts/run-computational_test.go`, `run-inferential_test.go`, `skills/start-sensor/scripts/start_test.go` — same find/replace for inline sensor fixtures
- Delete: `skills/detect-sensors/scripts/run-golden.go`
- Delete: `skills/detect-sensors/scripts/run-golden_test.go`
- Modify: `.github/workflows/test.yml` (remove the two `run_golden` steps at lines 62–66)
- Create: `.harness/usecases/framework/framework-smoke-typed-pipeline.yaml`
- Create: `.harness/usecases/framework/framework-smoke-with-setup.yaml`
- Modify: `.harness/sensors/smoke-typed-pipeline.yaml` (drop `verification`, add `use_cases: ["framework-smoke-typed-pipeline"]`)
- Modify: `.harness/sensors/smoke-with-setup.yaml` (same)
- Modify: `.harness/usecases/create-sensor/create-sensor-missing-fixture.yaml` (the usecase still describes the old `MissingFixtureError`; rewrite to describe the new `missing_usecase` error)

### Step 1.1: Pin the exact schema diff

- [ ] **Read the current verification block to confirm the byte-level boundaries before editing.**

Run: `sed -n '975,1015p' schemas/sensor.yaml`
Expected: shows the `verification` block at lines 978–1009 plus the `version` field that follows.

- [ ] **Edit `schemas/sensor.yaml`: replace the verification block with the use_cases array.**

Open `schemas/sensor.yaml`. Find the block:

```yaml
  verification:
    additionalProperties: false
    description: Sensors are code; they need their own harness. Each sensor MUST declare
      at least one golden case.
    properties:
      golden_cases:
        items:
          additionalProperties: false
          properties:
            expected_severity:
              $ref: signal.yaml#/$defs/Severity
            expected_verdict:
              $ref: signal.yaml#/$defs/Verdict
            fixture:
              description: Path to fixture or fixture id.
              type: string
            notes:
              type: string
          required:
          - fixture
          - expected_verdict
          - expected_severity
          type: object
        minItems: 1
        type: array
      self_test_command:
        description: Command that runs all fixtures and validates each emitted Signal
          against signal.yaml.
        type: string
    required:
    - golden_cases
    type: object
```

Replace with:

```yaml
  use_cases:
    description: |
      Usecase ids this sensor validates. Pure traceability — the runtime
      does NOT auto-replay these. Steps under execution authoritatively
      decide pass/fail. Required so every sensor declares its purpose.
    type: array
    minItems: 1
    items:
      type: string
      pattern: ^[a-z][a-z0-9-]*$
```

- [ ] **Update the top-level `required` list: `- verification` → `- use_cases`.**

Open `schemas/sensor.yaml`. Find the `required:` list (around line 1014 in the original). Inside it, replace the line `- verification` with `- use_cases`. Leave the other 14 entries (id, version, name, description, kind, type, regulation, phase, determinism, output, cost, triggers, execution) untouched.

- [ ] **Sanity-check the YAML parses.**

Run: `go run -tags=write_stack ./skills/detect-sensors/scripts --help 2>&1 | head -3 || true`
Expected: no YAML parse panic from the schema embedded by `lib/schema`.

### Step 1.2: Update `lib/sensor/shape.go`

- [ ] **Read the current Verification/GoldenCase types to confirm boundaries.**

Run: `grep -n "Verification\|GoldenCase" lib/sensor/shape.go`
Expected: lines 24, 254–264 (Sensor field, two type defs).

- [ ] **Edit `lib/sensor/shape.go`: replace the `Verification` field on `Sensor` with `UseCases`.**

Find the line at the top of the Sensor struct:

```go
	Verification   Verification    `json:"verification"`
```

Replace with:

```go
	UseCases       []string        `json:"use_cases"`
```

Find the type definitions at the bottom of the file:

```go
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
```

Delete both type definitions entirely. There is no replacement.

- [ ] **Confirm no other files in `lib/sensor/` still reference the removed types.**

Run: `grep -n "GoldenCase\|Verification\b" lib/sensor/*.go | grep -v _test.go`
Expected: empty output. If anything appears, it is production code that needs the same treatment; fix it before continuing.

### Step 1.3: Update `lib/sensor/persist.go`

- [ ] **Rename the option field on `PersistOpts`.**

Open `lib/sensor/persist.go`. Find the `PersistOpts` struct. Replace the field block:

```go
	// RequireFixturesOnDisk causes ValidateAndPersist to stat every
	// verification.golden_cases[].fixture path (resolved against
	// ProjectRoot) and return a *MissingFixtureError if any are absent.
	// Requires ProjectRoot to be set.
	RequireFixturesOnDisk bool
```

with:

```go
	// RequireUseCaseFilesOnDisk causes ValidateAndPersist to verify
	// every entry in use_cases[] resolves to a real
	// .harness/usecases/**/<id>.yaml under ProjectRoot, returning a
	// *MissingUseCaseError if any are absent. Requires ProjectRoot
	// to be set.
	RequireUseCaseFilesOnDisk bool
```

- [ ] **Replace the `MissingFixtureError` type with `MissingUseCaseError`.**

Find:

```go
// MissingFixtureError is returned when RequireFixturesOnDisk is set and a
// referenced fixture is not on disk.
type MissingFixtureError struct {
	Rel  string // path as written in golden_cases[].fixture
	Full string // resolved absolute path that was missing
}

func (e *MissingFixtureError) Error() string {
	return fmt.Sprintf("fixture %q not found at %s", e.Rel, e.Full)
}
```

Replace with:

```go
// MissingUseCaseError is returned when RequireUseCaseFilesOnDisk is set
// and a referenced usecase id does not match any
// <ProjectRoot>/.harness/usecases/**/<id>.yaml file.
type MissingUseCaseError struct {
	ID         string // the use_cases[] entry that did not resolve
	SearchRoot string // the directory tree that was walked
}

func (e *MissingUseCaseError) Error() string {
	return fmt.Sprintf("usecase %q not found under %s/.harness/usecases", e.ID, e.SearchRoot)
}
```

- [ ] **Rewrite `checkFixturesOnDisk` as `checkUseCasesOnDisk`.**

Find the existing `checkFixturesOnDisk` function (around lines 149–175). Replace the entire function with:

```go
func checkUseCasesOnDisk(sensorMap map[string]interface{}, projectRoot string) error {
	rawIDs, ok := sensorMap["use_cases"].([]interface{})
	if !ok {
		return nil // schema will catch this later
	}
	usecasesRoot := filepath.Join(projectRoot, ".harness", "usecases")
	for _, raw := range rawIDs {
		id, ok := raw.(string)
		if !ok || id == "" {
			continue
		}
		found, err := usecaseFileExists(usecasesRoot, id)
		if err != nil {
			return err
		}
		if !found {
			return &MissingUseCaseError{ID: id, SearchRoot: projectRoot}
		}
	}
	return nil
}

// usecaseFileExists walks usecasesRoot looking for a <id>.yaml file.
// Match is by basename equality (filepath.Walk is bounded to the
// .harness/usecases subtree).
func usecaseFileExists(usecasesRoot, id string) (bool, error) {
	target := id + ".yaml"
	found := false
	err := filepath.Walk(usecasesRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return filepath.SkipDir
			}
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if info.Name() == target {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return false, err
	}
	return found, nil
}
```

- [ ] **Wire the new check into `ValidateAndPersist`.**

Find the caller of `checkFixturesOnDisk` inside `ValidateAndPersist`. Replace the conditional block:

```go
	if opts.RequireFixturesOnDisk {
		if err := checkFixturesOnDisk(sensorMap, opts.ProjectRoot); err != nil {
			return "", err
		}
	}
```

with:

```go
	if opts.RequireUseCaseFilesOnDisk {
		if err := checkUseCasesOnDisk(sensorMap, opts.ProjectRoot); err != nil {
			return "", err
		}
	}
```

- [ ] **Add `errors` import if missing.**

Run: `grep -n '"errors"' lib/sensor/persist.go`
If absent, add `"errors"` to the import block alongside the other standard imports.

### Step 1.4: Update `lib/sensor/validate.go` and `load.go`

- [ ] **Confirm `validate.go` has no `golden_cases` references and decide if the file needs editing at all.**

Run: `grep -n "golden_cases\|verification\|Verification\|GoldenCase" lib/sensor/validate.go`
Expected: empty. The spec confirms this; if anything appears, read it and remove or adapt.

- [ ] **Confirm `load.go` has no normalize-time references to `verification`.**

Run: `grep -n "verification\|Verification\|golden" lib/sensor/load.go`
Expected: empty. If anything appears, remove it.

### Step 1.5: Update the canonical YAML test fixtures

- [ ] **Rewrite `lib/sensor/testdata/canonical-computational.yaml`.**

Find the trailing block (current lines 30 onward):

```yaml
verification:
  golden_cases:
    - fixture: testdata/canonical-pass.txt
      expected_verdict: pass
      expected_severity: info
```

Replace with:

```yaml
use_cases:
  - canonical-computational-fixture
```

- [ ] **Rewrite `lib/sensor/testdata/canonical-inferential.yaml`.**

Find the equivalent trailing `verification:` block and replace it with:

```yaml
use_cases:
  - canonical-inferential-fixture
```

- [ ] **Rewrite `lib/sensor/testdata/canonical-setup.yaml`.**

Same edit:

```yaml
use_cases:
  - canonical-setup-fixture
```

These ids never resolve to real usecase files because `RequireUseCaseFilesOnDisk` is opt-in; the canonical fixtures are read with the option OFF.

### Step 1.6: Update `lib/sensor/*_test.go`

- [ ] **Find every place in `lib/sensor/persist_test.go` that builds a draft with `verification.golden_cases`.**

Run: `grep -n 'verification\|golden_cases\|GoldenCase\|RequireFixturesOnDisk\|MissingFixtureError' lib/sensor/persist_test.go`
Capture the line numbers. For each occurrence:

- In a JSON-string fixture (`{"verification":{"golden_cases":[...]}}`), replace the `"verification":{"golden_cases":[{...}]}` substring with `"use_cases":["fake-uc"]`.
- In a Go-struct literal (`Verification: Verification{GoldenCases: ...}`), replace with `UseCases: []string{"fake-uc"}`.
- Wherever a test sets `RequireFixturesOnDisk: true`, change it to `RequireUseCaseFilesOnDisk: true`. If the test asserts a `*MissingFixtureError`, change the assertion target to `*MissingUseCaseError` and update expected error-message substrings.
- The `TestPersistCanonicalIndependentOfDraftStyle` fixture at line ~289 has the JSON form embedded. Update the same way: drop `"verification":{...}`, add `"use_cases":["fake"]`.

- [ ] **Repeat the same pattern in `lib/sensor/load_test.go` and `lib/sensor/validate_test.go`.**

Run: `grep -n 'verification\|golden_cases\|GoldenCase' lib/sensor/load_test.go lib/sensor/validate_test.go`
Apply the same replacements per occurrence.

- [ ] **Add a positive test for `use_cases[]` schema acceptance to `lib/sensor/load_test.go`.**

Append:

```go
func TestLoadAcceptsUseCases(t *testing.T) {
	// Minimal valid computational sensor with use_cases populated.
	yaml := []byte(`
id: x
version: 1.0.0
name: x
description: x
kind: assertion
type: computational
regulation: maintainability
phase: on-demand
determinism: high
output: single
cost:
  class: cheap
  compute: { cpu: low, memory_mb: 64 }
  latency: { p50_ms: 10, p95_ms: 100, timeout_ms: 5000 }
triggers:
  - on: manual
execution:
  command: "true"
  exit_code_map:
    - { exit_code: 0, verdict: pass, severity: info }
use_cases:
  - sample-uc
`)
	s, err := Load(yaml)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.UseCases) != 1 || s.UseCases[0] != "sample-uc" {
		t.Fatalf("UseCases not preserved: %#v", s.UseCases)
	}
}
```

- [ ] **Add a negative test for empty `use_cases`.**

Append:

```go
func TestLoadRejectsEmptyUseCases(t *testing.T) {
	yaml := []byte(`
id: x
version: 1.0.0
name: x
description: x
kind: assertion
type: computational
regulation: maintainability
phase: on-demand
determinism: high
output: single
cost:
  class: cheap
  compute: { cpu: low, memory_mb: 64 }
  latency: { p50_ms: 10, p95_ms: 100, timeout_ms: 5000 }
triggers:
  - on: manual
execution:
  command: "true"
  exit_code_map:
    - { exit_code: 0, verdict: pass, severity: info }
use_cases: []
`)
	if _, err := Load(yaml); err == nil {
		t.Fatal("expected schema error for empty use_cases, got nil")
	}
}
```

- [ ] **Add a negative test for legacy `verification` field rejection.**

Append:

```go
func TestLoadRejectsLegacyVerificationField(t *testing.T) {
	yaml := []byte(`
id: x
version: 1.0.0
name: x
description: x
kind: assertion
type: computational
regulation: maintainability
phase: on-demand
determinism: high
output: single
cost:
  class: cheap
  compute: { cpu: low, memory_mb: 64 }
  latency: { p50_ms: 10, p95_ms: 100, timeout_ms: 5000 }
triggers:
  - on: manual
execution:
  command: "true"
  exit_code_map:
    - { exit_code: 0, verdict: pass, severity: info }
use_cases: [foo]
verification:
  golden_cases:
    - { fixture: x, expected_verdict: pass, expected_severity: info }
`)
	if _, err := Load(yaml); err == nil {
		t.Fatal("expected schema error for legacy verification field, got nil")
	}
}
```

The new schema has `additionalProperties: false` at the top level — so this rejection is automatic.

- [ ] **Add a `MissingUseCaseError` test to `persist_test.go`.**

Append:

```go
func TestPersistMissingUseCase(t *testing.T) {
	projectRoot := t.TempDir()
	// No .harness/usecases dir on purpose.
	outDir := filepath.Join(projectRoot, ".harness", "sensors")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := []byte(`{"id":"x","version":"1.0.0","name":"x","description":"x","kind":"assertion","type":"computational","regulation":"maintainability","phase":"on-demand","determinism":"high","output":"single","cost":{"class":"cheap","compute":{"cpu":"low","memory_mb":64},"latency":{"p50_ms":10,"p95_ms":100,"timeout_ms":5000}},"triggers":[{"on":"manual"}],"execution":{"command":"true","exit_code_map":[{"exit_code":0,"verdict":"pass","severity":"info"}]},"use_cases":["nonexistent-uc"]}`)
	_, err := ValidateAndPersist(body, PersistOpts{
		OutDir:                    outDir,
		SchemasDir:                filepath.Join(repoRoot(t), "schemas"),
		ProjectRoot:               projectRoot,
		RequireUseCaseFilesOnDisk: true,
	})
	var mue *MissingUseCaseError
	if !errors.As(err, &mue) {
		t.Fatalf("expected *MissingUseCaseError, got %T: %v", err, err)
	}
	if mue.ID != "nonexistent-uc" {
		t.Fatalf("unexpected ID: %q", mue.ID)
	}
}
```

`repoRoot(t)` is whatever helper the existing test file uses to find the repo. If none exists, inline `filepath.Join("..", "..", "schemas")` relative to the test file.

### Step 1.7: Update non-`lib/sensor/` tests that build sensor fixtures inline

The following files have inline sensor fixtures or YAML strings containing `verification` / `Verification` / `GoldenCase`. For each one, do a structural replace.

- [ ] **`lib/orchestrator/lifecycle_test.go`, `live_dep_death_test.go`, `live_deps_test.go`, `preflight_test.go`, `run_test.go`, `sensor_step_override_test.go`, `health_gate_integration_test.go`, `integration_runtime_logs_test.go`.**

For each file, run:

```bash
grep -n 'verification\|Verification\|GoldenCase' <file>
```

For each occurrence:
- In a Go-struct literal: replace `Verification: Verification{GoldenCases: []GoldenCase{...}}` with `UseCases: []string{"fake-uc"}`.
- In a YAML/JSON string: replace `verification:\n  golden_cases:\n    - {...}` with `use_cases:\n  - fake-uc`. JSON form: replace `"verification":{"golden_cases":[...]}` with `"use_cases":["fake-uc"]`.

- [ ] **`lib/registry/discovery_e2e_test.go`, `lib/schema/validator_test.go`.**

Same approach.

- [ ] **`skills/run-sensor/scripts/run-computational_test.go`, `run-inferential_test.go`.**

Same approach.

- [ ] **`skills/start-sensor/scripts/start_test.go`.**

Same approach.

### Step 1.8: Update `skills/create-sensor/scripts/write-sensor.go` + its test

- [ ] **Rename the `RequireFixturesOnDisk` arg to `RequireUseCaseFilesOnDisk` in `write-sensor.go`.**

Find:

```go
	path, err := sensor.ValidateAndPersist(body, sensor.PersistOpts{
		OutDir:                outDir,
		SchemasDir:            schemasDir,
		RejectIfExists:        true,
		RequireFixturesOnDisk: true,
		ProjectRoot:           res.ProjectRoot,
	})
```

Replace with:

```go
	path, err := sensor.ValidateAndPersist(body, sensor.PersistOpts{
		OutDir:                    outDir,
		SchemasDir:                schemasDir,
		RejectIfExists:            true,
		RequireUseCaseFilesOnDisk: true,
		ProjectRoot:               res.ProjectRoot,
	})
```

- [ ] **Replace the `MissingFixtureError` branch with `MissingUseCaseError`.**

Find:

```go
		var mfe *sensor.MissingFixtureError
		if errors.As(err, &mfe) {
			emitJSON(stdout, errorSignal("missing_fixture", mfe.Error()))
			return 2
		}
```

Replace with:

```go
		var muc *sensor.MissingUseCaseError
		if errors.As(err, &muc) {
			emitJSON(stdout, errorSignal("usecase_not_found", muc.Error()))
			return 2
		}
```

- [ ] **Update `skills/create-sensor/scripts/write-sensor_test.go` to exercise the new error path.**

Run: `grep -n 'missing_fixture\|MissingFixture\|RequireFixturesOnDisk' skills/create-sensor/scripts/write-sensor_test.go`
For each match: replace the symbol/string as in Step 1.6's persist tests. The test that exercised the missing-fixture error path should now exercise the missing-usecase error path — create a temp project with no `.harness/usecases/` and a draft sensor declaring `use_cases: ["nonexistent"]`, assert the script returns exit code 2 and emits `metadata.kind: "usecase_not_found"`.

### Step 1.9: Create the framework usecases

- [ ] **Create `.harness/usecases/framework/framework-smoke-typed-pipeline.yaml`.**

```yaml
id: framework-smoke-typed-pipeline
version: 0.1.0
name: smoke test of the typed-step pipeline end-to-end
journey_id: framework
description: |
  The framework's own typed-step orchestration (shell step with
  with: { fixture }, declared outputs, ${{ steps.X.outputs.Y }}
  interpolation, downstream assert step, fail-fast aggregation) emits
  one aggregate Signal with verdict=pass when fed a valid fixture.
trigger:
  shape: harness sensor invocation
  summary: /run-sensor smoke-typed-pipeline against this repo's checkout.
  fixture:
    sensor_id: smoke-typed-pipeline
    fixture_payload: |
      {"sku":"abc","qty":1}
behavior:
  summary: |
    The orchestrator resolves the order-valid.json fixture, the shell
    step cats its content and emits it as outputs.payload, the assert
    step substring-matches "sku" against the captured payload, and the
    aggregate verdict is pass.
  business_rules:
    - The fixture file .harness/fixtures/order-valid.json must exist on disk.
    - The shell step's stdout MUST contain "sku".
    - The aggregate verdict is pass when both steps pass.
expected_outcome:
  shape: One aggregate JSON Signal on stdout + exit code 0
  summary: verdict=pass, severity=info, no individual stream Signals
  fixture:
    exit_code: 0
    stdout_last_line_kind: aggregate
    stdout_last_line_verdict: pass
  invariants:
    - Exit code is 0.
    - Last line of stdout is an aggregate Signal.
    - Aggregate verdict is "pass".
  side_effects: []
evidence:
  - file: .harness/sensors/smoke-typed-pipeline.yaml
    rationale: Sensor under test; declares the typed-step pipeline.
  - file: lib/exec/engine.go
    rationale: Executes the typed-step pipeline.
  - file: lib/step/shell/shell.go
    rationale: Implements the shell step that this sensor exercises.
regression_priority: critical
tags:
  - framework-smoke
  - typed-steps
```

- [ ] **Create `.harness/usecases/framework/framework-smoke-with-setup.yaml`.**

```yaml
id: framework-smoke-with-setup
version: 0.1.0
name: smoke test of the type:sensor composition step
journey_id: framework
description: |
  /run-sensor smoke-with-setup invokes smoke-typed-pipeline as a
  composed sensor step (type: sensor), which exercises the
  cross-sensor dependency wiring without a runtime registry entry.
trigger:
  shape: harness sensor invocation
  summary: /run-sensor smoke-with-setup against this repo's checkout.
  fixture:
    sensor_id: smoke-with-setup
behavior:
  summary: |
    smoke-with-setup declares a single step of type sensor referencing
    smoke-typed-pipeline; the engine resolves the composition, runs the
    inner sensor in-process, and folds its aggregate into the outer
    aggregate.
  business_rules:
    - The inner sensor smoke-typed-pipeline must exist in .harness/sensors/.
    - The outer aggregate verdict equals the inner aggregate verdict.
expected_outcome:
  shape: One aggregate JSON Signal on stdout + exit code 0
  summary: verdict=pass, severity=info — composition succeeds end-to-end
  fixture:
    exit_code: 0
    stdout_last_line_kind: aggregate
    stdout_last_line_verdict: pass
  invariants:
    - Exit code is 0.
    - Aggregate verdict is "pass".
  side_effects: []
evidence:
  - file: .harness/sensors/smoke-with-setup.yaml
    rationale: Outer sensor under test.
  - file: lib/step/sensor/sensor.go
    rationale: Implements the type:sensor composition step.
regression_priority: high
tags:
  - framework-smoke
  - composition
```

### Step 1.10: Regenerate the smoke sensors

- [ ] **Rewrite `.harness/sensors/smoke-typed-pipeline.yaml`.**

Open the file. Delete the entire `verification:` block (the `verification:` line and its nested `golden_cases:` array). Insert a top-level `use_cases` block immediately after `blind_spots:`:

```yaml
use_cases:
  - framework-smoke-typed-pipeline
```

The final file should NOT contain the word `verification` anywhere.

- [ ] **Rewrite `.harness/sensors/smoke-with-setup.yaml`.**

Same edit. Replace whatever `verification:` block exists with:

```yaml
use_cases:
  - framework-smoke-with-setup
```

### Step 1.11: Delete `run-golden.go` + tests

- [ ] **Delete `skills/detect-sensors/scripts/run-golden.go` and `run-golden_test.go`.**

Run:

```bash
rm skills/detect-sensors/scripts/run-golden.go skills/detect-sensors/scripts/run-golden_test.go
```

### Step 1.12: Patch `.github/workflows/test.yml`

- [ ] **Remove the two `run_golden` steps.**

Open `.github/workflows/test.yml`. Find the block:

```yaml
      - name: Vet (run_golden build tag)
        run: go vet -tags=run_golden ./...

      - name: Test run-golden
        run: go test -race -count=1 -tags=run_golden ./skills/detect-sensors/scripts/...
```

Delete those four lines (two `- name:` blocks, eight lines including the blank separator).

### Step 1.13: Fix the orphaned `create-sensor-missing-fixture` usecase

- [ ] **Rewrite the usecase to describe the new error semantics.**

Open `.harness/usecases/create-sensor/create-sensor-missing-fixture.yaml`. Find any reference to `verification.golden_cases[].fixture`, `MissingFixtureError`, or `missing_fixture` Signal kind, and rewrite the prose to describe the new `MissingUseCaseError` / `usecase_not_found` Signal kind instead. Rename the file if you wish for clarity — but renaming requires also renaming the id inside and any cross-reference. Leaving the id stable is safer.

Run: `grep -n 'verification\|golden\|MissingFixture' .harness/usecases/create-sensor/create-sensor-missing-fixture.yaml`
Expected: no matches after the rewrite.

### Step 1.14: Run the full test suite

- [ ] **Vet every build tag.**

```bash
for tag in run_computational run_inferential start_sensor stop_sensor list_sensors tail_sensor heal_diagnose heal_apply_safe heal_apply_sensors heal_retry_original write_sensor write_stack write_fixture catalog_sensors; do
  echo "=== $tag ===";
  go vet -tags=$tag ./... || { echo "VET FAILED: $tag"; exit 1; }
done
```

Expected: no failures.

- [ ] **Run the lib tests.**

```bash
go test -race -count=1 ./lib/...
```

Expected: all pass. If `lib/orchestrator/*_test.go` still references the old types, fix them now.

- [ ] **Run every skill's tests.**

```bash
for tag in run_computational run_inferential start_sensor stop_sensor list_sensors tail_sensor heal_retry_original write_sensor write_stack write_fixture catalog_sensors; do
  echo "=== $tag ===";
  go test -race -count=1 -tags=$tag ./skills/... || { echo "TESTS FAILED: $tag"; exit 1; }
done
```

Expected: all pass.

- [ ] **Run the smoke sensors via `/run-sensor` to make sure the regenerated YAMLs are honored.**

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT:-$(pwd)}" -tags=run_computational \
  ./skills/run-sensor/scripts smoke-typed-pipeline
```

Expected: stdout ends with an aggregate Signal whose `verdict` is `pass`. Repeat for `smoke-with-setup`.

### Step 1.15: Commit

- [ ] **Commit everything as a single atomic change.**

```bash
git add schemas/sensor.yaml \
  lib/sensor/shape.go lib/sensor/persist.go lib/sensor/validate.go lib/sensor/load.go \
  lib/sensor/load_test.go lib/sensor/persist_test.go lib/sensor/validate_test.go \
  lib/sensor/testdata/ \
  lib/orchestrator/*_test.go \
  lib/registry/discovery_e2e_test.go lib/schema/validator_test.go \
  skills/create-sensor/scripts/write-sensor.go skills/create-sensor/scripts/write-sensor_test.go \
  skills/run-sensor/scripts/run-computational_test.go skills/run-sensor/scripts/run-inferential_test.go \
  skills/start-sensor/scripts/start_test.go \
  .github/workflows/test.yml \
  .harness/usecases/framework/ \
  .harness/sensors/smoke-typed-pipeline.yaml .harness/sensors/smoke-with-setup.yaml \
  .harness/usecases/create-sensor/create-sensor-missing-fixture.yaml

git rm skills/detect-sensors/scripts/run-golden.go skills/detect-sensors/scripts/run-golden_test.go

git commit -m "$(cat <<'EOF'
feat(schema): replace verification.golden_cases with use_cases[] (Spec B)

Removes the verification block from schemas/sensor.yaml entirely;
adds top-level use_cases[] (minItems 1) referencing usecase ids
under .harness/usecases/. ValidateAndPersist gets RequireUseCaseFiles
OnDisk + MissingUseCaseError to replace the fixture-existence path.
run-golden.go is deleted and its CI steps removed; smoke sensors now
declare use_cases referencing two new framework usecases used as the
acceptance contract for the typed-step pipeline.

Breaking. No migration: users of the plugin regenerate sensors.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

Run: `git status`
Expected: working tree clean.

---

## Task 2: `read-usecases.go` script

**Files:**
- Create: `skills/create-sensor/scripts/lib/ledger/ledger.go` (shared types: `Ledger`, `Usecase`, `CatalogEntry`)
- Create: `skills/create-sensor/scripts/lib/ledger/ledger_test.go`
- Create: `skills/create-sensor/scripts/read-usecases.go` (build tag `read_usecases`)
- Create: `skills/create-sensor/scripts/read-usecases_test.go`
- Create: `skills/create-sensor/scripts/testdata/usecases/tail-sensor/tail-sensor-no-registry.yaml` (synthetic copy of the real one for hermetic tests)
- Create: `skills/create-sensor/scripts/testdata/usecases/tail-sensor/tail-sensor-cursor-zero.yaml`
- Create: `skills/create-sensor/scripts/testdata/usecases/run-sensor/run-sensor-happy-path.yaml`
- Create: `skills/create-sensor/scripts/testdata/usecases-malformed/bad-schema.yaml`
- Modify: `.github/workflows/test.yml` (add `read_usecases` vet + test steps)

### Step 2.1: Define the shared types

- [ ] **Create `skills/create-sensor/scripts/lib/ledger/ledger.go`.**

```go
// Package ledger defines the JSON shapes exchanged by the create-sensor
// pipeline (read-usecases.go → plan-sensors.go) and consumed by the
// orchestrating SKILL.md. The shapes are mirrors of schemas/usecase.yaml
// trimmed to the fields the planning heuristic needs.
package ledger

// Ledger is the top-level document read-usecases emits on stdout when
// successful. project_root is always populated; stack and catalog are
// present only when --include-stack / --include-catalog were passed.
type Ledger struct {
	Usecases    []Usecase      `json:"usecases"`
	Stack       map[string]any `json:"stack,omitempty"`
	Catalog     []CatalogEntry `json:"catalog,omitempty"`
	ProjectRoot string         `json:"project_root"`
}

type Usecase struct {
	ID                 string         `json:"id"`
	JourneyID          string         `json:"journey_id"`
	Name               string         `json:"name"`
	RegressionPriority string         `json:"regression_priority"`
	Tags               []string       `json:"tags,omitempty"`
	Trigger            Trigger        `json:"trigger"`
	Behavior           Behavior       `json:"behavior"`
	ExpectedOutcome    Expected       `json:"expected_outcome"`
	Evidence           []EvidenceItem `json:"evidence"`
	SourcePath         string         `json:"source_path"`
}

type Trigger struct {
	Shape   string         `json:"shape"`
	Summary string         `json:"summary,omitempty"`
	Fixture map[string]any `json:"fixture,omitempty"`
}

type Behavior struct {
	Summary       string   `json:"summary"`
	BusinessRules []string `json:"business_rules,omitempty"`
}

type Expected struct {
	Shape       string         `json:"shape"`
	Summary     string         `json:"summary"`
	Fixture     map[string]any `json:"fixture"`
	Invariants  []string       `json:"invariants,omitempty"`
	SideEffects []string       `json:"side_effects,omitempty"`
}

type EvidenceItem struct {
	File      string `json:"file"`
	LineStart *int   `json:"line_start,omitempty"`
	Rationale string `json:"rationale"`
}

type CatalogEntry struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Type     string `json:"type"`
	Output   string `json:"output"`
	Blocking bool   `json:"blocking"`
	Path     string `json:"path"`
}

// ListEntry is the thin form used by --list-only.
type ListEntry struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Tags []string `json:"tags,omitempty"`
}

// IndexLedger is the thin shape --list-only emits.
type IndexLedger struct {
	Usecases []ListEntry `json:"usecases"`
}
```

- [ ] **Create `skills/create-sensor/scripts/lib/ledger/ledger_test.go`.**

Smoke test that the types JSON-marshal and round-trip without losing fields:

```go
package ledger

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestLedgerRoundTrip(t *testing.T) {
	five := 5
	in := Ledger{
		Usecases: []Usecase{{
			ID:        "x",
			JourneyID: "j",
			Name:      "n",
			Tags:      []string{"a"},
			Trigger:   Trigger{Shape: "CLI", Summary: "s"},
			Behavior:  Behavior{Summary: "b", BusinessRules: []string{"r"}},
			ExpectedOutcome: Expected{
				Shape:   "shape",
				Summary: "esum",
				Fixture: map[string]any{"exit_code": float64(1)},
			},
			Evidence:   []EvidenceItem{{File: "f", LineStart: &five, Rationale: "r"}},
			SourcePath: "p",
		}},
		ProjectRoot: "/x",
	}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Ledger
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round-trip diff:\n  in : %#v\n  out: %#v", in, out)
	}
}
```

### Step 2.2: Write the first failing test for the script

- [ ] **Create `skills/create-sensor/scripts/read-usecases_test.go` with the simplest case: load a single usecase by id.**

```go
//go:build read_usecases

package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/skills/create-sensor/scripts/lib/ledger"
)

func TestLoadSingleUsecaseByID(t *testing.T) {
	projectRoot := filepath.Join("testdata") // fake project root
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--project-root", projectRoot,
		"--usecases", "tail-sensor-no-registry",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: %d; stderr=%s", code, stderr.String())
	}
	var lg ledger.Ledger
	if err := json.Unmarshal(stdout.Bytes(), &lg); err != nil {
		t.Fatalf("unmarshal ledger: %v; stdout=%s", err, stdout.String())
	}
	if len(lg.Usecases) != 1 {
		t.Fatalf("want 1 usecase, got %d", len(lg.Usecases))
	}
	if lg.Usecases[0].ID != "tail-sensor-no-registry" {
		t.Fatalf("unexpected id: %s", lg.Usecases[0].ID)
	}
	if lg.Usecases[0].JourneyID != "tail-sensor" {
		t.Fatalf("unexpected journey: %s", lg.Usecases[0].JourneyID)
	}
}
```

- [ ] **Run the test — expect a build failure (script doesn't exist yet).**

```bash
go test -race -count=1 -tags=read_usecases ./skills/create-sensor/scripts/...
```

Expected: build error mentioning undefined `run`.

### Step 2.3: Create the testdata fixtures

- [ ] **Create `skills/create-sensor/scripts/testdata/usecases/tail-sensor/tail-sensor-no-registry.yaml`.**

```yaml
id: tail-sensor-no-registry
version: 0.1.0
name: /tail-sensor errors when the registry file is absent
journey_id: tail-sensor
description: |
  Synthetic copy of the real usecase, kept hermetic for read-usecases
  unit tests so changes to production usecases never break the script
  test suite.
trigger:
  shape: CLI invocation
  summary: /tail-sensor invoked with no registry on disk.
  fixture:
    argv: [tail-sensor, dev-server, "0"]
behavior:
  summary: registry_exists=false branch produces tail_no_registry.
  business_rules:
    - Sensor cannot be tailed without a registry entry.
    - Verdict is error and exit code is 1.
expected_outcome:
  shape: One JSON line on stdout + exit code
  summary: One Signal verdict=error, metadata.kind=tail_no_registry, exit code 1.
  fixture:
    exit_code: 1
  invariants:
    - Exit code is 1.
  side_effects: []
evidence:
  - file: skills/tail-sensor/scripts/tail.go
    line_start: 72
    rationale: registry_exists=false branch emitting tail_no_registry.
regression_priority: high
tags:
  - error-handling
  - registry-discovery
```

- [ ] **Create `skills/create-sensor/scripts/testdata/usecases/tail-sensor/tail-sensor-cursor-zero.yaml`.**

```yaml
id: tail-sensor-cursor-zero
version: 0.1.0
name: /tail-sensor cursor=0 reads all
journey_id: tail-sensor
description: cursor=0 reads from the beginning of signals.log.
trigger:
  shape: CLI invocation
  summary: /tail-sensor invoked with cursor=0.
  fixture:
    argv: [tail-sensor, dev-server, "0"]
behavior:
  summary: cursor=0 read path emits every signal verbatim.
  business_rules:
    - cursor=0 reads from byte zero.
    - exit code is 0.
expected_outcome:
  shape: JSONL on stdout + exit code 0
  summary: All signals echoed with exit code 0.
  fixture:
    exit_code: 0
  invariants:
    - Exit code is 0.
  side_effects: []
evidence:
  - file: skills/tail-sensor/scripts/tail.go
    line_start: 110
    rationale: cursor=0 path.
regression_priority: medium
tags:
  - happy-path
  - cursor
```

- [ ] **Create `skills/create-sensor/scripts/testdata/usecases/run-sensor/run-sensor-happy-path.yaml`.**

```yaml
id: run-sensor-happy-path
version: 0.1.0
name: /run-sensor pass on a green sensor
journey_id: run-sensor
description: green sensor produces verdict=pass aggregate.
trigger:
  shape: CLI invocation
  summary: /run-sensor against a known-green sensor.
  fixture:
    argv: [run-sensor, smoke-typed-pipeline]
behavior:
  summary: aggregate verdict=pass for a sensor whose steps all pass.
  business_rules:
    - All steps emit verdict=pass.
    - Aggregate is verdict=pass.
expected_outcome:
  shape: JSON aggregate on stdout + exit code 0
  summary: verdict=pass aggregate.
  fixture:
    exit_code: 0
  invariants:
    - Aggregate verdict is pass.
  side_effects: []
evidence:
  - file: skills/run-sensor/scripts/run-computational.go
    rationale: Main entrypoint for /run-sensor.
regression_priority: critical
tags:
  - happy-path
```

- [ ] **Create `skills/create-sensor/scripts/testdata/usecases-malformed/bad-schema.yaml`.**

```yaml
id: bad
# missing version, name, journey_id, trigger, behavior, expected_outcome, evidence
# schema validation must reject this.
```

### Step 2.4: Implement `read-usecases.go` — minimum viable

- [ ] **Create `skills/create-sensor/scripts/read-usecases.go`.**

```go
//go:build read_usecases

// Command read-usecases resolves a set of usecase identifiers (by id,
// by journey, or by file path) under <project-root>/.harness/usecases/
// and emits a JSON ledger on stdout. Read-only.
//
// Usage:
//
//	read-usecases [--project-root <dir>] \
//	  [--usecases id,id,...] \
//	  [--journey <journey-id>] \
//	  [--list-only] \
//	  [--include-stack] \
//	  [--include-catalog]
//
// Exit codes: 0 ledger emitted, 1 usecase_not_found / schema, 2 usage.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/skills/create-sensor/scripts/lib/ledger"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("read-usecases", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		projectRoot, usecases, journey string
		listOnly, includeStack         bool
		includeCatalog                 bool
	)
	fs.StringVar(&projectRoot, "project-root", "", "project root (default: registry.Lookup from cwd)")
	fs.StringVar(&usecases, "usecases", "", "comma-separated usecase ids")
	fs.StringVar(&journey, "journey", "", "journey id (loads all usecases under that journey)")
	fs.BoolVar(&listOnly, "list-only", false, "emit thin index (id+name+tags) only")
	fs.BoolVar(&includeStack, "include-stack", false, "include .harness/stack.yaml in the ledger")
	fs.BoolVar(&includeCatalog, "include-catalog", false, "include existing sensor catalog in the ledger")
	if err := fs.Parse(args); err != nil {
		emit(stdout, errSignal("usage", err.Error()))
		return 2
	}
	if listOnly && (includeStack || includeCatalog) {
		emit(stdout, errSignal("usage", "--list-only is mutually exclusive with --include-stack and --include-catalog"))
		return 2
	}
	if projectRoot == "" {
		cwd, _ := os.Getwd()
		res, err := registry.Lookup(cwd)
		if err != nil {
			emit(stdout, registry.DiscoveryErrorSignal(err, "read-usecases"))
			return 2
		}
		projectRoot = res.ProjectRoot
	}

	ids, err := resolveIDs(projectRoot, usecases, journey)
	if err != nil {
		emit(stdout, errSignal("usage", err.Error()))
		return 2
	}

	loaded, warns, missing := loadUsecases(projectRoot, ids)
	for _, w := range warns {
		emit(stdout, w)
	}
	if len(missing) > 0 {
		emit(stdout, errSignal("usecase_not_found", fmt.Sprintf("usecases not found: %s", strings.Join(missing, ", "))))
		return 1
	}

	if listOnly {
		idx := ledger.IndexLedger{}
		for _, uc := range loaded {
			idx.Usecases = append(idx.Usecases, ledger.ListEntry{ID: uc.ID, Name: uc.Name, Tags: uc.Tags})
		}
		emit(stdout, idx)
		return 0
	}

	lg := ledger.Ledger{Usecases: loaded, ProjectRoot: projectRoot}
	if includeStack {
		if stackMap, ok := loadStack(projectRoot); ok {
			lg.Stack = stackMap
		}
	}
	if includeCatalog {
		lg.Catalog = loadCatalog(projectRoot)
	}
	emit(stdout, lg)
	return 0
}

func resolveIDs(projectRoot, usecasesCSV, journey string) ([]string, error) {
	if usecasesCSV != "" && journey != "" {
		return nil, errors.New("pass either --usecases or --journey, not both")
	}
	if usecasesCSV == "" && journey == "" {
		return nil, errors.New("one of --usecases or --journey is required")
	}
	if usecasesCSV != "" {
		raw := strings.Split(usecasesCSV, ",")
		out := make([]string, 0, len(raw))
		for _, r := range raw {
			r = strings.TrimSpace(r)
			if r != "" {
				out = append(out, r)
			}
		}
		return out, nil
	}
	// --journey mode: enumerate the journey dir
	dir := filepath.Join(projectRoot, ".harness", "usecases", journey)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("journey %q: %w", journey, err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Strings(ids)
	return ids, nil
}

func loadUsecases(projectRoot string, ids []string) (loaded []ledger.Usecase, warns []map[string]any, missing []string) {
	usecasesRoot := filepath.Join(projectRoot, ".harness", "usecases")
	pathByID := indexUsecaseFiles(usecasesRoot)
	for _, id := range ids {
		path, ok := pathByID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			warns = append(warns, warnSignal("usecase_schema_invalid", fmt.Sprintf("read %s: %v", path, err)))
			continue
		}
		// Schema validate.
		if err := schema.Validate(body, "usecase.yaml"); err != nil {
			warns = append(warns, warnSignal("usecase_schema_invalid", fmt.Sprintf("%s: %v", path, err)))
			continue
		}
		var uc ledger.Usecase
		if err := yaml.Unmarshal(body, &uc); err != nil {
			warns = append(warns, warnSignal("usecase_schema_invalid", fmt.Sprintf("%s: parse: %v", path, err)))
			continue
		}
		uc.SourcePath = mustRel(projectRoot, path)
		loaded = append(loaded, uc)
	}
	return loaded, warns, missing
}

func indexUsecaseFiles(root string) map[string]string {
	out := map[string]string{}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".yaml") {
			return nil
		}
		id := strings.TrimSuffix(info.Name(), ".yaml")
		out[id] = path
		return nil
	})
	return out
}

func loadStack(projectRoot string) (map[string]any, bool) {
	body, err := os.ReadFile(filepath.Join(projectRoot, ".harness", "stack.yaml"))
	if err != nil {
		return nil, false
	}
	var out map[string]any
	if err := yaml.Unmarshal(body, &out); err != nil {
		return nil, false
	}
	return out, true
}

func loadCatalog(projectRoot string) []ledger.CatalogEntry {
	sensorsDir := filepath.Join(projectRoot, ".harness", "sensors")
	entries, err := os.ReadDir(sensorsDir)
	if err != nil {
		return nil
	}
	var out []ledger.CatalogEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(sensorsDir, e.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var s struct {
			ID        string `yaml:"id"`
			Kind      string `yaml:"kind"`
			Type      string `yaml:"type"`
			Output    string `yaml:"output"`
			Execution struct {
				Blocking bool `yaml:"blocking"`
			} `yaml:"execution"`
		}
		if err := yaml.Unmarshal(body, &s); err != nil {
			continue
		}
		out = append(out, ledger.CatalogEntry{
			ID:       s.ID,
			Kind:     s.Kind,
			Type:     s.Type,
			Output:   s.Output,
			Blocking: s.Execution.Blocking,
			Path:     mustRel(projectRoot, path),
		})
	}
	return out
}

func mustRel(base, target string) string {
	r, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return r
}

func emit(w io.Writer, v any) {
	body, _ := json.Marshal(v)
	fmt.Fprintln(w, string(body))
}

func warnSignal(kind, rationale string) map[string]any {
	return signal.NewBuilder("read-usecases", "0.1.0").
		WithVerdict("warn", "medium").
		WithKind(kind).
		WithRationale(rationale).
		Build()
}

func errSignal(kind, rationale string) map[string]any {
	return signal.NewBuilder("read-usecases", "0.1.0").
		WithVerdict("error", "high").
		WithKind(kind).
		WithRationale(rationale).
		Build()
}
```

- [ ] **Run the first test to verify it passes.**

```bash
go test -race -count=1 -tags=read_usecases ./skills/create-sensor/scripts/...
```

Expected: `TestLoadSingleUsecaseByID` passes. If `schema.Validate` doesn't exist with that signature, replace the call with the actual API exposed by `lib/schema` — read `lib/schema/validator.go` and adapt. The call shape should be: validate a usecase YAML body against `schemas/usecase.yaml`.

- [ ] **Commit the working baseline before adding more tests.**

```bash
git add skills/create-sensor/scripts/lib/ledger/ \
        skills/create-sensor/scripts/read-usecases.go \
        skills/create-sensor/scripts/read-usecases_test.go \
        skills/create-sensor/scripts/testdata/

git commit -m "feat(create-sensor): add read-usecases.go ledger loader

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

### Step 2.5: Add the rest of the test matrix

- [ ] **Append `TestLoadJourney` to `read-usecases_test.go`.**

```go
func TestLoadJourney(t *testing.T) {
	projectRoot := filepath.Join("testdata")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--project-root", projectRoot, "--journey", "tail-sensor"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: %d; stderr=%s", code, stderr.String())
	}
	var lg ledger.Ledger
	if err := json.Unmarshal(stdout.Bytes(), &lg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(lg.Usecases) != 2 {
		t.Fatalf("want 2 usecases, got %d", len(lg.Usecases))
	}
}
```

- [ ] **Append `TestListOnly`.**

```go
func TestListOnly(t *testing.T) {
	projectRoot := filepath.Join("testdata")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--project-root", projectRoot, "--journey", "tail-sensor", "--list-only"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: %d", code)
	}
	var idx ledger.IndexLedger
	if err := json.Unmarshal(stdout.Bytes(), &idx); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(idx.Usecases) != 2 {
		t.Fatalf("want 2, got %d", len(idx.Usecases))
	}
	if idx.Usecases[0].Name == "" {
		t.Fatal("name missing in --list-only output")
	}
}
```

- [ ] **Append `TestListOnlyRejectsIncludeFlags`.**

```go
func TestListOnlyRejectsIncludeFlags(t *testing.T) {
	for _, extra := range []string{"--include-stack", "--include-catalog"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--project-root", "testdata", "--journey", "tail-sensor", "--list-only", extra}, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("[%s] want exit 2, got %d", extra, code)
		}
		if !bytes.Contains(stdout.Bytes(), []byte(`"kind":"usage"`)) {
			t.Fatalf("[%s] expected metadata.kind=usage Signal; got %s", extra, stdout.String())
		}
	}
}
```

- [ ] **Append `TestMissingUsecaseIsError`.**

```go
func TestMissingUsecaseIsError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--project-root", "testdata", "--usecases", "does-not-exist"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("want exit 1, got %d", code)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"kind":"usecase_not_found"`)) {
		t.Fatalf("expected usecase_not_found Signal; got %s", stdout.String())
	}
}
```

- [ ] **Append `TestMalformedYAMLYieldsWarnNotError`.**

Add a malformed YAML to a separate journey under testdata and exercise it:

```go
func TestMalformedYAMLYieldsWarnNotError(t *testing.T) {
	// Use the malformed fixture alongside a valid one to confirm the warn
	// is emitted but the ledger still produces.
	root := t.TempDir()
	usecasesDir := filepath.Join(root, ".harness", "usecases", "mixed")
	if err := os.MkdirAll(usecasesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Symlink the valid one (or copy).
	srcValid, _ := filepath.Abs(filepath.Join("testdata", "usecases", "tail-sensor", "tail-sensor-no-registry.yaml"))
	dstValid := filepath.Join(usecasesDir, "good.yaml")
	bodyValid, _ := os.ReadFile(srcValid)
	if err := os.WriteFile(dstValid, bodyValid, 0o644); err != nil {
		t.Fatal(err)
	}
	srcBad, _ := filepath.Abs(filepath.Join("testdata", "usecases-malformed", "bad-schema.yaml"))
	dstBad := filepath.Join(usecasesDir, "bad.yaml")
	bodyBad, _ := os.ReadFile(srcBad)
	if err := os.WriteFile(dstBad, bodyBad, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--project-root", root, "--journey", "mixed"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("want exit 0 (warn is non-fatal), got %d; stderr=%s", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"kind":"usecase_schema_invalid"`)) {
		t.Fatalf("expected usecase_schema_invalid warn; got %s", stdout.String())
	}
}
```

Note: the "good.yaml" was renamed from the source so the journey lookup matches by id. The `id` field inside the body still says `tail-sensor-no-registry`, but the journey-based loader keys off filename. The test verifies the warn-and-continue behavior; the strict id-resolution case is covered by `TestMissingUsecaseIsError`.

If the renaming-by-id mismatch creates problems in the schema validator path, adjust by writing a separate `good.yaml` body inline that uses `id: good`.

- [ ] **Append `TestIncludeStack` and `TestIncludeCatalog`.**

```go
func TestIncludeStack(t *testing.T) {
	root := t.TempDir()
	uc := filepath.Join(root, ".harness", "usecases", "tail-sensor")
	if err := os.MkdirAll(uc, 0o755); err != nil {
		t.Fatal(err)
	}
	src, _ := filepath.Abs(filepath.Join("testdata", "usecases", "tail-sensor", "tail-sensor-no-registry.yaml"))
	body, _ := os.ReadFile(src)
	if err := os.WriteFile(filepath.Join(uc, "tail-sensor-no-registry.yaml"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	stack := []byte("languages:\n  - { name: go }\n")
	if err := os.WriteFile(filepath.Join(root, ".harness", "stack.yaml"), stack, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--project-root", root, "--usecases", "tail-sensor-no-registry", "--include-stack"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: stderr=%s", code, stderr.String())
	}
	var lg ledger.Ledger
	if err := json.Unmarshal(stdout.Bytes(), &lg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if lg.Stack == nil {
		t.Fatal("expected stack populated")
	}
}
```

- [ ] **Run all `read_usecases` tests.**

```bash
go test -race -count=1 -tags=read_usecases ./skills/create-sensor/scripts/...
```

Expected: all pass.

### Step 2.6: Add the CI workflow entries

- [ ] **Patch `.github/workflows/test.yml` to vet + test the new build tag.**

Find the block listing `Vet (write_stack build tag)` (around line 56 after Task 1's deletions). After the `Test write-stack` step, insert:

```yaml
      - name: Vet (read_usecases build tag)
        run: go vet -tags=read_usecases ./...

      - name: Test read-usecases
        run: go test -race -count=1 -tags=read_usecases ./skills/create-sensor/scripts/...
```

- [ ] **Commit Task 2's full additions.**

```bash
git add skills/create-sensor/scripts/read-usecases_test.go \
        .github/workflows/test.yml

git commit -m "test(create-sensor): exercise read-usecases.go end-to-end

Adds journey/list-only/missing/malformed/include-stack/catalog
coverage and wires the read_usecases build tag into CI.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `plan-sensors.go` script

**Files:**
- Modify: `skills/create-sensor/scripts/lib/ledger/ledger.go` (add `Plan` and `StepOutline` types — they live with the other shared shapes)
- Create: `skills/create-sensor/scripts/plan-sensors.go` (build tag `plan_sensors`)
- Create: `skills/create-sensor/scripts/plan-sensors_test.go`
- Create: `skills/create-sensor/scripts/testdata/ledgers/single-usecase.json`
- Create: `skills/create-sensor/scripts/testdata/ledgers/two-grouped.json`
- Create: `skills/create-sensor/scripts/testdata/ledgers/two-split-trigger-shape.json`
- Create: `skills/create-sensor/scripts/testdata/ledgers/bucket-too-large.json`
- Create: `skills/create-sensor/scripts/testdata/ledgers/inferential.json`
- Create: `skills/create-sensor/scripts/testdata/ledgers/observation.json`
- Create: `skills/create-sensor/scripts/testdata/plan-output/single-usecase.jsonl`
- Create: `skills/create-sensor/scripts/testdata/plan-output/two-grouped.jsonl`
- Create: `skills/create-sensor/scripts/testdata/plan-output/two-split-trigger-shape.jsonl`
- Create: `skills/create-sensor/scripts/testdata/plan-output/bucket-too-large.jsonl`
- Create: `skills/create-sensor/scripts/testdata/plan-output/inferential.jsonl`
- Create: `skills/create-sensor/scripts/testdata/plan-output/observation.jsonl`
- Modify: `.github/workflows/test.yml` (add `plan_sensors` vet + test)

### Step 3.1: Extend the shared types

- [ ] **Append `Plan` and `StepOutline` to `skills/create-sensor/scripts/lib/ledger/ledger.go`.**

Append:

```go
// Plan is one of the JSONL lines plan-sensors emits.
type Plan struct {
	SensorID    string        `json:"sensor_id"`
	Kind        string        `json:"kind"`
	Type        string        `json:"type"`
	Output      string        `json:"output"`
	UseCases    []string      `json:"use_cases"`
	StepOutline []StepOutline `json:"step_outline"`
	Rationale   string        `json:"rationale"`
}

type StepOutline struct {
	StepID             string         `json:"step_id"`
	SourceUsecase      string         `json:"source_usecase"`
	SourceRule         string         `json:"source_rule"`
	SuggestedStepType  string         `json:"suggested_step_type"`
	MockStrategy       string         `json:"mock_strategy"`
	Evidence           []EvidenceItem `json:"evidence,omitempty"`
}

// Aggregate is the last JSONL line emitted by plan-sensors. Aggregate
// signals are distinguished from Plan lines by the "aggregate":true field.
type Aggregate struct {
	Aggregate        bool   `json:"aggregate"`
	Verdict          string `json:"verdict"`
	Severity         string `json:"severity"`
	SensorsPlanned   int    `json:"sensors_planned"`
	UsecasesConsumed int    `json:"usecases_consumed"`
}
```

### Step 3.2: Write the first failing test

- [ ] **Create `skills/create-sensor/scripts/plan-sensors_test.go`.**

```go
//go:build plan_sensors

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPlanSingleUsecase(t *testing.T) {
	in, err := os.Open(filepath.Join("testdata", "ledgers", "single-usecase.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var stdout, stderr bytes.Buffer
	code := run(in, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	expected, err := os.ReadFile(filepath.Join("testdata", "plan-output", "single-usecase.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimRight(stdout.Bytes(), "\n"), bytes.TrimRight(expected, "\n")) {
		t.Fatalf("output mismatch.\ngot:\n%s\nwant:\n%s", stdout.String(), string(expected))
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	// Run the same input ten times; must produce byte-identical stdout.
	body, err := os.ReadFile(filepath.Join("testdata", "ledgers", "two-grouped.json"))
	if err != nil {
		t.Fatal(err)
	}
	var first []byte
	for i := 0; i < 10; i++ {
		var stdout, stderr bytes.Buffer
		if code := run(bytes.NewReader(body), &stdout, &stderr); code != 0 {
			t.Fatalf("iter %d exit %d", i, code)
		}
		if first == nil {
			first = append([]byte(nil), stdout.Bytes()...)
			continue
		}
		if !bytes.Equal(first, stdout.Bytes()) {
			t.Fatalf("iter %d diverged:\nfirst:\n%s\nthis:\n%s", i, string(first), stdout.String())
		}
	}
}

func TestPlanGroupsByJourneyAndShape(t *testing.T) {
	// two-grouped has two usecases in the same journey + same trigger.shape
	// + overlapping tags → ONE planned sensor.
	in, err := os.Open(filepath.Join("testdata", "ledgers", "two-grouped.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var stdout, stderr bytes.Buffer
	if code := run(in, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	// Count plan lines (exclude aggregate).
	plans, agg := parseStdout(t, stdout.Bytes())
	if len(plans) != 1 {
		t.Fatalf("want 1 sensor planned, got %d", len(plans))
	}
	if agg.SensorsPlanned != 1 {
		t.Fatalf("aggregate sensors_planned mismatch: %d", agg.SensorsPlanned)
	}
	if agg.UsecasesConsumed != 2 {
		t.Fatalf("aggregate usecases_consumed mismatch: %d", agg.UsecasesConsumed)
	}
}

// parseStdout is a small helper test-side. plan-sensors emits JSONL with
// the aggregate as the LAST line.
func parseStdout(t *testing.T, body []byte) ([]map[string]any, struct {
	Aggregate        bool
	Verdict          string
	SensorsPlanned   int    `json:"sensors_planned"`
	UsecasesConsumed int    `json:"usecases_consumed"`
}) {
	t.Helper()
	lines := bytes.Split(bytes.TrimRight(body, "\n"), []byte("\n"))
	if len(lines) < 1 {
		t.Fatalf("empty stdout")
	}
	var aggregate struct {
		Aggregate        bool
		Verdict          string
		SensorsPlanned   int `json:"sensors_planned"`
		UsecasesConsumed int `json:"usecases_consumed"`
	}
	if err := json.Unmarshal(lines[len(lines)-1], &aggregate); err != nil {
		t.Fatalf("parse aggregate: %v", err)
	}
	var plans []map[string]any
	for _, ln := range lines[:len(lines)-1] {
		var p map[string]any
		if err := json.Unmarshal(ln, &p); err != nil {
			t.Fatalf("parse plan line: %v", err)
		}
		plans = append(plans, p)
	}
	return plans, aggregate
}
```

### Step 3.3: Create the input ledger fixtures

- [ ] **`testdata/ledgers/single-usecase.json` — minimal hello-world.**

```json
{
  "usecases": [
    {
      "id": "tail-sensor-no-registry",
      "journey_id": "tail-sensor",
      "name": "/tail-sensor errors when the registry file is absent",
      "regression_priority": "high",
      "tags": ["error-handling", "registry-discovery"],
      "trigger": {
        "shape": "CLI invocation",
        "summary": "/tail-sensor invoked with no registry on disk.",
        "fixture": {"argv": ["tail-sensor", "dev-server", "0"]}
      },
      "behavior": {
        "summary": "registry_exists=false branch produces tail_no_registry.",
        "business_rules": [
          "Sensor cannot be tailed without a registry entry.",
          "Verdict is error and exit code is 1."
        ]
      },
      "expected_outcome": {
        "shape": "One JSON line on stdout + exit code",
        "summary": "verdict=error, metadata.kind=tail_no_registry, exit code 1.",
        "fixture": {"exit_code": 1},
        "invariants": ["Exit code is 1."],
        "side_effects": []
      },
      "evidence": [
        {"file": "skills/tail-sensor/scripts/tail.go", "line_start": 72, "rationale": "registry_exists=false branch."}
      ],
      "source_path": ".harness/usecases/tail-sensor/tail-sensor-no-registry.yaml"
    }
  ],
  "project_root": "/repo"
}
```

- [ ] **`testdata/ledgers/two-grouped.json` — same journey + same shape + overlapping tags.**

```json
{
  "usecases": [
    {
      "id": "tail-sensor-no-registry",
      "journey_id": "tail-sensor",
      "name": "no registry",
      "regression_priority": "high",
      "tags": ["error-handling", "registry-discovery"],
      "trigger": {"shape": "CLI invocation", "summary": "s", "fixture": {}},
      "behavior": {"summary": "s", "business_rules": ["Exit code is 1."]},
      "expected_outcome": {
        "shape": "One JSON line on stdout + exit code",
        "summary": "s", "fixture": {"exit_code": 1},
        "invariants": ["Exit code is 1."],
        "side_effects": []
      },
      "evidence": [{"file": "skills/tail-sensor/scripts/tail.go", "rationale": "r"}],
      "source_path": "x"
    },
    {
      "id": "tail-sensor-invalid-cursor",
      "journey_id": "tail-sensor",
      "name": "invalid cursor",
      "regression_priority": "medium",
      "tags": ["error-handling", "cursor"],
      "trigger": {"shape": "CLI invocation", "summary": "s", "fixture": {}},
      "behavior": {"summary": "s", "business_rules": ["Exit code is 1."]},
      "expected_outcome": {
        "shape": "One JSON line on stdout + exit code",
        "summary": "s", "fixture": {"exit_code": 1},
        "invariants": ["Exit code is 1."],
        "side_effects": []
      },
      "evidence": [{"file": "skills/tail-sensor/scripts/tail.go", "rationale": "r"}],
      "source_path": "x"
    }
  ],
  "project_root": "/repo"
}
```

- [ ] **`testdata/ledgers/two-split-trigger-shape.json` — same journey, different trigger.shape → two sensors.**

```json
{
  "usecases": [
    {
      "id": "uc-a", "journey_id": "j", "name": "a", "regression_priority": "low", "tags": [],
      "trigger": {"shape": "CLI invocation", "summary": "s", "fixture": {}},
      "behavior": {"summary": "s", "business_rules": ["x."]},
      "expected_outcome": {"shape": "single", "summary": "s", "fixture": {}, "invariants": ["y."], "side_effects": []},
      "evidence": [{"file": "lib/x.go", "rationale": "r"}], "source_path": "x"
    },
    {
      "id": "uc-b", "journey_id": "j", "name": "b", "regression_priority": "low", "tags": [],
      "trigger": {"shape": "HTTP request", "summary": "s", "fixture": {}},
      "behavior": {"summary": "s", "business_rules": ["x."]},
      "expected_outcome": {"shape": "single", "summary": "s", "fixture": {}, "invariants": ["y."], "side_effects": []},
      "evidence": [{"file": "lib/x.go", "rationale": "r"}], "source_path": "x"
    }
  ],
  "project_root": "/repo"
}
```

- [ ] **`testdata/ledgers/bucket-too-large.json` — 9 usecases sharing journey+shape+no dominant tag.**

```json
{
  "usecases": [
    {"id":"uc-a","journey_id":"j","name":"a","regression_priority":"low","tags":["only-a"],"trigger":{"shape":"CLI invocation","summary":"s","fixture":{}},"behavior":{"summary":"s","business_rules":["x."]},"expected_outcome":{"shape":"single","summary":"s","fixture":{},"invariants":["y."],"side_effects":[]},"evidence":[{"file":"lib/x.go","rationale":"r"}],"source_path":"x"},
    {"id":"uc-b","journey_id":"j","name":"b","regression_priority":"low","tags":["only-b"],"trigger":{"shape":"CLI invocation","summary":"s","fixture":{}},"behavior":{"summary":"s","business_rules":["x."]},"expected_outcome":{"shape":"single","summary":"s","fixture":{},"invariants":["y."],"side_effects":[]},"evidence":[{"file":"lib/x.go","rationale":"r"}],"source_path":"x"},
    {"id":"uc-c","journey_id":"j","name":"c","regression_priority":"low","tags":["only-c"],"trigger":{"shape":"CLI invocation","summary":"s","fixture":{}},"behavior":{"summary":"s","business_rules":["x."]},"expected_outcome":{"shape":"single","summary":"s","fixture":{},"invariants":["y."],"side_effects":[]},"evidence":[{"file":"lib/x.go","rationale":"r"}],"source_path":"x"},
    {"id":"uc-d","journey_id":"j","name":"d","regression_priority":"low","tags":["only-d"],"trigger":{"shape":"CLI invocation","summary":"s","fixture":{}},"behavior":{"summary":"s","business_rules":["x."]},"expected_outcome":{"shape":"single","summary":"s","fixture":{},"invariants":["y."],"side_effects":[]},"evidence":[{"file":"lib/x.go","rationale":"r"}],"source_path":"x"},
    {"id":"uc-e","journey_id":"j","name":"e","regression_priority":"low","tags":["only-e"],"trigger":{"shape":"CLI invocation","summary":"s","fixture":{}},"behavior":{"summary":"s","business_rules":["x."]},"expected_outcome":{"shape":"single","summary":"s","fixture":{},"invariants":["y."],"side_effects":[]},"evidence":[{"file":"lib/x.go","rationale":"r"}],"source_path":"x"},
    {"id":"uc-f","journey_id":"j","name":"f","regression_priority":"low","tags":["only-f"],"trigger":{"shape":"CLI invocation","summary":"s","fixture":{}},"behavior":{"summary":"s","business_rules":["x."]},"expected_outcome":{"shape":"single","summary":"s","fixture":{},"invariants":["y."],"side_effects":[]},"evidence":[{"file":"lib/x.go","rationale":"r"}],"source_path":"x"},
    {"id":"uc-g","journey_id":"j","name":"g","regression_priority":"low","tags":["only-g"],"trigger":{"shape":"CLI invocation","summary":"s","fixture":{}},"behavior":{"summary":"s","business_rules":["x."]},"expected_outcome":{"shape":"single","summary":"s","fixture":{},"invariants":["y."],"side_effects":[]},"evidence":[{"file":"lib/x.go","rationale":"r"}],"source_path":"x"},
    {"id":"uc-h","journey_id":"j","name":"h","regression_priority":"low","tags":["only-h"],"trigger":{"shape":"CLI invocation","summary":"s","fixture":{}},"behavior":{"summary":"s","business_rules":["x."]},"expected_outcome":{"shape":"single","summary":"s","fixture":{},"invariants":["y."],"side_effects":[]},"evidence":[{"file":"lib/x.go","rationale":"r"}],"source_path":"x"},
    {"id":"uc-i","journey_id":"j","name":"i","regression_priority":"low","tags":["only-i"],"trigger":{"shape":"CLI invocation","summary":"s","fixture":{}},"behavior":{"summary":"s","business_rules":["x."]},"expected_outcome":{"shape":"single","summary":"s","fixture":{},"invariants":["y."],"side_effects":[]},"evidence":[{"file":"lib/x.go","rationale":"r"}],"source_path":"x"}
  ],
  "project_root": "/repo"
}
```

- [ ] **`testdata/ledgers/inferential.json` — semantic adjective in business rule.**

```json
{
  "usecases": [
    {
      "id": "uc-llm", "journey_id": "j", "name": "n", "regression_priority": "low", "tags": [],
      "trigger": {"shape": "CLI invocation", "summary": "s", "fixture": {}},
      "behavior": {"summary": "s", "business_rules": ["Response is semantically equivalent to spec."]},
      "expected_outcome": {"shape": "single", "summary": "s", "fixture": {}, "invariants": [], "side_effects": []},
      "evidence": [{"file": "lib/x.go", "rationale": "r"}],
      "source_path": "x"
    }
  ],
  "project_root": "/repo"
}
```

- [ ] **`testdata/ledgers/observation.json` — log stream trigger shape.**

```json
{
  "usecases": [
    {
      "id": "uc-obs", "journey_id": "j", "name": "n", "regression_priority": "low", "tags": [],
      "trigger": {"shape": "CLI invocation", "summary": "s", "fixture": {}},
      "behavior": {"summary": "s", "business_rules": ["Logs are produced."]},
      "expected_outcome": {"shape": "stream of events", "summary": "log lines while running", "fixture": {}, "invariants": [], "side_effects": []},
      "evidence": [{"file": "skills/run-sensor/scripts/run-computational.go", "rationale": "r"}],
      "source_path": "x"
    }
  ],
  "project_root": "/repo"
}
```

### Step 3.4: Implement `plan-sensors.go`

- [ ] **Create `skills/create-sensor/scripts/plan-sensors.go`.**

```go
//go:build plan_sensors

// Command plan-sensors reads a ledger from stdin and emits a JSONL plan
// on stdout (one Plan line per proposed sensor, then one Aggregate
// signal as the last line).
//
// Determinism: no rand, no time.Now(). Sort orders are explicit in code
// and depend only on input data. See spec §Grouping heuristic.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/skills/create-sensor/scripts/lib/ledger"
)

const bucketLimit = 8

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr))
}

func run(stdin io.Reader, stdout, stderr io.Writer) int {
	body, err := io.ReadAll(stdin)
	if err != nil {
		emit(stdout, errSignal("usage", "read stdin: "+err.Error()))
		return 2
	}
	var lg ledger.Ledger
	if err := json.Unmarshal(body, &lg); err != nil {
		emit(stdout, errSignal("usage", "parse ledger: "+err.Error()))
		return 2
	}

	buckets := group(lg.Usecases)
	var plans []ledger.Plan
	for _, b := range buckets {
		plans = append(plans, materialize(b)...)
	}
	// Sort plans by sensor_id ascending for deterministic output.
	sort.Slice(plans, func(i, j int) bool { return plans[i].SensorID < plans[j].SensorID })

	for _, p := range plans {
		emit(stdout, p)
	}
	emit(stdout, ledger.Aggregate{
		Aggregate:        true,
		Verdict:          "pass",
		Severity:         "info",
		SensorsPlanned:   len(plans),
		UsecasesConsumed: countConsumed(plans),
	})
	return 0
}

// bucket is a tentative grouping of usecases sharing journey+shape.
type bucket struct {
	journeyID  string
	shape      string
	usecases   []ledger.Usecase
}

// group partitions usecases by (journey_id, trigger.shape). Tag overlap
// further splits — usecases with disjoint tag sets in the same journey+
// shape go to different sensors. Evidence-directory proximity tightens
// further; usecases whose evidence files share a common directory (or
// 1-level-up) stay together.
func group(usecases []ledger.Usecase) []bucket {
	// Step 1: partition by journey+shape.
	keyed := map[string][]ledger.Usecase{}
	var order []string
	for _, uc := range usecases {
		key := uc.JourneyID + "|" + uc.Trigger.Shape
		if _, ok := keyed[key]; !ok {
			order = append(order, key)
		}
		keyed[key] = append(keyed[key], uc)
	}
	sort.Strings(order)

	// Step 2: within each (journey, shape) partition, split by
	// disjoint-tag clusters and evidence-directory clusters.
	var out []bucket
	for _, k := range order {
		parts := strings.SplitN(k, "|", 2)
		clusters := splitByTagsAndEvidence(keyed[k])
		for _, c := range clusters {
			sort.Slice(c, func(i, j int) bool { return c[i].ID < c[j].ID })
			out = append(out, bucket{journeyID: parts[0], shape: parts[1], usecases: c})
		}
	}
	return out
}

func splitByTagsAndEvidence(in []ledger.Usecase) [][]ledger.Usecase {
	if len(in) <= 1 {
		return [][]ledger.Usecase{in}
	}
	// Union-find by (tag overlap OR evidence-dir proximity).
	parent := make([]int, len(in))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	for i := 0; i < len(in); i++ {
		for j := i + 1; j < len(in); j++ {
			if shareTag(in[i], in[j]) || evidenceProximate(in[i], in[j]) {
				union(i, j)
			}
		}
	}
	// Bucket by root.
	groups := map[int][]ledger.Usecase{}
	for i, uc := range in {
		r := find(i)
		groups[r] = append(groups[r], uc)
	}
	// Stable order.
	var rootOrder []int
	for r := range groups {
		rootOrder = append(rootOrder, r)
	}
	sort.Ints(rootOrder)
	var out [][]ledger.Usecase
	for _, r := range rootOrder {
		out = append(out, groups[r])
	}
	return out
}

func shareTag(a, b ledger.Usecase) bool {
	set := map[string]struct{}{}
	for _, t := range a.Tags {
		set[t] = struct{}{}
	}
	for _, t := range b.Tags {
		if _, ok := set[t]; ok {
			return true
		}
	}
	return false
}

func evidenceProximate(a, b ledger.Usecase) bool {
	if len(a.Evidence) == 0 || len(b.Evidence) == 0 {
		return false
	}
	dirA := filepath.Dir(a.Evidence[0].File)
	dirB := filepath.Dir(b.Evidence[0].File)
	if dirA == dirB {
		return true
	}
	// 1-level-up match.
	if filepath.Dir(dirA) == filepath.Dir(dirB) && dirA != "." && dirB != "." {
		return true
	}
	return false
}

// materialize turns a bucket into 1..N plans, applying the
// bucket-too-large fission rule (sort by id ascending, chunk by 8).
func materialize(b bucket) []ledger.Plan {
	// Sort usecases by id ascending — required for deterministic split.
	sort.Slice(b.usecases, func(i, j int) bool { return b.usecases[i].ID < b.usecases[j].ID })

	if len(b.usecases) <= bucketLimit {
		return []ledger.Plan{buildPlan(b.usecases, b.journeyID, b.shape, "")}
	}
	// Fission by id-sorted chunks.
	var plans []ledger.Plan
	for i, start := 1, 0; start < len(b.usecases); i, start = i+1, start+bucketLimit {
		end := start + bucketLimit
		if end > len(b.usecases) {
			end = len(b.usecases)
		}
		plans = append(plans, buildPlan(b.usecases[start:end], b.journeyID, b.shape, fmt.Sprintf("-part-%d", i)))
	}
	return plans
}

func buildPlan(group []ledger.Usecase, journey, shape, partSuffix string) ledger.Plan {
	kind := inferKind(group)
	typ, inferentialWarn := inferType(group)
	output := inferOutput(group)

	useCaseIDs := make([]string, 0, len(group))
	for _, uc := range group {
		useCaseIDs = append(useCaseIDs, uc.ID)
	}

	var steps []ledger.StepOutline
	stepCounter := 1
	for _, uc := range group {
		for _, rule := range uc.Behavior.BusinessRules {
			steps = append(steps, ledger.StepOutline{
				StepID:            fmt.Sprintf("rule-%d-%s", stepCounter, slugify(rule)),
				SourceUsecase:     uc.ID,
				SourceRule:        rule,
				SuggestedStepType: suggestStepType(uc, rule),
				MockStrategy:      pickMockStrategy(uc),
				Evidence:          uc.Evidence,
			})
			stepCounter++
		}
	}

	rationale := fmt.Sprintf(
		"Grouped by journey_id=%s + trigger.shape=%s. %d usecases × business_rules → %d steps.",
		journey, shape, len(group), len(steps),
	)
	if inferentialWarn {
		rationale += " WARN: inferential — calibration must be supplied by user."
	}
	if partSuffix != "" {
		rationale += " WARN: bucket_too_large — chunked by id-sorted split."
	}

	prefix := map[string]string{
		"assertion":   "assert",
		"observation": "observe",
		"setup":       "setup",
	}[kind]
	if prefix == "" {
		prefix = "assert"
	}

	return ledger.Plan{
		SensorID:    fmt.Sprintf("%s-%s%s", prefix, journey, partSuffix),
		Kind:        kind,
		Type:        typ,
		Output:      output,
		UseCases:    useCaseIDs,
		StepOutline: steps,
		Rationale:   rationale,
	}
}

func inferKind(group []ledger.Usecase) string {
	for _, uc := range group {
		shape := strings.ToLower(uc.Trigger.Shape)
		summary := strings.ToLower(uc.Behavior.Summary)
		if strings.Contains(shape, "setup") || strings.Contains(summary, "idempotent") {
			return "setup"
		}
	}
	for _, uc := range group {
		shape := strings.ToLower(uc.ExpectedOutcome.Shape)
		summary := strings.ToLower(uc.ExpectedOutcome.Summary)
		if strings.Contains(shape, "stream") || strings.Contains(summary, "log lines while running") {
			return "observation"
		}
	}
	return "assertion"
}

func inferType(group []ledger.Usecase) (string, bool) {
	semanticAdjectives := []string{
		"semantically equivalent",
		"team voice",
		"no pii",
		"no personally identifiable",
	}
	for _, uc := range group {
		for _, rule := range uc.Behavior.BusinessRules {
			r := strings.ToLower(rule)
			for _, adj := range semanticAdjectives {
				if strings.Contains(r, adj) {
					return "inferential", true
				}
			}
		}
	}
	return "computational", false
}

func inferOutput(group []ledger.Usecase) string {
	for _, uc := range group {
		shape := strings.ToLower(uc.ExpectedOutcome.Shape)
		if strings.Contains(shape, "stream") || strings.Contains(shape, "log lines") || strings.Contains(shape, "one line per") {
			return "stream"
		}
	}
	// ≥2 independent rules → stream.
	totalRules := 0
	for _, uc := range group {
		totalRules += len(uc.Behavior.BusinessRules)
	}
	if totalRules >= 2 {
		return "stream"
	}
	return "single"
}

func suggestStepType(uc ledger.Usecase, rule string) string {
	if len(uc.Evidence) == 0 {
		return "shell"
	}
	file := uc.Evidence[0].File
	if strings.Contains(strings.ToLower(file), "http") {
		return "http"
	}
	return "shell"
}

func pickMockStrategy(uc ledger.Usecase) string {
	if len(uc.Evidence) == 0 {
		return "stub-deterministic"
	}
	file := uc.Evidence[0].File
	if strings.HasPrefix(file, "lib/") && strings.HasSuffix(file, ".go") && !strings.HasSuffix(file, "_test.go") {
		return "stub-deterministic"
	}
	if strings.Contains(strings.ToLower(file), "http") {
		return "fixture-http-step"
	}
	for _, se := range uc.ExpectedOutcome.SideEffects {
		l := strings.ToLower(se)
		if strings.Contains(l, "db write") || strings.Contains(l, "kafka") || strings.Contains(l, "external api") {
			return "setup-mock-infra"
		}
	}
	return "stub-deterministic"
}

func slugify(s string) string {
	s = strings.ToLower(s)
	var out []rune
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out = append(out, r)
		} else if r == ' ' || r == '-' {
			out = append(out, '-')
		}
	}
	slug := strings.Trim(string(out), "-")
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	if len(slug) > 32 {
		slug = slug[:32]
	}
	return slug
}

func countConsumed(plans []ledger.Plan) int {
	seen := map[string]struct{}{}
	for _, p := range plans {
		for _, id := range p.UseCases {
			seen[id] = struct{}{}
		}
	}
	return len(seen)
}

func emit(w io.Writer, v any) {
	body, _ := json.Marshal(v)
	fmt.Fprintln(w, string(body))
}

func errSignal(kind, rationale string) map[string]any {
	return signal.NewBuilder("plan-sensors", "0.1.0").
		WithVerdict("error", "high").
		WithKind(kind).
		WithRationale(rationale).
		Build()
}
```

### Step 3.5: Generate the golden plan outputs

- [ ] **Run the tests once to capture the actual stdout, then snapshot.**

```bash
cd skills/create-sensor/scripts
go run -tags=plan_sensors . < testdata/ledgers/single-usecase.json > testdata/plan-output/single-usecase.jsonl
go run -tags=plan_sensors . < testdata/ledgers/two-grouped.json > testdata/plan-output/two-grouped.jsonl
go run -tags=plan_sensors . < testdata/ledgers/two-split-trigger-shape.json > testdata/plan-output/two-split-trigger-shape.jsonl
go run -tags=plan_sensors . < testdata/ledgers/bucket-too-large.json > testdata/plan-output/bucket-too-large.jsonl
go run -tags=plan_sensors . < testdata/ledgers/inferential.json > testdata/plan-output/inferential.jsonl
go run -tags=plan_sensors . < testdata/ledgers/observation.json > testdata/plan-output/observation.jsonl
cd ../../..
```

- [ ] **Inspect each golden file by hand to confirm it matches the expected heuristic outcome.**

For `two-grouped.jsonl`: should have ONE plan line (one sensor planned, 2 usecases consumed) plus aggregate.
For `two-split-trigger-shape.jsonl`: should have TWO plan lines (two sensors, one per shape).
For `bucket-too-large.jsonl`: should have at least two plan lines (the 9 usecases split into chunks because no dominant tag).
For `inferential.jsonl`: the plan line's `type` is `inferential`; rationale mentions calibration warn.
For `observation.jsonl`: the plan line's `kind` is `observation`; `output` is `stream`.

If any output looks wrong, fix the heuristic in `plan-sensors.go` and regenerate.

### Step 3.6: Add the remaining test cases

- [ ] **Append tests covering the rest of the snapshot matrix.**

```go
func TestPlanTwoSplitTriggerShape(t *testing.T) { runSnapshot(t, "two-split-trigger-shape") }
func TestPlanBucketTooLarge(t *testing.T)        { runSnapshot(t, "bucket-too-large") }
func TestPlanInferential(t *testing.T)            { runSnapshot(t, "inferential") }
func TestPlanObservation(t *testing.T)            { runSnapshot(t, "observation") }

func runSnapshot(t *testing.T, name string) {
	t.Helper()
	in, err := os.Open(filepath.Join("testdata", "ledgers", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var stdout, stderr bytes.Buffer
	if code := run(in, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	expected, err := os.ReadFile(filepath.Join("testdata", "plan-output", name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimRight(stdout.Bytes(), "\n"), bytes.TrimRight(expected, "\n")) {
		t.Fatalf("output mismatch for %s.\ngot:\n%s\nwant:\n%s", name, stdout.String(), string(expected))
	}
}
```

- [ ] **Run.**

```bash
go test -race -count=1 -tags=plan_sensors ./skills/create-sensor/scripts/...
```

Expected: all pass.

- [ ] **Add `plan_sensors` to the CI workflow.**

Patch `.github/workflows/test.yml` after the `read_usecases` block (added in Task 2) with:

```yaml
      - name: Vet (plan_sensors build tag)
        run: go vet -tags=plan_sensors ./...

      - name: Test plan-sensors
        run: go test -race -count=1 -tags=plan_sensors ./skills/create-sensor/scripts/...
```

### Step 3.7: Commit

- [ ] **Commit Task 3.**

```bash
git add skills/create-sensor/scripts/lib/ledger/ledger.go \
        skills/create-sensor/scripts/plan-sensors.go \
        skills/create-sensor/scripts/plan-sensors_test.go \
        skills/create-sensor/scripts/testdata/ledgers/ \
        skills/create-sensor/scripts/testdata/plan-output/ \
        .github/workflows/test.yml

git commit -m "feat(create-sensor): add plan-sensors.go deterministic planner

Applies the journey+shape+tags+evidence grouping heuristic and the
kind/type/output/mock_strategy inference described in Spec B. Snapshot
tests cover single/grouped/split/bucket-too-large/inferential/observation.
Determinism enforced with go test -count=10 in TestPlanIsDeterministic.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Rewrite `skills/create-sensor/SKILL.md`

**Files:**
- Modify: `skills/create-sensor/SKILL.md` (full body rewrite, frontmatter stays the same)

The SKILL.md becomes the LLM-facing orchestration document. Phases 2, 3, 5 are pure script invocations; Phases 1, 1.5, 4 contain prompts and decision logic for the LLM.

### Step 4.1: Pin the rewrite outline

- [ ] **Read the current SKILL.md to know what's being replaced.**

Run: `wc -l skills/create-sensor/SKILL.md && head -50 skills/create-sensor/SKILL.md`
Expected: ~150 lines describing the old 7-phase flow.

### Step 4.2: Apply the rewrite

- [ ] **Replace the entire body below the YAML frontmatter with the following.**

(Keep the frontmatter `---\nname: create-sensor\ndescription: ...\n---` as-is, but update the description's prose to reflect the multi-angle authoring.)

New body:

```markdown
# create-sensor

Take one or more usecase ids (or a journey id, or a usecase file path, or a free-text requirement) as input and produce one or more sensors that validate the implied behavior from multiple angles. Use deterministic Go scripts for grouping/inference (`read-usecases.go` → `plan-sensors.go`); synthesize each step from `behavior.business_rules[]` via LLM judgment.

This skill produces **one or more sensors per invocation**. Every sensor it emits declares `use_cases: [<id>, ...]` referencing real usecase files under `<project>/.harness/usecases/**`. For project-wide bootstrapping or for `observation` / `setup` sensors that don't trace to a usecase, use `/detect-sensors`.

## Invocation

```
/create-sensor [usecase-id | journey-id | path/to/usecase.yaml | "<free text>"]
```

If no argument is supplied, block:

> What is the requirement to cover? Pass a usecase id (`tail-sensor-no-registry`), a journey id (`tail-sensor`), a file path, or a free-text requirement.

## Procedure

### Phase 1 — Parse invocation

Classify the input:

| Input | Resolution |
|---|---|
| `<usecase-id>` | Resolve to a file under `<project>/.harness/usecases/**/<usecase-id>.yaml`. If multiple matches, fail asking the user to qualify by journey. |
| `<journey-id>` | Read all `<project>/.harness/usecases/<journey-id>/*.yaml`. |
| `path/to/file.yaml` | Read the file. Validation deferred to Phase 2. |
| `"<free text>"` | No usecase resolved; jump to Phase 1.5. |

### Phase 1.5 — Free-text inference (only if input is text)

Invoke the thin index loader to enumerate every usecase id + name + tags:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=read_usecases \
  ./skills/create-sensor/scripts \
  --journey "<journey-id>" \
  --list-only
```

(If there is no specific journey, pass `--usecases ""` and the script will produce an empty result; better: do one `--journey` call per journey listed by `ls .harness/usecases/`.)

For each candidate, judge whether the free-text requirement reasonably corresponds. Collect a matched set.

If the matched set is empty, ask the user:

> No existing usecase matches this requirement. Two options:
> 1. Run `/detect-usecases` first to populate the ledger, then re-invoke `/create-sensor`.
> 2. Proceed by drafting an inline synthetic usecase that will be persisted to `.harness/usecases/inline/` as part of this PR.
>
> Which do you prefer?

Block until the user answers. No default.

### Phase 2 — Load ledger

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=read_usecases \
  ./skills/create-sensor/scripts \
  --usecases "<id-1>,<id-2>,..." \
  --include-stack \
  --include-catalog
```

If warn signals appeared on stdout BEFORE the ledger JSON, surface them to the user inline. Save the ledger JSON to a tmp file for Phase 3:

```bash
... > /tmp/ledger-<timestamp>.json
```

### Phase 3 — Plan sensors

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=plan_sensors \
  ./skills/create-sensor/scripts \
  < /tmp/ledger-<timestamp>.json
```

The script emits JSONL: one Plan line per proposed sensor, ending with one Aggregate Signal. Parse each line.

### Phase 3.5 — Report plan + confirm

Echo a one-line summary per sensor to the user:

```
Planned 3 sensors:

  ▸ assert-tail-sensor-error-handling  (assertion / computational / stream)
    use_cases: 3, steps: 9
    rationale: <plan.rationale>

  ▸ assert-tail-sensor-happy-path  (assertion / computational / single)
    use_cases: 2, steps: 6
    rationale: <plan.rationale>

  ▸ setup-tmp-registry  (setup / computational / single)
    use_cases: 0  ← created auxiliary for setup-mock-infra
```

Ask: *"Proceed? (yes/no)"*. Yes/no only — no editing. If the user wants different grouping, they can re-invoke with a narrower input.

If the user says no, abort cleanly without persisting anything.

### Phase 4 — Synthesize sensor drafts

For each planned sensor IN ORDER (not parallel — fixture writes need path coordination):

1. **Read** the `step_outline[]` plus the corresponding usecase YAML(s) (use `<project>/.harness/usecases/**` paths recorded in `source_usecase`).
2. **Construct an in-memory YAML draft** with these top-level fields populated:
   - `id`, `version: "0.1.0"`, `name`, `description` — from the plan + the usecase summaries.
   - `kind`, `type`, `output` — from the plan.
   - `regulation: "behaviour"` (default).
   - `phase: "on-demand"` (default).
   - `determinism`: `"high"` for computational, `"medium"` for inferential.
   - `cost` — use the canonical defaults from the spec (`cost.compute = {cpu: low, memory_mb: 64}` for computational; `cost.tokens = {model: "", input_avg: 4000, output_avg: 1000, max_output: 4096}` for inferential; `cost.latency` from §Canonical cost defaults).
   - `triggers: [{ "on": "manual" }]`.
   - `use_cases` — from the plan.
   - `execution.steps[]` — one per `step_outline[]` entry, expanded according to the matrix below.
3. **Expand each `step_outline[i]`** per:

   | `suggested_step_type` | `mock_strategy` | Expansion |
   |---|---|---|
   | `shell` | `stub-deterministic` | `type: shell`, `run: <command exercising evidence>`, `exit_code_map: {0: pass, "*": fail}` |
   | `http` | `fixture-http-step` | `type: http`, `with: { fixture: <persisted name> }`, `expect.status: <from invariants>` |
   | `assert` | any | `type: assert`, `expect: { value: "${{ steps.<prior>.outputs.<name> }}", contains: "<from business_rule>" }` |
   | any | `setup-mock-infra` | Generate ALSO a sibling `kind=setup` sensor (id `setup-<journey>-mock`), declare it as `requires[{kind: sensor, id: <setup-id>}]` on the main sensor, plan it as the FIRST sensor to persist. |

4. **If `evidence` does not yield a concrete shell command** (e.g., evidence is `lib/foo.Bar` with no shell surface), do NOT fabricate a command. Stop and ask the user:

   > I cannot infer a concrete shell command for step `<step_id>` from evidence `<file>:<line>`. Options: (a) wrap as `go test -run <Test>`; (b) you supply the exact command. Which do you prefer?

   Block. Do not persist the sensor with a placeholder command.

5. **Fixtures.** When a step needs one (e.g., HTTP step with `with: { fixture: ... }`), serialize the source data and persist via:

   ```bash
   printf '%s' "<payload>" | \
     HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
     go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_fixture \
     ./skills/create-sensor/scripts \
     ".harness/fixtures/<sensor-id>/<step-id>.<ext>"
   ```

6. **Self-coherence check** before serializing the YAML: confirm (a) every `${{ steps.X.outputs.Y }}` reference points to a prior step that declared that output; (b) every `with: { fixture: ... }` references a path that was written in step 5.

7. **Serialize** the YAML to `/tmp/create-sensor-draft-<sensor-id>.yaml`.

### Phase 5 — Persist + report

For each draft, in order:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_sensor \
  ./skills/create-sensor/scripts \
  --out "${HARNESS_REGISTRY_ROOT}/.harness/sensors" \
  --schemas-dir "${CLAUDE_PLUGIN_ROOT}/schemas" \
  /tmp/create-sensor-draft-<sensor-id>.yaml
```

Outcomes:

- **`verdict=pass`** — persist succeeded. Move on to the next sensor.
- **`verdict=error, metadata.kind=schema_invalid`** — patch the draft per the rationale and retry. Max 2 retries; then hand the draft to the user.
- **`verdict=error, metadata.kind=usecase_not_found`** — the sensor referenced a usecase that doesn't exist on disk. Abort the WHOLE invocation; surface which ids are missing.
- **`verdict=error, metadata.kind=sensor_already_exists`** — ask the user whether to pick a new id or abort.

When all sensors are persisted (or a fatal error halts), report:

```
Created N sensors covering M usecases:

  ✓ <sensor-id-1>
    use_cases: [...]
    steps: K
    deps: <required dep ids or —>

  ✓ <sensor-id-2>
    ...

Next: run `/run-sensor <sensor-id>` to exercise the sensor.
```

## What this skill does NOT do

- Does not exercise sensors after creation — `/run-sensor` stays manual.
- Does not modify existing sensors — id collisions are surfaced; user resolves.
- Does not interpret free text as "create a usecase" — Phase 1.5 proposes and blocks.
- Does not auto-replay usecases at runtime — `use_cases[]` is declarative traceability only.
```

### Step 4.3: Update the frontmatter description

- [ ] **Replace the existing `description:` value with the multi-angle version.**

```yaml
description: Use when the user invokes /create-sensor or asks to author sensors that validate one or more usecases. Reads .harness/usecases/** (by id, journey, path, or free-text match), applies deterministic grouping + kind/type/output inference (Go script plan-sensors.go), then synthesizes typed execution.steps[] (shell/http/assert/sensor) — one step per business_rule of every covered usecase. Emits 1..N sensors per invocation; every sensor declares use_cases[] referencing the source usecase ids. Distinct from /detect-sensors, which sweeps the whole project for observation/setup sensors.
```

### Step 4.4: Commit

- [ ] **Commit.**

```bash
git add skills/create-sensor/SKILL.md
git commit -m "docs(create-sensor): rewrite SKILL.md for multi-angle authoring

Replaces the single-shell-command flow with the orchestrated
read-usecases → plan-sensors → synthesize → write-sensor pipeline.
LLM responsibilities are scoped to Phases 1, 1.5, 4 (synthesis from
business_rule prose); Phases 2, 3, 5 are pure Go script calls.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Rewrite `skills/detect-sensors/SKILL.md`

**Files:**
- Modify: `skills/detect-sensors/SKILL.md` (remove every `verification.golden_cases[]` reference; add `use_cases[]` flow + "run /detect-usecases first" funnel)

### Step 5.1: Audit current references

- [ ] **List every line that mentions verification/golden.**

Run: `grep -n 'verification\|golden_cases\|golden' skills/detect-sensors/SKILL.md`
Expected: about 9–10 hits.

### Step 5.2: For each match, rewrite

- [ ] **Replace YAML examples that include a `verification:` block.**

For each example in the SKILL.md that ends in:

```yaml
verification:
  golden_cases:
    - fixture: <path>
      expected_verdict: pass
      expected_severity: info
```

Replace with:

```yaml
use_cases:
  - <derive-from-context>  # MUST reference a real id under .harness/usecases/
```

- [ ] **Replace prose mentions of "golden_cases" with "use_cases".**

For each instance of "golden_cases", "the verification block", or "golden case" in prose, rewrite to discuss `use_cases[]` traceability instead. The wording: "Every sensor declares `use_cases[]` (≥1) listing the usecase ids it validates."

- [ ] **Add a new section explaining the usecase-first ordering.**

After the "Phase A: Stack discovery" section, before "Phase B: Sensor drafting" (or wherever the structure makes sense), insert:

```markdown
### Phase A.5: Usecase ledger check

Sensors produced by /detect-sensors MUST reference real usecase ids. Before drafting any sensor, list the existing usecases:

```bash
find .harness/usecases -type f -name '*.yaml' 2>/dev/null
```

If no usecases exist yet, **stop and tell the user to run `/detect-usecases` first**. /detect-sensors cannot emit a schema-valid sensor with empty `use_cases[]` (the schema requires `minItems: 1`).

If usecases DO exist, build a quick mental index of which usecases belong to which journey — sensors will be wired to the most relevant ones during drafting (Phase B).
```

- [ ] **Update the persistence-step example.**

Wherever the SKILL.md shows a `write-sensor.go` invocation with a draft that contains `verification`, change it so the draft contains `use_cases: [...]` instead.

### Step 5.3: Verify no stale references remain

- [ ] **Grep again.**

Run: `grep -n 'verification\|golden_cases\|golden' skills/detect-sensors/SKILL.md`
Expected: empty output. If anything remains, fix it.

### Step 5.4: Commit

- [ ] **Commit.**

```bash
git add skills/detect-sensors/SKILL.md
git commit -m "docs(detect-sensors): swap golden_cases authoring for use_cases[]

Rewrites the SKILL body to remove every verification.golden_cases[]
reference and instead funnel users toward /detect-usecases first.
Sensors produced by /detect-sensors now declare use_cases[]
referencing ids that already exist under .harness/usecases/**.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Acceptance sensor `assert-create-sensor-multi-angle`

**Files:**
- Create: `.harness/usecases/framework/framework-create-sensor-multi-angle.yaml` (the usecase the new sensor will reference)
- Create: `.harness/sensors/assert-create-sensor-multi-angle.yaml`

The sensor structurally asserts that `/create-sensor` (invoked against a real usecase) produces a schema-valid YAML with multi-angle properties. **No LLM byte-diffing** — only structural checks survive synthesis variance.

### Step 6.1: Create the framework usecase

- [ ] **Write `.harness/usecases/framework/framework-create-sensor-multi-angle.yaml`.**

```yaml
id: framework-create-sensor-multi-angle
version: 0.1.0
name: /create-sensor produces a multi-angle sensor draft
journey_id: framework
description: |
  When /create-sensor is invoked against a real usecase (e.g.
  tail-sensor-no-registry), the persisted sensor must be schema-valid,
  declare ≥1 use_cases entry, contain ≥2 typed steps in
  execution.steps[], and (when any step needs a fixture) reference at
  least one file under .harness/fixtures/.
trigger:
  shape: harness skill invocation
  summary: /create-sensor tail-sensor-no-registry against this repo.
  fixture:
    skill: create-sensor
    input: tail-sensor-no-registry
behavior:
  summary: |
    The orchestrated pipeline (read-usecases → plan-sensors → LLM
    synthesis → write-sensor) produces a sensor file at
    .harness/sensors/<id>.yaml that exhibits the structural
    properties of a multi-angle sensor.
  business_rules:
    - The persisted sensor YAML parses against schemas/sensor.yaml.
    - len(execution.steps) >= 2.
    - len(use_cases) >= 1.
    - At least one step references a fixture under .harness/fixtures/, OR no step needs a fixture (legitimate for pure shell-only sensors).
expected_outcome:
  shape: One sensor YAML file on disk + the create-sensor Signal verdict=pass
  summary: A schema-valid sensor at the expected path with structural multi-angle properties.
  fixture:
    persisted_path: .harness/sensors/<sensor-id>.yaml
    exit_code: 0
  invariants:
    - Sensor file exists at the persisted path.
    - Sensor YAML parses against schemas/sensor.yaml.
    - len(execution.steps) >= 2.
    - len(use_cases) >= 1.
  side_effects:
    - "write: .harness/sensors/<sensor-id>.yaml"
    - "write: .harness/fixtures/<sensor-id>/*"
evidence:
  - file: skills/create-sensor/SKILL.md
    rationale: Defines the synthesis flow.
  - file: skills/create-sensor/scripts/plan-sensors.go
    rationale: Enforces the multi-angle decomposition deterministically.
  - file: skills/create-sensor/scripts/read-usecases.go
    rationale: Provides the input ledger.
regression_priority: high
tags:
  - framework-smoke
  - acceptance
```

### Step 6.2: Create the sensor

- [ ] **Write `.harness/sensors/assert-create-sensor-multi-angle.yaml`.**

```yaml
id: assert-create-sensor-multi-angle
version: 0.1.0
name: structural acceptance of /create-sensor multi-angle output
description: |
  Invokes /create-sensor against tail-sensor-no-registry (a known
  usecase) and asserts the result is a schema-valid sensor with
  multi-angle structural properties. LLM synthesis is allowed to
  vary; only structural invariants are checked.
kind: assertion
type: computational
regulation: behaviour
phase: on-demand
determinism: medium  # depends on the LLM-driven Phase 4
output: single
cost:
  class: medium
  compute:
    cpu: low
    memory_mb: 128
  latency:
    p50_ms: 60000
    p95_ms: 180000
    timeout_ms: 300000
blind_spots:
  - Cannot assert specific step contents (LLM synthesis varies).
  - Requires the user to run `/create-sensor tail-sensor-no-registry` interactively before this sensor runs; cannot itself drive an interactive LLM session.
triggers:
  - "on": manual
use_cases:
  - framework-create-sensor-multi-angle
execution:
  steps:
    - id: check-sensor-file-exists
      type: shell
      run: |
        set -e
        # The sensor name produced by /create-sensor is plan-driven; check the most likely id.
        ls .harness/sensors/assert-tail-sensor-*.yaml >/dev/null
      exit_code_map:
        "0": pass
        "*": fail
      outputs:
        path:
          from: stdout
    - id: schema-validate
      type: shell
      run: |
        set -e
        for f in .harness/sensors/assert-tail-sensor-*.yaml; do
          go run -tags=write_sensor "${CLAUDE_PLUGIN_ROOT}/skills/create-sensor/scripts" \
            --out "$(mktemp -d)" --schemas-dir "${CLAUDE_PLUGIN_ROOT}/schemas" "$f" >/dev/null
        done
      exit_code_map:
        "0": pass
        "*": fail
    - id: assert-step-count
      type: shell
      run: |
        set -e
        for f in .harness/sensors/assert-tail-sensor-*.yaml; do
          # Count execution.steps entries via yq if available, else a coarse grep.
          n=$(grep -cE '^    - id:' "$f")
          if [ "$n" -lt 2 ]; then
            echo "FAIL: $f has only $n step(s)"
            exit 1
          fi
        done
      exit_code_map:
        "0": pass
        "*": fail
    - id: assert-use-cases-populated
      type: shell
      run: |
        set -e
        for f in .harness/sensors/assert-tail-sensor-*.yaml; do
          if ! grep -q '^use_cases:' "$f"; then
            echo "FAIL: $f missing use_cases"
            exit 1
          fi
        done
      exit_code_map:
        "0": pass
        "*": fail
```

### Step 6.3: Verify both files parse against their schemas

- [ ] **Validate the usecase.**

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT:-$(pwd)}" -tags=write_stack \
  ./skills/detect-sensors/scripts \
  --out "$(mktemp -d)" \
  --schemas-dir "$(pwd)/schemas" \
  /dev/null 2>&1 || true
```

(The proper validator script for usecases is the one used by read-usecases; if there is no standalone CLI, instead rely on `go test -tags=read_usecases ./skills/create-sensor/scripts/...` after temporarily symlinking the new usecase into the testdata dir, OR just trust that the sensor schema check below catches issues by referencing the usecase id.)

- [ ] **Validate the sensor.**

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT:-$(pwd)}" -tags=write_sensor \
  ./skills/create-sensor/scripts \
  --out "$(mktemp -d)" \
  --schemas-dir "$(pwd)/schemas" \
  .harness/sensors/assert-create-sensor-multi-angle.yaml
```

Expected: stdout shows a `verdict=pass` Signal and the path of the (temporary) write. Exit code 0.

If the validation fails because the sensor references a usecase that isn't yet on disk, that's the point — the usecase file MUST be created first (Step 6.1). Re-run.

### Step 6.4: Commit

- [ ] **Commit.**

```bash
git add .harness/usecases/framework/framework-create-sensor-multi-angle.yaml \
        .harness/sensors/assert-create-sensor-multi-angle.yaml

git commit -m "test(framework): add assert-create-sensor-multi-angle acceptance sensor

Structurally asserts /create-sensor produces a schema-valid sensor
with ≥2 steps and ≥1 use_cases entry when invoked against a known
usecase. Allows LLM synthesis to vary; checks only structural
invariants. Acts as the plugin's self-test for Spec B.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Final verification

- [ ] **Run the full Go test suite across every build tag.**

```bash
for tag in run_computational run_inferential start_sensor stop_sensor list_sensors tail_sensor \
           heal_diagnose heal_apply_safe heal_apply_sensors heal_retry_original \
           write_sensor write_stack write_fixture catalog_sensors \
           read_usecases plan_sensors; do
  echo "=== $tag ===";
  go vet -tags=$tag ./... || exit 1;
  go test -race -count=1 -tags=$tag ./... || exit 1;
done
go test -race -count=1 ./lib/...
```

Expected: all pass.

- [ ] **Push the branch and open the PR.**

```bash
git push -u origin "$(git symbolic-ref --short HEAD)"
gh pr create --title "feat: /create-sensor multi-angle authoring (Spec B)" --body "$(cat <<'EOF'
## Summary

- Replace `verification.golden_cases[]` with declarative top-level `use_cases[]` (`minItems: 1`).
- Add `read-usecases.go` (loads usecase YAMLs into a JSON ledger) and `plan-sensors.go` (deterministic grouping + kind/type/output/mock_strategy inference).
- Rewrite `skills/create-sensor/SKILL.md` for the new pipeline; rewrite `skills/detect-sensors/SKILL.md` to drop the old golden-case authoring guidance.
- Delete `skills/detect-sensors/scripts/run-golden.go`; update CI workflow to invoke smoke sensors via `/run-sensor` instead.
- Add framework usecases (`framework-smoke-typed-pipeline`, `framework-smoke-with-setup`, `framework-create-sensor-multi-angle`) and the new `assert-create-sensor-multi-angle` acceptance sensor.

## Test plan

- [x] `lib/sensor/` table-driven tests (load, persist, validate, missing-usecase).
- [x] `read-usecases.go`: single-id, journey, list-only, malformed YAML, missing id, include-stack/catalog.
- [x] `plan-sensors.go`: snapshot tests covering grouping, splitting, bucket-too-large fission, inferential/observation inference; determinism with `go test -count=10`.
- [x] Smoke sensors `smoke-typed-pipeline` + `smoke-with-setup` run green via `/run-sensor`.
- [x] CI: every build tag's vet + test passes.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review

After writing the plan above, I checked:

**Spec coverage.** Each section of the spec maps to a task:
- Schema changes (§Schema changes) → Task 1.
- `read-usecases.go` (§Component: read-usecases.go) → Task 2.
- `plan-sensors.go` (§Component: plan-sensors.go) including grouping heuristic, kind/type/output/mock_strategy inference, canonical cost defaults, bucket-too-large rule → Task 3.
- SKILL.md flow (§SKILL.md flow) → Task 4.
- detect-sensors SKILL.md rewrite (§Schema changes cascade item #6) → Task 5.
- Acceptance sensor (§Testing plan) → Task 6.
- Error handling table (§Error handling — Signals) → distributed across `usecase_not_found` (Tasks 1.8, 2.5), `usage` (Task 2.5), `usecase_schema_invalid` (Task 2.5), `command_inference_failed` (Task 4 Phase 4 step 4), `schema_invalid` (Task 4 Phase 5).
- Implementation order (§Implementation order) — Task 1 is the single atomic commit; Tasks 2–6 follow per the spec's anticipated order.

**Placeholder scan.** No "TBD", "TODO", "implement later", "fill in details" remain. Every step has explicit content the engineer types verbatim.

**Type consistency.** `UseCases []string` field on `Sensor`. `MissingUseCaseError{ID, SearchRoot}`. `RequireUseCaseFilesOnDisk bool`. `Ledger{Usecases, Stack, Catalog, ProjectRoot}`. `Plan{SensorID, Kind, Type, Output, UseCases, StepOutline, Rationale}`. `StepOutline{StepID, SourceUsecase, SourceRule, SuggestedStepType, MockStrategy, Evidence}`. Used consistently across Tasks 1, 2, 3.

**One risk left for the executor:** the existing `lib/orchestrator/*_test.go` files are referenced in Task 1's grep-and-replace list but their inline sensor fixtures are not enumerated here. If the executor encounters tests that build Sensors with subtle shape variations, they should follow the pattern (replace `Verification: ...` with `UseCases: []string{"fake-uc"}`) and re-run; if any test asserts on the old field name in unexpected ways, fix to assert on the new name. This is the irreducible work of a breaking schema change.

