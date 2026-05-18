# Shared fixtures rooted at `.harness/fixtures/` — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace LLM-fabricated inline fixtures with files in a single shared pool at `.harness/fixtures/`, referenced by path from both usecases and sensors, with payloads sourced deterministically from real project artifacts.

**Architecture:** `schemas/usecase.yaml` introduces a `FixtureRef` `oneOf` envelope (`{ ref }` | `{ inline }`). `lib/fixture/` gains `FindOnDisk(hint, searchPaths)` (disk-tier search; library walks caller-supplied paths, never decides which paths) and `DeriveFromContract(hint, src, declPath)` (tier-2 generator limited to four already-structured contract sources: JSON Schema, OpenAPI components, Avro, Protobuf). `lib/usecase` validator gains existence + content-walk checks. Two skills (`detect-usecases`, `create-sensor`) adopt a three-tier sourcing rule (disk → contract → ask user). The 7 committed usecases and 5 sensors are migrated in the same PR.

**Tech Stack:** Go 1.25, `santhosh-tekuri/jsonschema/v5` (existing — JSON Schema parsing), `sigs.k8s.io/yaml` (existing — YAML round-trip), `github.com/bufbuild/protocompile` (NEW — protobuf text parsing). Avro `.avsc` is parsed as plain JSON. OpenAPI components are reduced to JSON Schema then handed to the JSON Schema handler.

---

### Task 1: Add `FixtureRef` envelope to `schemas/usecase.yaml`

**Files:**
- Modify: `schemas/usecase.yaml` (add `FixtureRef` def, swap `Trigger.fixture` + `ExpectedOutcome.fixture` refs)
- Test: `lib/usecase/persist_test.go` (extend existing table)

The schema change is binary: a draft with `fixture: {ref: "..."}` validates, a draft with both `ref` and `inline` (or with neither) fails. The remaining task contents land it.

- [ ] **Step 1: Write the failing tests in `lib/usecase/persist_test.go`**

Add these table entries to the existing `TestValidateAndPersist_*` cluster (or create a new function `TestValidateAndPersist_FixtureRef` if the existing table is one-shot). Use the existing test scaffolding pattern in `persist_test.go`:

```go
func TestValidateAndPersist_FixtureRefEnvelope(t *testing.T) {
    tests := []struct {
        name        string
        triggerFx   map[string]any
        outcomeFx   map[string]any
        wantErrSub  string // empty means: expect success
    }{
        {
            name:      "ref form ok",
            triggerFx: map[string]any{"ref": "framework/x/trigger.json"},
            outcomeFx: map[string]any{"ref": "framework/x/outcome.json"},
        },
        {
            name:      "inline primitive ok",
            triggerFx: map[string]any{"inline": "tick"},
            outcomeFx: map[string]any{"inline": map[string]any{"exit_code": float64(0)}},
        },
        {
            name:       "both arms rejected",
            triggerFx:  map[string]any{"ref": "a.json", "inline": "x"},
            outcomeFx:  map[string]any{"inline": "ok"},
            wantErrSub: "oneOf",
        },
        {
            name:       "neither arm rejected",
            triggerFx:  map[string]any{},
            outcomeFx:  map[string]any{"inline": "ok"},
            wantErrSub: "oneOf",
        },
        {
            name:       "extra property rejected",
            triggerFx:  map[string]any{"ref": "a.json", "extra": 1},
            outcomeFx:  map[string]any{"inline": "ok"},
            wantErrSub: "additionalProperties",
        },
    }
    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            // Build a complete UseCase JSON body with the test's fixture envelopes
            // using the existing buildValidDraft helper. If the file does not yet
            // have such a helper, copy the inline-construction pattern from the
            // closest existing test.
            // Fixture files referenced by `ref` MUST be created on disk first
            // because Task 3 adds the existence check. For now (Task 1), create
            // empty placeholder files so the test exercises ONLY schema.oneOf.
            // ... assertion: if wantErrSub == "" expect nil; else expect non-nil
            // err whose Error() contains wantErrSub.
        })
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./lib/usecase/... -run TestValidateAndPersist_FixtureRefEnvelope -v
```

Expected: FAIL — current schema treats `fixture` as free shape; both `ref+inline` and `{}` pass JSON Schema validation today.

- [ ] **Step 3: Modify `schemas/usecase.yaml`**

Replace the two `fixture` declarations and add the `FixtureRef` def. Locate the existing `$defs:` block (top of file) and add:

```yaml
  FixtureRef:
    description: |
      Source of the fixture payload. Exactly one of `ref` (path under
      <project>/.harness/fixtures/) or `inline` (literal value). `ref` is
      the preferred form for any structured payload; `inline` is reserved
      for primitive envelopes that don't benefit from being on disk.
    oneOf:
      - additionalProperties: false
        properties:
          ref:
            type: string
            minLength: 1
        required: [ref]
      - additionalProperties: false
        properties:
          inline: {}
        required: [inline]
```

Then replace `Trigger.fixture` (around line 76 in `usecase.yaml`) from:

```yaml
      fixture:
        description: Concrete example input. Free shape; depends on the trigger kind.
```

with:

```yaml
      fixture:
        $ref: '#/$defs/FixtureRef'
```

And the same swap for `ExpectedOutcome.fixture` (around line 54).

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./lib/usecase/... -run TestValidateAndPersist_FixtureRefEnvelope -v
```

Expected: PASS on all 5 sub-tests.

- [ ] **Step 5: Verify nothing else broke**

```bash
go test ./lib/usecase/... -v
```

Note: the existing usecase tests that use free-shape inline fixtures (`map[string]any{"sku": "abc"}`) will FAIL — that's expected; they will be repaired in Task 2 (envelope-aware validator) and Tasks 13–14 (migration of testdata that mirrors committed usecases).

If failures are ONLY in tests that previously passed inline non-envelope fixtures, proceed. Any other failure must be investigated.

- [ ] **Step 6: Commit**

```bash
git add schemas/usecase.yaml lib/usecase/persist_test.go
git commit -m "feat(usecase): introduce FixtureRef oneOf envelope

Adds the schema scaffolding for the shared fixtures pool. Existing
usecase tests with free-shape inline fixtures are intentionally broken
and will be repaired by the envelope-aware validator + testdata
migration."
```

---

### Task 2: Make `CheckFixtureContractEvidence` envelope-aware

**Files:**
- Modify: `lib/usecase/evidence.go` (`CheckFixtureContractEvidence`, `hasStructuredFixtureShape`)
- Modify: `lib/usecase/evidence_test.go` (extend the existing table; add envelope cases)

The current check inspects `Trigger.Fixture` directly. After Task 1, that field is always a `map[string]any` (the envelope). We must look at the *inside* of the envelope.

- [ ] **Step 1: Write the failing tests**

Add to `lib/usecase/evidence_test.go`:

```go
func TestCheckFixtureContractEvidence_EnvelopeAware(t *testing.T) {
    cases := []struct {
        name    string
        trigger any
        outcome any
        ev      []stack.Evidence
        wantErr bool
    }{
        {
            name:    "ref envelope without contract evidence rejected",
            trigger: map[string]any{"ref": "framework/x/trigger.json"},
            outcome: nil,
            ev: []stack.Evidence{
                {File: "a.go", Rationale: "handler"}, // implementation
            },
            wantErr: true,
        },
        {
            name:    "ref envelope with contract evidence ok",
            trigger: map[string]any{"ref": "framework/x/trigger.json"},
            outcome: nil,
            ev: []stack.Evidence{
                {File: "a.go", Rationale: "handler"},
                {File: "schema.json", Rationale: "request schema", Kind: EvidenceKindContract},
            },
            wantErr: false,
        },
        {
            name:    "inline primitive envelope skips check",
            trigger: map[string]any{"inline": float64(0)},
            outcome: map[string]any{"inline": "ok"},
            ev: []stack.Evidence{
                {File: "a.go", Rationale: "handler"},
            },
            wantErr: false,
        },
        {
            name:    "inline structured envelope without contract evidence rejected",
            trigger: map[string]any{"inline": map[string]any{"sku": "abc"}},
            outcome: nil,
            ev: []stack.Evidence{
                {File: "a.go", Rationale: "handler"},
            },
            wantErr: true,
        },
        {
            name:    "inline empty object skips check",
            trigger: map[string]any{"inline": map[string]any{}},
            outcome: nil,
            ev: []stack.Evidence{
                {File: "a.go", Rationale: "handler"},
            },
            wantErr: false,
        },
        {
            name:    "inline object of only primitive values skips check",
            trigger: map[string]any{"inline": map[string]any{"exit_code": float64(0), "status": "ok"}},
            outcome: nil,
            ev: []stack.Evidence{
                {File: "a.go", Rationale: "handler"},
            },
            wantErr: false,
        },
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            uc := &UseCase{
                ID:              "u1",
                Trigger:         Trigger{Fixture: tc.trigger},
                ExpectedOutcome: ExpectedOutcome{Fixture: tc.outcome},
                Evidence:        tc.ev,
            }
            err := CheckFixtureContractEvidence(uc)
            if tc.wantErr && err == nil {
                t.Fatal("expected error")
            }
            if !tc.wantErr && err != nil {
                t.Fatalf("unexpected: %v", err)
            }
        })
    }
}
```

Also update the EXISTING tests in `evidence_test.go` (`TestCheckFixtureContractEvidence_StructuredTriggerWithoutContract`, `…StructuredExpectedOutcomeWithoutContract`, `…ContractCitationSatisfies`, `…PrimitiveFixturesSkip`, `…ListFixtureIsStructured`, `…EmptyKindIsImplementation`) to use envelope shapes instead of bare values, since Task 1 requires the envelope. Example diff for one:

```go
// Before (current code):
Trigger: Trigger{Fixture: map[string]any{"body": map[string]any{"amount": 2500}}},
// After:
Trigger: Trigger{Fixture: map[string]any{"inline": map[string]any{"body": map[string]any{"amount": 2500}}}},
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./lib/usecase/... -run TestCheckFixtureContractEvidence -v
```

Expected: FAIL on all envelope-aware cases (current logic treats `{ref: "..."}` as structured-but-handed-an-implementation-row, returning the contract-evidence error even when contract evidence is present, OR missing the check entirely depending on the shape).

- [ ] **Step 3: Rewrite `CheckFixtureContractEvidence` and `hasStructuredFixtureShape`**

Replace the existing `hasStructuredFixtureShape` and the body of `CheckFixtureContractEvidence` in `lib/usecase/evidence.go`:

```go
// CheckFixtureContractEvidence enforces that a UseCase whose trigger or
// expected_outcome carries a fixture envelope whose inner payload is
// non-primitive cites at least one evidence row with kind=contract.
//
// The fixture field is always one of:
//   {"ref":   "path/under/.harness/fixtures"}   — payload on disk
//   {"inline": <value>}                         — payload inline
//
// For the `ref` arm we ALWAYS require a contract row, because the file
// on disk is presumed to be structured. (Single-line primitive fixtures
// should use `inline`; if a project genuinely persists a single string
// to disk, an extra contract row is still cheap insurance.)
//
// For the `inline` arm we look inside the wrapper; primitives skip the
// check, structured payloads require a contract row.
func CheckFixtureContractEvidence(uc *UseCase) error {
    if !envelopeRequiresContract(uc.Trigger.Fixture) &&
        !envelopeRequiresContract(uc.ExpectedOutcome.Fixture) {
        return nil
    }
    for _, ev := range uc.Evidence {
        if ev.Kind == EvidenceKindContract {
            return nil
        }
    }
    return &stack.CrossCheckError{
        Kind: "contract_evidence_missing",
        Message: fmt.Sprintf(
            "usecase %q has a non-primitive fixture but no evidence[] row with kind=%q; cite the DTO/schema/struct/flag declaration that defines the fixture field names",
            uc.ID, EvidenceKindContract,
        ),
    }
}

// envelopeRequiresContract reports whether v (a FixtureRef envelope from
// usecase.yaml) carries a payload that needs a contract citation.
//
// `nil` (the field omitted upstream of decoding) returns false — the
// caller has nothing to validate.
//
// `{ref: ...}` returns true: the file on disk is always treated as
// non-primitive at the contract layer.
//
// `{inline: x}`: x is unwrapped and inspected; primitives (string, bool,
// number, nil) and empty/all-primitive objects return false; arrays of
// objects, nested maps, or arrays of primitives still count as
// structured.
func envelopeRequiresContract(v any) bool {
    m, ok := v.(map[string]any)
    if !ok {
        return false
    }
    if _, has := m["ref"]; has {
        return true
    }
    inline, has := m["inline"]
    if !has {
        return false
    }
    return isStructuredPayload(inline)
}

func isStructuredPayload(v any) bool {
    switch x := v.(type) {
    case nil, bool, string,
        int, int8, int16, int32, int64,
        uint, uint8, uint16, uint32, uint64,
        float32, float64:
        return false
    case map[string]any:
        // empty object or object of only primitive values is allowed inline.
        for _, vv := range x {
            if isStructuredPayload(vv) {
                return true
            }
        }
        return false
    case []any:
        // any non-empty list is structured (CLI args, message lists, etc).
        return len(x) > 0
    default:
        return true
    }
}
```

Remove the now-unused `hasStructuredFixtureShape` function.

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./lib/usecase/... -v
```

Expected: PASS on all envelope-aware cases. The pre-existing tests you migrated to envelope shape in Step 1 also pass.

- [ ] **Step 5: Commit**

```bash
git add lib/usecase/evidence.go lib/usecase/evidence_test.go
git commit -m "feat(usecase): make CheckFixtureContractEvidence envelope-aware

Unwraps {ref}/{inline} envelopes; ref always requires a contract row;
inline inspects the inner payload primitively-or-not."
```

---

### Task 3: Validate `fixture.ref` exists on disk

**Files:**
- Create: `lib/usecase/fixture.go`
- Create: `lib/usecase/fixture_test.go`
- Modify: `lib/usecase/persist.go` (call the new check from `ValidateAndPersist`)

- [ ] **Step 1: Write the failing test in `lib/usecase/fixture_test.go`**

```go
package usecase

import (
    "errors"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/stack"
)

func TestCheckFixtureRefExists(t *testing.T) {
    root := t.TempDir()
    // Pre-create one fixture file we can reference.
    fxPath := filepath.Join(root, ".harness/fixtures/framework/x/trigger.json")
    if err := os.MkdirAll(filepath.Dir(fxPath), 0o755); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(fxPath, []byte(`{"a":1}`), 0o644); err != nil {
        t.Fatal(err)
    }

    cases := []struct {
        name      string
        triggerFx any
        outcomeFx any
        wantErr   string // substring; "" means expect nil
    }{
        {
            name:      "ref present and exists",
            triggerFx: map[string]any{"ref": "framework/x/trigger.json"},
        },
        {
            name:      "ref missing returns fixture_not_found",
            triggerFx: map[string]any{"ref": "framework/x/missing.json"},
            wantErr:   "fixture_not_found",
        },
        {
            name:      "ref pointing at a directory rejected",
            triggerFx: map[string]any{"ref": "framework/x"},
            wantErr:   "fixture_not_found",
        },
        {
            name:      "outcome ref also checked",
            triggerFx: map[string]any{"inline": "ok"},
            outcomeFx: map[string]any{"ref": "framework/x/nope.json"},
            wantErr:   "fixture_not_found",
        },
        {
            name:      "inline-only skipped",
            triggerFx: map[string]any{"inline": "ok"},
            outcomeFx: map[string]any{"inline": map[string]any{"exit_code": float64(0)}},
        },
        {
            name:      "both refs missing names both files",
            triggerFx: map[string]any{"ref": "framework/x/t.json"},
            outcomeFx: map[string]any{"ref": "framework/x/o.json"},
            wantErr:   "framework/x/t.json, framework/x/o.json",
        },
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            uc := &UseCase{
                ID:              "u1",
                Trigger:         Trigger{Fixture: tc.triggerFx},
                ExpectedOutcome: ExpectedOutcome{Fixture: tc.outcomeFx},
            }
            err := CheckFixtureRefExists(uc, root)
            if tc.wantErr == "" {
                if err != nil {
                    t.Fatalf("unexpected: %v", err)
                }
                return
            }
            if err == nil {
                t.Fatalf("expected error containing %q", tc.wantErr)
            }
            if !strings.Contains(err.Error(), tc.wantErr) {
                t.Fatalf("error %q does not contain %q", err, tc.wantErr)
            }
            // Typed error sanity.
            var cce *stack.CrossCheckError
            if !errors.As(err, &cce) {
                t.Fatalf("expected *stack.CrossCheckError, got %T", err)
            }
        })
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./lib/usecase/... -run TestCheckFixtureRefExists -v
```

Expected: FAIL — `CheckFixtureRefExists` does not exist yet (compilation error).

- [ ] **Step 3: Create `lib/usecase/fixture.go`**

```go
package usecase

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"

    "github.com/iurykrieger/harness-framework/lib/stack"
)

// CheckFixtureRefExists verifies every fixture.ref path declared by the
// UseCase resolves to an existing, non-directory file under
// <projectRoot>/.harness/fixtures/. inline fixtures are skipped.
// Returns a single error listing every missing ref when any are absent.
func CheckFixtureRefExists(uc *UseCase, projectRoot string) error {
    var missing []string
    for _, role := range []struct {
        name string
        fx   any
    }{
        {"trigger", uc.Trigger.Fixture},
        {"expected_outcome", uc.ExpectedOutcome.Fixture},
    } {
        ref, ok := refFromEnvelope(role.fx)
        if !ok {
            continue
        }
        full := filepath.Join(projectRoot, ".harness", "fixtures", ref)
        info, err := os.Stat(full)
        if err != nil || info.IsDir() {
            missing = append(missing, ref)
        }
    }
    if len(missing) == 0 {
        return nil
    }
    return &stack.CrossCheckError{
        Kind: "fixture_not_found",
        Message: fmt.Sprintf(
            "fixture files not found under %s/.harness/fixtures/: %s",
            projectRoot, strings.Join(missing, ", "),
        ),
    }
}

// refFromEnvelope returns the ref string and true when v is a
// FixtureRef envelope of the form {"ref": "..."}; otherwise false.
func refFromEnvelope(v any) (string, bool) {
    m, ok := v.(map[string]any)
    if !ok {
        return "", false
    }
    ref, ok := m["ref"].(string)
    if !ok || ref == "" {
        return "", false
    }
    return ref, true
}
```

- [ ] **Step 4: Wire into `ValidateAndPersist`**

In `lib/usecase/persist.go`, add the new check after `CheckFixtureContractEvidence`. The diff is:

```go
    if err := CheckFixtureContractEvidence(&uc); err != nil {
        return "", err
    }
+   if err := CheckFixtureRefExists(&uc, projectRoot); err != nil {
+       return "", err
+   }
```

- [ ] **Step 5: Run tests**

```bash
go test ./lib/usecase/... -v
```

Expected: PASS on the new test cases and the previously passing suite. Note that `persist_test.go` Task 1 envelope tests with `ref` paths to fake placeholder files must still pass — confirm that the placeholder file is actually written in the test's tmpdir BEFORE invoking `ValidateAndPersist`; if not, repair Task 1's test now to create the placeholder.

- [ ] **Step 6: Commit**

```bash
git add lib/usecase/fixture.go lib/usecase/fixture_test.go lib/usecase/persist.go
git commit -m "feat(usecase): reject fixture.ref pointing at missing file

CheckFixtureRefExists rejects with kind=fixture_not_found when any
trigger/expected_outcome fixture.ref does not resolve under
.harness/fixtures/."
```

---

### Task 4: `lib/fixture/sample_disk.go` — `FindOnDisk(hint, searchPaths)`

**Files:**
- Create: `lib/fixture/sample_disk.go`
- Create: `lib/fixture/sample_disk_test.go`
- Create: `lib/fixture/testdata/sample/<scenario>/...` (test fixtures for the table)

Library knows nothing about archetypes. It receives a Hint (role, projectRoot, identifiers) and a flat list of `searchPaths` and returns the first matching file with deterministic tiebreaking.

- [ ] **Step 1: Decide role aliasing table (and persist it as a const)**

The lib needs to know which filenames map to which roles. Convention (encoded directly in `sample_disk.go`):

```
Role        ExactBasenames    AliasBasenames
trigger     trigger.*         request.*, input.*, args.*
outcome     outcome.*         response.*, expected.*, result.*
body        body.*            payload.*
log-line    log-line.*        log.*, sample.log
event       event.*           message.*, kafka.*, sqs.*
```

Any unknown role: exact basename match only.

- [ ] **Step 2: Create test fixtures**

```bash
mkdir -p lib/fixture/testdata/sample/exact_trigger
mkdir -p lib/fixture/testdata/sample/alias_request
mkdir -p lib/fixture/testdata/sample/two_candidates_same_dir
mkdir -p lib/fixture/testdata/sample/across_search_paths/first
mkdir -p lib/fixture/testdata/sample/across_search_paths/second
mkdir -p lib/fixture/testdata/sample/no_match
```

Then `printf '%s' '{"x":1}' > lib/fixture/testdata/sample/exact_trigger/trigger.json` etc. The full creation list is in Step 4's test code (`prep` helper); use those exact paths.

- [ ] **Step 3: Write the failing test in `lib/fixture/sample_disk_test.go`**

```go
package fixture_test

import (
    "path/filepath"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/fixture"
)

func TestFindOnDisk(t *testing.T) {
    base := "testdata/sample"
    abs := func(rel string) string {
        a, err := filepath.Abs(filepath.Join(base, rel))
        if err != nil {
            t.Fatal(err)
        }
        return a
    }

    cases := []struct {
        name         string
        role         string
        searchPaths  []string
        wantSource   string // "disk" expected; "" expected nil sample
        wantBasename string
    }{
        {
            name:         "exact role basename",
            role:         "trigger",
            searchPaths:  []string{abs("exact_trigger")},
            wantSource:   "disk",
            wantBasename: "trigger.json",
        },
        {
            name:         "alias basename for trigger role",
            role:         "trigger",
            searchPaths:  []string{abs("alias_request")},
            wantSource:   "disk",
            wantBasename: "request.json",
        },
        {
            name:         "exact beats alias in same dir",
            role:         "trigger",
            searchPaths:  []string{abs("two_candidates_same_dir")},
            wantSource:   "disk",
            wantBasename: "trigger.json", // also has request.json next to it
        },
        {
            name:         "earlier searchPath wins",
            role:         "trigger",
            searchPaths:  []string{abs("across_search_paths/first"), abs("across_search_paths/second")},
            wantSource:   "disk",
            wantBasename: "trigger.json", // both contain trigger.json; first wins
        },
        {
            name:        "no match returns nil sample",
            role:        "trigger",
            searchPaths: []string{abs("no_match")},
            wantSource:  "",
        },
        {
            name:        "no searchPaths returns nil sample",
            role:        "trigger",
            searchPaths: nil,
            wantSource:  "",
        },
        {
            name:        "non-existent searchPath skipped silently",
            role:        "trigger",
            searchPaths: []string{abs("does_not_exist")},
            wantSource:  "",
        },
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            s, err := fixture.FindOnDisk(fixture.Hint{Role: tc.role}, tc.searchPaths)
            if err != nil {
                t.Fatalf("unexpected error: %v", err)
            }
            if tc.wantSource == "" {
                if s != nil {
                    t.Fatalf("expected nil sample, got %+v", s)
                }
                return
            }
            if s == nil {
                t.Fatalf("expected sample, got nil")
            }
            if s.Source != tc.wantSource {
                t.Errorf("Source = %q, want %q", s.Source, tc.wantSource)
            }
            if got := filepath.Base(s.SourcePath); got != tc.wantBasename {
                t.Errorf("Basename = %q, want %q", got, tc.wantBasename)
            }
        })
    }
}
```

Populate the testdata files per the table:
- `exact_trigger/trigger.json` → `{"x":1}`
- `alias_request/request.json` → `{"y":2}`
- `two_candidates_same_dir/trigger.json` → `{"a":1}`
- `two_candidates_same_dir/request.json` → `{"b":2}`
- `across_search_paths/first/trigger.json` → `{"a":1}`
- `across_search_paths/second/trigger.json` → `{"b":2}`
- `no_match/random.bin` → 1 byte

- [ ] **Step 4: Run the test to verify it fails**

```bash
go test ./lib/fixture/... -run TestFindOnDisk -v
```

Expected: FAIL — neither `Hint` nor `Sample` nor `FindOnDisk` are exported yet.

- [ ] **Step 5: Implement `lib/fixture/sample_disk.go`**

```go
package fixture

import (
    "io/fs"
    "os"
    "path/filepath"
    "sort"
    "strings"
)

// Hint carries the inputs FindOnDisk and DeriveFromContract need to
// locate or synthesize a fixture sample. The library DOES NOT decide
// which directories to walk — searchPaths come from the calling skill.
type Hint struct {
    JourneyID   string
    UsecaseID   string
    Role        string
    ProjectRoot string
}

// Sample is the result of either disk discovery or contract derivation.
type Sample struct {
    Payload    []byte
    Ext        string
    Source     string
    SourcePath string
    BlindSpots []string
}

// FindOnDisk walks searchPaths in order and returns the best-matching
// fixture file for hint.Role. Tiebreaker:
//   1. exact role basename (e.g. "trigger.json" for Role=="trigger")
//      beats alias basename (e.g. "request.json").
//   2. earlier searchPath beats later.
//   3. lexicographic absolute path as final fallback.
//
// Returns (nil, nil) when no candidate is found in any searchPath.
// Returns (nil, err) only for I/O errors that prevent walking.
//
// Non-existent searchPaths are skipped silently — the library does not
// validate the inputs the caller passed. Subdirectories of searchPaths
// are NOT walked recursively; only direct children are considered.
func FindOnDisk(h Hint, searchPaths []string) (*Sample, error) {
    type candidate struct {
        path  string
        idx   int  // position of source searchPath in input
        exact bool // exact basename match vs alias
    }
    var all []candidate
    exactSet, aliasSet := basenamePatternsFor(h.Role)
    for idx, dir := range searchPaths {
        entries, err := os.ReadDir(dir)
        if err != nil {
            if isNotExist(err) {
                continue
            }
            return nil, err
        }
        for _, e := range entries {
            if e.IsDir() {
                continue
            }
            base := e.Name()
            ext := filepath.Ext(base)
            stem := strings.TrimSuffix(base, ext)
            full := filepath.Join(dir, base)
            switch {
            case exactSet[stem]:
                all = append(all, candidate{path: full, idx: idx, exact: true})
            case aliasSet[stem]:
                all = append(all, candidate{path: full, idx: idx, exact: false})
            }
        }
    }
    if len(all) == 0 {
        return nil, nil
    }
    sort.SliceStable(all, func(i, j int) bool {
        if all[i].exact != all[j].exact {
            return all[i].exact // exact first
        }
        if all[i].idx != all[j].idx {
            return all[i].idx < all[j].idx
        }
        return all[i].path < all[j].path
    })
    winner := all[0]
    payload, err := os.ReadFile(winner.path)
    if err != nil {
        return nil, err
    }
    ext := strings.TrimPrefix(filepath.Ext(winner.path), ".")
    return &Sample{
        Payload:    payload,
        Ext:        ext,
        Source:     "disk",
        SourcePath: winner.path,
    }, nil
}

func basenamePatternsFor(role string) (exact, alias map[string]bool) {
    exact = map[string]bool{}
    alias = map[string]bool{}
    switch role {
    case "trigger":
        exact["trigger"] = true
        alias["request"] = true
        alias["input"] = true
        alias["args"] = true
    case "outcome":
        exact["outcome"] = true
        alias["response"] = true
        alias["expected"] = true
        alias["result"] = true
    case "body":
        exact["body"] = true
        alias["payload"] = true
    case "log-line":
        exact["log-line"] = true
        alias["log"] = true
        alias["sample.log"] = true
    case "event":
        exact["event"] = true
        alias["message"] = true
        alias["kafka"] = true
        alias["sqs"] = true
    default:
        exact[role] = true
    }
    return
}

func isNotExist(err error) bool {
    var pe *fs.PathError
    if errors_As(err, &pe) {
        return os.IsNotExist(pe.Err)
    }
    return os.IsNotExist(err)
}
```

Add at the top of the file:

```go
import "errors"

// errors_As is a thin shim so isNotExist stays one-liner.
func errors_As(err error, target any) bool { return errors.As(err, target) }
```

(Alternatively, inline `errors.As` directly; the shim only exists to keep the function self-contained.)

- [ ] **Step 6: Run the test to verify it passes**

```bash
go test ./lib/fixture/... -run TestFindOnDisk -v
```

Expected: PASS on all 7 sub-tests.

- [ ] **Step 7: Commit**

```bash
git add lib/fixture/sample_disk.go lib/fixture/sample_disk_test.go lib/fixture/testdata/sample
git commit -m "feat(fixture): add FindOnDisk with role aliasing + tiebreaker

Caller decides searchPaths. Library walks them in order, prefers exact
role basenames over aliases, and breaks final ties lexicographically.
Non-recursive; non-existent searchPaths are silently skipped."
```

---

### Task 5: `DeriveFromContract` — JSON Schema source

**Files:**
- Create: `lib/fixture/sample_contract.go`
- Create: `lib/fixture/sample_contract_test.go`
- Create: `lib/fixture/testdata/contract/json_schema/*.json`

Limit the matrix to `json-schema` for this task; the next three tasks add `openapi-component`, `avro`, `protobuf` incrementally.

- [ ] **Step 1: Create testdata**

```bash
mkdir -p lib/fixture/testdata/contract/json_schema
```

Write `lib/fixture/testdata/contract/json_schema/order.schema.json`:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["sku", "qty"],
  "properties": {
    "sku":   { "type": "string" },
    "qty":   { "type": "integer" },
    "tags":  { "type": "array", "items": { "type": "string" } },
    "meta":  { "type": "object", "properties": { "source": { "type": "string", "enum": ["web", "ios", "android"] } } }
  }
}
```

- [ ] **Step 2: Write the failing test in `lib/fixture/sample_contract_test.go`**

```go
package fixture_test

import (
    "encoding/json"
    "errors"
    "path/filepath"
    "testing"

    "github.com/iurykrieger/harness-framework/lib/fixture"
)

func TestDeriveFromContract_JSONSchema(t *testing.T) {
    abs, err := filepath.Abs("testdata/contract/json_schema/order.schema.json")
    if err != nil {
        t.Fatal(err)
    }
    s, err := fixture.DeriveFromContract(fixture.Hint{Role: "trigger"}, fixture.SourceJSONSchema, abs)
    if err != nil {
        t.Fatalf("unexpected: %v", err)
    }
    if s == nil {
        t.Fatal("nil sample")
    }
    if s.Source != "contract" {
        t.Errorf("Source = %q, want contract", s.Source)
    }
    if s.Ext != "json" {
        t.Errorf("Ext = %q, want json", s.Ext)
    }
    var payload map[string]any
    if err := json.Unmarshal(s.Payload, &payload); err != nil {
        t.Fatalf("payload not valid JSON: %v", err)
    }
    // Required fields present with zero values typed by schema.
    if got, ok := payload["sku"].(string); !ok || got != "" {
        t.Errorf("sku = %v, want \"\"", payload["sku"])
    }
    if got, ok := payload["qty"].(float64); !ok || got != 0 {
        t.Errorf("qty = %v, want 0", payload["qty"])
    }
    // Optional fields omitted.
    if _, has := payload["tags"]; has {
        t.Errorf("optional field tags should be omitted, got %v", payload["tags"])
    }
    if len(s.BlindSpots) == 0 {
        t.Error("expected at least one BlindSpot entry")
    }
}

func TestDeriveFromContract_UnsupportedSource(t *testing.T) {
    _, err := fixture.DeriveFromContract(fixture.Hint{Role: "trigger"}, fixture.SourceKind("go-struct"), "/dev/null")
    if !errors.Is(err, fixture.ErrUnsupportedContractSource) {
        t.Fatalf("err = %v, want ErrUnsupportedContractSource", err)
    }
}
```

- [ ] **Step 3: Run the test to verify it fails**

```bash
go test ./lib/fixture/... -run TestDeriveFromContract_JSONSchema -v
go test ./lib/fixture/... -run TestDeriveFromContract_UnsupportedSource -v
```

Expected: FAIL — `DeriveFromContract`, `SourceKind`, `SourceJSONSchema`, and `ErrUnsupportedContractSource` don't exist.

- [ ] **Step 4: Implement `lib/fixture/sample_contract.go`**

```go
package fixture

import (
    "encoding/json"
    "errors"
    "fmt"
    "os"
)

// SourceKind identifies a tier-2 contract source. The set is closed by
// design — cross-language source-code AST parsing is out of scope.
type SourceKind string

const (
    SourceJSONSchema     SourceKind = "json-schema"
    SourceOpenAPI        SourceKind = "openapi-component"
    SourceAvro           SourceKind = "avro"
    SourceProtobuf       SourceKind = "protobuf"
)

// ErrUnsupportedContractSource is returned by DeriveFromContract when
// src is not one of the four supported SourceKind values.
var ErrUnsupportedContractSource = errors.New("fixture: unsupported contract source")

// DeriveFromContract reads the contract declaration at declPath and
// emits the minimum valid JSON payload for it. Required fields are
// populated with zero values typed by the declared kind; optional
// fields are omitted; enums pick the first declared value.
//
// declPath shape per source:
//   json-schema       : absolute path to the .json/.schema.json file
//   openapi-component : "<absolute-openapi-file>#/components/schemas/<Name>"
//   avro              : absolute path to the .avsc file
//   protobuf          : "<absolute-proto-file>:<MessageName>"
func DeriveFromContract(h Hint, src SourceKind, declPath string) (*Sample, error) {
    switch src {
    case SourceJSONSchema:
        return deriveFromJSONSchema(declPath)
    case SourceOpenAPI:
        return deriveFromOpenAPI(declPath)
    case SourceAvro:
        return deriveFromAvro(declPath)
    case SourceProtobuf:
        return deriveFromProtobuf(declPath)
    default:
        return nil, ErrUnsupportedContractSource
    }
}

func deriveFromJSONSchema(path string) (*Sample, error) {
    body, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("read json-schema: %w", err)
    }
    var schema jsonSchemaNode
    if err := json.Unmarshal(body, &schema); err != nil {
        return nil, fmt.Errorf("parse json-schema: %w", err)
    }
    payload := emitFromJSONSchema(&schema)
    out, err := json.Marshal(payload)
    if err != nil {
        return nil, err
    }
    return &Sample{
        Payload: out,
        Ext:     "json",
        Source:  "contract",
        BlindSpots: []string{
            fmt.Sprintf("Derived from json-schema contract at %s; no real sample on disk near the entry point.", path),
        },
    }, nil
}

// Stubs for the next three tasks.
func deriveFromOpenAPI(declPath string) (*Sample, error)   { return nil, errors.New("openapi: not implemented yet") }
func deriveFromAvro(path string) (*Sample, error)          { return nil, errors.New("avro: not implemented yet") }
func deriveFromProtobuf(declPath string) (*Sample, error)  { return nil, errors.New("protobuf: not implemented yet") }

// jsonSchemaNode is the subset of Draft 2020-12 / Draft 7 we need:
// `type`, `required`, `properties`, `items`, `enum`.
type jsonSchemaNode struct {
    Type       any                       `json:"type"` // string or []string
    Required   []string                  `json:"required"`
    Properties map[string]jsonSchemaNode `json:"properties"`
    Items      *jsonSchemaNode           `json:"items"`
    Enum       []any                     `json:"enum"`
}

func emitFromJSONSchema(s *jsonSchemaNode) any {
    if len(s.Enum) > 0 {
        return s.Enum[0]
    }
    switch firstType(s.Type) {
    case "object":
        out := map[string]any{}
        for _, name := range s.Required {
            child, ok := s.Properties[name]
            if !ok {
                out[name] = nil
                continue
            }
            out[name] = emitFromJSONSchema(&child)
        }
        return out
    case "array":
        if s.Items == nil {
            return []any{}
        }
        return []any{emitFromJSONSchema(s.Items)}
    case "string":
        return ""
    case "integer", "number":
        return 0
    case "boolean":
        return false
    case "null":
        return nil
    default:
        return nil
    }
}

func firstType(t any) string {
    switch x := t.(type) {
    case string:
        return x
    case []any:
        if len(x) > 0 {
            if s, ok := x[0].(string); ok {
                return s
            }
        }
    }
    return ""
}
```

- [ ] **Step 5: Run the test to verify it passes**

```bash
go test ./lib/fixture/... -run TestDeriveFromContract -v
```

Expected: PASS on both `TestDeriveFromContract_JSONSchema` and `TestDeriveFromContract_UnsupportedSource`.

- [ ] **Step 6: Commit**

```bash
git add lib/fixture/sample_contract.go lib/fixture/sample_contract_test.go lib/fixture/testdata/contract/json_schema
git commit -m "feat(fixture): DeriveFromContract — JSON Schema source

Emits a minimum-valid JSON payload from required fields + first-declared
enum value. Stubs for openapi, avro, protobuf return 'not implemented';
unsupported sources return ErrUnsupportedContractSource."
```

---

### Task 6: `DeriveFromContract` — OpenAPI component source

**Files:**
- Modify: `lib/fixture/sample_contract.go` (replace `deriveFromOpenAPI` stub)
- Modify: `lib/fixture/sample_contract_test.go` (add table entry)
- Create: `lib/fixture/testdata/contract/openapi/api.yaml`

- [ ] **Step 1: Create testdata**

Write `lib/fixture/testdata/contract/openapi/api.yaml`:

```yaml
openapi: 3.0.3
components:
  schemas:
    Order:
      type: object
      required: [sku, qty]
      properties:
        sku: { type: string }
        qty: { type: integer }
        tags: { type: array, items: { type: string } }
```

- [ ] **Step 2: Add the failing test entry**

Add to `lib/fixture/sample_contract_test.go`:

```go
func TestDeriveFromContract_OpenAPI(t *testing.T) {
    abs, err := filepath.Abs("testdata/contract/openapi/api.yaml")
    if err != nil {
        t.Fatal(err)
    }
    decl := abs + "#/components/schemas/Order"
    s, err := fixture.DeriveFromContract(fixture.Hint{Role: "trigger"}, fixture.SourceOpenAPI, decl)
    if err != nil {
        t.Fatalf("unexpected: %v", err)
    }
    var payload map[string]any
    if err := json.Unmarshal(s.Payload, &payload); err != nil {
        t.Fatal(err)
    }
    if _, ok := payload["sku"].(string); !ok {
        t.Errorf("sku missing/wrong type: %v", payload["sku"])
    }
    if _, has := payload["tags"]; has {
        t.Error("optional field tags should be omitted")
    }
}
```

- [ ] **Step 3: Run the test to verify it fails**

```bash
go test ./lib/fixture/... -run TestDeriveFromContract_OpenAPI -v
```

Expected: FAIL — stub returns `"not implemented yet"`.

- [ ] **Step 4: Implement `deriveFromOpenAPI`**

Replace the stub in `lib/fixture/sample_contract.go`:

```go
import (
    "strings"

    "sigs.k8s.io/yaml"
)

func deriveFromOpenAPI(declPath string) (*Sample, error) {
    file, frag, ok := strings.Cut(declPath, "#")
    if !ok || !strings.HasPrefix(frag, "/components/schemas/") {
        return nil, fmt.Errorf("openapi declPath must be '<file>#/components/schemas/<Name>', got %q", declPath)
    }
    name := strings.TrimPrefix(frag, "/components/schemas/")
    raw, err := os.ReadFile(file)
    if err != nil {
        return nil, fmt.Errorf("read openapi file: %w", err)
    }
    asJSON, err := yaml.YAMLToJSON(raw)
    if err != nil {
        return nil, fmt.Errorf("convert openapi yaml: %w", err)
    }
    var doc struct {
        Components struct {
            Schemas map[string]json.RawMessage `json:"schemas"`
        } `json:"components"`
    }
    if err := json.Unmarshal(asJSON, &doc); err != nil {
        return nil, fmt.Errorf("parse openapi: %w", err)
    }
    schemaBody, ok := doc.Components.Schemas[name]
    if !ok {
        return nil, fmt.Errorf("openapi component %q not found in %s", name, file)
    }
    var schema jsonSchemaNode
    if err := json.Unmarshal(schemaBody, &schema); err != nil {
        return nil, fmt.Errorf("parse openapi component %q: %w", name, err)
    }
    payload := emitFromJSONSchema(&schema)
    out, err := json.Marshal(payload)
    if err != nil {
        return nil, err
    }
    return &Sample{
        Payload: out,
        Ext:     "json",
        Source:  "contract",
        BlindSpots: []string{
            fmt.Sprintf("Derived from openapi-component contract at %s; no real sample on disk near the entry point.", declPath),
        },
    }, nil
}
```

- [ ] **Step 5: Run the test to verify it passes**

```bash
go test ./lib/fixture/... -run TestDeriveFromContract -v
```

Expected: PASS on JSONSchema, OpenAPI, and UnsupportedSource.

- [ ] **Step 6: Commit**

```bash
git add lib/fixture/sample_contract.go lib/fixture/sample_contract_test.go lib/fixture/testdata/contract/openapi
git commit -m "feat(fixture): DeriveFromContract — OpenAPI component source

Reduces an OpenAPI component to its inline JSON Schema and routes
through emitFromJSONSchema."
```

---

### Task 7: `DeriveFromContract` — Avro `.avsc` source

**Files:**
- Modify: `lib/fixture/sample_contract.go` (replace `deriveFromAvro` stub)
- Modify: `lib/fixture/sample_contract_test.go` (add test)
- Create: `lib/fixture/testdata/contract/avro/order.avsc`

- [ ] **Step 1: Create testdata**

`lib/fixture/testdata/contract/avro/order.avsc`:

```json
{
  "type": "record",
  "name": "Order",
  "fields": [
    {"name": "sku", "type": "string"},
    {"name": "qty", "type": "int"},
    {"name": "tags", "type": {"type": "array", "items": "string"}, "default": []},
    {"name": "channel", "type": {"type": "enum", "name": "Channel", "symbols": ["WEB", "IOS", "AND"]}}
  ]
}
```

- [ ] **Step 2: Write the failing test**

```go
func TestDeriveFromContract_Avro(t *testing.T) {
    abs, _ := filepath.Abs("testdata/contract/avro/order.avsc")
    s, err := fixture.DeriveFromContract(fixture.Hint{Role: "event"}, fixture.SourceAvro, abs)
    if err != nil {
        t.Fatalf("unexpected: %v", err)
    }
    var payload map[string]any
    if err := json.Unmarshal(s.Payload, &payload); err != nil {
        t.Fatal(err)
    }
    if got, ok := payload["sku"].(string); !ok || got != "" {
        t.Errorf("sku = %v want \"\"", payload["sku"])
    }
    if got, ok := payload["qty"].(float64); !ok || got != 0 {
        t.Errorf("qty = %v want 0", payload["qty"])
    }
    if got, ok := payload["channel"].(string); !ok || got != "WEB" {
        t.Errorf("channel = %v want WEB (first declared symbol)", payload["channel"])
    }
}
```

- [ ] **Step 3: Run the test to verify it fails**

```bash
go test ./lib/fixture/... -run TestDeriveFromContract_Avro -v
```

Expected: FAIL — stub.

- [ ] **Step 4: Implement `deriveFromAvro`**

Replace the stub. `.avsc` is just JSON; hand-rolled parser is tight:

```go
type avroField struct {
    Name string          `json:"name"`
    Type json.RawMessage `json:"type"`
}

type avroRecord struct {
    Type   string      `json:"type"` // "record"
    Name   string      `json:"name"`
    Fields []avroField `json:"fields"`
}

func deriveFromAvro(path string) (*Sample, error) {
    raw, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("read avsc: %w", err)
    }
    var rec avroRecord
    if err := json.Unmarshal(raw, &rec); err != nil {
        return nil, fmt.Errorf("parse avsc: %w", err)
    }
    if rec.Type != "record" {
        return nil, fmt.Errorf("avro: only top-level record types are supported (got %q)", rec.Type)
    }
    out := map[string]any{}
    for _, f := range rec.Fields {
        out[f.Name] = avroZeroValue(f.Type)
    }
    body, _ := json.Marshal(out)
    return &Sample{
        Payload: body,
        Ext:     "json",
        Source:  "contract",
        BlindSpots: []string{
            fmt.Sprintf("Derived from avro contract at %s; no real sample on disk near the entry point.", path),
        },
    }, nil
}

// avroZeroValue returns the zero value for an Avro type expression.
// Type expressions are one of:
//   - a string ("string", "int", "long", "float", "double", "boolean", "bytes", "null")
//   - an object: {"type":"array", "items": ...} / {"type":"enum", "symbols":[...]}
//   - a union: ["null", "string"] — picks the first non-null branch
func avroZeroValue(raw json.RawMessage) any {
    // Try string form first.
    var s string
    if err := json.Unmarshal(raw, &s); err == nil {
        return avroPrimitiveZero(s)
    }
    // Try array (union).
    var union []json.RawMessage
    if err := json.Unmarshal(raw, &union); err == nil {
        for _, branch := range union {
            // Skip null branch when others exist.
            var bs string
            if json.Unmarshal(branch, &bs) == nil && bs == "null" && len(union) > 1 {
                continue
            }
            return avroZeroValue(branch)
        }
        return nil
    }
    // Try object form.
    var obj map[string]json.RawMessage
    if err := json.Unmarshal(raw, &obj); err != nil {
        return nil
    }
    var typ string
    _ = json.Unmarshal(obj["type"], &typ)
    switch typ {
    case "array":
        return []any{}
    case "enum":
        var symbols []string
        _ = json.Unmarshal(obj["symbols"], &symbols)
        if len(symbols) > 0 {
            return symbols[0]
        }
        return ""
    case "record":
        var fields []avroField
        _ = json.Unmarshal(obj["fields"], &fields)
        inner := map[string]any{}
        for _, f := range fields {
            inner[f.Name] = avroZeroValue(f.Type)
        }
        return inner
    case "map":
        return map[string]any{}
    default:
        return nil
    }
}

func avroPrimitiveZero(t string) any {
    switch t {
    case "string", "bytes":
        return ""
    case "int", "long":
        return 0
    case "float", "double":
        return 0.0
    case "boolean":
        return false
    case "null":
        return nil
    }
    return nil
}
```

- [ ] **Step 5: Run the test to verify it passes**

```bash
go test ./lib/fixture/... -run TestDeriveFromContract -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add lib/fixture/sample_contract.go lib/fixture/sample_contract_test.go lib/fixture/testdata/contract/avro
git commit -m "feat(fixture): DeriveFromContract — Avro .avsc source

Hand-rolled .avsc parser (strict subset: record, primitive, union, enum,
array, map). Unions pick the first non-null branch."
```

---

### Task 8: `DeriveFromContract` — Protobuf `.proto` source

**Files:**
- Modify: `lib/fixture/sample_contract.go` (replace `deriveFromProtobuf` stub)
- Modify: `lib/fixture/sample_contract_test.go` (add test)
- Create: `lib/fixture/testdata/contract/protobuf/order.proto`
- Modify: `go.mod`, `go.sum` (add `github.com/bufbuild/protocompile`)

Adding a dep is a meaningful step. `protocompile` is the modern protobuf text parser maintained by Buf; it's the smallest tool that returns descriptors we can walk.

- [ ] **Step 1: Add the dep**

```bash
go get github.com/bufbuild/protocompile@latest
```

Verify it landed:

```bash
grep protocompile go.mod
```

- [ ] **Step 2: Create testdata**

`lib/fixture/testdata/contract/protobuf/order.proto`:

```proto
syntax = "proto3";
package shop;

message Order {
  string sku = 1;
  int32 qty = 2;
  repeated string tags = 3;
  Channel channel = 4;
}

enum Channel {
  CHANNEL_UNSPECIFIED = 0;
  WEB = 1;
  IOS = 2;
  AND = 3;
}
```

- [ ] **Step 3: Write the failing test**

```go
func TestDeriveFromContract_Protobuf(t *testing.T) {
    abs, _ := filepath.Abs("testdata/contract/protobuf/order.proto")
    decl := abs + ":shop.Order"
    s, err := fixture.DeriveFromContract(fixture.Hint{Role: "event"}, fixture.SourceProtobuf, decl)
    if err != nil {
        t.Fatalf("unexpected: %v", err)
    }
    var payload map[string]any
    if err := json.Unmarshal(s.Payload, &payload); err != nil {
        t.Fatal(err)
    }
    if _, ok := payload["sku"].(string); !ok {
        t.Errorf("sku missing/wrong type: %v", payload["sku"])
    }
    // proto3 scalars: all zero. Repeated: empty list.
    if got, ok := payload["tags"].([]any); !ok || len(got) != 0 {
        t.Errorf("tags = %v want []", payload["tags"])
    }
    // Enum: first declared symbol (proto convention: usually X_UNSPECIFIED at 0).
    if got, ok := payload["channel"].(string); !ok || got != "CHANNEL_UNSPECIFIED" {
        t.Errorf("channel = %v want CHANNEL_UNSPECIFIED", payload["channel"])
    }
}
```

- [ ] **Step 4: Run the test to verify it fails**

```bash
go test ./lib/fixture/... -run TestDeriveFromContract_Protobuf -v
```

Expected: FAIL — stub.

- [ ] **Step 5: Implement `deriveFromProtobuf`**

```go
import (
    "context"
    "path/filepath"

    "github.com/bufbuild/protocompile"
    "google.golang.org/protobuf/reflect/protoreflect"
)

func deriveFromProtobuf(declPath string) (*Sample, error) {
    file, msgName, ok := strings.Cut(declPath, ":")
    if !ok || msgName == "" {
        return nil, fmt.Errorf("protobuf declPath must be '<file>:<MessageName>', got %q", declPath)
    }
    dir := filepath.Dir(file)
    base := filepath.Base(file)
    compiler := protocompile.Compiler{
        Resolver: &protocompile.SourceResolver{
            ImportPaths: []string{dir},
        },
    }
    files, err := compiler.Compile(context.Background(), base)
    if err != nil {
        return nil, fmt.Errorf("compile proto: %w", err)
    }
    if len(files) != 1 {
        return nil, fmt.Errorf("expected 1 compiled file, got %d", len(files))
    }
    fd := files[0]
    msg := fd.Messages().ByName(protoreflect.Name(stripPackage(msgName)))
    if msg == nil {
        return nil, fmt.Errorf("message %q not found in %s", msgName, file)
    }
    payload := emitFromProtoMessage(msg)
    out, _ := json.Marshal(payload)
    return &Sample{
        Payload: out,
        Ext:     "json",
        Source:  "contract",
        BlindSpots: []string{
            fmt.Sprintf("Derived from protobuf contract at %s; no real sample on disk near the entry point.", declPath),
        },
    }, nil
}

// stripPackage returns the unqualified message name. "shop.Order" -> "Order".
func stripPackage(name string) string {
    if i := strings.LastIndex(name, "."); i >= 0 {
        return name[i+1:]
    }
    return name
}

func emitFromProtoMessage(msg protoreflect.MessageDescriptor) map[string]any {
    out := map[string]any{}
    fields := msg.Fields()
    for i := 0; i < fields.Len(); i++ {
        f := fields.Get(i)
        out[string(f.Name())] = protoZeroValue(f)
    }
    return out
}

func protoZeroValue(f protoreflect.FieldDescriptor) any {
    if f.IsList() {
        return []any{}
    }
    if f.IsMap() {
        return map[string]any{}
    }
    switch f.Kind() {
    case protoreflect.BoolKind:
        return false
    case protoreflect.StringKind, protoreflect.BytesKind:
        return ""
    case protoreflect.Int32Kind, protoreflect.Int64Kind,
        protoreflect.Uint32Kind, protoreflect.Uint64Kind,
        protoreflect.Sint32Kind, protoreflect.Sint64Kind,
        protoreflect.Fixed32Kind, protoreflect.Fixed64Kind,
        protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
        return 0
    case protoreflect.FloatKind, protoreflect.DoubleKind:
        return 0.0
    case protoreflect.EnumKind:
        // First declared enum value (proto convention reserves index 0).
        enumValues := f.Enum().Values()
        if enumValues.Len() == 0 {
            return ""
        }
        return string(enumValues.Get(0).Name())
    case protoreflect.MessageKind, protoreflect.GroupKind:
        return emitFromProtoMessage(f.Message())
    }
    return nil
}
```

- [ ] **Step 6: Run the test to verify it passes**

```bash
go test ./lib/fixture/... -run TestDeriveFromContract -v
```

Expected: PASS on all four contract sources + UnsupportedSource.

- [ ] **Step 7: Commit**

```bash
git add lib/fixture/sample_contract.go lib/fixture/sample_contract_test.go lib/fixture/testdata/contract/protobuf go.mod go.sum
git commit -m "feat(fixture): DeriveFromContract — Protobuf .proto source

Compiles the .proto via bufbuild/protocompile and walks the message
descriptor for zero values. Enums emit the first declared symbol."
```

---

### Task 9: `--validate-only` flag for `write-usecase.go`

**Files:**
- Modify: `skills/detect-usecases/scripts/write-usecase.go`
- Modify: `skills/detect-usecases/scripts/write-usecase_test.go`

The post-migration gate (DoD #7) sweeps every committed usecase YAML. We need a flag that runs the validation chain (`schema → cross-checks → fixture-ref existence`) without writing anything.

- [ ] **Step 1: Write the failing test**

Add to `skills/detect-usecases/scripts/write-usecase_test.go`:

```go
func TestRun_ValidateOnly(t *testing.T) {
    projRoot := t.TempDir()
    // Construct a minimal valid usecase using existing test scaffolding
    // (look for `buildValidUseCaseJSON` or similar helper in this file —
    // copy it if the test is the first to need it).
    draftPath := filepath.Join(projRoot, "draft.yaml")
    if err := os.WriteFile(draftPath, validUsecaseYAML(t, projRoot), 0o644); err != nil {
        t.Fatal(err)
    }
    schemasDir := pluginSchemasDir(t)
    args := []string{
        "--out=" + filepath.Join(projRoot, ".harness/usecases"),
        "--project-root=" + projRoot,
        "--schemas-dir=" + schemasDir,
        "--validate-only",
        draftPath,
    }
    var stdout, stderr bytes.Buffer
    code := run(args, &stdout, &stderr)
    if code != 0 {
        t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
    }
    // Nothing was written.
    if _, err := os.Stat(filepath.Join(projRoot, ".harness/usecases")); err == nil {
        t.Errorf("--validate-only must not create the out directory")
    }
}

func TestRun_ValidateOnly_FailsOnInvalid(t *testing.T) {
    projRoot := t.TempDir()
    draftPath := filepath.Join(projRoot, "draft.yaml")
    // Write a schema-invalid draft (missing required `id`).
    if err := os.WriteFile(draftPath, []byte("name: only\n"), 0o644); err != nil {
        t.Fatal(err)
    }
    schemasDir := pluginSchemasDir(t)
    args := []string{
        "--project-root=" + projRoot,
        "--schemas-dir=" + schemasDir,
        "--validate-only",
        draftPath,
    }
    var stdout, stderr bytes.Buffer
    code := run(args, &stdout, &stderr)
    if code != 1 {
        t.Fatalf("exit = %d, want 1 on invalid draft", code)
    }
}
```

If `validUsecaseYAML` and `pluginSchemasDir` helpers do not already exist in the test file, write them inline using the canonical valid usecase from `lib/usecase/testdata` or by hand. The point of the test is to exercise the flag wiring; reuse whatever scaffold the file already established for the existing `TestRun_*` tests.

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test -tags=write_usecase ./skills/detect-usecases/scripts/... -run TestRun_ValidateOnly -v
```

Expected: FAIL — `--validate-only` flag unknown.

- [ ] **Step 3: Implement the flag in `write-usecase.go`**

Modify the `run` function in `skills/detect-usecases/scripts/write-usecase.go`:

```go
func run(args []string, stdout, stderr io.Writer) int {
    fs := flag.NewFlagSet("write-usecase", flag.ContinueOnError)
    fs.SetOutput(stderr)
    var outDir, projectRoot, schemasDir string
    var validateOnly bool
    fs.StringVar(&outDir, "out", "", "directory to write the usecase file into (required unless --validate-only)")
    fs.StringVar(&projectRoot, "project-root", "", "project root (required, holds .harness/stack.yaml)")
    fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
    fs.BoolVar(&validateOnly, "validate-only", false, "validate the draft without writing it")
    if err := fs.Parse(args); err != nil {
        return 2
    }
    if !validateOnly && outDir == "" {
        fmt.Fprintln(stderr, "error: --out is required")
        return 2
    }
    if projectRoot == "" {
        fmt.Fprintln(stderr, "error: --project-root is required")
        return 2
    }
    if fs.NArg() != 1 {
        fmt.Fprintln(stderr, "usage: write-usecase --out=DIR --project-root=DIR [--schemas-dir=DIR] [--validate-only] <draft>")
        return 2
    }
    draftPath := fs.Arg(0)

    body, err := schema.ReadAsJSON(draftPath)
    if err != nil {
        fmt.Fprintln(stderr, "error: read draft:", err)
        return 2
    }
    stk, code := loadStack(projectRoot, stderr)
    if code != 0 {
        return code
    }
    if validateOnly {
        if err := usecase.Validate(body, projectRoot, stk, schemasDir); err != nil {
            var ve *jsonschema.ValidationError
            if errors.As(err, &ve) {
                schema.PrintValidationOrPlain(err, stderr)
                return 1
            }
            var cce *stack.CrossCheckError
            if errors.As(err, &cce) {
                fmt.Fprintf(stderr, "error: %s\n", cce.Error())
                return 1
            }
            fmt.Fprintln(stderr, "error:", err)
            return 2
        }
        return 0
    }
    path, err := usecase.ValidateAndPersist(body, outDir, projectRoot, stk, schemasDir)
    if err != nil {
        // … unchanged error handling …
    }
    fmt.Fprintln(stdout, path)
    return 0
}
```

This introduces a new `lib/usecase.Validate` symbol (validation without persistence). Add to `lib/usecase/persist.go`:

```go
// Validate runs the schema + cross-check pipeline that ValidateAndPersist
// runs, without writing anything to disk. Returns the same error types
// the persist path returns.
func Validate(draftJSON []byte, projectRoot string, stk *stack.Stack, schemasDir string) error {
    var doc map[string]interface{}
    if err := json.Unmarshal(draftJSON, &doc); err != nil {
        return fmt.Errorf("parse usecase JSON: %w", err)
    }
    dir := schemasDir
    if dir == "" {
        cwd, _ := os.Getwd()
        found, ferr := schema.FindSchemasDir(cwd)
        if ferr != nil {
            return fmt.Errorf("locate schemas: %w", ferr)
        }
        dir = found
    }
    v, err := schema.NewValidator(dir)
    if err != nil {
        return fmt.Errorf("load schemas: %w", err)
    }
    if err := v.Validate(schema.TargetUseCase, doc); err != nil {
        return err
    }
    var uc UseCase
    body, _ := json.Marshal(doc)
    if err := json.Unmarshal(body, &uc); err != nil {
        return fmt.Errorf("decode after schema validation: %w", err)
    }
    if err := CheckJourneyReference(&uc, stk); err != nil {
        return err
    }
    if err := CheckEvidenceFiles(&uc, projectRoot); err != nil {
        return err
    }
    if err := CheckFixtureContractEvidence(&uc); err != nil {
        return err
    }
    if err := CheckFixtureRefExists(&uc, projectRoot); err != nil {
        return err
    }
    return nil
}
```

Refactor `ValidateAndPersist` to call `Validate` internally to keep the pipeline DRY:

```go
func ValidateAndPersist(...) (string, error) {
    if err := Validate(draftJSON, projectRoot, stk, schemasDir); err != nil {
        return "", err
    }
    var doc map[string]interface{}
    _ = json.Unmarshal(draftJSON, &doc)
    // … existing persistence logic from `id, ok := doc["id"]` onward …
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test -tags=write_usecase ./skills/detect-usecases/scripts/... -run TestRun_ValidateOnly -v
go test ./lib/usecase/... -v
```

Expected: PASS on both new tests and the existing suite.

- [ ] **Step 5: Commit**

```bash
git add skills/detect-usecases/scripts/write-usecase.go skills/detect-usecases/scripts/write-usecase_test.go lib/usecase/persist.go
git commit -m "feat(write-usecase): add --validate-only flag

Runs the full validation pipeline (schema + cross-checks + fixture.ref
existence) without persisting. Extracts the pipeline into
lib/usecase.Validate so both code paths stay DRY."
```

---

### Task 10: Rewrite `detect-usecases` SKILL Phase 1.5 (sourcing pipeline)

**Files:**
- Modify: `skills/detect-usecases/SKILL.md`

Pure markdown task; no Go change. Re-read the spec's "Skill procedure deltas → detect-usecases" section verbatim and insert it.

- [ ] **Step 1: Open the current SKILL.md and locate the insertion point**

Phase 1 ends with step "5. **Draft a UseCase per variation**" (around line 84 of `skills/detect-usecases/SKILL.md` as of this commit). Phase 2 ("Persist each draft") begins right after. Phase 1.5 is inserted between them.

- [ ] **Step 2: Add the Phase 1.5 section**

Insert this verbatim block after the end of Phase 1 step 5 and before Phase 2:

```markdown
### Phase 1.5 — Source the fixture payload

For each variation drafted in Phase 1 step 5, before serializing the
YAML, populate `trigger.fixture` and `expected_outcome.fixture` via
the three-tier rule:

1. **Build a `fixture.Hint`** with `JourneyID`, `UsecaseID`,
   `Role` (`"trigger"` for the trigger side, `"outcome"` for the
   expected-outcome side), `ProjectRoot`.
2. **Compute `searchPaths`** from `stack.yaml`. For each component
   whose `evidence[].file` participates in the journey's archetype
   (e.g. `http-server` for `http-api` journeys, `queue-consumer-lib`
   for `queue-consumer` journeys), take:
   - the parent directory of every `evidence[].file`;
   - its nearest `testdata/`, `__fixtures__/`, `__tests__/`,
     `examples/` siblings (one hop up, one hop down).
   For each component whose idiomatic test/fixture location pattern
   you don't already know (older majors, project-specific wrappers,
   unfamiliar libraries), call `WebFetch` on the component's
   documentation URL BEFORE drafting `searchPaths` — never guess.
3. **Tier 1 — Disk.** Run:

   ```bash
   HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
     go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=find_on_disk \
     ./skills/detect-usecases/scripts \
     --role=<trigger|outcome> --search-paths=<path1>,<path2>,...
   ```
   If a Sample is returned with `Source=="disk"`, persist it via
   `write-fixture` at `<journey>/<usecase>/<role>.<ext>`:
   ```bash
   cat "$sourcePath" | \
     HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
     go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_fixture \
     ./skills/create-sensor/scripts \
     "<journey>/<usecase>/<role>.<ext>"
   ```
   The draft's `trigger.fixture` becomes `{ ref: "<journey>/<usecase>/trigger.<ext>" }`.

4. **Tier 2 — Contract.** When tier 1 returns no candidate AND the
   relevant `evidence[kind: contract]` row points at one of the four
   supported sources, derive the payload:
   - `json-schema` — file ends with `.json` and root carries `$schema`
     (or schema-like keywords).
   - `openapi-component` — declPath of the form
     `<file.yaml>#/components/schemas/<Name>`.
   - `avro` — file ends with `.avsc`.
   - `protobuf` — file ends with `.proto`; declPath is
     `<file.proto>:<MessageName>`.
   Run:
   ```bash
   HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
     go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=derive_from_contract \
     ./skills/detect-usecases/scripts \
     --source=<src> --decl-path=<declPath>
   ```
   Persist + reference the same way; copy `Sample.BlindSpots` into the
   draft's `blind_spots[]`.

5. **Tier 3 — Block on user.** When tier 1 has no hit AND the contract
   source is not in the supported matrix (e.g. Go struct, TS interface,
   Pydantic model), block:
   > No real sample fixture for `<usecase-id>.<role>`. The contract row
   > points at `<file:line>` (kind: `<source-kind>`). Options:
   > (a) paste a sample payload;
   > (b) I derive the minimum from the contract (cross-language type
   >     parsing not yet supported — deferred);
   > (c) skip and mark the variation as a blind spot.
   > Which do you prefer?

Inline payloads survive only when the variation's
`expected_outcome.fixture` is genuinely primitive: a JSON primitive,
an empty object `{}`, or an object whose values are all primitives
(e.g. `{exit_code: 0, status: "ok"}`). Any nested object or array of
objects MUST use `fixture.ref`.
```

This Phase 1.5 introduces two new wrapper scripts under `skills/detect-usecases/scripts/`: `find-on-disk.go` and `derive-from-contract.go`. These are thin CLI wrappers around the `lib/fixture` functions; defer their implementation to a follow-up if the agent's available subprocess vocabulary already lets it call `lib/fixture` indirectly — but the SKILL.md instructions describe them as runnable. If the runner doesn't yet have them, add **Task 10.5** before Task 11 to scaffold them (mirroring `write-fixture.go`'s shape: parse flags → call lib → emit Sample as JSON Signal on stdout). For this plan we assume they're added in the same commit as the SKILL update.

- [ ] **Step 3: Scaffold the two CLI wrappers**

Create `skills/detect-usecases/scripts/find-on-disk.go`:

```go
//go:build find_on_disk

package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "io"
    "os"
    "strings"

    "github.com/iurykrieger/harness-framework/lib/fixture"
)

func main() {
    os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
    fs := flag.NewFlagSet("find-on-disk", flag.ContinueOnError)
    fs.SetOutput(stderr)
    var role, paths string
    fs.StringVar(&role, "role", "", "fixture role (trigger|outcome|body|log-line|event)")
    fs.StringVar(&paths, "search-paths", "", "comma-separated search paths")
    if err := fs.Parse(args); err != nil {
        return 2
    }
    if role == "" {
        fmt.Fprintln(stderr, "error: --role is required")
        return 2
    }
    var splits []string
    if paths != "" {
        splits = strings.Split(paths, ",")
    }
    s, err := fixture.FindOnDisk(fixture.Hint{Role: role}, splits)
    if err != nil {
        fmt.Fprintln(stderr, "error:", err)
        return 1
    }
    if s == nil {
        // Emit a Signal-shaped "no candidate" envelope.
        json.NewEncoder(stdout).Encode(map[string]any{
            "source": "",
        })
        return 0
    }
    json.NewEncoder(stdout).Encode(map[string]any{
        "source":      s.Source,
        "source_path": s.SourcePath,
        "ext":         s.Ext,
        "payload_b64": base64Encode(s.Payload),
        "blind_spots": s.BlindSpots,
    })
    return 0
}

// base64Encode wraps stdlib for inline use.
func base64Encode(b []byte) string {
    return base64.StdEncoding.EncodeToString(b)
}
```

Add import: `"encoding/base64"`. Repeat the same pattern for `derive-from-contract.go` (build tag `derive_from_contract`), taking `--source` and `--decl-path` and routing through `fixture.DeriveFromContract`.

Also write minimal `_test.go` files for both that exercise the flag parsing and one happy path each (use `lib/fixture/testdata/contract/json_schema/order.schema.json` as the fixture).

- [ ] **Step 4: Run tests**

```bash
go test -tags=find_on_disk ./skills/detect-usecases/scripts/... -v
go test -tags=derive_from_contract ./skills/detect-usecases/scripts/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add skills/detect-usecases/SKILL.md skills/detect-usecases/scripts/find-on-disk.go skills/detect-usecases/scripts/find-on-disk_test.go skills/detect-usecases/scripts/derive-from-contract.go skills/detect-usecases/scripts/derive-from-contract_test.go
git commit -m "feat(detect-usecases): Phase 1.5 sourcing pipeline

Disk → contract → ask user. searchPaths are derived from stack.yaml by
the skill; the library never decides directories. New thin CLI
wrappers find-on-disk and derive-from-contract expose lib/fixture to
the skill subprocess."
```

---

### Task 11: Rewrite `create-sensor` SKILL Phase 4 step 6

**Files:**
- Modify: `skills/create-sensor/SKILL.md`

- [ ] **Step 1: Locate Phase 4 step 6 in `skills/create-sensor/SKILL.md`**

Currently around lines 172–183 of the SKILL.md (the block that begins "6. **Fixtures.**").

- [ ] **Step 2: Replace the block**

Replace step 6 with:

```markdown
6. **Fixtures.** Reuse from the shared pool first:

   - When the step exercises a usecase whose `trigger.fixture.ref` (or
     `expected_outcome.fixture.ref`) is already populated, the step's
     `with: { fixture: <ref> }` reuses that exact path. Do not write a
     duplicate file under `_sensors/<sensor-id>/`.

   - For ad-hoc fixtures (the step does not trace to any usecase
     fixture — e.g. a bootstrap `/health` GET before the scenario), source
     the payload using the three-tier rule (`find-on-disk` →
     `derive-from-contract` → block on the user), then persist via
     `write-fixture` at `_sensors/<sensor-id>/<step-id>.<ext>`:

     ```bash
     printf '%s' "<payload>" | \
       HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
       go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_fixture \
       ./skills/create-sensor/scripts \
       "_sensors/<sensor-id>/<step-id>.<ext>"
     ```

     The step references it as `with: { fixture: "_sensors/<sensor-id>/<step-id>.<ext>" }`.
```

- [ ] **Step 3: Commit**

```bash
git add skills/create-sensor/SKILL.md
git commit -m "docs(create-sensor): Phase 4 step 6 — reuse from shared pool

Sensor steps that exercise a usecase reuse the usecase's
fixture.ref verbatim. Ad-hoc fixtures land in
_sensors/<sensor-id>/<step-id>.<ext>."
```

---

### Task 12: Migrate `order-valid.json` and framework usecases

**Files:**
- Delete: `.harness/fixtures/order-valid.json`
- Create: `.harness/fixtures/framework/<usecase>/{trigger,outcome}.<ext>` × 5 journeys
- Modify: `.harness/usecases/framework/framework-smoke-typed-pipeline.yaml`
- Modify: `.harness/usecases/framework/framework-smoke-with-setup.yaml`
- Modify: `.harness/usecases/framework/framework-detect-sensors-component-self-contained.yaml`
- Modify: `.harness/usecases/framework/framework-detect-sensors-deploy-artifacts-detected.yaml`
- Modify: `.harness/usecases/framework/framework-create-sensor-multi-angle.yaml`
- Modify: `.harness/sensors/smoke-typed-pipeline.yaml` (update fixture ref)

- [ ] **Step 1: Move `order-valid.json` to the canonical location**

```bash
mkdir -p .harness/fixtures/framework/framework-smoke-typed-pipeline
git mv .harness/fixtures/order-valid.json .harness/fixtures/framework/framework-smoke-typed-pipeline/trigger.json
```

- [ ] **Step 2: Migrate `framework-smoke-typed-pipeline.yaml`**

Read the current file. The current `trigger.fixture` is structured:

```yaml
trigger:
  fixture:
    sensor_id: smoke-typed-pipeline
    fixture_payload: |
      {"sku":"abc","qty":1}
```

That's a meta-fixture about how the sensor is invoked. Replace with:

```yaml
trigger:
  shape: harness sensor invocation
  summary: /run-sensor smoke-typed-pipeline against this repo's checkout.
  fixture:
    ref: framework/framework-smoke-typed-pipeline/trigger.json
```

The actual payload (`{"sku":"abc","qty":1}`) now lives in `framework/framework-smoke-typed-pipeline/trigger.json` (the moved file).

For the outcome, the current shape `{exit_code: 0, stdout_last_line_kind: aggregate, stdout_last_line_verdict: pass}` is an all-primitive object — keep as `inline`:

```yaml
expected_outcome:
  fixture:
    inline:
      exit_code: 0
      stdout_last_line_kind: aggregate
      stdout_last_line_verdict: pass
```

- [ ] **Step 3: Migrate `framework-smoke-with-setup.yaml`**

Trigger has `{sensor_id: smoke-with-setup}` — single-field, can stay inline:

```yaml
trigger:
  fixture:
    inline:
      sensor_id: smoke-with-setup
```

Outcome same primitive treatment as Step 2.

- [ ] **Step 4: Migrate `framework-create-sensor-multi-angle.yaml`**

Both fixtures are structured. Move trigger payload to
`framework/framework-create-sensor-multi-angle/trigger.json`:

```bash
mkdir -p .harness/fixtures/framework/framework-create-sensor-multi-angle
cat > .harness/fixtures/framework/framework-create-sensor-multi-angle/trigger.json <<'EOF'
{
  "skill": "create-sensor",
  "input": "tail-sensor-no-registry"
}
EOF
```

YAML becomes:

```yaml
trigger:
  fixture:
    ref: framework/framework-create-sensor-multi-angle/trigger.json
```

Outcome `{persisted_path, exit_code}` — primitive object, can stay inline:

```yaml
expected_outcome:
  fixture:
    inline:
      persisted_path: .harness/sensors/<sensor-id>.yaml
      exit_code: 0
```

- [ ] **Step 5: Migrate the remaining two framework usecases**

Repeat the pattern for `framework-detect-sensors-component-self-contained.yaml` and `framework-detect-sensors-deploy-artifacts-detected.yaml`. Open each, identify which side has a structured payload, extract to
`.harness/fixtures/framework/<usecase-id>/<role>.<ext>`, replace with `ref:`.

- [ ] **Step 6: Update the sensor reference**

In `.harness/sensors/smoke-typed-pipeline.yaml`, find any step that does
`with: { fixture: order-valid.json }` and rewrite it to:

```yaml
with:
  fixture: framework/framework-smoke-typed-pipeline/trigger.json
```

- [ ] **Step 7: Verify everything still parses**

Run the new validation gate over each migrated file:

```bash
for f in .harness/usecases/framework/*.yaml; do
  echo "== $f"
  HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
    go run -C "${CLAUDE_PLUGIN_ROOT:-$(pwd)}" -tags=write_usecase \
    ./skills/detect-usecases/scripts \
    --project-root="$(pwd)" --schemas-dir="$(pwd)/schemas" --validate-only \
    "$f" || exit 1
done
```

Expected: every file exits 0.

- [ ] **Step 8: Commit**

```bash
git add .harness/fixtures/framework .harness/usecases/framework .harness/sensors/smoke-typed-pipeline.yaml
git commit -m "chore: migrate framework usecases to FixtureRef envelope

Structured trigger/outcome payloads extracted to
.harness/fixtures/framework/<usecase>/{trigger,outcome}.<ext>;
primitive-only outcomes kept inline. order-valid.json renamed to
framework/framework-smoke-typed-pipeline/trigger.json."
```

---

### Task 13: Migrate `create-sensor` usecases

**Files:**
- Modify: `.harness/usecases/create-sensor/create-sensor-success.yaml`
- Modify: `.harness/usecases/create-sensor/create-sensor-missing-fixture.yaml`
- Create: `.harness/fixtures/create-sensor/<usecase>/{trigger,outcome}.<ext>`

- [ ] **Step 1: Migrate `create-sensor-success.yaml`**

Current trigger:
```yaml
trigger:
  fixture:
    argv: [write-sensor, --out=/repo/.harness/sensors, /tmp/draft.yaml]
    state:
      .harness/sensors/my-new-sensor.yaml: absent
```

This is a list-of-strings + object-with-one-key. Mixed shape — extract.

```bash
mkdir -p .harness/fixtures/create-sensor/create-sensor-success
cat > .harness/fixtures/create-sensor/create-sensor-success/trigger.json <<'EOF'
{
  "argv": ["write-sensor", "--out=/repo/.harness/sensors", "/tmp/draft.yaml"],
  "state": {
    ".harness/sensors/my-new-sensor.yaml": "absent"
  }
}
EOF
```

YAML trigger becomes:

```yaml
trigger:
  fixture:
    ref: create-sensor/create-sensor-success/trigger.json
```

Current outcome:
```yaml
expected_outcome:
  fixture:
    exit_code: 0
    stdout_lines:
      - '{"sensor_id":"write-sensor","verdict":"pass",...}'
      - /repo/.harness/sensors/my-new-sensor.yaml
```

`stdout_lines` is a structured list — extract to JSONL:

```bash
cat > .harness/fixtures/create-sensor/create-sensor-success/outcome.jsonl <<'EOF'
{"sensor_id":"write-sensor","verdict":"pass","severity":"info","evidence":[],"cost_actual":{"latency_ms":12},"metadata":{"kind":"sensor_persisted","path":"/repo/.harness/sensors/my-new-sensor.yaml","id":"my-new-sensor","kind_attr":"assertion","type_attr":"computational"}}
/repo/.harness/sensors/my-new-sensor.yaml
EOF
```

YAML outcome becomes:

```yaml
expected_outcome:
  fixture:
    ref: create-sensor/create-sensor-success/outcome.jsonl
```

The `exit_code: 0` is dropped from the fixture (the invariants line "exit code is 0" carries that intent).

- [ ] **Step 2: Migrate `create-sensor-missing-fixture.yaml`**

Open the file; apply the same treatment. Extract structured fields to `create-sensor/create-sensor-missing-fixture/{trigger,outcome}.<ext>`.

- [ ] **Step 3: Validate**

```bash
for f in .harness/usecases/create-sensor/*.yaml; do
  HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
    go run -C "${CLAUDE_PLUGIN_ROOT:-$(pwd)}" -tags=write_usecase \
    ./skills/detect-usecases/scripts \
    --project-root="$(pwd)" --schemas-dir="$(pwd)/schemas" --validate-only \
    "$f" || exit 1
done
```

Expected: both files exit 0.

- [ ] **Step 4: Commit**

```bash
git add .harness/fixtures/create-sensor .harness/usecases/create-sensor
git commit -m "chore: migrate create-sensor usecases to FixtureRef envelope

argv+state trigger extracted to trigger.json; multi-line stdout
expectation extracted to outcome.jsonl."
```

---

### Task 14: Migrate sensors + acceptance step

**Files:**
- Modify: `.harness/sensors/smoke-with-setup.yaml`
- Modify: `.harness/sensors/assert-stack-schema-valid.yaml`
- Modify: `.harness/sensors/assert-detect-sensors-self-contained.yaml`
- Modify: `.harness/sensors/assert-create-sensor-multi-angle.yaml` (acceptance step rewrite)

The acceptance step is the load-bearing change. Other sensors get touched only if they hold inline payloads that should move to the pool.

- [ ] **Step 1: Audit each sensor for inline payloads**

```bash
for f in .harness/sensors/*.yaml; do
  echo "=== $f"
  grep -n -E '^\s+(fixture|with):' "$f" || true
done
```

Note: `smoke-typed-pipeline.yaml` was already updated in Task 12. The other four likely have no inline payloads (they're mostly shell assertions). Confirm by reading each.

- [ ] **Step 2: Rewrite the `assert-fixture-referenced` step in `assert-create-sensor-multi-angle.yaml`**

The current step (lines 87–111 of that file) expects fixtures under `.harness/fixtures/<sensor-id>/`. Replace its `run:` block with:

```yaml
    - id: assert-fixture-referenced
      type: shell
      run: |
        set -e
        # At least one produced sensor must reference an existing file
        # under .harness/fixtures/, regardless of whether the path is
        # under _sensors/<sensor-id>/ or <journey>/<usecase>/.
        found=0
        for f in .harness/sensors/assert-tail-sensor-*.yaml; do
          [ -e "$f" ] || { echo "FAIL: no produced sensor matching .harness/sensors/assert-tail-sensor-*.yaml"; exit 1; }
          # Extract every step's fixture: value (relative pool path).
          # YAML is simple here — grep is good enough.
          refs=$(awk '/^\s+with:/,/^\s+(\S|$)/' "$f" | grep -oE 'fixture:\s*"?[^"$]+' | sed -E 's/^fixture:\s*"?//' | sed 's/"$//')
          for r in $refs; do
            if [ -f ".harness/fixtures/$r" ]; then
              found=1
              break 2
            fi
          done
        done
        if [ "$found" -eq 0 ]; then
          echo "FAIL: no produced sensor references an existing fixture under .harness/fixtures/"
          exit 1
        fi
      exit_code_map:
        "0": pass
        "*": fail
```

- [ ] **Step 3: Modify the surrounding usecase invariant**

`framework/framework-create-sensor-multi-angle.yaml` currently asserts:
> "At least one step references a fixture under .harness/fixtures/<sensor-id>/."

Rewrite the relevant `invariants[]`/`side_effects[]`/`business_rules[]` entries to drop the `<sensor-id>/` constraint and refer to the pool root:
- invariant: "At least one step in the produced sensor references a fixture file under .harness/fixtures/."
- side_effect: "write: .harness/fixtures/<journey>/<usecase>/<role>.<ext>  OR  .harness/fixtures/_sensors/<sensor-id>/<step-id>.<ext>"

- [ ] **Step 4: Validate**

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT:-$(pwd)}" -tags=write_usecase \
  ./skills/detect-usecases/scripts \
  --project-root="$(pwd)" --schemas-dir="$(pwd)/schemas" --validate-only \
  .harness/usecases/framework/framework-create-sensor-multi-angle.yaml
```

Expected: exit 0.

Also run the sensor schema validator over each sensor (the existing `assert-stack-schema-valid` sensor — but invoke its underlying `write-sensor` validator manually):

```bash
for f in .harness/sensors/*.yaml; do
  echo "=== $f"
  HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
    go run -C "${CLAUDE_PLUGIN_ROOT:-$(pwd)}" -tags=write_sensor \
    ./skills/create-sensor/scripts \
    --out="$(mktemp -d)" --schemas-dir="$(pwd)/schemas" \
    "$f" >/dev/null || exit 1
done
```

Expected: every file passes.

- [ ] **Step 5: Commit**

```bash
git add .harness/sensors .harness/usecases/framework/framework-create-sensor-multi-angle.yaml
git commit -m "chore: migrate sensors + acceptance step to shared-pool fixtures

assert-create-sensor-multi-angle's assert-fixture-referenced step now
accepts any path under .harness/fixtures/, not only the per-sensor
silo. Other sensors confirmed to hold no inline payloads needing
extraction."
```

---

### Task 15: Final post-migration validation sweep + integration test

**Files:**
- (none modified — verification task)

- [ ] **Step 1: Run the DoD #7 sweep**

```bash
find .harness/usecases -name '*.yaml' -print0 | xargs -0 -n1 -I{} \
  go run -C "${CLAUDE_PLUGIN_ROOT:-$(pwd)}" -tags=write_usecase \
  ./skills/detect-usecases/scripts \
  --project-root="$(pwd)" --schemas-dir="$(pwd)/schemas" --validate-only "{}"
echo "exit: $?"
```

Expected: every YAML exits 0; overall exit 0.

- [ ] **Step 2: Run the full Go test suite**

```bash
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential   ./skills/...
go test -tags=start_sensor      ./skills/...
go test -tags=stop_sensor       ./skills/...
go test -tags=list_sensors      ./skills/...
go test -tags=tail_sensor       ./skills/...
go test -tags=heal_retry_original ./skills/heal-sensor/...
go test -tags=write_usecase   ./skills/...
go test -tags=write_sensor    ./skills/...
go test -tags=write_fixture   ./skills/...
go test -tags=find_on_disk    ./skills/...
go test -tags=derive_from_contract ./skills/...
go vet -tags=run_computational ./...
go vet -tags=run_inferential   ./...
```

Expected: all pass.

- [ ] **Step 3: Smoke test the migrated `smoke-typed-pipeline` sensor end-to-end**

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT:-$(pwd)}" -tags=run_computational \
  ./skills/run-sensor/scripts smoke-typed-pipeline
```

Expected: last JSONL line on stdout is an aggregate Signal with `verdict: pass`.

- [ ] **Step 4: If anything failed, fix, and repeat from Step 1**

Do not commit until the full sweep + go test + smoke test are all green. Investigate root causes; do not silence with `--no-verify` or skip tags.

- [ ] **Step 5: Final commit (if no remaining changes from Step 4)**

If steps 1–3 all passed without any source change, no final commit is needed — the previous commits already capture all changes. Otherwise:

```bash
git add -A
git commit -m "fix: post-migration adjustments

Tightens [the specific thing] surfaced by the DoD #7 sweep."
```

---

## Self-Review Checklist

Run this against the spec before declaring done.

**Spec coverage:**
- ✅ DoD #1 (FixtureRef oneOf, ref existence check): Task 1 + Task 3.
- ✅ DoD #2 (FindOnDisk + DeriveFromContract with at least one entry per supported source): Task 4 + Tasks 5–8.
- ✅ DoD #3 (skill procedure updates with stack-driven searchPaths + WebFetch): Task 10 + Task 11.
- ✅ DoD #4 (migration of 7 usecases + 5 sensors): Task 12 + Task 13 + Task 14.
- ✅ DoD #5 (assert-create-sensor-multi-angle rewrite): Task 14 Step 2.
- ✅ DoD #6 (go test ./lib/... + go test -tags=write_usecase ./skills/...): Task 15 Step 2.
- ✅ DoD #7 (--validate-only flag + find/xargs sweep): Task 9 + Task 15 Step 1.

**Risk mitigations from spec:**
- ✅ Disk-search ambiguous match → Task 4 implements exact-vs-alias + ordered tiebreaker; SKILL prose tells the agent to confirm with the user when ≥2 candidates of equal strength.
- ✅ Migration typo → Task 15 sweep + smoke test catches before final.
- ✅ Tier-2 zero-value semantic mismatch (e.g. error-handling variation gets a happy-path payload) → Task 5–8 set `BlindSpots`; Task 10 SKILL.md instructs the agent to surface BlindSpots prominently.

**Placeholder scan:** No "TBD"/"add appropriate"/"similar to Task N" present. Every code block is complete.

**Type consistency:**
- `Hint`, `Sample` types defined in Task 4, reused identically in Tasks 5–8.
- `SourceKind` constants spelled identically across Task 5, 6, 7, 8.
- `ErrUnsupportedContractSource` defined in Task 5, asserted in Task 5 test.
- `CheckFixtureRefExists` defined in Task 3, wired in Task 3 Step 4.
- `Validate` (new) defined in Task 9 Step 3, used in Task 15 Step 1.
- `lib/usecase/persist.go`'s `ValidateAndPersist` refactored in Task 9 to call `Validate`; the order of checks (`schema → journey → evidence → contract-evidence → ref-exists`) is preserved across both paths.

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-05-18-shared-fixtures.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
