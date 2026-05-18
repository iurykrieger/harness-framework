# Shared fixtures rooted at `.harness/fixtures/`

## Objective

Move fixtures out of inline YAML in usecases and sensors, into a single shared
root at `<project>/.harness/fixtures/` referenced by path from both entity
types. Replace LLM-fabricated payloads with values sourced deterministically
from real project artifacts; fall back to typed-contract derivation, then to
asking the user. Never to invention.

## Definition of Done

1. `schemas/usecase.yaml` defines a `FixtureRef` `oneOf` envelope and uses it
   for `Trigger.fixture` and `ExpectedOutcome.fixture`; the validator rejects
   any draft whose `fixture.ref` points at a missing file.
2. `lib/fixture/sample_disk.go` exposes `FindOnDisk(hint Hint, searchPaths []string)`;
   `lib/fixture/sample_contract.go` exposes `DeriveFromContract(hint Hint)`
   limited to the four already-structured contract sources listed below
   (JSON Schema, OpenAPI components, Avro, Protobuf). Each file ships with
   a sibling `_test.go` whose table contains at least one entry per
   supported contract source and at least one disk-tier hit-and-miss pair.
3. `skills/detect-usecases/SKILL.md` and `skills/create-sensor/SKILL.md`
   adopt the three-tier sourcing rule (disk → contract → user) and persist
   payloads via `lib/fixture.Write` before the YAML draft is finalized.
   The skill — not `lib/fixture` — derives `searchPaths` by reading
   `stack.components[].evidence[].file` neighborhoods and consulting the
   component's documentation via `WebFetch` when the convention is not
   already encoded in `stack.yaml`.
4. The 7 committed usecases and the 5 committed sensors are migrated. After
   migration, every surviving `fixture.inline` payload in any
   `.harness/usecases/**/*.yaml` is one of: a JSON primitive (string,
   number, boolean, null), an empty object `{}`, or an object whose
   values are all JSON primitives (e.g. `{exit_code: 0, status: "ok"}`).
   Any nested object or any array of objects MUST be expressed as
   `fixture.ref` to a file under `.harness/fixtures/`.
5. The acceptance sensor `assert-create-sensor-multi-angle` is updated to
   accept fixtures shared via the usecase path, not only the per-sensor
   silo path it expects today.
6. `go test ./lib/...` and `go test -tags=write_usecase ./skills/...` pass.
7. A one-shot post-migration gate re-validates every committed usecase:
   `find .harness/usecases -name '*.yaml' -exec go run -tags=write_usecase
   ./skills/detect-usecases/scripts --validate-only {} \;` exits 0 for
   all files. The `--validate-only` flag is added to `write-usecase.go`
   for this purpose and runs the same checks the write path runs without
   persisting.

## In scope

- Usecase schema change.
- New library code in `lib/fixture/`.
- Skill procedure changes in `detect-usecases` and `create-sensor`.
- Migration of committed YAMLs and the framework's own sensors.
- Validator hardening: `fixture.ref` existence check.

## Out of scope (deferred to follow-up specs)

- JSON Schema cross-check of fixture *content* against the contract type
  named by `evidence[kind:contract]`. This spec only enforces structural
  envelope rules, not semantic shape of the payload.
- Automatic rename of a usecase id with migration of its fixture directory.
- Independent versioning of fixtures (a `version:` field per fixture).
- Repurposing fixtures as inferential sensor calibration sets
  (`calibration.calibration_set` is already path-based; orthogonal).

## Architecture

### Three principles

1. **One root, two consumers.** `.harness/fixtures/` is one namespace.
   A usecase and a sensor that validate the same scenario MUST point at
   the same file — no inline↔file duplication.
2. **Evidence first, contract second, ask third.** Payload sourcing is
   deterministic: search the project disk for real samples, then derive
   the minimum payload from the typed contract, then block on the user.
   Free-form LLM fabrication is no longer an accepted path.
3. **Schema is the guard.** The validator enforces `fixture` as a typed
   envelope (`ref` or `inline`), rejects refs that don't exist on disk,
   and keeps the existing `evidence[kind:contract]` requirement on
   non-primitive payloads regardless of which arm is used.

### Schema delta — `schemas/usecase.yaml`

```yaml
$defs:
  FixtureRef:
    description: |
      Source of the fixture payload. Exactly one of `ref` (path under
      <project>/.harness/fixtures/) or `inline` (literal value). `ref` is
      the preferred form for any structured payload — HTTP/event bodies,
      stdout transcripts, log lines, multi-field records. `inline` is
      reserved for primitive envelopes that don't benefit from being on
      disk (a lone exit_code, a single status string, an empty object).
    oneOf:
      - properties:
          ref:
            type: string
            minLength: 1
            description: |
              Path relative to <project>/.harness/fixtures/, with forward
              slashes regardless of platform. Must point at an existing,
              non-directory file at write time; the validator rejects
              missing refs with metadata.kind=fixture_not_found.
        required: [ref]
        additionalProperties: false
      - properties:
          inline:
            description: Literal payload (any JSON value). Use only for primitives.
        required: [inline]
        additionalProperties: false

  Trigger:
    properties:
      # … unchanged …
      fixture:
        $ref: '#/$defs/FixtureRef'

  ExpectedOutcome:
    properties:
      # … unchanged …
      fixture:
        $ref: '#/$defs/FixtureRef'
```

Sensor schema is NOT touched. `StepInputs.fixture` and `HttpStep.body_from.fixture`
are already path-keyed against the pool.

### Pool layout convention

Schema-agnostic; enforced by the two authoring skills.

```
.harness/fixtures/
  <journey-id>/
    <usecase-id>/
      trigger.<ext>           # canonical usecase input
      outcome.<ext>           # canonical usecase expected output (when structured)
    _shared/<name>.<ext>      # multiple usecases of one journey reuse
  _sensors/<sensor-id>/<step-id>.<ext>   # ad-hoc, sensor-only (no usecase mapping)
```

The leading-underscore prefixes `_sensors/` and `_shared/` visually
segregate the silo namespaces from journey directories in an `ls`.
Journey ids cannot start with `_` (the schema's `journey_id` pattern
is `^[a-z][a-z0-9-]*$`), so collisions are impossible.

Sensor-step rule: when `use_cases[]` of the sensor declares usecase X and the
step exercises X's trigger, the step MUST `with: { fixture: <journey>/<X>/trigger.<ext> }`.
The `_sensors/<sensor-id>/<step-id>.<ext>` namespace is reserved for
ad-hoc fixtures that don't trace to any usecase fixture (e.g. a
bootstrap `/health` GET before the scenario under test).

`lib/fixture.Discover` continues to walk the tree without imposing a convention.

### The sourcing pipeline

`lib/fixture/` gains two narrow primitives, each in its own file per
project rule 9 (one file per action):

```go
// lib/fixture/sample_disk.go
type Hint struct {
    JourneyID    string
    UsecaseID    string         // "" when sourcing for an ad-hoc sensor step
    Role         string         // "trigger" | "outcome" | "body" | "log-line" | "event"
    ContractRows []usecase.Evidence
    ProjectRoot  string
}

type Sample struct {
    Payload    []byte
    Ext        string         // json | yaml | jsonl | txt | avro | proto
    Source     string         // "disk" | "contract" | "user"
    SourcePath string         // when Source == "disk", absolute path the sample came from
    BlindSpots []string
}

// FindOnDisk walks the caller-supplied searchPaths in order, returning the
// first file whose filename or path encodes the Hint.Role (e.g. "trigger",
// "request", "input" for Role=="trigger"; "response", "outcome", "expected"
// for Role=="outcome"). The library does NOT decide which paths to walk —
// that decision is stack-driven and lives in the calling skill.
func FindOnDisk(h Hint, searchPaths []string) (*Sample, error)
```

```go
// lib/fixture/sample_contract.go — accepts only ALREADY-STRUCTURED contract
// sources. Source-code AST parsing (Go structs, TS interfaces, Python type
// declarations, Pydantic models) is deliberately out of scope; those fall
// through to tier 3.

// SupportedContractSource is the closed set tier 2 understands:
//   "json-schema"      — JSON Schema (Draft 7 or 2020-12) on disk
//   "openapi-component" — OpenAPI 3.x components/schemas/<name>
//   "avro"             — Apache Avro .avsc
//   "protobuf"         — Protocol Buffers .proto message
type SupportedContractSource string

func DeriveFromContract(h Hint, src SupportedContractSource, declPath string) (*Sample, error)
```

**Stack-driven `searchPaths`.** The disk tier knows nothing about archetypes
or framework conventions. The two skills compute `searchPaths` themselves
by:

1. Reading `<projectRoot>/.harness/stack.yaml` and enumerating
   `components[]` relevant to the journey's archetype (the relevance
   relation — e.g. "http-server component participates in `http-api`
   journeys" — lives in the skill prose, NOT in `lib`).
2. For each relevant component, taking the directories of every
   `evidence[].file` and their nearest `testdata/` / `__fixtures__/` /
   `examples/` / `__tests__/` siblings.
3. When the component is unfamiliar to the skill (rare libraries, pinned
   older majors, project-specific wrappers), the skill calls `WebFetch`
   on the component's documentation URL to learn the idiomatic location
   pattern before drafting `searchPaths`.

The expected per-archetype outcomes are an emergent property of the
component evidence rows, not a baked-in switch table. If `stack.yaml`
does not declare a component capable of producing the sample (e.g. an
`http-api` journey with no test runner component), tier 1 yields no
hit and the skill falls through — same Rule 13 spirit that governs
sensor applicability.

**Tier 2 (contract) — supported source matrix.**

| `SupportedContractSource` | Recognized by |
|---|---|
| `json-schema` | File extension `.schema.json` / `.json` whose root carries `$schema`; or schema-like keywords `type`/`properties` |
| `openapi-component` | Decl path of the form `<openapi-file>#/components/schemas/<Name>`; file resolved + parsed; the named component is the schema |
| `avro` | File extension `.avsc` |
| `protobuf` | File extension `.proto`; the message named by `declPath` (e.g. `myproto.MyMessage`) is resolved by parsing the proto file |

For each, the derivation rule is the same: emit a minimum valid payload
using declared field names + zero values typed by the declared kind
(`""` string, `0` number, `false` boolean, `[]`/`{}` container; enums
pick the first declared value; required fields filled, optional fields
omitted). `Source = "contract"`, `BlindSpots = ["Derived from <source>
contract at <declPath>; no real sample on disk near the entry point."]`.

When `evidence[kind:contract]` points at a source-code type declaration
(Go struct, TS interface, Python dataclass/Pydantic, etc.), tier 2 is
NOT applicable and the skill falls through to tier 3. Cross-language
AST parsing is deferred to a follow-up spec.

**Tier 3 (block).** The skill emits:

> No real sample fixture for `<usecase-id>.<role>`. The contract row points at
> `<file:line>` (kind: `<source-kind>`). Options: (a) paste a sample payload;
> (b) I derive the minimum from the contract; (c) skip and mark the variation
> as a blind spot. Which do you prefer?

Tier choice is deterministic per `Hint` + `searchPaths`. The LLM's role is bounded:
- composing `searchPaths` from stack components (which the skill prose
  guides explicitly, with `WebFetch` available for unfamiliar libraries),
- when tier 1 yields multiple candidates, picking the one closest to the
  entry point on the import graph,
- when copying a tier-1 sample, renaming identifiers so it traces to
  the current usecase id,
- writing the prose `BlindSpots` entry when applicable.

The LLM never invents payload field values from nothing; it never decides
that a contract source is "close enough" to tier 2 when its kind is not
in the supported matrix.

### Validator deltas — `lib/usecase/validate.go`

After JSON Schema validation succeeds:

1. If `trigger.fixture.ref` is present: stat `<projectRoot>/.harness/fixtures/<ref>`.
   Emit `metadata.kind=fixture_not_found` if absent or non-regular.
2. If `expected_outcome.fixture.ref` is present: same check.
3. The existing `contract_evidence_missing` check (for non-primitive payloads)
   is preserved and now applies to *both* arms:
   - `inline` arm: walk the literal payload; non-primitive → require `evidence[kind:contract]`.
   - `ref` arm: read the file from disk (size-capped via `HARNESS_FIXTURE_MAX_BYTES`),
     parse by extension (`.json`/`.yaml` → tree; `.jsonl` → list of trees; `.txt`/other → string).
     Non-primitive content → require `evidence[kind:contract]`.

The file-read cap reuses `lib/fixture`'s existing `HARNESS_FIXTURE_MAX_BYTES`
configuration (default 1 MiB).

### Skill procedure deltas

**`skills/detect-usecases/SKILL.md`** — insert Phase 1.5 between contract
resolution and drafting:

> ### Phase 1.5 — Source the fixture payload
>
> For each variation drafted in Phase 1.4:
>
> 1. Build a `fixture.Hint` (journey id, usecase id, role, contract rows,
>    projectRoot).
> 2. Compute `searchPaths` from `stack.components[].evidence[].file`:
>    take the directory of each evidence file, then append its nearest
>    `testdata/`, `__fixtures__/`, `__tests__/`, `examples/` siblings.
>    For each component whose idiomatic location pattern you don't already
>    know (older majors, project-specific wrappers, unfamiliar libraries),
>    call `WebFetch` on the component's documentation URL before adding
>    paths — do not guess.
> 3. Call `fixture.FindOnDisk(hint, searchPaths)`. If a `Sample` is returned,
>    persist it via `lib/fixture.Write` at `<journey>/<usecase>/<role>.<ext>`
>    (where `<role>` is `trigger` or `outcome`). The draft's
>    `trigger.fixture`/`expected_outcome.fixture` becomes `{ ref: "<journey>/<usecase>/<role>.<ext>" }`.
> 4. Else, if the relevant `evidence[kind:contract]` row points at one of
>    the four supported contract sources (`json-schema`, `openapi-component`,
>    `avro`, `protobuf`), call `fixture.DeriveFromContract(hint, src, declPath)`.
>    Persist + reference the same way; copy `Sample.BlindSpots` into the
>    draft's `blind_spots[]`.
> 5. Else block on the user with the tier-3 prompt above.
>
> Inline payloads survive only when the variation's `expected_outcome.fixture`
> is genuinely primitive (a single `exit_code`, a status string).

**`skills/create-sensor/SKILL.md`** — Phase 4 step 6 rewrites:

> 6. **Fixtures.** When a step exercises a usecase whose `trigger.fixture.ref`
>    is already populated, the step's `with: { fixture: <ref> }` reuses that
>    path verbatim. Do not write a duplicate file under
>    `_sensors/<sensor-id>/`. For ad-hoc fixtures (step has no usecase
>    trace), serialize the payload and persist via `write-fixture` at
>    `_sensors/<sensor-id>/<step-id>.<ext>`.

### Migration of committed artifacts (same PR)

7 usecases under `.harness/usecases/`:

- `framework/framework-create-sensor-multi-angle.yaml` — `trigger.fixture` is a
  literal `{skill, input}` envelope; structured but small → migrate to
  `framework/framework-create-sensor-multi-angle/trigger.json`. `expected_outcome.fixture`
  has structured `persisted_path`/`exit_code` → migrate to `outcome.json`.
- `framework/framework-detect-sensors-component-self-contained.yaml`,
  `framework/framework-detect-sensors-deploy-artifacts-detected.yaml`,
  `framework/framework-smoke-typed-pipeline.yaml`,
  `framework/framework-smoke-with-setup.yaml` — same treatment per file.
- `create-sensor/create-sensor-success.yaml` and `create-sensor/create-sensor-missing-fixture.yaml`
  — multi-line stdout payload moves to `outcome.jsonl`; CLI argv `trigger`
  moves to `trigger.json`.

5 sensors under `.harness/sensors/`:

- `smoke-typed-pipeline.yaml` already references `.harness/fixtures/order-valid.json`;
  rename the file to `framework/framework-smoke-typed-pipeline/trigger.json` to
  match the new convention and update the reference.
- `smoke-with-setup.yaml`, `assert-stack-schema-valid.yaml`,
  `assert-detect-sensors-self-contained.yaml`,
  `assert-create-sensor-multi-angle.yaml` — each step either points at a
  pool fixture or runs an inline shell command. Inline shell stays; any
  embedded payload moves to the pool.

The existing `order-valid.json` migrates to
`framework/framework-smoke-typed-pipeline/trigger.json`. Old path deleted.

### Acceptance sensor adjustment

`assert-create-sensor-multi-angle.yaml`'s `assert-fixture-referenced` step
today requires the per-sensor directory `.harness/fixtures/<sensor-id>/`
to be populated. New rule:

```
At least one step of the produced sensor references a path that resolves
under <projectRoot>/.harness/fixtures/ to an existing file, regardless of
whether the path is under _sensors/<sensor-id>/ or under <journey>/<usecase>/.
```

The shell logic of that step is rewritten to discover the path from the
sensor YAML and stat it under the project root.

## Component map

```
schemas/
  usecase.yaml                   ← FixtureRef oneOf envelope

lib/
  fixture/
    load.go                      ← unchanged
    resolve.go                   ← unchanged
    write.go                     ← unchanged
    sample_disk.go               ← NEW: FindOnDisk(hint, searchPaths)
    sample_disk_test.go          ← NEW
    sample_contract.go           ← NEW: DeriveFromContract limited to 4 sources
    sample_contract_test.go      ← NEW
  usecase/
    validate.go                  ← fixture.ref existence + content-tree
                                   contract-evidence check
    validate_test.go             ← extend table

skills/
  detect-usecases/
    SKILL.md                     ← Phase 1.5 sourcing pipeline (stack-driven
                                   searchPaths; WebFetch on unfamiliar libs)
    scripts/
      write-usecase.go           ← add --validate-only flag (DoD item 7)
  create-sensor/
    SKILL.md                     ← Phase 4 step 6 rewrite (reuse from pool)

.harness/                        ← project-tree (this repo's own dogfooding)
  fixtures/                      ← migrated layout
    framework/<usecase-id>/{trigger,outcome}.<ext>
    create-sensor/<usecase-id>/{trigger,outcome}.<ext>
  usecases/**/*.yaml             ← rewritten: fixture: { ref | inline }
  sensors/**/*.yaml              ← references updated to pool paths
```

## What gets harder

- **Code review.** A fixture change is now a "new/changed file + unchanged
  YAML" pair rather than a self-contained YAML diff. Slightly more friction
  for the human reviewer.
- **Rename atomicity.** Renaming a usecase id requires moving its fixture
  directory. Out of scope for this spec; the validator only checks
  existence, not naming coherence.

## What gets easier

- **Sharing.** One fixture, two consumers (usecase + sensor).
- **Diffing.** A standalone JSON/JSONL/TXT file is easier to diff than a
  multi-line string inside YAML.
- **Anti-hallucination auditing.** Every non-primitive payload now has a
  file on disk + a contract-evidence row pointing at the type that declared
  the field names. Pure-imagination payloads either fail validation
  (no contract row) or are absent entirely (no disk sample, derivation
  marked as `BlindSpots`).

## Test plan

- `lib/fixture/sample_disk_test.go` — table entries:
  - `searchPaths` contains a directory with a file named `trigger.json`
    → returned `Sample.Source=="disk"`, `Ext=="json"`, `SourcePath` set.
  - `searchPaths` contains a directory with `request.json` and Role=="trigger"
    → returned (role-aliasing recognized).
  - `searchPaths` contains a directory with `outcome.jsonl` and Role=="outcome"
    → returned, `Ext=="jsonl"`.
  - `searchPaths` empty or no candidate file matches the role → nil sample,
    no error (signals "miss" to the skill).
  - Two candidates in the same `searchPath` directory → resolved by a
    three-step deterministic tiebreaker, in this order:
    (a) **filename match strength**: an exact filename equal to the role
        (`trigger.json` for Role=="trigger") beats an alias match
        (`request.json`, `input.json`) beats a substring match;
    (b) **path proximity**: the candidate in the earlier-listed
        `searchPath` (the caller-supplied order is load-bearing — the
        skill places more-specific paths first);
    (c) **lexicographic** order over the absolute path, as final
        deterministic fallback.
    The library never asks the LLM. When the skill needs LLM judgment
    (e.g. to disambiguate equally-strong matches across `searchPath`
    entries), it calls `FindOnDisk` once per candidate `searchPath` and
    decides at its own layer.
- `lib/fixture/sample_contract_test.go` — one entry per supported source:
  - `json-schema` Draft 2020-12 with two required string fields → payload
    `{"a":"","b":""}`.
  - `openapi-component` referencing `components/schemas/Order` → payload
    matches declared properties.
  - `avro` record with two fields → payload matches Avro field names.
  - `protobuf` message with two fields → payload matches proto field names.
  - Unsupported source kind (e.g. "go-struct") → returns
    `ErrUnsupportedContractSource`, payload nil.
- `lib/usecase/validate_test.go`:
  - `fixture.ref` exists → pass.
  - `fixture.ref` missing → `fixture_not_found`.
  - `fixture.inline` non-primitive without contract row → `contract_evidence_missing`.
  - `fixture.ref` to non-primitive content (parsed by ext) without contract
    row → `contract_evidence_missing`.
  - `--validate-only` exits 0 on a passing file and 1 on a failing one
    without writing anything.
- End-to-end: `/detect-usecases` on a fixture project laid out as
  `skills/detect-usecases/scripts/testdata/<scenario>/` produces drafts
  whose `fixture.ref` resolves on disk and whose `blind_spots[]` reflect
  the tier used.
- `/create-sensor` against one of those drafts produces a sensor whose
  `with: { fixture }` reuses the usecase's path (no duplicate under
  `_sensors/<sensor-id>/`).
- Post-migration gate (DoD item 7): the find+xargs validate loop exits 0
  across all migrated `.harness/usecases/**/*.yaml`.

## Risks and mitigations

- **Risk:** tier 1 disk search is heuristic and may pick a misleading sample.
  **Mitigation:** the chosen `SourcePath` is recorded in `Sample` and
  surfaced in the skill's phase report; the skill asks the user to confirm
  the choice before persistence when the match is ambiguous (`>1` candidate
  of equal proximity).
- **Risk:** the migration step touches many YAMLs at once; a typo breaks
  the framework's own dogfooding.
  **Mitigation:** the migration is a single commit, gated by `go test`
  + `assert-stack-schema-valid` + `assert-create-sensor-multi-angle` +
  the DoD #7 find/validate sweep across `.harness/usecases/**`.
- **Risk:** `HARNESS_FIXTURE_MAX_BYTES` cap (1 MiB) starts to bite for
  log-line corpora or large stdout transcripts.
  **Mitigation:** documented override env var already exists; raise per-run
  if needed. No schema change required.
- **Risk:** tier 2's "first declared value for enums + zero values for
  other primitives" produces a payload that semantically violates the
  business rule the variation is meant to exercise (e.g. an `error-handling`
  variation receives a happy-path payload).
  **Mitigation:** `Sample.BlindSpots` records this explicitly; the skill
  reports the blind spot prominently in Phase 4 so the user knows to
  hand-edit the file or invoke tier 3 instead. This is an accepted
  limitation, not a defect — the alternative would be to LLM-guess the
  payload, which the spec rejects on principle.
