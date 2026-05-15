# Group usecases by journey folder

Status: proposed
Date: 2026-05-15
Related: `lib/usecase/`, `skills/detect-usecases/`, `.harness/usecases/`

## Why

`lib/usecase.ValidateAndPersist` currently writes every UseCase to a flat directory: `<project>/.harness/usecases/<id>.yaml`. The schema already requires every UseCase to declare a `journey_id`, and the framework already cross-checks that id against `stack.journeys[].id` before persisting. The on-disk layout, however, does not reflect this relationship — a project with 30 use cases spread across 8 journeys becomes a flat list of 30 files and the UseCase → Journey membership is invisible until each file is opened.

Partitioning by journey id mirrors the conceptual structure declared in `stack.yaml`. Reading `ls .harness/usecases/` becomes a directory of journeys; reading `ls .harness/usecases/<journey-id>/` becomes the variations within that journey. A future `/create-sensor` skill can glob a single journey's worth of cases (`.harness/usecases/<journey-id>/*.yaml`) instead of loading every file and filtering by field.

## What changes

The persistence convention shifts from flat to journey-partitioned:

```
<project>/.harness/usecases/
├── user-registration/
│   ├── create-user-with-email.yaml
│   └── create-user-duplicate-email-conflict.yaml
└── user-login/
    └── login-with-wrong-password.yaml
```

The change is contained:

1. **`lib/usecase/persist.go`** — `ValidateAndPersist` partitions by `journey_id` after schema validation and cross-check succeed.
2. **`lib/usecase/persist_test.go`** — assertions on the written path move into the journey subdir; the "rejects bad journey" / "rejects missing evidence" cases additionally assert the subdir was not created; a new `TestValidateAndPersist_IdempotentUnderJourneyDir` exercises two consecutive calls with the same draft and asserts byte-identical output.
3. **`skills/detect-usecases/scripts/write-usecase_test.go`** — `TestRun_Happy` asserts the output path under the journey subdir.
4. **`skills/detect-usecases/SKILL.md`** — Phase 2 notes the journey partitioning, Phase 3's report displays the subdir, Safety notes' atomicity statement is updated to refer to the per-journey directory.
5. **`CLAUDE.md`** — the sentence in the "Skills" subsection that reads "persists each through the validator script at `<project>/.harness/sensors/<sensor-id>.yaml`" is left alone (it's about sensors). The companion sentence describing `/detect-usecases` (the one beginning "`skills/detect-usecases/` scans the project…") is updated so its closing path reflects the new layout: `<project>/.harness/usecases/<journey-id>/<usecase-id>.yaml`.
6. **Existing flat files in this repo** — all 36 `.harness/usecases/*.yaml` files are deleted. The user regenerates them post-merge via `/detect-usecases`. This matches the user-confirmed direction ("no migration, delete and regenerate").

The design **does not** change:

- **`schemas/usecase.yaml`** — `journey_id` is already required and constrained to `^[a-z][a-z0-9-]*$`, which is safe as a directory name (no traversal, no separators, no whitespace). Layout is a persistence convention, not a schema fact; no schema bump.
- **`lib/usecase/load.go`** (`LoadUseCaseFile`) — accepts any path; the caller picks the file. Agnostic to layout.
- **`skills/detect-usecases/scripts/write-usecase.go`** — CLI flags and exit codes are unchanged. The script keeps receiving `--out=<project>/.harness/usecases` and delegating layout choice to the library.
- **`lib/usecase/{cross_check,evidence,shape}.go`** — no disk I/O, no path concerns.
- **`/create-sensor`** (future) — out of scope. When it lands, it will glob recursively or by journey id.

## Architecture

The relevant control flow is local to `ValidateAndPersist`. Today:

```
ValidateAndPersist(draft, outDir, projectRoot, stk, schemasDir):
  parse draft JSON
  schema-validate
  cross-check journey_id ∈ stk.journeys[].id
  cross-check every evidence[].file exists under projectRoot
  read id from doc
  MkdirAll(outDir)
  target := outDir / id + ".yaml"
  writeCanonical(target, doc)
  return abs(target)
```

After:

```
ValidateAndPersist(draft, outDir, projectRoot, stk, schemasDir):
  parse draft JSON
  schema-validate
  cross-check journey_id ∈ stk.journeys[].id
  cross-check every evidence[].file exists under projectRoot
  read id from doc
  read journey_id from doc        ← new (defensive, schema already guarantees non-empty)
  journeyDir := outDir / journey_id
  MkdirAll(journeyDir)            ← was MkdirAll(outDir)
  target := journeyDir / id + ".yaml"
  writeCanonical(target, doc)
  return abs(target)
```

The atomic write inside `writeCanonical` is unchanged: `os.CreateTemp(filepath.Dir(target), ...)` writes the tempfile alongside the destination (now inside the journey subdir) and renames within the same directory, preserving atomicity on POSIX filesystems.

## Why the convention lives in the library

The library — not the caller — owns layout conventions. `ValidateAndPersist` already enforces:

- file extension (`.yaml`)
- filename stem (`<id>`)
- file permissions (`0o644`)
- atomic temp + rename
- canonical YAML form via `yaml.JSONToYAML`

Journey partitioning is the same category of decision. Pushing it to the CLI script (so the caller passes `--out=<project>/.harness/usecases/<journey-id>`) would force every present and future writer of UseCases to re-derive the journey id from `doc["journey_id"]` and re-encode the same path convention. The library has the journey id at hand and already does the cross-check; it is the right place.

## Tests

### `lib/usecase/persist_test.go`

Four cases amended, one added:

- `TestValidateAndPersist_Happy` — assert the returned path ends in `<journey_id>/<id>.yaml` (concretely `user-registration/create-user-with-email.yaml`) and that the file exists at that path.
- `TestValidateAndPersist_OverwritesAtomically` — pre-write `STALE` content at `outDir/<journey_id>/<id>.yaml` (creating the subdir manually); after `ValidateAndPersist`, the file no longer contains `STALE`.
- `TestValidateAndPersist_RejectsBadJourney` — after the call fails, assert that `outDir` is empty (no journey subdir created). The MkdirAll runs after the cross-check; a failed cross-check must not leave the subdir behind.
- `TestValidateAndPersist_RejectsMissingEvidence` — same invariant: failed evidence check, no journey subdir.
- **New: `TestValidateAndPersist_IdempotentUnderJourneyDir`** — call `ValidateAndPersist` twice in sequence with the same draft against the same `outDir`. Assert both return the same absolute path, both write the file, and the file's bytes are identical between calls. Binds the existing docstring contract ("Idempotent: re-persisting the same body produces a byte-identical file") to the new layout — confirms that `MkdirAll` over an existing journey subdir does not perturb the file content.

`TestValidateAndPersist_RejectsSchemaViolation` is unchanged in spirit; the existing "expected error" check is sufficient.

### `skills/detect-usecases/scripts/write-usecase_test.go`

- `TestRun_Happy` — the expected path becomes `out/<journey_id>/<id>.yaml` (`out/user-registration/create-user-with-email.yaml`). The stdout substring check stays focused on the filename stem.

### Manual smoke

```bash
go test ./lib/usecase/...
go test -tags=write_usecase ./skills/detect-usecases/...
```

Both suites must pass. No new packages, no new build tags.

## Risks

### `journey_id` typed-assertion safety

After schema validation, `doc["journey_id"]` is guaranteed to be a non-empty string matching `^[a-z][a-z0-9-]*$`. The implementation still performs a `journeyID, ok := doc["journey_id"].(string)` and returns an explicit `journey_id missing after validation` error if the assertion fails, mirroring the existing defensive check for `id`. This is belt-and-suspenders against a future schema regression — cheap insurance, two lines of code.

### Path traversal

`^[a-z][a-z0-9-]*$` forbids `..`, `/`, `\`, and whitespace. There is no path traversal vector via `journey_id`. No additional sanitization is needed.

### Atomicity across the new subdir

`writeCanonical` continues to use `os.CreateTemp(filepath.Dir(target), ".persist-*")`. With the new layout, `filepath.Dir(target)` is the journey subdir; `os.Rename` within that subdir is atomic. No regression.

### Stale flat-layout files in user projects

Out of scope: this design is for the framework, not for downstream user projects. If a downstream project upgrades, it can `rm .harness/usecases/*.yaml` and re-run `/detect-usecases`. No code-level shim.

For this repo specifically, all 36 existing flat files in `.harness/usecases/*.yaml` are deleted as part of the change. The user regenerates them via `/detect-usecases` after merge, at which point they appear under their respective `<journey-id>/` subdirectories.

### Caller-side audit: who reads `.harness/usecases/`?

Grep results (`grep -rn "\.harness/usecases" --include="*.go" --include="*.md" --include="*.yaml"`) confirm the layout change has no hidden consumers:

- **No Go code globs `.harness/usecases/*.yaml`.** The only consumer is `lib/usecase.LoadUseCaseFile(path)`, which takes a specific path and is layout-agnostic.
- **No sensor `verification.golden_cases[].fixture`** references a UseCase path.
- **No script under `skills/`** enumerates UseCases by directory walk.
- **Documentation references** (`README.md`, `CLAUDE.md`, `skills/detect-usecases/SKILL.md`) describe the path as prose. `CLAUDE.md`'s description of the persistence path is included in the "What changes" list; the others are updated as part of item 4 (SKILL.md).
- **Narrative references inside sensor YAML** (e.g. `.harness/sensors/assert-run-sensor-rejects-blocking.yaml` references `.harness/usecases/run-sensor-blocking-rejected.json` in `tags`/`description` prose) are stale `.json` mentions that predate the YAML migration; they are narrative-only, not glob targets, and out of scope here.

Conclusion: the layout change ships without breaking any consumer. `/create-sensor` (when implemented) will receive the journey-partitioned layout as its starting assumption.

## Definition of Done

1. `ValidateAndPersist` writes to `<outDir>/<journey_id>/<id>.yaml`.
2. The journey subdir is created via `MkdirAll` only after schema validation, journey cross-check, and evidence check succeed.
3. `TestValidateAndPersist_IdempotentUnderJourneyDir` exists and passes; re-persisting the same draft yields a byte-identical file.
4. `go test ./lib/usecase/...` passes with the amended assertions.
5. `go test -tags=write_usecase ./skills/detect-usecases/...` passes with the amended `TestRun_Happy`.
6. `skills/detect-usecases/SKILL.md` documents the per-journey layout in Phase 2, Phase 3 report, and Safety notes.
7. `CLAUDE.md`'s description of the `/detect-usecases` persistence path reflects `<project>/.harness/usecases/<journey-id>/<usecase-id>.yaml`.
8. All 36 flat-layout files in `.harness/usecases/*.yaml` are removed from the repo (a single `find .harness/usecases -maxdepth 1 -name '*.yaml' -delete` is sufficient).
9. The `Validate` → cross-check → `MkdirAll` → write sequence is preserved (failures do not leave directories behind).

## Out of scope

- `/create-sensor` glob behavior.
- Downstream-project migration scripts or shims.
- Reading UseCases recursively in any current library function.
- Indexing or caching by journey.
- Any change to `schemas/usecase.yaml`.
