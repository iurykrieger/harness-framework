---
name: create-sensors
description: Use when the user invokes /create-sensors or asks to author the per-usecase sensor bundle that validates one or more usecases from multiple angles. Resolves the input to one or more usecase ids (by id, journey, path, or free-text match), applies the closed-enum layer matrix in lib/planning/layer/, auto-creates missing platform primitives via lib/planning/coredetect/, and persists each draft to .harness/sensors/<usecase-id>/<sensor-id>.yaml. Distinct from /detect-sensors (sweeps the project for root-tier primitives) and from /validate-usecase (orchestrates the bundle and reports confidence).
---

# create-sensors

Take one or more usecase ids (or a journey id, or a usecase file path, or a free-text requirement) as input and produce, per usecase, the multi-layer bundle of narrow + composite sensors that validates it through every applicable lens. Deterministic Go scripts cover catalog walking, layer application, fixture writing, and persistence; LLM judgment is reserved for synthesizing fixture bodies the recipe cannot infer from evidence alone.

## Invocation

```
/create-sensors [usecase-id | journey-id | path/to/usecase.yaml | "<free text>"]
```

If no argument is supplied, block:

> What is the requirement to cover? Pass a usecase id (`create-user`), a journey id (`users`), a file path, or a free-text requirement.

## Phase 1 — Resolve input

Classify and resolve to one or more usecase ids. The thin index loader (`read-usecases.go` with `--list-only`) supports free-text matching by id+name+tags.

## Phase 2 — Load context

Run:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=read_usecases \
  ./skills/create-sensors/scripts \
  --usecases "<id-1>,<id-2>,..." \
  --include-stack \
  --include-catalog \
  > /tmp/ledger-$(date +%s).json
```

The ledger now includes ALL sensors under `.harness/sensors/` (root-tier + per-usecase folders) via the recursive walk.

## Phase 3 — Plan via layer matrix

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=plan_and_emit \
  ./skills/create-sensors/scripts \
  < /tmp/ledger-<saved-epoch>.json
```

The script emits JSONL: `core_scaffold` entries first (one per missing platform primitive), then `draft` entries (one per planned sensor), then `layer_skipped` entries (with reasons), then a final aggregate envelope.

## Phase 4 — Report plan + confirm

Summarise the plan to the user: per usecase, list the applicable layers (with the number of drafts each emits), the skipped layers (with reasons), and the core scaffolds that will be auto-created. Ask: *"Proceed? (yes/no)"*. Yes/no only — no editing here. If the user wants different scoping, they re-invoke with a narrower input.

## Phase 5 — Synthesize fixtures and persist

For each `core_scaffold` and `draft`, in order:

1. If the layer requires a fixture body the recipe could not infer, prompt the user OR fall back to a documented placeholder; persist via `write-fixture.go`.
2. Persist the draft via `write-sensor.go` with `--out` pointing at:
   - `.harness/sensors/` for `core_scaffold` entries.
   - `.harness/sensors/<usecase-id>/` for layer drafts.

Inferential drafts (`code-quality`, `architecture`, `security`, `dependency-health`) carry default calibration produced by the recipe; do NOT block on a calibration gate.

## Phase 6 — Verify catalog grew correctly

Re-run `catalog-sensors.go`. The catalog should now include every persisted draft plus any auto-created core sensors.

## Phase 7 — Per-usecase report

Print a short summary per usecase:

```
<usecase-id>: <N> sensors across <M> generated layers (out of <K> applicable; skipped layers + reasons).
Next: /validate-usecase <usecase-id>
```

## Policy for an existing usecase folder

- Incremental by default: layers absent from the folder are generated; layers present are skipped silently.
- `--force-layer <name>`: regenerate ONE specific layer (bump `sensor.version`).
- `--regenerate`: delete and regenerate the entire bundle.

## What this skill does NOT do

- Does not exercise sensors after creation — `/validate-usecase` is the next step.
- Does not modify existing sensors — id collisions are surfaced; the user resolves.
- Does not interpret free text as "create a usecase" — Phase 1 free-text matches existing usecases only.
- Does not auto-replay usecases at runtime — `use_cases[]` is declarative traceability.
