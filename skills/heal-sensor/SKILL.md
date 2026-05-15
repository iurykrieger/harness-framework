---
name: heal-sensor
description: Use when the user invokes /heal-sensor or when the setup-failure-detector hook injects a directive after a /run-sensor setup-shaped failure. Reads the failing Signal + sensor + project state, builds a Setup Plan, applies allowlisted idempotent fixes (cp .env.example .env, mkdir, touch, set-env-in-file with chmod 600), persists patched/new sensors via lib/sensor.ValidateAndPersist, and retries the original sensor exactly once.
---

# heal-sensor

Recover from setup-shaped sensor failures: missing env vars, missing binaries, absent .env files, unavailable services. Run by the calling agent in response to a hook injection (most common) or a manual `/heal-sensor` invocation.

## Invocation

```
/heal-sensor --signal=<path-to-aggregate-signal-json> --sensor=<path-to-failing-sensor-yaml>
```

The hook's `additionalContext` includes both arguments. When invoked manually, ask the user for the failing Signal path (it's the last JSONL line emitted by the most recent `/run-sensor`).

## Procedure

### 1. Diagnose

Run the deterministic input collector:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" \
  ./skills/heal-sensor/scripts/diagnose.go \
  --signal=<signal-path> \
  --sensor=<sensor-path> \
  --root=<project-root> > /tmp/heal-input.json
```

The output contains: the failing Signal verbatim, the sensor body (converted to JSON for the heal-input envelope), the contents of README/CLAUDE/AGENTS/GEMINI/CONTRIBUTING (capped to 16 KB each), and the list of `.example` template files in the tree.

### 2. Build the Setup Plan

Read `/tmp/heal-input.json` and write a Setup Plan to `/tmp/heal-plan.json` that conforms to the contract in `lib/heal/plan.go`:

```json
{
  "diagnosis": {
    "failed_sensor_id": "<id>",
    "shape": "missing-env" | "binary-not-found" | "env-file-absent" | "service-unavailable",
    "evidence_excerpt": "...",
    "root_cause_hint": "..."
  },
  "auto_apply": [
    { "kind": "copy-template", "src": "<absolute path>", "dst": "<absolute path>" },
    { "kind": "set-env-in-file", "file": "<.env path>", "name": "<VAR>", "value_source": "ask-user" },
    { "kind": "mkdir", "dir": "<path under requires[kind=context]>" },
    { "kind": "touch", "file": "<path under requires[kind=context]>" }
  ],
  "propose_only": [
    { "kind": "shell", "command": "<unsafe or non-allowlisted>", "rationale": "..." }
  ],
  "sensor_patches": [
    { "id": "<sensor id>", "patch": { "...full sensor body post-edit..." } }
  ],
  "new_setup_sensors": [
    { "id": "setup-env-from-example-<x>", "json": { "...full new sensor body..." } }
  ]
}
```

Rules for filling in the slots:

- `shape`: pick from the closed enum. Match the rule that fired (the hook's injection message names it).
- `auto_apply[]`: only the four kinds listed above. Anything else (`pnpm install`, `docker compose up`, `gcloud auth login`, custom Makefile targets) goes into `propose_only[]`. The `lib/heal.Apply` allowlist will reject anything else even if you list it.
- `sensor_patches[]`: when the failing sensor would benefit from declaring an additional `requires[kind=env]` entry or wiring a `requires[kind=sensor]` reference to a new setup sensor — emit the patched full sensor object. Don't emit a JSON patch document; emit the new full sensor object. `apply-sensors.go` will run `lib/heal/version.BumpPatch` before persisting.
- `new_setup_sensors[]`: when the project would benefit from a reusable setup sensor (e.g., `setup-env-from-example`) — emit the full new sensor object at version `0.1.0` with `kind: "setup"`.

### 3. Apply file mutations

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" \
  ./skills/heal-sensor/scripts/apply-safe.go \
  --plan=/tmp/heal-plan.json \
  --sensor=<sensor-path> \
  --root=<project-root> > /tmp/heal-apply.json
```

Inspect `/tmp/heal-apply.json`. For each result with `needs_input: true`:

1. Find the matching `auto_apply[]` item (by file/name).
2. Read the failed sensor's `requires[kind=env]` entry for `<NAME>` and its `description` for context.
3. Invoke the `AskUserQuestion` tool synchronously with the description as the question; let the user paste the value.
4. Edit the Plan: set the `value` field on the matching auto_apply item to the user's answer; remove `value_source`.
5. Re-run `apply-safe.go` against the patched Plan — it will write the line via `WriteEnvVar` (chmod 600).

If the user cancels or returns empty: skip step 5 (retry), jump to step 6 (surface remediation) explaining the cancellation.

### 4. Apply sensor mutations

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" \
  ./skills/heal-sensor/scripts/apply-sensors.go \
  --plan=/tmp/heal-plan.json \
  --out=<project-root>/sensors > /tmp/heal-persist.json
```

This validates each `sensor_patches[]` and `new_setup_sensors[]` entry against `schemas/sensor.yaml` via the shared `lib/sensor.ValidateAndPersist`. If any sensor fails validation, the script exits 1 and previously-written entries stay (they were valid).

### 5. Retry exactly once

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" \
  ./skills/heal-sensor/scripts/retry-original.go --sensor=<sensor-path>
```

Pipe its stdout into the response. The retry's aggregate Signal is the LAST JSONL line. If the aggregate's verdict is now `pass` or `warn`, heal succeeded — surface it as the outcome.

### 6. If the retry still fails

Do NOT iterate. Compose a final Signal that:

- Echoes the retry's aggregate as-is, plus
- In `remediation.instructions`, lists everything in the Plan's `propose_only[]` plus any `auto_apply[]` items whose result was `applied: false` AND any cancelled `AskUserQuestion` prompts.

The user (or a future agent turn) decides next steps. The next `/run-sensor` invocation will trigger the hook again if the failure is still setup-shape; the hook's idempotence guard prevents loops within the current turn.

## What heal does NOT do

- Run arbitrary commands extracted from project docs. `pnpm install`, `docker compose up`, `gcloud auth login` are always `propose_only[]`.
- Modify `.gitignore`. The envwriter refuses to write to a path whose ancestor directories don't already gitignore the target.
- Iterate beyond one retry per `/run-sensor` invocation.
- Heal sensor-design failures (regex doesn't match, exit_code_map wrong, fixture mismatch). Those are the responsibility of `/detect-sensors` or manual editing.

## Failure modes

| Symptom | Action |
|---|---|
| `apply-safe` returns `applied: false` for a `copy-template` (dst exists) | Surface in remediation; the user already configured this; nothing to fix |
| `apply-sensors` fails with schema error | Surface validator output verbatim; the Plan was malformed; do NOT retry |
| `retry-original` still fails with the SAME setup-shape | Surface and stop; the Plan didn't address the root cause |
| `retry-original` fails with a DIFFERENT setup-shape | Surface and stop; the next `/run-sensor` invocation will trigger heal again |
| `AskUserQuestion` cancelled | Skip the dependent items; surface them in remediation |
