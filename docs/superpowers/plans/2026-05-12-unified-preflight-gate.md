# Unified preflight gate implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the blocking-dep preflight gate gap reported in issue #36 by establishing a single invariant — *gate evaluation is inseparable from sensor spawn* — implemented as one canonical helper (`orchestrator.PreflightGate`) routed through by every existing spawn call site, producing a single canonical signal shape (`metadata.kind="failed", cause="preflight_failed"`) across runners.

**Architecture:** A new helper `orchestrator.PreflightGate(s, env, outputMode) → (sig, failed)` lives in `lib/orchestrator/gate.go`. It wraps the existing `sensor.CheckRequiresGate` and the enriched `sensor.BuildRequiresGateSignal` (which gains `cause`, `missing_envs[]`, `missing_tools[]`, `missing_contexts[]` in metadata). Five spawn call sites — `RunOne`, `RunOneWithRoot`, `run-inferential.go`'s direct preflight, `/start-sensor`'s pre-spawn check, and `AttachLiveDep`'s spawn-fresh branch — route through `PreflightGate`. `AttachLiveDep`'s signature changes to return an `AttachResult { Live, GateSignal }` struct so the gate-fail path is observable without conflating with hard errors. The `hooks/setup-failure-detector.go` switch is broadened to recognize `kind="failed"` so preflight signals from `/run-sensor` continue to trigger heal classification.

**Tech Stack:** Go 1.25 standard library, `lib/sensor`, `lib/orchestrator`, `lib/schema`, `lib/signal`, `lib/registry`. No new dependencies.

**Spec:** [docs/superpowers/specs/2026-05-12-unified-preflight-gate-design.md](../specs/2026-05-12-unified-preflight-gate-design.md)

---

## File structure

Files created or modified, grouped by phase. Each file remains focused on one responsibility.

### Phase 1 — Canonical signal shape and helper

| Path | Verb | Responsibility |
|---|---|---|
| `lib/sensor/requires.go` | Modify | `BuildRequiresGateSignal` gains `metadata.cause="preflight_failed"`, `metadata.missing_envs/_tools/_contexts` lists (omitted when empty), and changes `metadata.kind` from `"aggregate"` to `"failed"`. |
| `lib/sensor/requires_test.go` | Modify | Existing shape test updates to new metadata; two new tests for missing_* lists. |
| `lib/orchestrator/gate.go` | Create | `PreflightGate(s, env, outputMode) (sig, failed)` — wraps `CheckRequiresGate` + `BuildRequiresGateSignal`. The single entry point all spawn call sites use. |
| `lib/orchestrator/gate_test.go` | Create | Three test cases: pass returns nil signal, fail returns canonical signal, envelope is preserved verbatim. |

### Phase 2 — Refactor RunOne / RunOneWithRoot / run-inferential

| Path | Verb | Responsibility |
|---|---|---|
| `lib/orchestrator/lifecycle.go` | Modify | `RunOne` and `RunOneWithRoot` replace `RunRequiresGate` + `emitRequiresGateAggregate` calls with one `PreflightGate` call. `emitRequiresGateAggregate` is renamed `emitPreflightSignal` and slimmed (only validates + emits; construction moves to `PreflightGate`). |
| `lib/orchestrator/lifecycle_test.go` | Modify | Update any assertion of `metadata.kind == "aggregate"` on the Phase-0-fail path. |
| `skills/run-sensor/scripts/run-inferential.go` | Modify | Lines 225-232: replace `CheckRequiredEnv` + `BuildMissingEnvSignal` with `orchestrator.PreflightGate`. Gains tool/context gating for inferential sensors. |
| `skills/run-sensor/scripts/run-inferential_test.go` | Modify | Update assertions for the new shape. |

### Phase 3 — Refactor /start-sensor

| Path | Verb | Responsibility |
|---|---|---|
| `skills/start-sensor/scripts/start.go` | Modify | Lines 137-142: switch to `orchestrator.PreflightGate`. Lines 410-475: delete `requiresGateAux` and `requiresGateRationale`. Merge `b.Diagnose` into the returned signal's metadata after the helper returns. |
| `skills/start-sensor/scripts/start_test.go` | Modify | Update assertions to expect the canonical signal (rich per-failure `evidence[]`, `remediation.instructions` populated, preserved `metadata.diagnose`). |

### Phase 4 — Hook switch broadening

| Path | Verb | Responsibility |
|---|---|---|
| `hooks/setup-failure-detector.go` | Modify | Line 173: broaden `case "aggregate", "start_failed":` to `case "aggregate", "start_failed", "failed":`. Rewrite the stale comment block (lines 174-177). |
| `hooks/setup-failure-detector_test.go` | Modify | Add a test case for `kind="failed", cause="preflight_failed"` signal classification. |

### Phase 5 — AttachLiveDep signature change + gate inside spawn-fresh

| Path | Verb | Responsibility |
|---|---|---|
| `lib/orchestrator/live_deps.go` | Modify | Define `AttachResult { Live LiveDep, GateSignal map[string]interface{} }`. Change `AttachLiveDep` return type to `(AttachResult, error)`. Inside the file-lock callback, in the branch that decides to spawn-fresh, call `orchestrator.PreflightGate`; on fail, set `gateSignal` and skip `startBlockingDep`. |
| `lib/orchestrator/live_deps_test.go` | Modify | Adapt existing tests to the new return type. Add `TestAttachLiveDep_SpawnFreshGateFails_ReturnsGateSignalNoSpawn` and `TestAttachLiveDep_ReattachToLiveDep_DoesNotGate`. |
| `lib/orchestrator/preflight.go` | Modify | `RunDeps` line 90: adapt to `AttachResult` return; on `result.GateSignal != nil`, emit signal, record in `res.Signals[s.ID]`, `continue`. |
| `lib/orchestrator/preflight_test.go` | Modify | Existing `TestRunDeps_BlockingDepStartFresh` adapts to new return type. Add `TestRunDeps_BlockingDepGateFails_EmitsPreflightSignalAndCascadesRoot` which closes #36. |
| `skills/run-sensor/scripts/run-inferential.go` | Modify | Line 181: adapt to `AttachResult` return. |

### Phase 6 — Remove obsoleted code + migrate fixtures

| Path | Verb | Responsibility |
|---|---|---|
| `lib/orchestrator/preflight.go` | Modify | Remove `RunRequiresGate` (lines 142-157). |
| `lib/sensor/env.go` | Modify | Remove `BuildMissingEnvSignal`, `CheckRequiredEnv`, `MissingEnv`. File may become empty / removed entirely. |
| `lib/sensor/env_test.go` | Modify | Migrate useful assertions into `requires_test.go`; delete file or empty it. |
| `lib/heal/rules/missing_env_test.go` | Modify | Replace `sensor.BuildMissingEnvSignal` fixture builder with `sensor.CheckRequiresGate` + `sensor.BuildRequiresGateSignal`. |

### Phase 7 — Static invariant test

| Path | Verb | Responsibility |
|---|---|---|
| `lib/orchestrator/gate_invariant_test.go` | Create | Grep-scans the repo for calls to `subprocess.{StreamSubprocess,Start,SpawnDetached}` and verifies each call site's file mentions `orchestrator.PreflightGate` (or is allowlisted). |

### Phase 8 — Documentation

| Path | Verb | Responsibility |
|---|---|---|
| `CLAUDE.md` | Modify | Add Project rule #11 ("Spawn de sensor é gated, sem exceção"). Add gate paragraph to the "Dependencies and lifecycle" subsection. |
| `CHANGELOG.md` (if present) | Modify | Record `metadata.kind` change for `/run-sensor` preflight failures (`"aggregate"` → `"failed"`); recommend reading `metadata.cause`. |

### Phase 9 — Final validation

No file changes. Test matrix + manual e2e against issue #36 reproduction.

---

## Task 1: Enrich BuildRequiresGateSignal

**Files:**
- Modify: `lib/sensor/requires.go:152-191` (the helper)
- Modify: `lib/sensor/requires_test.go` (existing shape tests)

- [ ] **Step 1: Update existing shape test to new contract**

Open `lib/sensor/requires_test.go`. Locate `TestBuildRequiresGateSignal_Shape` (currently around line 327). Replace its assertions:

```go
func TestBuildRequiresGateSignal_Shape(t *testing.T) {
	env := Envelope{SensorID: "demo", Version: "1.2.3", RunID: "r-1", StartedAt: "2026-05-12T00:00:00Z"}
	gate := Gate{Failures: []Failure{
		{Kind: "env", Identifier: "FOO", Rationale: "Required environment variable FOO is not set", HealShape: "missing-env"},
		{Kind: "tool", Identifier: "redis-cli", Rationale: `Required tool "redis-cli" is not on PATH`, HealShape: "binary-not-found"},
	}}

	sig := BuildRequiresGateSignal(env, "stream", gate)

	if sig["sensor_id"] != "demo" {
		t.Errorf("sensor_id = %v, want demo", sig["sensor_id"])
	}
	if sig["verdict"] != "error" {
		t.Errorf("verdict = %v, want error", sig["verdict"])
	}
	if sig["severity"] != "high" {
		t.Errorf("severity = %v, want high", sig["severity"])
	}

	md, ok := sig["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata is not a map: %T", sig["metadata"])
	}
	if md["kind"] != "failed" {
		t.Errorf("metadata.kind = %v, want failed", md["kind"])
	}
	if md["cause"] != "preflight_failed" {
		t.Errorf("metadata.cause = %v, want preflight_failed", md["cause"])
	}
	if md["output_mode"] != "stream" {
		t.Errorf("metadata.output_mode = %v, want stream", md["output_mode"])
	}
	if md["heal_hint"] != "missing-env:FOO" {
		t.Errorf("metadata.heal_hint = %v, want missing-env:FOO", md["heal_hint"])
	}

	envs, ok := md["missing_envs"].([]interface{})
	if !ok || len(envs) != 1 || envs[0] != "FOO" {
		t.Errorf("metadata.missing_envs = %v, want [FOO]", md["missing_envs"])
	}
	tools, ok := md["missing_tools"].([]interface{})
	if !ok || len(tools) != 1 || tools[0] != "redis-cli" {
		t.Errorf("metadata.missing_tools = %v, want [redis-cli]", md["missing_tools"])
	}
	if _, present := md["missing_contexts"]; present {
		t.Errorf("metadata.missing_contexts should be omitted when empty, got %v", md["missing_contexts"])
	}

	ev, ok := sig["evidence"].([]interface{})
	if !ok || len(ev) != 2 {
		t.Fatalf("evidence: got %v (%T), want length 2", sig["evidence"], sig["evidence"])
	}
}
```

- [ ] **Step 2: Add a test that omits all empty missing_* lists**

Append to `lib/sensor/requires_test.go`:

```go
func TestBuildRequiresGateSignal_EnvOnly_OmitsToolAndContextLists(t *testing.T) {
	env := Envelope{SensorID: "demo", Version: "1.0.0", RunID: "r-1", StartedAt: "2026-05-12T00:00:00Z"}
	gate := Gate{Failures: []Failure{
		{Kind: "env", Identifier: "ONLY_ENV", Rationale: "Required environment variable ONLY_ENV is not set", HealShape: "missing-env"},
	}}

	sig := BuildRequiresGateSignal(env, "single", gate)
	md := sig["metadata"].(map[string]interface{})

	if _, present := md["missing_tools"]; present {
		t.Errorf("metadata.missing_tools should be omitted when empty, got %v", md["missing_tools"])
	}
	if _, present := md["missing_contexts"]; present {
		t.Errorf("metadata.missing_contexts should be omitted when empty, got %v", md["missing_contexts"])
	}
	if envs := md["missing_envs"].([]interface{}); len(envs) != 1 {
		t.Errorf("metadata.missing_envs = %v, want [ONLY_ENV]", envs)
	}
}
```

- [ ] **Step 3: Add a test that populates all three lists**

Append to `lib/sensor/requires_test.go`:

```go
func TestBuildRequiresGateSignal_AllKinds_PopulatesAllMissingLists(t *testing.T) {
	env := Envelope{SensorID: "demo", Version: "1.0.0", RunID: "r-1", StartedAt: "2026-05-12T00:00:00Z"}
	gate := Gate{Failures: []Failure{
		{Kind: "env", Identifier: "E1", Rationale: "Required environment variable E1 is not set", HealShape: "missing-env"},
		{Kind: "tool", Identifier: "T1", Rationale: `Required tool "T1" is not on PATH`, HealShape: "binary-not-found"},
		{Kind: "context", Identifier: "/missing/path", Rationale: `Required context path "/missing/path" does not exist`, HealShape: "missing-context"},
	}}

	sig := BuildRequiresGateSignal(env, "stream", gate)
	md := sig["metadata"].(map[string]interface{})

	if envs := md["missing_envs"].([]interface{}); len(envs) != 1 || envs[0] != "E1" {
		t.Errorf("metadata.missing_envs = %v, want [E1]", envs)
	}
	if tools := md["missing_tools"].([]interface{}); len(tools) != 1 || tools[0] != "T1" {
		t.Errorf("metadata.missing_tools = %v, want [T1]", tools)
	}
	if ctxs := md["missing_contexts"].([]interface{}); len(ctxs) != 1 || ctxs[0] != "/missing/path" {
		t.Errorf("metadata.missing_contexts = %v, want [/missing/path]", ctxs)
	}
}
```

- [ ] **Step 4: Update the existing empty-gate test if it asserts the old shape**

Locate `TestBuildRequiresGateSignal_EmptyGate` in `lib/sensor/requires_test.go` (around line 383). It currently builds a signal from an empty `Gate`. The new shape still produces `metadata.kind="failed", cause="preflight_failed"` for an empty gate (the helper does not branch on gate-empty — callers don't call it when the gate passes; the test exists to lock the no-failure shape). Update asserts to match: `metadata.kind == "failed"`, `metadata.cause == "preflight_failed"`, no `missing_envs/_tools/_contexts` keys present.

- [ ] **Step 5: Run failing tests**

Run: `go test ./lib/sensor/ -run 'TestBuildRequiresGateSignal' -v`
Expected: FAIL — current implementation emits `metadata.kind="aggregate"`, no `cause` or `missing_*` keys.

- [ ] **Step 6: Implement the enriched helper**

Open `lib/sensor/requires.go`. Replace the `BuildRequiresGateSignal` function (lines 152-191) with:

```go
// BuildRequiresGateSignal constructs the canonical verdict=error Signal emitted
// when CheckRequiresGate returns a non-empty Gate. Shape is identical across
// every caller (RunOne, RunOneWithRoot, /start-sensor, RunDeps blocking branch,
// run-inferential.go) — see docs/superpowers/specs/2026-05-12-unified-preflight-gate-design.md.
//
// The Signal carries one evidence entry per Failure, a metadata.heal_hint
// shaped from the FIRST failure (drives /heal-sensor routing), per-kind
// machine-readable lists in metadata.missing_envs / .missing_tools /
// .missing_contexts (each omitted when its list is empty), and an aggregate
// remediation.instructions string listing every failure.
func BuildRequiresGateSignal(env Envelope, outputMode string, gate Gate) map[string]interface{} {
	finished := NowFn().Format("2006-01-02T15:04:05Z")
	evidence := make([]interface{}, 0, len(gate.Failures))
	var envs, tools, contexts []interface{}
	for _, f := range gate.Failures {
		evidence = append(evidence, map[string]interface{}{"rationale": f.Rationale})
		switch f.Kind {
		case "env":
			envs = append(envs, f.Identifier)
		case "tool":
			tools = append(tools, f.Identifier)
		case "context":
			contexts = append(contexts, f.Identifier)
		}
	}

	md := map[string]interface{}{
		"kind":        "failed",
		"cause":       "preflight_failed",
		"output_mode": outputMode,
	}
	if len(gate.Failures) > 0 {
		first := gate.Failures[0]
		md["heal_hint"] = first.HealShape + ":" + first.Identifier
	}
	if len(envs) > 0 {
		md["missing_envs"] = envs
	}
	if len(tools) > 0 {
		md["missing_tools"] = tools
	}
	if len(contexts) > 0 {
		md["missing_contexts"] = contexts
	}

	sig := map[string]interface{}{
		"sensor_id":   env.SensorID,
		"version":     env.Version,
		"run_id":      env.RunID,
		"started_at":  env.StartedAt,
		"finished_at": finished,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    evidence,
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    md,
	}
	if rem := buildRequiresGateRemediation(gate); rem != "" {
		sig["remediation"] = map[string]interface{}{"instructions": rem}
	}
	return sig
}
```

- [ ] **Step 7: Run tests to verify pass**

Run: `go test ./lib/sensor/ -run 'TestBuildRequiresGateSignal' -v`
Expected: PASS for all four tests.

- [ ] **Step 8: Run full sensor package tests**

Run: `go test ./lib/sensor/`
Expected: PASS. If `env_test.go::TestBuildMissingEnvSignal_*` fails (it calls the wrapper which calls into the helper), don't fix here — Task 10 removes those.

- [ ] **Step 9: Commit**

```bash
git add lib/sensor/requires.go lib/sensor/requires_test.go
git commit -m "$(cat <<'EOF'
feat(sensor): enrich BuildRequiresGateSignal with cause + missing_* lists

Canonical preflight-failed signal shape now carries:
- metadata.kind="failed" (was "aggregate")
- metadata.cause="preflight_failed"
- metadata.missing_envs/missing_tools/missing_contexts (omitted when empty)

Foundation for closing #36 — every spawn call site will route through this
helper via a new orchestrator.PreflightGate wrapper.
EOF
)"
```

---

## Task 2: Create PreflightGate helper

**Files:**
- Create: `lib/orchestrator/gate.go`
- Create: `lib/orchestrator/gate_test.go`

- [ ] **Step 1: Write the gate_test.go with three failing tests**

Create `lib/orchestrator/gate_test.go`:

```go
package orchestrator_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestPreflightGate_PassReturnsNilSignal(t *testing.T) {
	s := orchestrator.Sensor{
		ID: "ok",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{"command": "true"},
		},
	}
	env := sensor.Envelope{SensorID: "ok", Version: "1.0.0", RunID: "r-1", StartedAt: "2026-05-12T00:00:00Z"}

	sig, failed := orchestrator.PreflightGate(s, env, "single")
	if failed {
		t.Errorf("failed: got true, want false (no requires[])")
	}
	if sig != nil {
		t.Errorf("sig: got %v, want nil", sig)
	}
}

func TestPreflightGate_FailReturnsCanonicalSignal(t *testing.T) {
	t.Setenv("__PREFLIGHT_GATE_TEST_UNSET__", "")
	// Use a name we are certain is unset.
	const envName = "__HARNESS_PREFLIGHT_GATE_NEVER_SET__"
	s := orchestrator.Sensor{
		ID: "needs-env",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{"command": "true"},
			"requires": []interface{}{
				map[string]interface{}{"kind": "env", "name": envName},
			},
		},
	}
	env := sensor.Envelope{SensorID: "needs-env", Version: "1.0.0", RunID: "r-2", StartedAt: "2026-05-12T00:00:00Z"}

	sig, failed := orchestrator.PreflightGate(s, env, "single")
	if !failed {
		t.Fatalf("failed: got false, want true (env %s is unset)", envName)
	}
	if sig == nil {
		t.Fatal("sig: got nil, want non-nil")
	}
	if sig["verdict"] != "error" {
		t.Errorf("verdict: got %v, want error", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "failed" {
		t.Errorf("metadata.kind: got %v, want failed", md["kind"])
	}
	if md["cause"] != "preflight_failed" {
		t.Errorf("metadata.cause: got %v, want preflight_failed", md["cause"])
	}
	envs := md["missing_envs"].([]interface{})
	if len(envs) != 1 || envs[0] != envName {
		t.Errorf("metadata.missing_envs: got %v, want [%s]", envs, envName)
	}
	if md["heal_hint"] != "missing-env:"+envName {
		t.Errorf("metadata.heal_hint: got %v, want missing-env:%s", md["heal_hint"], envName)
	}
}

func TestPreflightGate_UsesProvidedEnvelope(t *testing.T) {
	const envName = "__HARNESS_PREFLIGHT_GATE_NEVER_SET_2__"
	s := orchestrator.Sensor{
		ID: "ignored-in-envelope",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{"command": "true"},
			"requires": []interface{}{
				map[string]interface{}{"kind": "env", "name": envName},
			},
		},
	}
	env := sensor.Envelope{
		SensorID:  "caller-chose-this",
		Version:   "9.9.9",
		RunID:     "r-from-caller",
		StartedAt: "2099-12-31T23:59:59Z",
	}

	sig, _ := orchestrator.PreflightGate(s, env, "stream")
	if sig["sensor_id"] != "caller-chose-this" {
		t.Errorf("sensor_id: got %v, want caller-chose-this (helper must not re-derive)", sig["sensor_id"])
	}
	if sig["version"] != "9.9.9" {
		t.Errorf("version: got %v, want 9.9.9", sig["version"])
	}
	if sig["run_id"] != "r-from-caller" {
		t.Errorf("run_id: got %v, want r-from-caller", sig["run_id"])
	}
	if sig["started_at"] != "2099-12-31T23:59:59Z" {
		t.Errorf("started_at: got %v, want 2099-12-31T23:59:59Z", sig["started_at"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails (file not yet created)**

Run: `go test ./lib/orchestrator/ -run 'TestPreflightGate' -v`
Expected: FAIL with `undefined: orchestrator.PreflightGate`.

- [ ] **Step 3: Create gate.go**

Create `lib/orchestrator/gate.go`:

```go
package orchestrator

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// PreflightGate evaluates the requires[kind ∈ {tool, context, env}] gate for s
// and returns the canonical preflight-failed Signal when any precondition is
// unmet. It is the single entry point that every sensor-spawn call site MUST
// use before invoking subprocess.{StreamSubprocess, Start, SpawnDetached} with
// the sensor's execution.command.
//
// Returns:
//
//	sig=nil,  failed=false → gate passed; caller may spawn.
//	sig!=nil, failed=true  → caller emits sig and aborts spawn. The signal
//	                         carries verdict=error, metadata.kind="failed",
//	                         metadata.cause="preflight_failed", and machine-
//	                         readable missing_envs / missing_tools /
//	                         missing_contexts lists (omitted when empty).
//
// The Envelope is supplied by the caller (not constructed here): for a runtime
// sensor execution it is the same envelope used for the eventual aggregate
// signal; for /start-sensor it is built from the target sensor before the
// detach spawn; for AttachLiveDep's spawn-fresh branch it is built from the
// dep sensor immediately before startBlockingDep is called.
func PreflightGate(s Sensor, env sensor.Envelope, outputMode string) (sig map[string]interface{}, failed bool) {
	gate := sensor.CheckRequiresGate(s.JSON, sensor.GateOpts{LookupEnv: sensor.LookupEnvFn})
	if !gate.Failed() {
		return nil, false
	}
	return sensor.BuildRequiresGateSignal(env, outputMode, gate), true
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./lib/orchestrator/ -run 'TestPreflightGate' -v`
Expected: PASS for all three tests.

- [ ] **Step 5: Commit**

```bash
git add lib/orchestrator/gate.go lib/orchestrator/gate_test.go
git commit -m "feat(orchestrator): add PreflightGate canonical helper"
```

---

## Task 3: Switch RunOne / RunOneWithRoot to PreflightGate

**Files:**
- Modify: `lib/orchestrator/lifecycle.go:62-64, 246-248, 688-702`
- Modify: `lib/orchestrator/lifecycle_test.go` (kind assertions)

- [ ] **Step 1: Find lifecycle_test assertions that match the old kind**

Run: `grep -n '"aggregate"' lib/orchestrator/lifecycle_test.go`

For each match: if the surrounding test exercises the Phase 0 gate path (sensor with `requires[]` and a deliberately unset env), update the expected `kind` to `"failed"`. Also expect `metadata.cause == "preflight_failed"` on these tests.

For matches on the runtime-execution path (sensor that actually runs a command), leave them alone — those still emit `kind="aggregate"`.

If no Phase-0-fail test exists, no changes are needed here.

- [ ] **Step 2: Replace RunOne Phase 0 (lifecycle.go:62-64)**

Open `lib/orchestrator/lifecycle.go`. Locate:

```go
	if gate := RunRequiresGate(s); gate.Failed() {
		return emitRequiresGateAggregate(envelope, output, gate, v, stdout, stderr)
	}
```

Replace with:

```go
	if sig, failed := PreflightGate(s, envelope, output); failed {
		return emitPreflightSignal(sig, v, stdout, stderr)
	}
```

- [ ] **Step 3: Replace RunOneWithRoot Phase 0 (lifecycle.go:246-248)**

In the same file, locate the identical block inside `runOneWithPersistence` (around line 246):

```go
	if gate := RunRequiresGate(s); gate.Failed() {
		return emitRequiresGateAggregate(envelope, output, gate, v, stdout, stderr)
	}
```

Replace with:

```go
	if sig, failed := PreflightGate(s, envelope, output); failed {
		return emitPreflightSignal(sig, v, stdout, stderr)
	}
```

- [ ] **Step 4: Replace emitRequiresGateAggregate with emitPreflightSignal**

In the same file, locate the `emitRequiresGateAggregate` function (around lines 688-702):

```go
// emitRequiresGateAggregate writes the verdict=error aggregate Signal that
// /run-sensor (RunOne and runOneWithPersistence) emits when the gate fails.
// Returns (signal, 0) on success or (nil, 1) when schema validation rejects
// the signal — both paths leave stderr informative.
func emitRequiresGateAggregate(envelope sensor.Envelope, output string, gate sensor.Gate, v *schema.Validator, stdout, stderr io.Writer) (map[string]interface{}, int) {
	sig := sensor.BuildRequiresGateSignal(envelope, output, gate)
	if v != nil {
		if err := v.Validate(schema.TargetSignal, sig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return nil, 1
		}
	}
	_ = json.NewEncoder(stdout).Encode(sig)
	return sig, 0
}
```

Replace the entire function with:

```go
// emitPreflightSignal validates and emits the canonical preflight-failed
// Signal produced by PreflightGate. Returns (signal, 0) on success or
// (nil, 1) when schema validation rejects the signal — both paths leave
// stderr informative.
func emitPreflightSignal(sig map[string]interface{}, v *schema.Validator, stdout, stderr io.Writer) (map[string]interface{}, int) {
	if v != nil {
		if err := v.Validate(schema.TargetSignal, sig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return nil, 1
		}
	}
	_ = json.NewEncoder(stdout).Encode(sig)
	return sig, 0
}
```

- [ ] **Step 5: Drop unused imports if necessary**

If `lib/sensor` was only imported for `sensor.Gate` or `sensor.BuildRequiresGateSignal` in `emitRequiresGateAggregate`, verify the import is still used elsewhere in the file (it is — e.g., `sensor.BuildEnvelope`, `sensor.NowFn`). No import changes expected.

- [ ] **Step 6: Run lifecycle tests**

Run: `go test ./lib/orchestrator/ -run 'TestRunOne' -v`
Expected: PASS. If any Phase 0 test fails because of unchanged assertions from Step 1, fix the assertion and rerun.

- [ ] **Step 7: Run full orchestrator tests**

Run: `go test ./lib/orchestrator/`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add lib/orchestrator/lifecycle.go lib/orchestrator/lifecycle_test.go
git commit -m "$(cat <<'EOF'
refactor(orchestrator): RunOne uses PreflightGate canonical helper

emitRequiresGateAggregate becomes emitPreflightSignal — construction moves
to PreflightGate; this helper now only validates and emits.
EOF
)"
```

---

## Task 4: Refactor run-inferential.go

**Files:**
- Modify: `skills/run-sensor/scripts/run-inferential.go:225-232`
- Modify: `skills/run-sensor/scripts/run-inferential_test.go`

- [ ] **Step 1: Locate inferential tests that assert the old shape**

Run: `grep -n '"kind"\|missing_envs\|BuildMissingEnvSignal\|CheckRequiredEnv' skills/run-sensor/scripts/run-inferential_test.go`

For each test asserting `metadata.kind` or evidence on the preflight path, update assertions to match the new canonical shape:
- `metadata.kind == "failed"` (was missing or wrong)
- `metadata.cause == "preflight_failed"`
- `metadata.missing_envs == [...]`
- `evidence[]` is per-failure rationale

If the existing tests do not exercise the preflight path, no changes here.

- [ ] **Step 2: Replace the env-only preflight call**

Open `skills/run-sensor/scripts/run-inferential.go`. Locate lines 219-232:

```go
	envelope, err := sensor.BuildEnvelope(sensorJSON)
	if err != nil {
		fmt.Fprintln(stderr, "error: envelope:", err)
		return 2
	}

	if missing := sensor.CheckRequiredEnv(sensorJSON); len(missing) > 0 {
		sig := sensor.BuildMissingEnvSignal(envelope, output, missing)
		if err := v.Validate(schema.TargetSignal, sig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(sig)
		return 0
	}
```

Replace with:

```go
	envelope, err := sensor.BuildEnvelope(sensorJSON)
	if err != nil {
		fmt.Fprintln(stderr, "error: envelope:", err)
		return 2
	}

	// Build a transient Sensor wrapper so PreflightGate can consume our raw
	// sensorJSON. The id and path are recorded for diagnose-friendliness; the
	// helper only reads JSON.
	target := orchestrator.Sensor{ID: requested.ID, Path: requested.Path, JSON: sensorJSON}
	if sig, failed := orchestrator.PreflightGate(target, envelope, output); failed {
		if err := v.Validate(schema.TargetSignal, sig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(sig)
		return 0
	}
```

Note: this assumes `requested` is the variable already in scope holding the resolved sensor (verify by reading the function context around line 200-225). If it isn't, use the ID and path local variables present in this function — search for where `sensorJSON` is loaded.

- [ ] **Step 3: Verify imports**

The file already imports `lib/orchestrator` (it calls `orchestrator.AttachLiveDep` at line 181) and `lib/sensor`. No new imports needed.

- [ ] **Step 4: Run inferential tests**

Run: `go test -tags=run_inferential ./skills/run-sensor/scripts/ -v`
Expected: PASS.

- [ ] **Step 5: Build verification**

Run: `go build -tags=run_inferential ./skills/run-sensor/scripts/`
Expected: clean build.

- [ ] **Step 6: Commit**

```bash
git add skills/run-sensor/scripts/run-inferential.go skills/run-sensor/scripts/run-inferential_test.go
git commit -m "$(cat <<'EOF'
refactor(run-inferential): switch from CheckRequiredEnv to PreflightGate

Inferential sensors now gate on tool/context preconditions in addition to
env, matching the computational runner. Drops the env-only fast path.
EOF
)"
```

---

## Task 5: Refactor /start-sensor

**Files:**
- Modify: `skills/start-sensor/scripts/start.go:137-142, 410-475`
- Modify: `skills/start-sensor/scripts/start_test.go`

- [ ] **Step 1: Locate start_test assertions for the old gate shape**

Run: `grep -n '"missing_envs"\|"missing_tools"\|"missing_contexts"\|requiresGateRationale' skills/start-sensor/scripts/start_test.go`

Update assertions to expect the canonical shape:
- `metadata.kind == "failed"` (unchanged)
- `metadata.cause == "preflight_failed"` (unchanged)
- `metadata.missing_envs/_tools/_contexts` — omitted when empty
- `evidence[]` is per-failure rich rationale (was single coarse summary; tests that match `evidence[0]["rationale"]` to the old "required preconditions unmet: N env var(s)" string must update to match e.g. the actual `"Required environment variable FOO is not set"` text).

- [ ] **Step 2: Replace the gate block (start.go:133-142)**

Open `skills/start-sensor/scripts/start.go`. Locate:

```go
	// Phase 0: requires[] gate — mirrors RunOne's Phase 0 so /start-sensor
	// refuses to spawn when env/tool/context preconditions are unmet. The
	// failure carries the missing names in metadata so /heal-sensor can
	// act on them (e.g. copy .env.example, install missing tool).
	if gate := orchestrator.RunRequiresGate(target); gate.Failed() {
		detachAll()
		return 1, signal.ValidateOrEmergency(v, startSignal(id, sensorJSON, "failed", "preflight_failed",
			requiresGateRationale(gate),
			requiresGateAux(gate), b.Diagnose), id, os.Stderr)
	}
```

Replace with:

```go
	// Phase 0: requires[] gate. The canonical signal produced by PreflightGate
	// already carries metadata.kind="failed", cause="preflight_failed", and
	// the machine-readable missing_envs/_tools/_contexts lists. We merge
	// /start-sensor's Diagnose block into metadata after the helper returns
	// (PreflightGate is caller-agnostic and does not know about Diagnose).
	targetEnv, eerr := libsensor.BuildEnvelope(target.JSON)
	if eerr != nil {
		detachAll()
		return 2, signal.ValidateOrEmergency(v, startSignal(id, sensorJSON, "failed", "envelope_failed",
			fmt.Sprintf("build envelope: %v", eerr), nil, b.Diagnose), id, os.Stderr)
	}
	targetOutput, _ := target.JSON["output"].(string)
	if sig, failed := orchestrator.PreflightGate(target, targetEnv, targetOutput); failed {
		md := sig["metadata"].(map[string]interface{})
		if b.Diagnose != nil {
			md["diagnose"] = b.Diagnose
		}
		detachAll()
		return 1, signal.ValidateOrEmergency(v, sig, id, os.Stderr)
	}
```

- [ ] **Step 3: Delete requiresGateAux and requiresGateRationale**

In the same file, locate lines 410-475 (`requiresGateAux` and `requiresGateRationale`). Delete both functions in their entirety, including the doc comments. The grep below confirms no other code in this file calls them:

Run: `grep -n 'requiresGateAux\|requiresGateRationale' skills/start-sensor/scripts/start.go`
Expected: only the lines being deleted appear.

- [ ] **Step 4: Verify imports**

The file already imports `lib/orchestrator` (uses `orchestrator.RunRequiresGate` today, becomes `orchestrator.PreflightGate`) and `libsensor` (alias for `lib/sensor`, already used for `BuildEnvelope`). No new imports.

- [ ] **Step 5: Run start-sensor tests**

Run: `go test -tags=start_sensor ./skills/start-sensor/scripts/ -v`
Expected: PASS. Adjust test assertions per Step 1 if any fail.

- [ ] **Step 6: Build verification**

Run: `go build -tags=start_sensor ./skills/start-sensor/scripts/`
Expected: clean build.

- [ ] **Step 7: Commit**

```bash
git add skills/start-sensor/scripts/start.go skills/start-sensor/scripts/start_test.go
git commit -m "$(cat <<'EOF'
refactor(start-sensor): use PreflightGate canonical helper

Drops ~55 LoC (requiresGateAux + requiresGateRationale) — the canonical
helper produces machine-readable missing_envs/missing_tools/missing_contexts
lists and per-failure evidence directly. Diagnose merges into the signal's
metadata post-construction so PreflightGate stays caller-agnostic.
EOF
)"
```

---

## Task 6: Broaden setup-failure-detector switch

**Files:**
- Modify: `hooks/setup-failure-detector.go:172-196`
- Modify: `hooks/setup-failure-detector_test.go`

- [ ] **Step 1: Write a failing test for kind="failed"**

Open `hooks/setup-failure-detector_test.go`. Find the test fixture pattern used for `kind="aggregate"` scenarios. Add a new test:

```go
func TestScanTranscript_RoutesKindFailedPreflight(t *testing.T) {
	// Build a transcript with /run-sensor producing a preflight-failed signal
	// (metadata.kind="failed", cause="preflight_failed"). The hook should
	// recognize it as a heal candidate.
	transcript := buildTranscriptWithSignal(t, map[string]interface{}{
		"sensor_id": "demo",
		"verdict":   "error",
		"severity":  "high",
		"metadata": map[string]interface{}{
			"kind":         "failed",
			"cause":        "preflight_failed",
			"output_mode":  "single",
			"missing_envs": []interface{}{"FOO"},
			"heal_hint":    "missing-env:FOO",
		},
	}, "/run-sensor demo")

	result, ok := scanTranscript(transcript, "/tmp/project")
	if !ok {
		t.Fatal("scanTranscript: returned ok=false, want true")
	}
	if result.Signal.Verdict != "error" {
		t.Errorf("signal verdict: got %v, want error", result.Signal.Verdict)
	}
	// Heal hint should be available for classification downstream.
	if hint := signalHealHint(result.Signal); hint != "missing-env:FOO" {
		t.Errorf("heal_hint: got %v, want missing-env:FOO", hint)
	}
}
```

Note: `buildTranscriptWithSignal` and `signalHealHint` are helper names; reuse whatever the file already names them. Inspect the existing tests in `setup-failure-detector_test.go` to find the actual builder signature and adapt the test body accordingly.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=error_autofiler ./hooks/ -run 'TestScanTranscript_RoutesKindFailedPreflight' -v`

(If the build tag is `setup_failure_detector` or similar in this codebase, adjust. Verify with `grep -n 'go:build' hooks/setup-failure-detector.go`.)

Expected: FAIL — the current switch (line 173) does not match `kind="failed"`.

- [ ] **Step 3: Broaden the switch case and rewrite comment**

Open `hooks/setup-failure-detector.go`. Locate lines 172-178:

```go
		switch kind {
		case "aggregate", "start_failed":
			// /run-sensor and /stop-sensor produce metadata.kind=aggregate;
			// /start-sensor produces metadata.kind=start_failed when prepare,
			// schema validation, or fork+exec fails before the sensor is
			// registered. Both are candidates for setup-shape healing.
			sensorPath = originalSensorPath
```

Replace with:

```go
		switch kind {
		case "aggregate", "start_failed", "failed":
			// /run-sensor produces metadata.kind=aggregate for runtime
			// aggregates and metadata.kind=failed for preflight failures.
			// /start-sensor uses metadata.kind=failed for every terminal
			// envelope except "started" (and "rejected" for the singleton
			// case). "start_failed" is preserved for backward compatibility
			// with any pre-existing recorded transcripts. All are candidates
			// for setup-shape healing.
			sensorPath = originalSensorPath
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test -tags=<tag> ./hooks/ -run 'TestScanTranscript_RoutesKindFailedPreflight' -v`
Expected: PASS.

- [ ] **Step 5: Run full hook tests**

Run: `go test -tags=<tag> ./hooks/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add hooks/setup-failure-detector.go hooks/setup-failure-detector_test.go
git commit -m "$(cat <<'EOF'
fix(hooks): setup-failure-detector recognises kind="failed" preflight

Preflight failures from /run-sensor will change kind from "aggregate" to
"failed" (canonical preflight signal shape). Broaden the switch so heal
classification still fires. Also closes a pre-existing silent gap where
/start-sensor's terminal envelope kind="failed" was never recognised
(the comment claimed "start_failed" but the code has always emitted
"failed").
EOF
)"
```

---

## Task 7: AttachLiveDep AttachResult struct + gate in spawn-fresh

**Files:**
- Modify: `lib/orchestrator/live_deps.go`
- Modify: `lib/orchestrator/live_deps_test.go`
- Modify: `lib/orchestrator/preflight.go` (RunDeps caller adapt)
- Modify: `skills/run-sensor/scripts/run-inferential.go:181` (caller adapt)

- [ ] **Step 1: Write the two new failing tests**

Open `lib/orchestrator/live_deps_test.go`. Append:

```go
// helpers used below
//   loadDepSensor(t, root, id) -> Sensor: already exists at line ~128
//   writeBlockingDepWithRequiresEnv(t, root, id, envName): create a blocking
//   dep that declares requires[kind=env,name=envName]
func writeBlockingDepWithRequiresEnv(t *testing.T, root, id, envName string) {
	t.Helper()
	body := map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Blocking with env requirement",
		"description": "blocking fixture requiring " + envName,
		"determinism": "high",
		"kind":        "setup",
		"type":        "computational",
		"output":      "stream",
		"regulation":  "behaviour",
		"phase":       "continuous",
		"triggers":    []interface{}{map[string]interface{}{"on": "manual"}},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"},
			},
		},
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50},
		},
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": envName},
		},
		"execution": map[string]interface{}{
			"command":             "while true; do echo TICK; sleep 0.1; done",
			"blocking":            true,
			"graceful_timeout_ms": 200,
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": "*", "verdict": "pass", "severity": "info"},
			},
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^TICK$", "verdict": "pass", "severity": "info"},
				},
			},
		},
	}
	writeSensorJSON(t, root, id, body)
}

func TestAttachLiveDep_SpawnFreshGateFails_ReturnsGateSignalNoSpawn(t *testing.T) {
	const envName = "__HARNESS_ATTACH_NEVER_SET__"
	root := t.TempDir()
	writeBlockingDepWithRequiresEnv(t, root, "needs-env-blocking", envName)

	dep := loadDepSensor(t, root, "needs-env-blocking")
	v, _ := schema.LoadValidator(testfixtures.RepoSchemasDir(t), io.Discard)

	var out, errBuf bytes.Buffer
	result, err := orchestrator.AttachLiveDep(
		context.Background(),
		dep,
		root,
		"holder-x",
		os.Getpid(),
		v,
		&out,
		&errBuf,
	)
	if err != nil {
		t.Fatalf("err: got %v, want nil (gate-fail is not a hard error)", err)
	}
	if result.GateSignal == nil {
		t.Fatal("GateSignal: got nil, want non-nil (env is unset)")
	}
	if result.Live.ID != "" || result.Live.RunID != "" {
		t.Errorf("Live: got %+v, want zero-value (no spawn happened)", result.Live)
	}
	md := result.GateSignal["metadata"].(map[string]interface{})
	if md["kind"] != "failed" || md["cause"] != "preflight_failed" {
		t.Errorf("GateSignal metadata: kind=%v, cause=%v; want failed/preflight_failed", md["kind"], md["cause"])
	}

	// Registry must not have an entry.
	rs, _ := registry.Load(registry.NewRoot(root))
	if rs.FindEntry("needs-env-blocking") != nil {
		t.Error("registry has an entry for needs-env-blocking; expected none (no spawn)")
	}
}

func TestAttachLiveDep_ReattachToLiveDep_DoesNotGate(t *testing.T) {
	// A dep that declares requires[kind=tool, name=<missing>] must succeed in
	// re-attach because re-attach is not a spawn — the dep is already running
	// with whatever environment it had at its real spawn time.
	root := t.TempDir()
	writeBlockingDepWithRequiresTool(t, root, "live-dep-with-missing-tool", "absolutely-not-on-path-XYZ")

	// Pre-populate the registry with an alive entry for the dep. Use the
	// current test process PID — IsPIDAlive returns true.
	r := registry.NewRoot(root)
	rs := registry.RunningSensors{Entries: []registry.RunningSensorEntry{{
		SensorID:   "live-dep-with-missing-tool",
		RunID:      fmt.Sprintf("%d-fake", os.Getpid()),
		Blocking:   true,
		PID:        os.Getpid(),
		PGID:       os.Getpid(),
		WatcherPID: 0,
		StartedAt:  "2026-05-12T00:00:00Z",
		Command:    "stub",
		LogDir:     r.RelativeRunDir("live-dep-with-missing-tool", fmt.Sprintf("%d-fake", os.Getpid())),
		HeldBy:     []registry.HeldByEntry{},
	}}}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	dep := loadDepSensor(t, root, "live-dep-with-missing-tool")
	v, _ := schema.LoadValidator(testfixtures.RepoSchemasDir(t), io.Discard)

	var out, errBuf bytes.Buffer
	result, err := orchestrator.AttachLiveDep(
		context.Background(),
		dep,
		root,
		"holder-x",
		os.Getpid(),
		v,
		&out,
		&errBuf,
	)
	if err != nil {
		t.Fatalf("err: got %v, want nil", err)
	}
	if result.GateSignal != nil {
		t.Errorf("GateSignal: got non-nil %v, want nil (re-attach must not gate)", result.GateSignal)
	}
	if result.Live.ID != "live-dep-with-missing-tool" {
		t.Errorf("Live.ID: got %q, want live-dep-with-missing-tool", result.Live.ID)
	}
}

// writeBlockingDepWithRequiresTool — analogous helper for tool requires.
func writeBlockingDepWithRequiresTool(t *testing.T, root, id, toolName string) {
	t.Helper()
	body := map[string]interface{}{
		"version": "1.0.0", "name": "Blocking with tool requirement",
		"description": "blocking fixture requiring tool " + toolName,
		"determinism": "high", "kind": "setup", "type": "computational",
		"output": "stream", "regulation": "behaviour", "phase": "continuous",
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{
				"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}},
		},
		"cost": map[string]interface{}{
			"class": "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50},
		},
		"requires": []interface{}{
			map[string]interface{}{"kind": "tool", "name": toolName},
		},
		"execution": map[string]interface{}{
			"command":             "while true; do echo TICK; sleep 0.1; done",
			"blocking":            true,
			"graceful_timeout_ms": 200,
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": "*", "verdict": "pass", "severity": "info"},
			},
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^TICK$", "verdict": "pass", "severity": "info"},
				},
			},
		},
	}
	writeSensorJSON(t, root, id, body)
}
```

Note: `writeSensorJSON` and `loadDepSensor` already exist in the test files; reuse them.

- [ ] **Step 2: Run new tests to verify failure**

Run: `go test ./lib/orchestrator/ -run 'TestAttachLiveDep_(SpawnFreshGateFails|ReattachToLiveDep)' -v`
Expected: FAIL — `result.GateSignal` doesn't compile (no such field on `LiveDep`).

- [ ] **Step 3: Define AttachResult struct**

Open `lib/orchestrator/live_deps.go`. Locate the `LiveDep` struct definition (line 25). Immediately below it, add:

```go
// AttachResult is the structured return of AttachLiveDep. Exactly one of
// Live or GateSignal is populated on err==nil:
//
//   Live.ID != ""  → attach succeeded (fresh spawn or re-attach). Caller
//                    pushes Live onto its LiveStack for later detach.
//   GateSignal != nil → spawn-fresh path detected an unmet precondition.
//                       No subprocess was spawned and no registry entry
//                       was created. Caller emits the signal and records
//                       it for downstream cascade machinery.
type AttachResult struct {
	Live       LiveDep
	GateSignal map[string]interface{}
}
```

- [ ] **Step 4: Change AttachLiveDep signature and add gate in the spawn-fresh branch**

In the same file, replace the entire `AttachLiveDep` function (currently lines 63-104) with:

```go
func AttachLiveDep(
	ctx context.Context,
	dep Sensor,
	projectRoot, holderID string,
	holderPID int,
	v *schema.Validator,
	stdout, stderr io.Writer,
) (AttachResult, error) {
	r := registry.NewRoot(projectRoot)
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	holder := registry.HeldByEntry{Kind: "sensor", ID: holderID, PID: holderPID, AttachedAt: now}

	startedFresh := false
	var runID string
	var gateSig map[string]interface{}
	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		existing := rs.FindBlockingEntry(dep.ID)
		if existing != nil && registry.IsPIDAlive(existing.PID) {
			reapDeadSameIDHolders(existing, holderID)
			if !hasLiveSameIDHolder(existing, holderID) {
				registry.AddHolder(existing, holder)
			}
			runID = existing.RunID
			return registry.Save(r, rs)
		}
		// Spawn-fresh branch — gate the dep's requires[] BEFORE startBlockingDep.
		// Re-attach (above) explicitly does NOT gate: the dep is already alive
		// with whatever env/PATH it spawned with, and the holder's current
		// environment may legitimately differ (e.g., redis-cli on PATH at
		// dep's spawn time, not on PATH for a second holder).
		env, eerr := sensor.BuildEnvelope(dep.JSON)
		if eerr != nil {
			return fmt.Errorf("build envelope for gate: %w", eerr)
		}
		output, _ := dep.JSON["output"].(string)
		if sig, failed := PreflightGate(dep, env, output); failed {
			gateSig = sig
			return nil
		}
		startedFresh = true
		newID, startErr := startBlockingDep(&rs, r, dep, holder, projectRoot)
		if startErr != nil {
			return startErr
		}
		runID = newID
		return nil
	}); err != nil {
		return AttachResult{}, err
	}

	if gateSig != nil {
		// Emit gate signal here so callers don't have to repeat schema
		// validation. Schema validation falls back to validateOrFallback's
		// emergency signal on rejection (mirrors the pattern used for other
		// orchestrator-emitted signals in this file).
		gateSig = validateOrFallback(v, gateSig, dep.ID, stderr)
		_ = json.NewEncoder(stdout).Encode(gateSig)
		return AttachResult{GateSignal: gateSig}, nil
	}

	kind := "dep_attached"
	if startedFresh {
		kind = "dep_started"
	}
	sig := buildSimpleSignal(dep.ID, "pass", "info", kind, fmt.Sprintf("blocking dep %q held by %q", dep.ID, holderID))
	sig = validateOrFallback(v, sig, dep.ID, stderr)
	_ = json.NewEncoder(stdout).Encode(sig)
	return AttachResult{Live: LiveDep{ID: dep.ID, RunID: runID}}, nil
}
```

- [ ] **Step 5: Update RunDeps in preflight.go**

Open `lib/orchestrator/preflight.go`. Locate lines 89-99:

```go
		if blocking {
			live, attachErr := AttachLiveDep(ctx, s, projectRoot, holderID, holderPID, v, stdout, stderr)
			if attachErr != nil {
				cascade := buildSimpleSignal(targetID, "error", "high", "dep_start_failed", attachErr.Error())
				_ = json.NewEncoder(stdout).Encode(cascade)
				res.ExitCode = 1
				return res
			}
			res.LiveStack = append(res.LiveStack, live)
			res.Signals[s.ID] = map[string]interface{}{"verdict": "pass"}
			continue
		}
```

Replace with:

```go
		if blocking {
			result, attachErr := AttachLiveDep(ctx, s, projectRoot, holderID, holderPID, v, stdout, stderr)
			if attachErr != nil {
				cascade := buildSimpleSignal(targetID, "error", "high", "dep_start_failed", attachErr.Error())
				_ = json.NewEncoder(stdout).Encode(cascade)
				res.ExitCode = 1
				return res
			}
			if result.GateSignal != nil {
				// AttachLiveDep already emitted on stdout and validated.
				// Record in Signals so FirstFailedDep / BuildCascadeSignal
				// propagate to dependents (including the root) in later
				// iterations.
				res.Signals[s.ID] = result.GateSignal
				continue
			}
			res.LiveStack = append(res.LiveStack, result.Live)
			res.Signals[s.ID] = map[string]interface{}{"verdict": "pass"}
			continue
		}
```

- [ ] **Step 6: Update run-inferential.go caller**

Open `skills/run-sensor/scripts/run-inferential.go`. Locate line 181:

```go
			live, aerr := orchestrator.AttachLiveDep(ctx, dep, projectRoot, rootID, os.Getpid(), v, stdout, stderr)
```

Adapt to:

```go
			result, aerr := orchestrator.AttachLiveDep(ctx, dep, projectRoot, rootID, os.Getpid(), v, stdout, stderr)
```

Then update the immediately-following block that uses `live` to use `result.Live`. Search backward and forward 10-15 lines for usages and update each (`liveStack = append(liveStack, live)` becomes `liveStack = append(liveStack, result.Live)`). Also add the gate-signal handling:

```go
			result, aerr := orchestrator.AttachLiveDep(ctx, dep, projectRoot, rootID, os.Getpid(), v, stdout, stderr)
			if aerr != nil {
				// ... existing error handling
			}
			if result.GateSignal != nil {
				// Gate failed on spawn-fresh path. AttachLiveDep already
				// emitted; we record and abort the dep loop. Dependents
				// receive a cascade via the existing logic if any.
				// For run-inferential, the simplest correct behaviour is to
				// stop processing further deps (the requested sensor cannot
				// run if a blocking dep cannot start).
				return 1
			}
			liveStack = append(liveStack, result.Live)
```

Read the existing surrounding code to integrate cleanly — the variable names and exit semantics may differ slightly from the snippet above.

- [ ] **Step 7: Update existing live_deps_test.go and preflight_test.go**

Run: `grep -n 'orchestrator\.AttachLiveDep' lib/orchestrator/*_test.go`

For each existing test that calls `AttachLiveDep`, update to use the new return signature:

```go
// Old:
live, err := orchestrator.AttachLiveDep(...)

// New:
result, err := orchestrator.AttachLiveDep(...)
live := result.Live
```

In `lib/orchestrator/preflight_test.go::TestRunDeps_BlockingDepStartFresh` (around line 269), the test calls `runDepsForTest` (not `AttachLiveDep` directly) so it adapts automatically — verify by reading the test.

- [ ] **Step 8: Run all orchestrator tests**

Run: `go test ./lib/orchestrator/ -v`
Expected: PASS for all (including the two new tests added in Step 1).

- [ ] **Step 9: Build verification for run-inferential**

Run: `go build -tags=run_inferential ./skills/run-sensor/scripts/`
Expected: clean build.

- [ ] **Step 10: Commit**

```bash
git add lib/orchestrator/live_deps.go lib/orchestrator/live_deps_test.go lib/orchestrator/preflight.go lib/orchestrator/preflight_test.go skills/run-sensor/scripts/run-inferential.go
git commit -m "$(cat <<'EOF'
fix(orchestrator): gate blocking deps before spawn — closes #36

AttachLiveDep returns AttachResult{Live, GateSignal} instead of bare LiveDep.
Inside the file lock, the spawn-fresh branch now calls PreflightGate on the
dep's requires[] before startBlockingDep — if gate fails, no subprocess is
spawned and the signal is propagated to the caller for cascade handling.

Re-attach explicitly does NOT gate: the dep is already alive with the env
it had at its original spawn, and the holder's environment may differ
legitimately. Gate must run under the lock because the fresh-vs-reattach
decision is lock-scoped.

Closes #36.
EOF
)"
```

---

## Task 8: RunDeps cascade test for #36

**Files:**
- Modify: `lib/orchestrator/preflight_test.go`

- [ ] **Step 1: Add the closure test**

Open `lib/orchestrator/preflight_test.go`. Append:

```go
func TestRunDeps_BlockingDepGateFails_EmitsPreflightSignalAndCascadesRoot(t *testing.T) {
	const envName = "__HARNESS_RUNDEPS_NEVER_SET__"
	root := t.TempDir()
	writeBlockingDepWithRequiresEnv(t, root, "blocking-dep", envName)
	writeNonBlockingDep(t, root, "target", []string{"blocking-dep"}, "true")

	res, stdout, _ := runDepsForTest(t, root, "target", "target", os.Getpid())

	if res.ExitCode != 0 {
		t.Fatalf("ExitCode: got %d, want 0", res.ExitCode)
	}

	depSig, ok := res.Signals["blocking-dep"]
	if !ok {
		t.Fatal("Signals[blocking-dep] missing")
	}
	if depSig["verdict"] != "error" {
		t.Errorf("dep verdict: got %v, want error", depSig["verdict"])
	}
	depMD := depSig["metadata"].(map[string]interface{})
	if depMD["kind"] != "failed" || depMD["cause"] != "preflight_failed" {
		t.Errorf("dep metadata: got kind=%v cause=%v; want failed/preflight_failed", depMD["kind"], depMD["cause"])
	}
	envs := depMD["missing_envs"].([]interface{})
	if len(envs) != 1 || envs[0] != envName {
		t.Errorf("dep missing_envs: got %v, want [%s]", envs, envName)
	}

	if len(res.LiveStack) != 0 {
		t.Errorf("LiveStack: got %v, want [] (no spawn)", res.LiveStack)
	}

	if res.CascadeSig == nil {
		t.Fatal("CascadeSig: got nil, want non-nil (root would cascade)")
	}
	cascadeMD := res.CascadeSig["metadata"].(map[string]interface{})
	if cascadeMD["failed_dep_id"] != "blocking-dep" {
		t.Errorf("CascadeSig.failed_dep_id: got %v, want blocking-dep", cascadeMD["failed_dep_id"])
	}

	if !strings.Contains(stdout, `"sensor_id":"blocking-dep"`) || !strings.Contains(stdout, `"cause":"preflight_failed"`) {
		t.Errorf("stdout should carry the dep's preflight-failed signal; got:\n%s", stdout)
	}

	// Verify no registry entry was created for the dep.
	rs, _ := registry.Load(registry.NewRoot(root))
	if rs.FindEntry("blocking-dep") != nil {
		t.Error("registry has entry for blocking-dep; expected none")
	}
}
```

This uses helpers `writeBlockingDepWithRequiresEnv`, `writeNonBlockingDep`, and `runDepsForTest`. The first is the one we just added in Task 7; the latter two already exist.

- [ ] **Step 2: Run the test**

Run: `go test ./lib/orchestrator/ -run 'TestRunDeps_BlockingDepGateFails' -v`
Expected: PASS — Task 7's implementation already produces the right cascade.

- [ ] **Step 3: Run full orchestrator test suite as regression check**

Run: `go test ./lib/orchestrator/`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add lib/orchestrator/preflight_test.go
git commit -m "test(orchestrator): regression test for #36 — blocking-dep gate cascade"
```

---

## Task 9: Migrate lib/heal/rules/missing_env_test fixture

**Files:**
- Modify: `lib/heal/rules/missing_env_test.go`

- [ ] **Step 1: Identify the fixture-building call**

Run: `grep -n 'BuildMissingEnvSignal\|CheckRequiredEnv\|MissingEnv{' lib/heal/rules/missing_env_test.go`

Open the file. Locate where the test calls `sensor.BuildMissingEnvSignal(envelope, output, missing)` or constructs `[]sensor.MissingEnv{...}`.

- [ ] **Step 2: Replace fixture builder**

Replace the fixture-construction block (whatever its exact shape) with the canonical pattern:

```go
gate := sensor.CheckRequiresGate(sensorJSON, sensor.GateOpts{
	LookupEnv: func(string) (string, bool) { return "", false }, // simulate all unset
})
sig := sensor.BuildRequiresGateSignal(envelope, "single", gate)
```

The sensor JSON used for the fixture should have a `requires[]` block declaring the env vars the test expects to be missing. If the existing fixture is shaped as `[]MissingEnv{...}`, transform it into a `requires[]` array in the sensor JSON.

If the existing test builds `MissingEnv` values directly without a `requires[]` JSON, the equivalent is constructing a `sensor.Gate` directly:

```go
gate := sensor.Gate{Failures: []sensor.Failure{
	{Kind: "env", Identifier: "FOO", Rationale: "Required environment variable FOO is not set", HealShape: "missing-env"},
	{Kind: "env", Identifier: "BAR", Rationale: "Required environment variable BAR is not set", HealShape: "missing-env"},
}}
sig := sensor.BuildRequiresGateSignal(envelope, "single", gate)
```

This is the closer 1:1 replacement.

- [ ] **Step 3: Run heal tests**

Run: `go test ./lib/heal/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add lib/heal/rules/missing_env_test.go
git commit -m "test(heal): migrate fixture to CheckRequiresGate + BuildRequiresGateSignal"
```

---

## Task 10: Remove obsoleted code

**Files:**
- Modify: `lib/orchestrator/preflight.go` (remove `RunRequiresGate`)
- Modify: `lib/sensor/env.go` (remove `BuildMissingEnvSignal`, `CheckRequiredEnv`, `MissingEnv`)
- Modify or delete: `lib/sensor/env_test.go`

- [ ] **Step 1: Confirm no remaining callers**

Run: `grep -rn 'orchestrator\.RunRequiresGate\|sensor\.BuildMissingEnvSignal\|sensor\.CheckRequiredEnv\|sensor\.MissingEnv' --include='*.go'`

Expected: no matches in production code (only any remaining test imports if env_test.go still has them). If matches appear, go back and update those call sites first.

- [ ] **Step 2: Remove `orchestrator.RunRequiresGate`**

Open `lib/orchestrator/preflight.go`. Delete lines 142-157 (the `RunRequiresGate` function and its doc comment):

```go
// RunRequiresGate evaluates target's requires[kind ∈ {tool, context, env}]
// preconditions and returns the resulting Gate. Callers MUST check
// gate.Failed() before invoking the sensor's command — both /run-sensor
// (lifecycle.go Phase 0) and /start-sensor (before its detached spawn)
// share this entry point so the fail-closed behavior is identical across
// runners.
//
// LookupEnv uses sensor.LookupEnvFn (process env). Entries with
// kind=sensor are handled by the DAG resolver (Resolve + RunDeps);
// kind=step entries are executed by RunPreparePhase; kind=permission is
// handled by Claude Code's permission engine.
func RunRequiresGate(target Sensor) sensor.Gate {
	return sensor.CheckRequiresGate(target.JSON, sensor.GateOpts{
		LookupEnv: sensor.LookupEnvFn,
	})
}
```

Delete the whole block.

- [ ] **Step 3: Remove deprecated env helpers**

Open `lib/sensor/env.go`. Delete the entire content of the file (all three: `MissingEnv` struct, `CheckRequiredEnv`, `BuildMissingEnvSignal`, and `missingEnvRationale` if it's not used elsewhere).

Run: `grep -rn 'missingEnvRationale' --include='*.go'` — if anything outside `env.go` still references it, surface that to the implementer. Otherwise delete the file:

```bash
rm lib/sensor/env.go
```

The package still has `LookupEnvFn` exported via `requires.go`. Verify `LookupEnvFn` lives in `requires.go` or another file:

Run: `grep -rn '^var LookupEnvFn\|^func LookupEnvFn' lib/sensor/`

If `LookupEnvFn` was defined in `env.go`, copy that single var declaration into `requires.go` before deleting `env.go`.

- [ ] **Step 4: Update or delete env_test.go**

Open `lib/sensor/env_test.go`. Any test that calls the removed APIs (`TestBuildMissingEnvSignal_*`, `TestCheckRequiredEnv_*`) is now stale.

Inspect each test. If it captures useful behavior already covered by `requires_test.go`, delete the test. If it captures a unique edge case (e.g., optional env vars are skipped — covered by `requires_test.go::TestCheckRequiresGate_*`?), migrate the assertion into `requires_test.go` under the new canonical API.

Run: `grep -n 'optional.*true\|optional.*=.*true' lib/sensor/requires_test.go`

If the optional-env coverage is missing, port the test:

```go
func TestCheckRequiresGate_SkipsOptionalEnv(t *testing.T) {
	sensorJSON := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "__OPTIONAL_UNSET__", "optional": true},
		},
	}
	gate := CheckRequiresGate(sensorJSON, GateOpts{
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if gate.Failed() {
		t.Errorf("gate: got Failed(), want pass (optional should be skipped)")
	}
}
```

Append it to `requires_test.go` and delete `env_test.go`.

```bash
rm lib/sensor/env_test.go
```

- [ ] **Step 5: Full build and test**

Run: `go build ./...`
Expected: clean build.

Run: `go test ./...`
Expected: PASS for all tests.

If anything breaks because of the env.go deletion (forgotten reference), the build/test output names the file:line. Fix and rerun.

- [ ] **Step 6: Commit**

```bash
git add lib/orchestrator/preflight.go lib/sensor/env.go lib/sensor/env_test.go lib/sensor/requires.go lib/sensor/requires_test.go
# 'rm'd files are recorded via git add -A or git add <file> after deletion; verify:
git status
git commit -m "$(cat <<'EOF'
refactor(sensor,orchestrator): remove deprecated env/gate wrappers

- orchestrator.RunRequiresGate — superseded by PreflightGate
- sensor.BuildMissingEnvSignal, CheckRequiredEnv, MissingEnv — env-only
  transitional API; canonical CheckRequiresGate + BuildRequiresGateSignal
  cover env, tool, and context uniformly.

Move LookupEnvFn declaration into requires.go (was in env.go).
EOF
)"
```

---

## Task 11: Static invariant test

**Files:**
- Create: `lib/orchestrator/gate_invariant_test.go`

- [ ] **Step 1: Implement the grep-based invariant test**

Create `lib/orchestrator/gate_invariant_test.go`:

```go
package orchestrator_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSpawnCallSitesGated enforces project rule #11: every call to
// subprocess.StreamSubprocess, subprocess.Start, or subprocess.SpawnDetached
// that executes a sensor's execution.command must be preceded (in the same
// file) by an orchestrator.PreflightGate call. Files in the allowlist are
// exempted because they either define the primitives themselves or spawn
// non-sensor processes (watcher, prepare/teardown step commands, tests).
func TestSpawnCallSitesGated(t *testing.T) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}

	allowedFiles := map[string]bool{
		// Primitives — these are the definitions.
		"lib/subprocess/stream.go": true,
		"lib/subprocess/detach.go": true,
		"lib/subprocess/step.go":   true,
		// Watcher spawn — not a sensor command.
		"lib/watcher/spawn.go": true,
	}
	allowedDirs := []string{
		"lib/subprocess/",
		"lib/watcher/",
	}

	spawnPattern := []string{
		"subprocess.StreamSubprocess",
		"subprocess.Start",
		"subprocess.SpawnDetached",
	}

	var violations []string
	err = filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, path)
		if allowedFiles[rel] {
			return nil
		}
		for _, dir := range allowedDirs {
			if strings.HasPrefix(rel, dir) {
				return nil
			}
		}

		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		text := string(body)

		hasSpawn := false
		for _, p := range spawnPattern {
			if strings.Contains(text, p) {
				hasSpawn = true
				break
			}
		}
		if !hasSpawn {
			return nil
		}
		if !strings.Contains(text, "orchestrator.PreflightGate") && !strings.Contains(text, "PreflightGate(") {
			violations = append(violations, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("project rule #11 violated — files spawn without PreflightGate:\n  %s\n\nIf the call site is legitimate (e.g., spawns a non-sensor process), add the file to allowedFiles/allowedDirs in this test.", strings.Join(violations, "\n  "))
	}
}

// findRepoRoot walks up from cwd until it finds a go.mod (or .git).
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
```

- [ ] **Step 2: Run the test to verify pass**

Run: `go test ./lib/orchestrator/ -run 'TestSpawnCallSitesGated' -v`
Expected: PASS — every spawn call site now contains `PreflightGate` (either directly or via routing through `AttachLiveDep`/`RunOne` whose source files mention it).

If it fails, the violation list names the offending files. Verify each: (a) legitimately needs allowlisting → add to the test's allowlist, OR (b) genuinely missed the gate in earlier tasks → fix.

- [ ] **Step 3: Commit**

```bash
git add lib/orchestrator/gate_invariant_test.go
git commit -m "$(cat <<'EOF'
test(orchestrator): static invariant — every sensor spawn calls PreflightGate

Enforces project rule #11 via grep. Allowlist covers the primitives
themselves and non-sensor spawns (watcher, step commands).
EOF
)"
```

---

## Task 12: Documentation

**Files:**
- Modify: `CLAUDE.md`
- Modify: `CHANGELOG.md` (if it exists)

- [ ] **Step 1: Add rule #11 to CLAUDE.md "Project rules"**

Open `CLAUDE.md`. Locate the "Project rules" section. After the last numbered rule (currently #10 ending with "...inside this CLAUDE.md."), append:

```markdown
11. **Spawn de sensor é gated, sem exceção.** Toda chamada a `subprocess.StreamSubprocess`, `subprocess.Start`, ou `subprocess.SpawnDetached` que execute o `execution.command` de um sensor DEVE ser precedida por `orchestrator.PreflightGate` no mesmo arquivo. Em falha de gate, o caller emite o signal canônico retornado (`metadata.kind="failed", cause="preflight_failed"`) e aborta o spawn. Exceções legítimas — porque não executam o comando do próprio sensor — são allowlisted em `lib/orchestrator/gate_invariant_test.go`: `lib/watcher/` (spawn do watcher binary), `lib/subprocess/step.go` (prepare/teardown step commands), e os arquivos do próprio `lib/subprocess/`. O helper único é `orchestrator.PreflightGate(s Sensor, env sensor.Envelope, outputMode string) → (sig, failed)`. Não chamar `sensor.CheckRequiresGate` ou `sensor.BuildRequiresGateSignal` direto — são detalhes de implementação encapsulados pelo helper.
```

- [ ] **Step 2: Add gate paragraph to Architecture section**

Locate the subsection "Dependencies and lifecycle" in CLAUDE.md (under "Architecture"). At the end of that subsection (before the next subsection starts), append:

```markdown
A `requires[kind ∈ {tool,context,env}]` gate é avaliada por `orchestrator.PreflightGate` antes de **qualquer** spawn de comando de sensor — `RunOne`/`RunOneWithRoot` Phase 0, `/start-sensor` antes do detach, `AttachLiveDep` no branch spawn-fresh sob o lock (não em re-attach), e `run-inferential.go` antes do spawn LLM. Gate fail emite um signal canônico (`metadata.kind="failed", cause="preflight_failed"`) atribuído ao sensor cujo gate falhou; dependents cascade via `FirstFailedDep` + `BuildCascadeSignal` como em qualquer outra falha.
```

- [ ] **Step 3: Check for CHANGELOG.md**

Run: `ls CHANGELOG.md 2>/dev/null && echo exists || echo absent`

If `exists`: open `CHANGELOG.md` and add at the top:

```markdown
## Unreleased

### Changed

- `metadata.kind` for preflight failures from `/run-sensor` changes from `"aggregate"` to `"failed"`. Consumers should read `metadata.cause == "preflight_failed"` to detect preflight rejection (more precise across runners). Heal classifier and the `setup-failure-detector` hook are updated in the same change. Closes #36.

### Added

- `metadata.cause`, `metadata.missing_envs`, `metadata.missing_tools`, `metadata.missing_contexts` on the canonical preflight signal across every spawn entry point. Inferential sensors with `requires[kind=tool]` are now gated for the first time.
```

If `absent`: skip this step.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
[ -f CHANGELOG.md ] && git add CHANGELOG.md
git commit -m "$(cat <<'EOF'
docs: record project rule #11 + canonical preflight signal contract

Adds rule #11 (spawn-is-gated invariant) to CLAUDE.md and a paragraph in
the Architecture section. CHANGELOG records the metadata.kind shift and
the new structured metadata for preflight failures.
EOF
)"
```

---

## Task 13: Final validation

**Files:** None modified.

- [ ] **Step 1: Run the full test matrix**

Run in parallel (separate terminals or one-by-one):

```bash
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
go test -tags=start_sensor ./skills/...
go test -tags=stop_sensor ./skills/...
go test -tags=list_sensors ./skills/...
go test -tags=tail_sensor ./skills/...
go test -tags=heal_retry_original ./skills/heal-sensor/...
```

Expected: PASS for every command.

- [ ] **Step 2: Run vet across build tags**

```bash
go vet -tags=run_computational ./...
go vet -tags=run_inferential ./...
```

Expected: clean.

- [ ] **Step 3: Static invariant final pass**

Run: `go test ./lib/orchestrator/ -run 'TestSpawnCallSitesGated' -v`
Expected: PASS.

- [ ] **Step 4: Optional manual end-to-end against #36 reproduction**

If a Nest project or similar with blocking-dep + multiple required envs is available, validate end-to-end per the spec's "Manual end-to-end" roteiro:

```bash
cd <project-with-health-check-and-run-project-nest>
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "$HARNESS_FRAMEWORK_ROOT" -tags=run_computational \
  ./skills/run-sensor/scripts health-check
```

Expected stdout (single-digit-millisecond latency):
- Line 1: `sensor_id="run-project-nest"`, `verdict="error"`, `metadata.kind="failed"`, `metadata.cause="preflight_failed"`, `metadata.missing_envs=[...]`, `metadata.heal_hint="missing-env:<first>"`.
- Line 2: `sensor_id="health-check"`, `verdict="error"`, `metadata.kind="cascade"`, `metadata.failed_dep_id="run-project-nest"`.

Expected `.harness/runtime/running_sensors.json`: empty (no orphan PID).

If unavailable, skip — the unit tests in Task 8 cover the same logical path.

- [ ] **Step 5: Final commit (only if anything was tweaked in steps 1-4)**

```bash
git status
# If clean, no commit needed.
# Otherwise: git add <files>, git commit with appropriate message.
```

---

## Done criteria

- [ ] All 13 tasks complete, each with its own commit.
- [ ] `go test ./...` passes (with the build-tag matrix in Task 13).
- [ ] `lib/orchestrator/gate_invariant_test.go::TestSpawnCallSitesGated` passes.
- [ ] `lib/orchestrator/preflight_test.go::TestRunDeps_BlockingDepGateFails_EmitsPreflightSignalAndCascadesRoot` passes — this is the literal closure of #36.
- [ ] `CLAUDE.md` carries rule #11 and the gate paragraph in the Architecture section.
- [ ] No reference remains to `RunRequiresGate`, `BuildMissingEnvSignal`, `CheckRequiredEnv`, `MissingEnv`, `requiresGateAux`, or `requiresGateRationale` anywhere in the tree.
