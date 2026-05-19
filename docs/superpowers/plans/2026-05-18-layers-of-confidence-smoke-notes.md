# Layers of Confidence — Smoke Notes

This is the manual smoke checklist for the layers-of-confidence implementation. The framework-level test environment does not seed a sample project, so the user-facing skills (`/detect-sensors`, `/create-sensors`, `/validate-usecase`) must be exercised against a real project tree by the operator. This document is the canonical step-list.

## Prerequisites

- A project with `.harness/stack.yaml` and at least one `.harness/usecases/<journey>/<usecase>.yaml` on disk.
- The framework checkout available via `CLAUDE_PLUGIN_ROOT`.
- The user's `gh` CLI and `git` configured for the target project (not the framework).

## Steps

### 1. Clear any pre-existing bundle

```bash
rm -rf .harness/sensors/*.yaml .harness/sensors/*/
```

This removes both top-level platform-primitive sensors and per-usecase folders from the previous schema.

### 2. Populate platform primitives via `/detect-sensors`

Invoke `/detect-sensors` in the target project's Claude Code session. Verify the catalog populates `.harness/sensors/` with at least: `run-project.yaml`, `setup-postgres.yaml` (if Postgres is in stack), `build.yaml`, `lint.yaml`, `type-check.yaml`, `run-all-tests.yaml`.

### 3. Generate a per-usecase bundle via `/create-sensors <usecase>`

Invoke `/create-sensors create-user` (or any usecase id you have). Verify:

- **Plan report** lists applicable and skipped layers with concrete reasons (e.g., "metric: no role=metrics component on stack").
- **Bundle persists** under `.harness/sensors/<usecase-id>/`.
- **Every persisted sensor carries `layer:`**. Quick check:
  ```bash
  for f in .harness/sensors/<usecase-id>/*.yaml; do
    if ! grep -q '^layer:' "$f"; then echo "MISSING layer: $f"; fi
  done
  ```
- **Composite for the `e2e` layer references scenarios via `SensorStep` (`type: sensor`, `ref: ...`)**. Spot-check `e2e-<usecase>.yaml`.

### 4. Validate the bundle via `/validate-usecase <usecase>`

Invoke `/validate-usecase create-user`. Verify:

- **Exit code** reflects the worst observed verdict: `pass` exit 0; otherwise non-zero.
- **`metadata.confidence_report`** is well-formed JSON with the keys `usecase_id`, `computed_at`, `ceiling`, `coverage`, `realized`, `ratios`, `aggregate_verdict`.
- **`ceiling.value`** matches the number of layers reported applicable by the layer matrix against the stack.
- **`coverage.value`** matches the number of unique `sensor.layer` values in the bundle folder.
- **`realized.value`** is the number of layer entrypoints with `verdict=pass` in this invocation.
- **`ratios`** carries 4 numbers (`completeness`, `pass_rate`, `executed_pass_rate`, `confidence`).
- **`aggregate_verdict`** is the worst result observed across entrypoints.

### 5. Re-run idempotence

Invoke `/create-sensors create-user` a second time. Verify the skill detects existing files and skips silently (per Phase 5 "incremental by default" policy). Then re-run `/validate-usecase create-user` — the second invocation must produce the same `aggregate_verdict` as the first, modulo flakey infrastructure dependencies (e.g. ephemeral Postgres).

## Outcome

When all five steps pass on a real project, the layers-of-confidence implementation is considered smoke-verified. Any deviation should be filed as a follow-up issue against the framework, NOT as a fix to this checklist.
