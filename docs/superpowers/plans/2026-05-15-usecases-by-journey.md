# UseCases by Journey Folder — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Group persisted UseCases under per-journey subdirectories, so the on-disk layout `<project>/.harness/usecases/<journey-id>/<usecase-id>.yaml` mirrors the conceptual `stack.journeys[]` structure.

**Architecture:** The library `lib/usecase.ValidateAndPersist` already extracts `journey_id` from the validated draft to cross-check it against the stack. The change is a tight, two-line shift: extract `journey_id` after the schema/cross-check phase, `MkdirAll(filepath.Join(outDir, journeyID))`, and join it into the target path. The library owns the layout convention; the writer CLI and consumers stay unchanged.

**Tech Stack:** Go 1.25, `sigs.k8s.io/yaml` (YAML I/O), `santhosh-tekuri/jsonschema/v5` (validator). Tests use the standard `testing` package, table-driven where applicable. Test invocation:

```bash
go test ./lib/usecase/...
go test -tags=write_usecase ./skills/detect-usecases/...
```

The plugin invocation contract (every script runs via `go run -C "${CLAUDE_PLUGIN_ROOT}"`) is documented in `CLAUDE.md` § "Build, validate, test".

---

## Spec reference

This plan implements `docs/superpowers/specs/2026-05-15-usecases-by-journey-design.md`. Each spec DoD item maps to a task below:

| DoD # | Spec item | Task |
|---|---|---|
| 1 | `ValidateAndPersist` writes to `<outDir>/<journey_id>/<id>.yaml` | Task 1 |
| 2 | MkdirAll only after schema/journey/evidence checks succeed | Task 1 (Step 7 implementation) |
| 3 | `TestValidateAndPersist_IdempotentUnderJourneyDir` exists and passes | Task 1 (Step 4) |
| 4 | `go test ./lib/usecase/...` passes | Task 1 (Step 9) |
| 5 | `go test -tags=write_usecase ./skills/detect-usecases/...` passes | Task 1 (Step 9) |
| 6 | `skills/detect-usecases/SKILL.md` documents per-journey layout | Task 2 |
| 7 | `CLAUDE.md` reflects new persistence path | Task 3 |
| 8 | All 36 flat `.harness/usecases/*.yaml` removed | Task 4 |
| 9 | Failure sequence does not leave directories behind | Task 1 (Step 3 covers this) |

---

## File Structure

Files touched by this plan:

| Path | Action | Responsibility |
|---|---|---|
| `lib/usecase/persist.go` | Modify | Production: target-path computation in `ValidateAndPersist`. |
| `lib/usecase/persist_test.go` | Modify | Tests: 4 amended cases + 1 new (idempotency). |
| `skills/detect-usecases/scripts/write-usecase_test.go` | Modify | Tests: `TestRun_Happy` expected path. |
| `skills/detect-usecases/SKILL.md` | Modify | Docs: Phase 2 note, Phase 3 report example, Safety. |
| `CLAUDE.md` | Modify | Docs: persistence-path sentence for `/detect-usecases`. |
| `.harness/usecases/*.yaml` (36 files) | Delete | Cleanup: regenerated post-merge via `/detect-usecases`. |

No file creations. No new packages, no new build tags, no new dependencies. No changes to:

- `lib/usecase/load.go`, `shape.go`, `cross_check.go`, `evidence.go`
- `lib/usecase/usecasetest/canonical.go`
- `schemas/usecase.yaml`
- `skills/detect-usecases/scripts/write-usecase.go` (CLI surface)

---

### Task 1: Implement journey partitioning in `ValidateAndPersist` (TDD cycle)

This is a single TDD cycle that bundles all test amendments and the implementation. It produces one commit.

**Files:**
- Modify: `lib/usecase/persist_test.go`
- Modify: `skills/detect-usecases/scripts/write-usecase_test.go`
- Modify: `lib/usecase/persist.go`

#### Background: what the canonical fixture looks like

`lib/usecase/usecasetest.CanonicalBody(t)` loads `lib/usecase/testdata/canonical-usecase.yaml`, which declares `id: create-user-with-email` and `journey_id: user-registration`. The minimal stack in `lib/usecase/persist_test.go::minimalStack()` already declares a matching journey. After the change, the expected output path becomes `<outDir>/user-registration/create-user-with-email.yaml`.

- [ ] **Step 1: Amend `TestValidateAndPersist_Happy` to assert the journey-partitioned path**

Edit `lib/usecase/persist_test.go`, replacing the suffix assertion in the happy test:

```go
func TestValidateAndPersist_Happy(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	projectRoot := projectRootWithEvidence(t)
	body := usecasetest.CanonicalBody(t)

	path, err := usecase.ValidateAndPersist(body, outDir, projectRoot, minimalStack(), schemasDir)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	wantSuffix := filepath.Join("user-registration", "create-user-with-email.yaml")
	if !strings.HasSuffix(path, wantSuffix) {
		t.Errorf("path = %q, want suffix %q", path, wantSuffix)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not written: %v", err)
	}
}
```

- [ ] **Step 2: Amend `TestValidateAndPersist_OverwritesAtomically` to seed STALE inside the journey subdir**

Replace the body:

```go
func TestValidateAndPersist_OverwritesAtomically(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	projectRoot := projectRootWithEvidence(t)

	journeyDir := filepath.Join(outDir, "user-registration")
	if err := os.MkdirAll(journeyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(journeyDir, "create-user-with-email.yaml")
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

- [ ] **Step 3: Amend `TestValidateAndPersist_RejectsBadJourney` and `_RejectsMissingEvidence` to assert no subdir is created**

For the bad-journey case, replace the body:

```go
func TestValidateAndPersist_RejectsBadJourney(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	projectRoot := projectRootWithEvidence(t)
	body := usecasetest.CanonicalBody(t)

	bad := &stack.Stack{Archetypes: []stack.Archetype{stack.ArchetypeHTTPAPI}}
	if _, err := usecase.ValidateAndPersist(body, outDir, projectRoot, bad, schemasDir); err == nil {
		t.Fatal("expected journey cross-check error")
	}
	entries, _ := os.ReadDir(outDir)
	if len(entries) != 0 {
		t.Errorf("expected nothing written on validation failure, got %d entries (subdir leak)", len(entries))
	}
}
```

For the missing-evidence case, append the same `ReadDir` assertion at the end. Concretely, change the test body to:

```go
func TestValidateAndPersist_RejectsMissingEvidence(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	body := usecasetest.CanonicalBody(t)

	// projectRoot WITHOUT the evidence file
	if _, err := usecase.ValidateAndPersist(body, outDir, t.TempDir(), minimalStack(), schemasDir); err == nil {
		t.Fatal("expected evidence cross-check error")
	}
	entries, _ := os.ReadDir(outDir)
	if len(entries) != 0 {
		t.Errorf("expected nothing written on validation failure, got %d entries (subdir leak)", len(entries))
	}
}
```

- [ ] **Step 4: Add `TestValidateAndPersist_IdempotentUnderJourneyDir`**

Append at the end of `lib/usecase/persist_test.go`:

```go
func TestValidateAndPersist_IdempotentUnderJourneyDir(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	projectRoot := projectRootWithEvidence(t)
	body := usecasetest.CanonicalBody(t)

	first, err := usecase.ValidateAndPersist(body, outDir, projectRoot, minimalStack(), schemasDir)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	dataFirst, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("read first: %v", err)
	}

	second, err := usecase.ValidateAndPersist(body, outDir, projectRoot, minimalStack(), schemasDir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if first != second {
		t.Errorf("paths differ: first=%q second=%q", first, second)
	}
	dataSecond, err := os.ReadFile(second)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if string(dataFirst) != string(dataSecond) {
		t.Errorf("bytes differ between calls; idempotency broken")
	}
}
```

- [ ] **Step 5: Amend `TestRun_Happy` in `write-usecase_test.go`**

Edit `skills/detect-usecases/scripts/write-usecase_test.go`. Replace the `expected` line and the `os.Stat` target:

```go
func TestRun_Happy(t *testing.T) {
	projectRoot := t.TempDir()
	writeStackYAML(t, projectRoot)
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
	expected := filepath.Join(out, "user-registration", "create-user-with-email.yaml")
	if !strings.Contains(stdout.String(), expected) {
		t.Fatalf("stdout %q missing expected path %q", stdout.String(), expected)
	}
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("file not written at %s: %v", expected, err)
	}
}
```

The stdout check is tightened to the full expected path (the script prints the absolute path; with the new layout, that path *must* include the journey subdir).

- [ ] **Step 6: Run both test suites to confirm RED**

Run:

```bash
go test ./lib/usecase/...
```

Expected: fails — at minimum `TestValidateAndPersist_Happy` reports a path-suffix mismatch (path ends in `create-user-with-email.yaml`, not `user-registration/create-user-with-email.yaml`).

Run:

```bash
go test -tags=write_usecase ./skills/detect-usecases/...
```

Expected: fails — `TestRun_Happy` reports the file is not at the expected path.

- [ ] **Step 7: Implement the journey partitioning in `ValidateAndPersist`**

Edit `lib/usecase/persist.go`. Within `ValidateAndPersist`, replace the block from the `id, ok := …` check down to the `target := …` line. The current block reads:

```go
id, ok := doc["id"].(string)
if !ok || id == "" {
	return "", fmt.Errorf("usecase.id missing after validation")
}
if err := os.MkdirAll(outDir, 0o755); err != nil {
	return "", fmt.Errorf("mkdir: %w", err)
}
target := filepath.Join(outDir, id+".yaml")
```

Replace it with:

```go
id, ok := doc["id"].(string)
if !ok || id == "" {
	return "", fmt.Errorf("usecase.id missing after validation")
}
journeyID, ok := doc["journey_id"].(string)
if !ok || journeyID == "" {
	return "", fmt.Errorf("usecase.journey_id missing after validation")
}
journeyDir := filepath.Join(outDir, journeyID)
if err := os.MkdirAll(journeyDir, 0o755); err != nil {
	return "", fmt.Errorf("mkdir: %w", err)
}
target := filepath.Join(journeyDir, id+".yaml")
```

The `journey_id` defensive check is belt-and-suspenders: the schema already guarantees a non-empty string matching `^[a-z][a-z0-9-]*$`, but the explicit error keeps the failure mode local if a future schema regression slips through.

No other changes to `persist.go` are needed. `writeCanonical` continues to use `filepath.Dir(target)` for the tempfile location, which now resolves to `journeyDir` — the atomic rename stays within the same directory and remains atomic on POSIX filesystems.

- [ ] **Step 8: Run both test suites to confirm GREEN**

Run:

```bash
go test ./lib/usecase/...
```

Expected: PASS (all 5 cases — 4 amended + 1 new — pass).

Run:

```bash
go test -tags=write_usecase ./skills/detect-usecases/...
```

Expected: PASS (`TestRun_Happy` passes; the other `TestRun_*` cases are not affected by the change — `TestRun_StackMissing`, `TestRun_SchemaViolation`, `TestRun_JourneyOrphan`, `TestRun_EvidenceMissing`, `TestRun_MissingOut`, `TestRun_MissingProjectRoot`, `TestRun_NoPositional` all exercise failure paths that abort before persist).

- [ ] **Step 9: Run vet for the affected build tags**

```bash
go vet ./lib/usecase/...
go vet -tags=write_usecase ./skills/detect-usecases/scripts/...
```

Expected: no output (no vet warnings).

- [ ] **Step 10: Commit**

```bash
git add lib/usecase/persist.go lib/usecase/persist_test.go skills/detect-usecases/scripts/write-usecase_test.go
git commit -m "$(cat <<'EOF'
feat(usecase): partition persisted UseCases by journey_id

ValidateAndPersist now writes to <outDir>/<journey_id>/<id>.yaml
instead of the previous flat <outDir>/<id>.yaml. The library owns the
layout convention; the writer CLI surface is unchanged.

Tests amended to assert the per-journey path; a new
TestValidateAndPersist_IdempotentUnderJourneyDir binds the
existing idempotency contract to the new layout.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Update `skills/detect-usecases/SKILL.md`

**Files:**
- Modify: `skills/detect-usecases/SKILL.md`

The skill text describes the layout in three places: Phase 2 (the writer-script flow), Phase 3 (the report-back format), and the Safety notes (atomicity statement).

- [ ] **Step 1: Update Phase 2 prose**

In `skills/detect-usecases/SKILL.md`, locate the sentence:

```
The script reads `<project>/.harness/stack.yaml`, validates the draft against `schemas/usecase.yaml`, cross-checks `journey_id` against `stack.journeys[].id`, verifies every `evidence[].file` exists, then writes canonical YAML to `<out>/<id>.yaml` atomically.
```

Replace `<out>/<id>.yaml` with `<out>/<journey_id>/<id>.yaml` and append a clarifying half-sentence:

```
The script reads `<project>/.harness/stack.yaml`, validates the draft against `schemas/usecase.yaml`, cross-checks `journey_id` against `stack.journeys[].id`, verifies every `evidence[].file` exists, then writes canonical YAML to `<out>/<journey_id>/<id>.yaml` atomically (the per-journey subdirectory is created on first write for that journey).
```

- [ ] **Step 2: Update Phase 3 report example**

Locate the report-back block (the fenced block that begins ```` ```\nGenerated 14 use cases at /repo/.harness/usecases/: ````). Replace it with:

````
```
Generated 14 use cases at /repo/.harness/usecases/:

journey: user-registration (5 use cases) → /repo/.harness/usecases/user-registration/
  - create-user-with-email.yaml                 — critical · happy-path
  - create-user-duplicate-email-conflict.yaml   — high · error-handling
  - create-user-invalid-email-format.yaml       — medium · validation
  - create-user-missing-password.yaml           — medium · validation
  - create-user-with-disposable-email.yaml      — low · edge-case

journey: user-login (4 use cases) → /repo/.harness/usecases/user-login/
  - ...

Next: /create-sensor <use-case-id> to generate a deterministic regression sensor for each.
```
````

- [ ] **Step 3: Update the Safety note about atomic overwrite**

Locate the bullet:

```
- Existing files at `<out>/<id>.yaml` are overwritten atomically by `os.Create` + `os.Rename`. Commit `.harness/usecases/` before re-running so diffs are reviewable.
```

Replace it with:

```
- Existing files at `<out>/<journey_id>/<id>.yaml` are overwritten atomically by `os.Create` + `os.Rename` within the per-journey subdirectory. Commit `.harness/usecases/` before re-running so diffs are reviewable.
```

- [ ] **Step 4: Commit**

```bash
git add skills/detect-usecases/SKILL.md
git commit -m "$(cat <<'EOF'
docs(detect-usecases): reflect per-journey persistence layout in SKILL.md

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Update `CLAUDE.md`

**Files:**
- Modify: `CLAUDE.md`

The project-level CLAUDE.md describes `/detect-usecases`'s persistence path in one place.

- [ ] **Step 1: Update the persistence-path sentence**

Locate the sentence in `CLAUDE.md` that reads:

```
`skills/detect-usecases/` scans the project, augments `stack.yaml` with `purpose`/`archetypes`/`journeys` when missing, then drafts one descriptive UseCase per observable journey variation and persists each via `skills/detect-usecases/scripts/write-usecase.go` to `<project>/.harness/usecases/<id>.yaml`.
```

Replace `<project>/.harness/usecases/<id>.yaml` with `<project>/.harness/usecases/<journey-id>/<usecase-id>.yaml`. The full sentence after the edit:

```
`skills/detect-usecases/` scans the project, augments `stack.yaml` with `purpose`/`archetypes`/`journeys` when missing, then drafts one descriptive UseCase per observable journey variation and persists each via `skills/detect-usecases/scripts/write-usecase.go` to `<project>/.harness/usecases/<journey-id>/<usecase-id>.yaml`.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
docs: update CLAUDE.md persistence path for /detect-usecases

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Delete the 36 flat-layout files in `.harness/usecases/`

**Files:**
- Delete: `.harness/usecases/*.yaml` (36 files, depth 1)

After merge, the user will regenerate them under their respective `<journey-id>/` subdirectories via `/detect-usecases`.

- [ ] **Step 1: Verify the current count**

```bash
find .harness/usecases -maxdepth 1 -name '*.yaml' -type f | wc -l
```

Expected: `36`. If the number differs, do not delete blindly — investigate why (a new UseCase was added since the spec was written, or files moved into subdirs already). The spec assumes 36; reconcile before proceeding.

- [ ] **Step 2: Delete the flat-layout files**

```bash
find .harness/usecases -maxdepth 1 -name '*.yaml' -type f -delete
```

The `-maxdepth 1` constraint is load-bearing: it ensures no future per-journey subdirectory contents are caught by the same command if Task 4 is re-run after other UseCases have been regenerated.

- [ ] **Step 3: Verify the directory is empty (of yamls)**

```bash
find .harness/usecases -name '*.yaml' -type f
```

Expected: empty output.

- [ ] **Step 4: Commit**

```bash
git add -A .harness/usecases/
git status --short
```

Confirm the status shows only deletions under `.harness/usecases/`. Then:

```bash
git commit -m "$(cat <<'EOF'
chore: remove flat-layout UseCases ahead of journey-partitioned regeneration

The 36 .harness/usecases/*.yaml files are removed in preparation for
the journey-partitioned layout. Regenerate post-merge via:

  /detect-usecases

which will write each UseCase under .harness/usecases/<journey-id>/.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Post-implementation smoke

Once all four tasks are committed, run the full local verification suite from `CLAUDE.md` § "Build, validate, test" to confirm no regression elsewhere:

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

Expected: all suites PASS. The change is local to `lib/usecase` and its sole writer-script consumer; no other suite is touched.

---

## Out of scope (do not implement)

- Recursive globbing of `.harness/usecases/**` in any current library function or skill (no consumer exists today; future `/create-sensor` will handle this).
- Migration scripts for downstream user projects.
- Any change to `schemas/usecase.yaml`.
- Any change to `write-usecase.go` CLI flags or exit codes.
- Touching `LoadUseCaseFile` or other `lib/usecase` files beyond `persist.go` and `persist_test.go`.
- Cleaning up stale `.json` references in narrative fields of `.harness/sensors/*.yaml` (predates this change, separate concern).
