---
name: create-sensor
description: Use when the user invokes /create-sensor or asks to create a single sensor that validates a specific acceptance criterion, functional requirement, or use case. Takes a free-text requirement as input, runs an interactive clarification dialogue, composes existing sensors as dependencies, synthesizes fixtures, and persists one new assertion sensor to <project>/.harness/sensors/<id>.json via the schema validator. Distinct from /detect-sensors, which sweeps the whole project; /create-sensor produces exactly one targeted sensor per invocation.
---

# create-sensor

Take a single requirement (acceptance criterion, functional requirement, use case) as a free-text prompt and produce one targeted assertion sensor that validates it deterministically. Compose existing sensors as dependencies when relevant. Persist the result to `<project>/.harness/sensors/<id>.json` after schema validation.

This skill produces **exactly one sensor per invocation** and only of kind `assertion`. For project-wide bootstrapping or for `observation` / `setup` sensors, refer the user to `/detect-sensors`.

## Invocation

```
/create-sensor "<requirement-as-text>"
```

If the user supplies no requirement-string argument, ask for one in plain prose before proceeding:

> What is the requirement / acceptance criterion / use case you want this sensor to validate? Paste it as free text.

Block until the user replies.

## Procedure

### Phase 1: Parse invocation

Read the user-supplied requirement string into a working draft. Do not start drafting JSON yet — Phase 2's catalog data feeds the draft.

### Phase 2: Catalog existing sensors + read stack

Invoke `catalog-sensors.go` to enumerate the existing sensor inventory:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=catalog_sensors \
  ./skills/create-sensor/scripts \
  --schemas-dir "${CLAUDE_PLUGIN_ROOT}/schemas"
```

Each stdout line is either a sensor digest (`{id, kind, type, output, blocking, description, path}`) or a `verdict=warn` Signal envelope describing a malformed entry that was skipped. Surface the warns to the user inline if any appeared so they know to clean up later, then proceed with the valid digests.

If `<projectRoot>/.harness/stack.json` exists, read it as additional context. The stack's `components[]` and `log_shapes[]` are not consumed by assertion sensors directly, but they help reason about which logger / HTTP framework the project uses when the requirement implies log observation as part of the check. When the file is absent or schema-invalid, ignore it.

### Phase 3: Classify

Decide the three discriminators:

- **`kind`** — always `assertion`. If the user's requirement is shaped like an observation (*"watch the logs while X runs"*) or a setup (*"install dependencies before Y"*), explain the boundary and recommend `/detect-sensors` instead. Do not proceed.
- **`type`** — `computational` by default. Escalate to `inferential` only when no deterministic shell command can verify the requirement (examples: *"the API response should be semantically equivalent to the spec example"*, *"the log line should not contain personally identifiable information"*). Inferential adds a top-level `calibration` block (`confidence_threshold: 0.8`, `calibration_set: ""`, `calibration_size: 0`, `calibration_date: <today>`) plus a `blind_spots[]` entry noting the calibration set is empty pending real samples.
- **`output`** — `stream` when the underlying tool naturally emits one independently-actionable observation per line (multi-record verification, batch tests). `single` otherwise.

### Phase 4: Draft v0

Produce a first-pass JSON in memory containing:

- `id` — kebab-case prefixed `assert-`. Derive from the requirement (e.g. `"POST /users/:id returns 200"` → `assert-post-users-id-200`).
- `version: "0.1.0"`.
- `name`, `description` — one-sentence summaries. The description cites the requirement and notes it was authored via `/create-sensor`.
- `kind: "assertion"`, `type` from Phase 3, `output` from Phase 3.
- `regulation: "behaviour"` for behavioral assertions; `"architecture-fitness"` only when the requirement is structural (file presence, dependency boundary).
- `phase: "on-demand"` by default; `"pre-merge"` when the requirement is gating a PR.
- `determinism: "high"` for computational; `"medium"` for inferential.
- `cost.class` — `cheap` for HTTP probes / file checks; `medium` for multi-step setups; `expensive` for inferential.
- `cost.latency` — sensible p50/p95/timeout for the inferred check.
- `triggers: [{"on": "manual"}]` by default.
- `requires[kind=env]` — one entry per env var the command references (auth tokens, base URLs, target ids). Each entry must have a `name` and a `description`.
- **Deps from the catalog** (the composability heart of this skill):
  - For each sensor in the catalog the LLM judges relevant to the requirement:
    - If its `blocking` field is `true`, encode it as `requires[kind=sensor]` (the orchestrator brings it up live and holds it during this sensor's run).
    - Otherwise, encode it as `depends_on` (the orchestrator runs it to completion before this sensor and propagates failures).
  - This mapping is mechanical: `blocking → requires[kind=sensor]`, `not-blocking → depends_on`. Always apply it consistently.
- `execution.command` — the shell invocation. Prefer commands available in most environments (`curl`, `jq`, `test`, `grep`).
- `execution.exit_code_map` — `[{"exit_code": 0, "verdict": "pass", "severity": "info"}, {"exit_code": "*", "verdict": "fail", "severity": "high"}]` by default. Adjust when the requirement implies severity tiers.
- `execution.output_parsing.patterns` — only when `output: "stream"`. One pattern per actionable verdict; anchor each regex to the kind of line the command emits.
- `verification.golden_cases` — list one case per declared verdict (at minimum `pass`; usually also `fail`). Populate `fixture` paths now (e.g. `.harness/sensors/fixtures/<id>/pass.txt`); the fixture *files* themselves are written in Phase 6.

### Phase 5: Interactive clarification loop

Identify gaps the LLM cannot resolve from the requirement + catalog + stack alone. **Ask one question per turn.** Common gap categories:

| Gap | Sample question |
|---|---|
| Command target | What URL, file path, or process should the check target? If localhost, which port? |
| Auth | Does the check need auth? If yes, which env var holds the token, and what header format does the service expect? |
| Inputs | What inputs does the command need (request body, query params, CLI flags)? Are these stable values, or should we template them via env vars? |
| Expected output | What does success look like in the command's output? What does failure look like? |
| Deps | The catalog lists [X, Y]. Does this sensor need [X] to bring the system up first? |
| Failure modes | Beyond the obvious exit code, are there other failure signals worth catching (timeouts, specific error messages)? |

After each user reply, update the in-memory draft and re-evaluate remaining gaps. Stop when no more gaps remain, or when the user signals *"that's enough, generate it"*.

### Phase 6: Fixture synthesis

Each `golden_case` needs a real fixture file on disk before `write-sensor.go` will persist the JSON.

For each verdict the sensor declares:

1. If the user's requirement or clarification answers contained an explicit sample output for that verdict, use it verbatim as the fixture content.
2. Otherwise, synthesize a plausible fixture from the requirement and the command. Examples:
   - `curl -w '%{http_code}'` → `pass.txt: "200\n"`, `fail-404.txt: "404\n"`.
   - `jq '.status' response.json` → `pass.txt: "\"ok\"\n"`, `fail.txt: "\"degraded\"\n"`.
   - `test -f /var/log/app.log` → `pass.txt: ""`, `fail.txt: "test: /var/log/app.log: No such file or directory\n"`.

Write each fixture via `write-fixture.go`, piping the payload through stdin (POSIX-safe):

```bash
printf '%s' "<payload>" | \
  HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_fixture \
  ./skills/create-sensor/scripts \
  ".harness/sensors/fixtures/<id>/<case>.txt"
```

The output is a Signal envelope on stdout. On `verdict=error`, surface the rationale to the user and stop — do not attempt the final persist with missing fixtures.

### Phase 7: Persist + report

Serialize the draft to a temp file (use `mktemp` or write to `/tmp/create-sensor-draft-<id>.json`), then invoke `write-sensor.go`:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_sensor \
  ./skills/create-sensor/scripts \
  --out ".harness/sensors" \
  --schemas-dir "${CLAUDE_PLUGIN_ROOT}/schemas" \
  /tmp/create-sensor-draft-<id>.json
```

Outcomes:

- **`verdict=pass`** — sensor persisted. Emit a final summary to the user:

  > Created sensor `<id>` at `.harness/sensors/<id>.json`.
  > Dependencies wired: `<dep-id-1>` (via `requires[kind=sensor]`, blocking), `<dep-id-2>` (via `depends_on`).
  > Fixtures: `pass.txt`, `fail-404.txt`.
  > Next: run `/run-sensor <id>` to exercise the sensor.

- **`verdict=error, metadata.kind=schema_invalid`** — the draft failed schema validation. Read the rationale, identify the violated field, patch the draft, re-serialize, and retry the persist. Offer the user *"shall I attempt to fix and retry, or hand the draft over for manual editing?"* before iterating.

- **`verdict=error, metadata.kind=sensor_already_exists`** — the user already has a sensor with this id. Ask the user whether to pick a new id (and re-run Phase 7) or to abort and resolve the collision manually. Do not delete the existing file.

- **`verdict=error, metadata.kind=missing_fixture`** — one of the fixture files Phase 6 was supposed to write is absent. Re-run Phase 6 for the missing file before retrying.

## What this skill does NOT do

- It does not exercise the sensor end-to-end after creation. Run `/run-sensor <id>` yourself to confirm the assertion holds against the current state.
- It does not split multi-AC prompts into multiple sensors. One requirement, one sensor. For multiple ACs, invoke `/create-sensor` once per AC.
- It does not produce `kind: observation` or `kind: setup` sensors. For those, use `/detect-sensors`.
- It does not modify existing sensors. Sensor id collisions are rejected and surfaced to the user.
