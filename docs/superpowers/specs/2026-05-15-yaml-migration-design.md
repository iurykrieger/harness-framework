# YAML migration for schemas and authored entities

Status: proposed
Date: 2026-05-15
Related: `schemas/`, `lib/schema/`, `lib/sensor/`, `lib/usecase/`, `lib/stack/`, `skills/detect-sensors/`, `skills/detect-usecases/`, `skills/heal-sensor/`, `CLAUDE.md`

## Why

The plugin's four schemas (`signal`, `sensor`, `stack`, `usecase`) and the entities they constrain (`.harness/sensors/<id>.json`, `.harness/usecases/<id>.json`, `.harness/stack.json`) are all authored as JSON. For schemas this is conventional. For the *instances* it is increasingly painful:

- Sensors carry `output_parsing.patterns[].regex` (escaping `"`, `\`, `\\s` inside JSON strings), `verification.golden_cases[].evidence` text, and — for inferential sensors — `execution.user_prompt_template` as a single string. Every newline becomes `\n`; every backslash doubles. The file is unreadable to the human meant to author it.
- UseCases pack narrative `summary`/`behavior`/`invariants` prose, plus `trigger.fixture` and `expected_outcome.fixture` blobs. Same problem: multiline content in single-line JSON strings, plus heavy comma/brace noise around the structure.
- Stack files declare components, log shapes, and (after the use-case entity work) journeys with entry-point evidence. Same multiline problem on a smaller scale.

YAML solves all three with literal block scalars (`|`), implicit string handling, and lower punctuation density. The cost is real but bounded: a YAML library on the load/persist path, schema `$ref` strings updated, file extensions changed, and writers/loaders re-glob.

This migration is scoped to **authored** files. The streaming wire format (`signals.log` JSONL, runner stdout JSONL, hook stdin/stdout) stays JSON — those are not files, they are protocols, and JSONL exists precisely because YAML cannot be tail-able line-by-line. Runtime state (`running_sensors.json`, `auto-issues.json`) also stays JSON: they are framework-generated caches, never edited by humans, and changing their format buys nothing.

## What changes

1. **All four schemas migrate to YAML**: `schemas/{signal,sensor,stack,usecase}.json` → `schemas/{signal,sensor,stack,usecase}.yaml`. Internal `$ref` strings are rewritten (e.g. `"$ref": "signal.json#/$defs/Verdict"` → `$ref: signal.yaml#/$defs/Verdict`).
2. **All authored entity files migrate to YAML**: `.harness/sensors/*.yaml`, `.harness/usecases/*.yaml`, `.harness/stack.yaml`. Filenames are otherwise unchanged — the id is still the stem; only the extension shifts.
3. **`lib/schema/` ships a YAML→JSON conversion layer** so the existing JSON Schema validator (`santhosh-tekuri/jsonschema/v5`) keeps consuming JSON bytes downstream. The library is `sigs.k8s.io/yaml`, chosen because it goes through `encoding/json` and preserves JSON-compatible semantics (no Norway problem, no surprise booleans).
4. **`lib/{sensor,usecase,stack}/` loaders and persisters switch I/O format**: globs target `*.yaml`, load goes YAML → JSON → validator → struct, persist goes struct → JSON → YAML → atomic write.
5. **Skill writers** (`write-sensor.go`, `write-stack.go`, `write-usecase.go`) emit YAML transparently by delegating to `lib.Persist`. Nothing else changes in the skill scripts.
6. **`/heal-sensor` script** persists patched/new sensors as YAML — same `lib` delegation.
7. **Test fixtures in `lib/{sensor,usecase,stack}/testdata/` migrate to YAML** as part of this PR. They are versioned source files of the framework, not user data.
8. **`CLAUDE.md` and SKILL.md examples** are rewritten: code fences showing sensor/usecase/stack content switch from `json` to `yaml`; code fences showing Signal content stay `json`.
9. **No migration script** ships in the plugin. Downstream projects upgrade by deleting `.harness/sensors/*.json`, `.harness/usecases/*.json`, and `.harness/stack.json`, then re-running `/detect-sensors` and `/detect-usecases`. This is acceptable because both commands are idempotent regenerators — that is their entire purpose.

This design **does not** change:

- The streaming protocol: `signals.log`, runner stdout, hook stdin/stdout remain JSONL (one JSON Signal per line). `lib/transcript/`, `lib/subprocess/`, `lib/watcher/`, `lib/signal/` are untouched.
- Runtime state: `.harness/runtime/running_sensors.json` and `.harness/runtime/auto-issues.json` remain JSON. `lib/registry/state.go` is untouched.
- The plugin manifest: `.claude-plugin/plugin.json` remains JSON (read by Claude Code's plugin loader, an external contract).
- Test fixtures in `lib/transcript/testdata/*.jsonl` and `hooks/testdata/*.jsonl` — they mirror the streaming protocol and stay JSONL.
- The JSON Schema validator engine. `santhosh-tekuri/jsonschema/v5` keeps receiving JSON bytes through `AddResource`.

## Architecture

The flow of bytes through the system shifts on the *edges* (file reads/writes) while the core (schema compilation, validation, struct decode) keeps speaking JSON.

```
                        ┌─────────────────────────────────────┐
                        │  authored YAML file on disk         │
                        │  .harness/sensors/<id>.yaml         │
                        └──────────────┬──────────────────────┘
                                       │  os.ReadFile
                                       ▼
                        ┌─────────────────────────────────────┐
                        │  lib/schema.ReadAsJSON              │
                        │    yaml.YAMLToJSON(rawYAML)         │
                        └──────────────┬──────────────────────┘
                                       │  []byte (JSON bytes)
                                       ▼
                        ┌─────────────────────────────────────┐
                        │  lib/schema.Validator.Validate      │
                        │    (santhosh-tekuri/jsonschema/v5)  │
                        └──────────────┬──────────────────────┘
                                       │  validated JSON bytes
                                       ▼
                        ┌─────────────────────────────────────┐
                        │  encoding/json.Unmarshal → *Sensor  │
                        └─────────────────────────────────────┘

                        Persist path (reverse):

                        ┌─────────────────────────────────────┐
                        │  *Sensor struct in memory           │
                        └──────────────┬──────────────────────┘
                                       │  sigs.k8s.io/yaml.Marshal
                                       │    (struct → JSON → YAML)
                                       ▼
                        ┌─────────────────────────────────────┐
                        │  YAML bytes → tmp file → atomic     │
                        │  rename to .harness/sensors/<id>.yaml│
                        └─────────────────────────────────────┘
```

Schemas themselves follow the same load path: at `Validator` construction time, each `schemas/<name>.yaml` is read, converted to JSON bytes via `yaml.YAMLToJSON`, and registered with the compiler under the key `<name>.yaml`. `$ref` strings inside the schemas reference siblings by that same key, so resolution Just Works without library patches.

## The YAML library

**`sigs.k8s.io/yaml`** is the load-bearing dependency.

Mechanics: `yaml.Unmarshal` parses YAML, converts to a JSON-equivalent intermediate, then defers to `encoding/json.Unmarshal`. `yaml.Marshal` does the inverse — `encoding/json.Marshal` first, then JSON → YAML. The library exposes the intermediate explicitly as `yaml.YAMLToJSON([]byte) ([]byte, error)` and `yaml.JSONToYAML([]byte) ([]byte, error)`, which are the two helpers this design uses directly.

Why this library over alternatives:

- `gopkg.in/yaml.v3` — pure YAML, full feature surface including comment round-trip. Powerful but loses JSON semantics (e.g. distinguishes nil from empty string differently, has its own bool resolver), which would force a parallel set of conversion rules for the validator path. Wrong shape for this project.
- `github.com/goccy/go-yaml` — faster parser, better error messages, comment-preserving. Same semantic-mismatch problem as `yaml.v3`. Worth revisiting later if conversion overhead becomes measurable; today it is not.
- `sigs.k8s.io/yaml` — slowest of the three, but the only one that gives "YAML over JSON Schema" without surprises. Apache-2.0, mature, used by every Kubernetes-adjacent Go project. The right default.

**Comment preservation is intentionally given up.** `sigs.k8s.io/yaml` discards comments on round-trip. A human who adds `# explanation` to a sensor and later triggers `/heal-sensor` will lose that comment when the file is rewritten. This is documented in `CLAUDE.md`; comments live in commit messages and the design doc, not in machine-managed YAML.

## Changes by package

### `lib/schema/`

The validator type and its construction expand to handle YAML schemas and to expose a YAML-aware reader for instance files.

**`NewValidator(schemasDir string)`** changes:
- Globs `<schemasDir>/*.yaml` (instead of `*.json`).
- For each file: `raw, _ := os.ReadFile(path); j, _ := yaml.YAMLToJSON(raw); compiler.AddResource(filepath.Base(path), bytes.NewReader(j))`.
- Compiles `sensor.yaml` (and any other entry points) by that resource name. The compiler's internal `$ref` resolution uses these registered keys, matching the `$ref: signal.yaml#/$defs/Verdict` strings inside the schemas.

**New helper `ReadAsJSON(path string) ([]byte, error)`** is the canonical reader for any YAML-authored entity file. It:
- Reads the file from disk.
- Returns `yaml.YAMLToJSON(raw)`.
- Surfaces YAML parse errors as structured `verdict=error metadata.kind=yaml_parse_failed` so failure modes are uniform with existing schema errors.

`Validator.Validate(target, jsonBytes)` is unchanged — it continues to accept JSON bytes.

### `lib/sensor/`, `lib/usecase/`, `lib/stack/`

The three entity packages mirror each other; the same edits land in each.

**`load.go`** — `LoadFromFile(path)` flow:
1. `jsonBytes, err := schema.ReadAsJSON(path)`.
2. `err := schema.Validate(target, jsonBytes)`.
3. `err := json.Unmarshal(jsonBytes, &out)`.
4. (For sensors/usecases) `cross_check` / `evidence` checks as today.

Directory listing helpers (`ListAll(dir)`) switch globs from `*.json` to `*.yaml`.

**`persist.go`** — `ValidateAndPersist(...)` flow:
1. JSON-Schema validate the draft (received as JSON bytes from the caller, e.g. from the writer script's `--draft=<path.json>` input *or* internally constructed).
2. `jsonOut, err := json.Marshal(validatedStruct)` (canonical form, alphabetized keys via `encoding/json` default ordering).
3. `yamlOut, err := yaml.JSONToYAML(jsonOut)`.
4. Atomic temp + rename to `<outDir>/<id>.yaml`.

Drafts handed to the writer scripts (`write-sensor.go`, `write-usecase.go`, `write-stack.go`) **may be either JSON or YAML on input** — the writer reads via `schema.ReadAsJSON`, so the LLM authoring the draft is free to emit YAML inline. The on-disk artifact is always YAML.

### Skill writer scripts

- `skills/detect-sensors/scripts/write-sensor.go` — no flag changes; output file becomes `<outDir>/<id>.yaml`.
- `skills/detect-sensors/scripts/write-stack.go` — output file becomes `<outDir>/.harness/stack.yaml`.
- `skills/detect-usecases/scripts/write-usecase.go` — output file becomes `<outDir>/<id>.yaml`.
- `skills/heal-sensor/scripts/*` — persists through `lib/sensor.ValidateAndPersist`, inherits YAML output.

All four delegate to `lib.Persist`, so the change concentrates in the lib. Each script's `_test.go` is updated to assert the written file is parseable YAML and round-trips through the loader.

### Test fixtures

In-repo fixtures consumed by the test suite are converted as part of this PR:

| Path | Action |
|---|---|
| `lib/sensor/testdata/canonical-{computational,inferential,setup}.json` | → `.yaml` |
| `lib/sensor/testdata/invalid-*.json` | → `.yaml` |
| `lib/usecase/testdata/canonical-usecase.json` | → `.yaml` |
| `lib/usecase/testdata/invalid-*.json` | → `.yaml` |
| `lib/stack/testdata/golden-stack*.json` | → `.yaml` |
| `lib/stack/testdata/stack-discovery/**` | inspected case-by-case; entities → `.yaml`, log samples ignored |
| `lib/transcript/testdata/*.jsonl` | **unchanged** (streaming protocol fixtures) |
| `hooks/testdata/*.jsonl` | **unchanged** (streaming protocol fixtures) |

Conversion is performed one-time during PR preparation (e.g. via `yq -P 'sort_keys(..)' -o=yaml in.json > out.yaml` or by hand for files small enough to inspect). It does **not** ship as plugin code. The cost is bounded — total fixture count is in the low dozens.

Tests that load fixtures by filename are updated for the new extension. Tests that build sensors/usecases inline as Go struct literals are unaffected. Tests that build JSON strings inline and pass them through the loader migrate to one of two patterns:
- For round-trip tests, build the Go struct directly and call `Persist` + `Load` (cleaner anyway).
- For schema-violation tests, build YAML strings inline (or use a `usecasetest`/`sensortest` helper that wraps `yaml.JSONToYAML` of an inline JSON literal — preserves existing test prose without forcing YAML syntax into Go heredocs).

### `lib/transcript/`, `lib/subprocess/`, `lib/watcher/`, `lib/signal/`, `lib/registry/`, `lib/orchestrator/`

Unchanged. These touch the streaming protocol (`signals.log`, runner stdout) and the runtime registry (`running_sensors.json`), both of which stay JSON. The `encoding/json` calls in these packages are correct as-is.

## Documentation updates

### `CLAUDE.md`

- The section "The four schemas" is rewritten: the four files are now `signal.yaml`, `sensor.yaml`, `stack.yaml`, `usecase.yaml`. The Draft 2020-12 statement stays — JSON Schema is the *language*, YAML is the *encoding*. The `$ref` example switches to `signal.yaml#/$defs/Verdict`.
- Rule 2 ("Schemas are versioned with the plugin") gets a single sentence appended noting that schemas are authored in YAML and converted to JSON bytes at validator construction time.
- The architecture section's entity-path examples switch: `.harness/sensors/<id>.yaml`, `.harness/usecases/<id>.yaml`, `.harness/stack.yaml`.
- A new short subsection under "Project rules" or "Architecture" notes the comment-loss trade-off so future contributors don't expect YAML comments to survive `/heal-sensor` round-trips.

### SKILL.md files

Each skill body with a code fence showing a sensor/usecase/stack instance switches the fence language from ```` ```json ```` to ```` ```yaml ```` and reformats the body. Code fences showing Signal content (verdict envelopes, metadata.kind examples) remain ```` ```json ````. Affected files:

- `skills/detect-sensors/SKILL.md`
- `skills/detect-usecases/SKILL.md`
- `skills/create-sensor/SKILL.md`
- `skills/run-sensor/SKILL.md`
- `skills/start-sensor/SKILL.md`
- `skills/tail-sensor/SKILL.md`
- `skills/list-sensors/SKILL.md`
- `skills/stop-sensor/SKILL.md`
- `skills/heal-sensor/SKILL.md`

The rewrite is mechanical; the substance of each skill is unaffected.

## Risks and mitigations

### Regex round-trip in `output_parsing.patterns[].regex`

Existing sensors use patterns like `^FAIL\s+(.+)$`, `(?i)error:`, `^\[\d{4}-\d{2}-\d{2}T[\d:.Z+-]+\]`, and similar. `sigs.k8s.io/yaml.Marshal` (and `yaml.JSONToYAML` upstream) quotes scalars containing colons, leading whitespace, special tokens, or characters that would otherwise be misinterpreted by the YAML parser, falling back to literal block scalars only when necessary. The risk is a corner case where a regex round-trips through marshal → unmarshal with a semantic difference (e.g. a trailing newline introduced by block-scalar style, or whitespace collapsed by folded-scalar style).

**Mitigation**: `lib/sensor/persist_test.go` gains a table-driven test covering every regex pattern currently used in the in-repo sensors plus a representative sample of edge cases — patterns containing `:`, `#`, `&`, `*`, `!`, `|`, `>`, leading/trailing whitespace, embedded newlines, and Unicode. Each table row asserts that `Unmarshal(Marshal(p)) == p` byte-for-byte. The test acts as a regression net for the library upgrade.

### Schema `$ref` resolution

`santhosh-tekuri/jsonschema/v5` resolves cross-file `$ref` by the resource name registered via `compiler.AddResource(name, ...)`. The migration renames each resource from `signal.json` to `signal.yaml`. If any `$ref` inside the schemas is missed during renaming, validation will fail with a clear "resource not found" error at validator construction time — not at runtime.

**Mitigation**: `lib/schema/validator_test.go` already exercises `NewValidator` against the on-disk schemas and validates representative valid and invalid instances. That test is the canary; if any `$ref` is stale, it fails before any other code runs. No additional mitigation needed beyond running the suite.

### Plugin upgrade for downstream projects

Existing user projects have `.harness/sensors/*.json`, `.harness/usecases/*.json`, and `.harness/stack.json` on disk. Post-migration, the framework only globs `*.yaml`, so those files become invisible. Loaders will silently return empty sensor/usecase sets, which would manifest as `/run-sensor <id>` returning "sensor not found" errors.

**Mitigation**: Document the upgrade path explicitly in the PR description and in a new "Upgrading" section of the repo `README.md`:

```
On upgrade from a JSON-era version:
  rm -f .harness/stack.json
  rm -f .harness/sensors/*.json
  rm -f .harness/usecases/*.json
  # then re-run:
  /detect-sensors
  /detect-usecases
```

No code-level shim, no dual-extension fallback, no warning loop on detecting orphan JSON files in `.harness/`. The framework treats `.json` files in those directories as foreign content.

### CI and lint expectations

Some IDE plugins and CI lint configs target `*.json` for schema-aware autocompletion (e.g. VS Code's JSON Schema mapping). Post-migration these mappings move to `*.yaml`.

**Mitigation**: out of scope for this design — the framework does not ship IDE config. A documentation note in the PR points contributors at adjusting their personal IDE settings if needed.

## Tests

### Unit

- `lib/schema/`:
  - `NewValidator` loads YAML schemas and resolves `$ref` correctly.
  - `ReadAsJSON` converts well-formed YAML to JSON bytes byte-equivalent to a known-good JSON file.
  - `ReadAsJSON` reports `yaml_parse_failed` with a usable error message on malformed input.

- `lib/sensor/`, `lib/usecase/`, `lib/stack/`:
  - `LoadFromFile` accepts a YAML file and produces the same struct as the JSON era did (parametric: same test cases, same expected structs, different input encoding).
  - `Persist` writes YAML that the loader round-trips losslessly.
  - `Persist` emits canonical form: alphabetized keys, no trailing whitespace, terminating newline.
  - `Persist` is atomic: a mid-write interruption (simulated by erroring inside the `Rename` step) leaves the original file intact.
  - Glob-based listing helpers find `*.yaml` and ignore `*.json` left over in the directory (deterministic upgrade behavior).

- `lib/sensor/persist_test.go` regex round-trip table (described above).

### Integration

- `skills/detect-sensors/scripts/write-sensor_test.go`:
  - Given a draft on stdin/file, writes a `.yaml` file to `--out`.
  - The written file passes `lib/sensor.LoadFromFile`.
  - Re-invocation with the same draft overwrites atomically.

- Equivalent tests for `write-stack.go` and `write-usecase.go`.

- `skills/heal-sensor/scripts/*` end-to-end:
  - Heal a corrupted sensor; verify the persisted result is YAML and loads cleanly.

### Manual smoke

Before merging, run on this repo:

```bash
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential   ./skills/...
go test -tags=start_sensor      ./skills/...
go test -tags=stop_sensor       ./skills/...
go test -tags=list_sensors      ./skills/...
go test -tags=tail_sensor       ./skills/...
go test -tags=heal_retry_original ./skills/heal-sensor/...
go test -tags=write_usecase     ./skills/...
go vet -tags=run_computational  ./...
go vet -tags=run_inferential    ./...
```

Then end-to-end against this repo's own `.harness/` (after re-running `/detect-sensors` and `/detect-usecases` on it):

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=run_computational \
  ./skills/run-sensor/scripts <a-known-sensor-id>
```

Expected: the runner reads the YAML sensor definition, spawns the subprocess, and emits JSONL Signals on stdout exactly as before — the wire format is untouched.

## Implementation milestones

Each milestone is a potential PR boundary. The ordering keeps the test suite passing at every step.

1. **Add `sigs.k8s.io/yaml` dependency** and the `lib/schema.ReadAsJSON` helper. No callers yet. Unit test covers happy path + parse failure.
2. **Migrate schemas**: convert `schemas/{signal,sensor,stack,usecase}.json` to `.yaml`, rewrite `$ref` strings. Update `lib/schema.NewValidator` to load YAML. Run `lib/schema/validator_test.go` — failures here are stale `$ref`s.
3. **Migrate `lib/sensor/`**: switch `load.go` to use `ReadAsJSON`; switch `persist.go` to emit YAML; convert `lib/sensor/testdata/*.json` to `.yaml`; update tests for the new extension. Add regex round-trip table to `persist_test.go`.
4. **Migrate `lib/stack/`**: same pattern. Convert `testdata/`; update tests.
5. **Migrate `lib/usecase/`**: same pattern. Convert `testdata/`; update tests.
6. **Update skill writers**: `write-sensor.go`, `write-stack.go`, `write-usecase.go` change their output filename suffix and rely on `lib.Persist` for the format. Update their `_test.go`.
7. **Update `/heal-sensor`** persist paths if they bypass `lib.Persist` for any reason (they should not, but verify).
8. **Documentation**: `CLAUDE.md` rewrites; SKILL.md code fences switched; `README.md` gains the "Upgrading" section.
9. **Repo `.harness/`**: re-run `/detect-sensors` and `/detect-usecases` on this repo so the committed `.harness/` reflects YAML artifacts. Commit the result.

Milestones 1 and 2 can land together; 3, 4, and 5 are independent and can be parallel PRs if convenient (each touches its own `lib/` package and its own testdata). 6 depends on 3–5. 7 is a verification step. 8 lands with the user-visible changes (any of 3–6 is the natural moment). 9 is the final commit.

## Open questions

- **`.yml` vs `.yaml`** — chosen: `.yaml`. The YAML.org spec recommends `.yaml`; Kubernetes, GitHub Actions, Helm, and Argo CD all use `.yaml`. `.yml` is a legacy Ruby/Rails-era convention. No accommodation for `.yml` in globs.
- **Multi-document YAML** — chosen: single-document per file. Sensors, use cases, and the stack file are each one entity. There is no use case for `---`-separated documents inside `.harness/sensors/<id>.yaml`.
- **Anchors and aliases** — chosen: discouraged but not blocked. `sigs.k8s.io/yaml` resolves anchors at parse time, producing a tree without aliases. Hand-authored YAML may use them; the persist round-trip will inline. No code-level restriction.
- **Inline JSON inside YAML** — chosen: allowed. YAML is a superset of JSON. A user who pastes a JSON fixture into `trigger.fixture:` and never reformats will still parse correctly. The framework does not normalize fixture style on persist beyond what `yaml.JSONToYAML` does naturally.

## Future work

- **Comment-preserving persist** — if YAML comments become important to users, evaluate switching the persist library to `goccy/go-yaml` with AST-based round-trip. Would require a separate validator path that does not go through `encoding/json`, so the architectural cost is non-trivial. Not justified by current evidence.
- **Schema-aware editor integration** — publish or document a JSON Schema → YAML language server config (e.g. `yaml.schemas` mapping in VS Code's `settings.json`) so authors get autocompletion and inline validation inside `.harness/sensors/<id>.yaml`. Out of scope here, natural follow-up.
- **YAML linter in CI** — add `yamllint` (or equivalent) as a hook that enforces indentation, line length, and trailing-newline conventions on committed YAML artifacts. Optional cleanup once the format is established.
