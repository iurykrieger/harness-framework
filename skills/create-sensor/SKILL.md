---
name: create-sensor
description: Use when the user invokes /create-sensor or asks to create a single sensor that validates a specific acceptance criterion, functional requirement, or use case. Takes a free-text requirement as input, runs an interactive clarification dialogue, composes existing sensors as dependencies, synthesizes fixtures, and persists one new assertion sensor to <project>/.harness/sensors/<id>.yaml via the schema validator. Distinct from /detect-sensors, which sweeps the whole project; /create-sensor produces exactly one targeted sensor per invocation.
---

# create-sensor

Take a single requirement (acceptance criterion, functional requirement, use case) as a free-text prompt and produce one targeted assertion sensor that validates it deterministically. Compose existing sensors as dependencies when relevant. Persist the result to `<project>/.harness/sensors/<id>.yaml` after schema validation.

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

Read the user-supplied requirement string into a working draft. Do not start drafting YAML yet — Phase 2's catalog data feeds the draft.

### Phase 2: Catalog existing sensors + read stack

Invoke `catalog-sensors.go` to enumerate the existing sensor inventory:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=catalog_sensors \
  ./skills/create-sensor/scripts
```

Each stdout line is either a sensor digest (`{id, kind, type, output, blocking, description, path}`) or a `verdict=warn` Signal envelope describing a malformed entry that was skipped. Surface the warns to the user inline if any appeared so they know to clean up later, then proceed with the valid digests.

If `<projectRoot>/.harness/stack.yaml` exists, load it. The stack's `archetypes[]`, `components[]`, and `log_shapes[]` are **consumed by Phase 4** whenever the assertion's command observes the running service — tails the service's stdout, attaches to a blocking sensor's `signals.log`, scrapes a queue's consumer log, or runs a probe whose response mirrors one of the project's `log_shapes[]`. In that branch Phase 4 derives `output_parsing.patterns` from the relevant shape (re-using the per-event observation rules `/detect-sensors` documents in its §4) rather than inventing a line format. When the assertion's command produces its own stdout (a test runner, `curl`, `jq`, a one-shot probe), the stack is irrelevant and Phase 4 falls back to command-specific regexes. When `.harness/stack.yaml` is absent or schema-invalid, log a warning and proceed in the command-specific-only mode — the sensor is still valid, just without stack-driven patterns.

### Phase 3: Classify

Decide the three discriminators:

- **`kind`** — always `assertion`. If the user's requirement is shaped like an observation (*"watch the logs while X runs"*) or a setup (*"install dependencies before Y"*), explain the boundary and recommend `/detect-sensors` instead. Do not proceed.
- **`type`** — `computational` by default. Escalate to `inferential` only when no deterministic shell command can verify the requirement (examples: *"the API response should be semantically equivalent to the spec example"*, *"the log line should not contain personally identifiable information"*). Inferential sensors require a populated `calibration` block (`confidence_threshold`, `calibration_set` path, `calibration_size ≥ 1`, `calibration_date`). If the user has not provided calibration data, **stop and ask for it** before drafting — do not fabricate placeholder values, since the schema rejects `calibration_size: 0` and empty `calibration_set` paths are not actionable. Also add a `blind_spots[]` entry naming the calibration set's source and freshness.
- **`output`** — `stream` when the underlying tool naturally emits one independently-actionable observation per line (multi-record verification, batch tests). `single` otherwise.

### Phase 4: Draft v0

Produce a first-pass YAML in memory containing:

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
  - For each sensor in the catalog the LLM judges relevant to the requirement, add an entry to `requires[]` of the form `{"kind": "sensor", "id": "<dep-id>"}`. All sensor deps use this single mechanism regardless of whether the dep is blocking; the orchestrator inspects the dep's own `execution.blocking` field at runtime to decide whether to hold it live or run it one-shot before this sensor's command.
- `execution.command` — the shell invocation. Prefer commands available in most environments (`curl`, `jq`, `test`, `grep`).
- `execution.exit_code_map` — `[{"exit_code": 0, "verdict": "pass", "severity": "info"}, {"exit_code": "*", "verdict": "fail", "severity": "high"}]` by default. Adjust when the requirement implies severity tiers.
- `execution.output_parsing.patterns` — only when `output: "stream"`. The drafting strategy depends on what produces those lines:
  - **Command produces its own stdout** (e.g. `curl`, `jq`, `vitest --reporter=tap`, `eslint --format=compact`, a project test runner). Regexes are command-specific. Anchor on the tool's documented output shape and capture the per-record identifier as `excerpt`/`rationale`. The stack is irrelevant.
  - **Command observes the running service** (the assertion's `requires[kind=sensor]` brings up a blocking sensor whose `signals.log` you read; the command tails `.harness/runtime/<id>/<run>/raw.log`; the command runs a `kafka-console-consumer`, `kubectl logs -f`, or similar log scraper; the command fires a probe and reads the service's structured response). The lines have the SHAPE of one of the project's `log_shapes[]` in `.harness/stack.yaml`. Re-use that shape — do NOT invent a new line format. Steps:
    1. Filter `log_shapes[]` to the components whose role the assertion exercises: `http-server`/`http-router`/`http-middleware` for an HTTP assertion; `queue-consumer`/`queue-producer` for a queue assertion; `rpc` for an RPC assertion; `db-client` for a DB assertion. The `stack.archetypes[]` field cross-checks your filter (e.g. an HTTP assertion on a project with `archetypes: [queue-consumer]` is suspicious — confirm with the user during Phase 5).
    2. For the selected shape, follow the per-event regex templates documented in `skills/detect-sensors/SKILL.md` §4 — combined-log-format positional, JSON-key on the literal `fields[].key`, or embedded-msg substring — exactly as `/detect-sensors` would for an observation sensor.
    3. The verdict mapping differs from `/detect-sensors`: an assertion's verdicts are **binary against the requirement**, not graded against service health. Map the per-event outcome that satisfies the requirement (e.g. status `2\d{2}` for *"POST /users returns 2xx"*, `"outcome":"committed"` for *"each event lands in the commit log"*, `grpc.code: OK` for *"all RPC calls succeed"*) → `verdict: pass, severity: info`; map every other outcome the regex can match → `verdict: fail, severity: high`. Do not use `warn` for assertion sensors — the requirement either holds or it does not.
    4. Cite the source shape in the sensor's `description` (e.g. *"output_parsing derived from log_shape 'chi-access-log' in .harness/stack.yaml; verdicts binary against requirement"*) so the audit trail points at the shape when patterns later fail.
  Aim for 1–4 patterns total (one per outcome class that the requirement distinguishes). Anchor every regex on the source shape's `sample` (or the command's documented output) — the regex MUST match the sample, otherwise it is wrong.
- `verification.golden_cases` — list one case per declared verdict (at minimum `pass`; usually also `fail`). Populate `fixture` paths now (e.g. `.harness/sensors/fixtures/<id>/pass.txt`); the fixture *files* themselves are written in Phase 6.

### Phase 5: Interactive clarification loop

Identify gaps the LLM cannot resolve from the requirement + catalog + stack alone. **Ask one question per turn.** Common gap categories:

| Gap | Sample question |
|---|---|
| Command target | What URL, file path, or process should the check target? If localhost, which port? |
| Auth | Does the check need auth? If yes, which env var holds the token, and what header format does the service expect? |
| Inputs | What inputs does the command need (request body, query params, CLI flags)? Are these stable values, or should we template them via env vars? |
| Expected output | What does success look like in the command's output? What does failure look like? |
| Pattern source | Will this assertion's command observe the running service's logs (in which case `output_parsing.patterns` derive from `.harness/stack.yaml`'s `log_shapes[]`), or only the command's own stdout (command-specific regexes)? When stack-driven and the project has multiple shapes, which `log_shapes[].id` should the patterns anchor on? |
| Deps | The catalog lists [X, Y]. Does this sensor need [X] to bring the system up first? |
| Failure modes | Beyond the obvious exit code, are there other failure signals worth catching (timeouts, specific error messages)? |

After each user reply, update the in-memory draft and re-evaluate remaining gaps. Stop when no more gaps remain, or when the user signals *"that's enough, generate it"*.

### Phase 6: Fixture synthesis

Each `golden_case` needs a real fixture file on disk before `write-sensor.go` will persist the sensor.

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

Serialize the draft to a temp file (use `mktemp` or write to `/tmp/create-sensor-draft-<id>.yaml`), then invoke `write-sensor.go`:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_sensor \
  ./skills/create-sensor/scripts \
  --out "${HARNESS_REGISTRY_ROOT}/.harness/sensors" \
  --schemas-dir "${CLAUDE_PLUGIN_ROOT}/schemas" \
  /tmp/create-sensor-draft-<id>.yaml
```

Outcomes:

- **`verdict=pass`** — sensor persisted. The script emits two stdout lines: the JSON Signal envelope first, then the absolute path of the written sensor file on a separate line. Parse the first line as JSON; the second line is a plain string (useful for confirming the write location). Emit a final summary to the user:

  > Created sensor `<id>` at `.harness/sensors/<id>.yaml`.
  > Dependencies wired: `<dep-id-1>` (via `requires[kind=sensor]`, blocking), `<dep-id-2>` (via `requires[kind=sensor]`, one-shot).
  > Fixtures: `pass.txt`, `fail-404.txt`.
  > Next: run `/run-sensor <id>` to exercise the sensor.

- **`verdict=error, metadata.kind=schema_invalid`** — the draft failed schema validation. Read the rationale, identify the violated field, patch the draft, re-serialize, and retry the persist. Offer the user *"shall I attempt to fix and retry, or hand the draft over for manual editing?"* before iterating.

- **`verdict=error, metadata.kind=sensor_already_exists`** — the user already has a sensor with this id. Ask the user whether to pick a new id (and re-run Phase 7) or to abort and resolve the collision manually. Do not delete the existing file.

- **`verdict=error, metadata.kind=missing_fixture`** — one of the fixture files Phase 6 was supposed to write is absent. Re-run Phase 6 for the missing file before retrying.

- **`verdict=error, metadata.kind=read_draft`** — the draft temp file was unreadable or contained invalid YAML/JSON. Re-serialize the in-memory draft to a fresh temp file and retry Phase 7. If the problem persists, surface the error message to the user.

- **`verdict=error, metadata.kind=persist_failed`** — `ValidateAndPersist` failed for a non-schema reason (disk full, missing parent directory, permission denied on `.harness/sensors/`). Surface the rationale to the user verbatim — this is an environment issue the user must resolve; the skill cannot recover automatically.

## What this skill does NOT do

- It does not exercise the sensor end-to-end after creation. Run `/run-sensor <id>` yourself to confirm the assertion holds against the current state.
- It does not split multi-AC prompts into multiple sensors. One requirement, one sensor. For multiple ACs, invoke `/create-sensor` once per AC.
- It does not produce `kind: observation` or `kind: setup` sensors. For those, use `/detect-sensors`.
- It does not modify existing sensors. Sensor id collisions are rejected and surfaced to the user.
