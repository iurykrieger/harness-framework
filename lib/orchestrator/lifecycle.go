package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/heal"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/subprocess"
)

// healHintExcerptCap bounds the length of the detail portion of a
// metadata.heal_hint string. Keeps the metadata block small while
// retaining enough context for human inspection. ~120 chars matches
// the spec's recommendation for setup-shape heal_hint emission.
const healHintExcerptCap = 120

// RunOne executes a single sensor's full lifecycle:
//
//  1. prepare[] (fail-fast, silent — first non-pass step aborts and skips
//     the main command)
//  2. execution.steps[] via lib/exec.Run (engine pipeline; emits individual
//     JSONL Signals per matched parse: pattern, plus an aggregate the
//     orchestrator wraps with lifecycle metadata)
//  3. teardown[] (best-effort, silent — every step runs regardless of
//     prepare/command outcome)
//
// On exit, RunOne emits exactly one aggregate Signal as a JSONL line on
// stdout. Per-step lifecycle results are folded into metadata.lifecycle;
// per-engine-step verdicts are folded into metadata.steps[]. Teardown
// failures contribute warn evidence but do NOT downgrade the aggregate
// verdict.
//
// projectRoot is the user's project directory. Prepare/teardown step
// subprocesses run with cwd=projectRoot; the engine threads projectRoot
// into the typed sensor's runtime Cwd so shell steps inherit the same.
//
// Returns (signal, exitCode). exitCode is 0 unless schema validation
// fails (1) or input is malformed (2).
func RunOne(ctx context.Context, s Sensor, projectRoot, schemasDir string, v *schema.Validator, stdout, stderr io.Writer) (map[string]interface{}, int) {
	return runOneImpl(ctx, s, projectRoot, schemasDir, v, stdout, stderr, true)
}

// runOneImpl is the shared body of RunOne. When emitAggregate is false the
// final aggregate Signal (and any preflight-failed Signal) is returned to
// the caller but not written to stdout — the caller is responsible for
// emitting it at the moment that preserves its ordering constraints.
func runOneImpl(ctx context.Context, s Sensor, projectRoot, schemasDir string, v *schema.Validator, stdout, stderr io.Writer, emitAggregate bool) (map[string]interface{}, int) {
	envelope, err := sensor.BuildEnvelope(s.JSON)
	if err != nil {
		fmt.Fprintln(stderr, "error: envelope:", err)
		return nil, 2
	}
	execMap, _ := s.JSON["execution"].(map[string]interface{})
	output, _ := s.JSON["output"].(string)

	// Phase 0: requires[] gate. Fail-closed pre-flight check across
	// tool / context / env preconditions. A non-empty gate emits a single
	// verdict=error Signal carrying one evidence per failure and
	// metadata.heal_hint shaped to drive /heal-sensor.
	if sig, failed := PreflightGate(s, envelope, output); failed {
		return emitPreflightSignal(sig, v, stdout, stderr, emitAggregate)
	}

	timeoutMS := readTimeoutMS(s.JSON)

	// Phase 1: prepare (fail-fast). Reads requires[kind=step] via sensor.Project().
	prepResults, prepFailed := runPreparePhase(ctx, s.JSON, projectRoot, timeoutMS)

	var aggregateMD map[string]interface{}
	var aggVerdict, aggSeverity string
	var commandRun string
	var engineSteps []map[string]interface{}
	var engineIndividuals []map[string]interface{}
	var elapsedMS int
	var stderrExcerpt string

	if prepFailed {
		// Skip command. Build degraded aggregate.
		aggVerdict, aggSeverity = "error", "high"
		commandRun, _ = execMap["command"].(string)
	} else {
		// Phase 2: command (engine pipeline).
		blocking, _ := execMap["blocking"].(bool)
		commandRun, _ = execMap["command"].(string)

		typed, terr := loadTypedSensor(s, v)
		if terr != nil {
			fmt.Fprintln(stderr, "error: load sensor:", terr)
			aggVerdict, aggSeverity = "error", "high"
		} else {
			start := sensor.NowFn()
			engineRes := runViaEngine(ctx, typed, projectRoot, schemasDir, v, nil, stdout)
			elapsedMS = int(sensor.NowFn().Sub(start) / time.Millisecond)
			aggVerdict = engineRes.Verdict
			aggSeverity = engineRes.Severity
			engineSteps = engineRes.Steps
			engineIndividuals = engineRes.Individuals
			stderrExcerpt = stderrFromEngineSteps(engineSteps)

			aggregateMD = map[string]interface{}{
				"kind":        "aggregate",
				"output_mode": output,
				"command":     commandRun,
				"counts":      signal.CountVerdicts(engineRes.Individuals),
			}
			if len(engineSteps) > 0 {
				aggregateMD["steps"] = engineSteps
			}
			if blocking {
				aggregateMD["blocking"] = true
			}
			if hint, ok := buildHealHint(output, aggVerdict, stderrExcerpt); ok {
				aggregateMD["heal_hint"] = hint
			}
			if engineRes.EngineError != nil {
				fmt.Fprintln(stderr, "error: exec.Run:", engineRes.EngineError)
				aggregateMD["engine_error"] = engineRes.EngineError.Error()
			}
		}
	}

	// Phase 3: teardown (best-effort, runs regardless of prepare/command outcome).
	tdResults := runTeardownPhase(ctx, execMap, projectRoot, timeoutMS)

	if aggregateMD == nil {
		aggregateMD = map[string]interface{}{
			"kind":        "aggregate",
			"output_mode": output,
			"command":     commandRun,
			"counts":      map[string]int{"pass": 0, "warn": 0, "fail": 0, "error": 1},
		}
	}
	if len(prepResults) > 0 || len(tdResults) > 0 {
		lc := map[string]interface{}{}
		if len(prepResults) > 0 {
			lc["prepare"] = prepResults
		}
		if len(tdResults) > 0 {
			lc["teardown"] = tdResults
		}
		aggregateMD["lifecycle"] = lc
	}

	// External termination: when ctx was cancelled (SIGINT/SIGTERM via
	// the runner script's signal handler, or any other caller-initiated
	// cancellation), the engine's subprocesses will have been killed.
	// Surface this on the aggregate so downstream agents can distinguish
	// a clean failure from a forced shutdown.
	if ctx.Err() != nil {
		aggregateMD["terminated_externally"] = true
	}

	finished := sensor.NowFn().Format("2006-01-02T15:04:05Z")
	sig := map[string]interface{}{
		"sensor_id":   envelope.SensorID,
		"version":     envelope.Version,
		"run_id":      envelope.RunID,
		"started_at":  envelope.StartedAt,
		"finished_at": finished,
		"verdict":     aggVerdict,
		"severity":    aggSeverity,
		"confidence":  1.0,
		"evidence":    composeEvidence(prepResults, tdResults, engineIndividuals, output, aggVerdict, stderrExcerpt),
		"cost_actual": map[string]interface{}{"latency_ms": elapsedMS},
		"metadata":    aggregateMD,
	}

	if v != nil {
		if err := v.Validate(schema.TargetSignal, sig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return nil, 1
		}
	}
	if emitAggregate {
		_ = json.NewEncoder(stdout).Encode(sig)
	}
	return sig, 0
}

// RunOneWithRoot is RunOne plus runtime persistence. When root is non-nil:
//
//   - Before invoking the engine, compute run_id = <orchestrator-pid>-<short-uuid8>
//     and mkdir <run-id>/.
//   - Insert a RunningSensorEntry with blocking=<sensor.execution.blocking>
//     keyed on the orchestrator's own PID — the orchestrator process is the
//     lifecycle boundary; the engine's per-step subprocesses come and go.
//   - defer remove the entry on any exit path.
//   - After exec.Run returns, append the aggregate Signal to
//     <run-id>/signals.log so /tail-sensor sees the final outcome.
//
// projectRoot is the user's project directory; it is threaded into the
// engine as the sensor's runtime Cwd so shell steps run from the right
// directory regardless of the orchestrator's own cwd.
//
// When root is nil, behavior is identical to RunOne.
func RunOneWithRoot(
	ctx context.Context, s Sensor, projectRoot, schemasDir string, v *schema.Validator,
	root *registry.Root, stdout, stderr io.Writer,
) (map[string]interface{}, int) {
	if root == nil {
		return runOneImpl(ctx, s, projectRoot, schemasDir, v, stdout, stderr, true)
	}
	return runOneWithPersistenceImpl(ctx, s, projectRoot, schemasDir, v, *root, stdout, stderr, true)
}

// RunOneWithRootCapture is RunOneWithRoot with the final aggregate
// emission to stdout suppressed. The aggregate Signal is built,
// validated, and persisted (to signals.log when root != nil) exactly
// as in RunOneWithRoot, but it is NOT written to stdout — the caller
// is responsible for emitting it at the moment that satisfies its
// ordering constraints. Individual Signals during the engine's stream
// still flow to stdout in real time, unchanged.
//
// Intended for callers that orchestrate blocking-dep teardown after
// the command finishes and need the requested sensor's aggregate to
// remain the LAST JSONL line on stdout (see runWithDepsImpl).
func RunOneWithRootCapture(
	ctx context.Context, s Sensor, projectRoot, schemasDir string, v *schema.Validator,
	root *registry.Root, stdout, stderr io.Writer,
) (map[string]interface{}, int) {
	if root == nil {
		return runOneImpl(ctx, s, projectRoot, schemasDir, v, stdout, stderr, false)
	}
	return runOneWithPersistenceImpl(ctx, s, projectRoot, schemasDir, v, *root, stdout, stderr, false)
}

// runOneWithPersistenceImpl mirrors runOneImpl but persists a <run-id>/
// directory and a running_sensors.json entry around the streaming
// subprocess. The entry is best-effort removed on every exit path
// (mkdir failure, registry insert failure, normal exit). Aggregate
// Signals are written to <run-id>/signals.log unconditionally and to
// stdout when emitAggregate is true.
func runOneWithPersistenceImpl(
	ctx context.Context, s Sensor, projectRoot, schemasDir string, v *schema.Validator,
	root registry.Root, stdout, stderr io.Writer, emitAggregate bool,
) (map[string]interface{}, int) {
	envelope, err := sensor.BuildEnvelope(s.JSON)
	if err != nil {
		fmt.Fprintln(stderr, "error: envelope:", err)
		return nil, 2
	}
	execMap, _ := s.JSON["execution"].(map[string]interface{})
	output, _ := s.JSON["output"].(string)

	// Phase 0: requires[] gate (tool/context/env). Same fast-path as RunOne
	// — no subprocess to manage, so no persistence required.
	if sig, failed := PreflightGate(s, envelope, output); failed {
		return emitPreflightSignal(sig, v, stdout, stderr, emitAggregate)
	}

	timeoutMS := readTimeoutMS(s.JSON)

	// Phase 1: prepare (fail-fast). No subprocess yet, no persistence.
	prepResults, prepFailed := runPreparePhase(ctx, s.JSON, projectRoot, timeoutMS)

	var aggregateMD map[string]interface{}
	var aggVerdict, aggSeverity string
	var commandRun string
	var engineSteps []map[string]interface{}
	var engineIndividuals []map[string]interface{}
	var elapsedMS int
	var stderrExcerpt string
	// runID is the synthesized <pid>-<short> identifier; populated only
	// when the command phase actually spawned a subprocess.
	// runDir is the on-disk <run-id>/ directory path; it is set only when
	// both os.MkdirAll and the registry insert succeed. Either failure path
	// clears runDir to "" so the post-Run signals.log append is skipped.
	var runID string
	var runDir string

	if prepFailed {
		aggVerdict, aggSeverity = "error", "high"
		commandRun, _ = execMap["command"].(string)
	} else {
		command, _ := execMap["command"].(string)
		blocking, _ := execMap["blocking"].(bool)

		typed, terr := loadTypedSensor(s, v)
		if terr != nil {
			fmt.Fprintln(stderr, "error: load sensor:", terr)
			aggVerdict, aggSeverity = "error", "high"
			commandRun = command
		} else {
			// Synthesize run_id using the orchestrator's own PID as the
			// composite root. With the engine, no single subprocess
			// owns the run; the orchestrator process IS the lifecycle
			// boundary, and its PID is what registry consumers should
			// poll for liveness.
			holderPID := os.Getpid()
			runID = fmt.Sprintf("%d-%s", holderPID, uuid.NewString()[:8])
			runDir = root.RunDir(envelope.SensorID, runID)

			persistOK := true
			if mkErr := os.MkdirAll(runDir, 0o755); mkErr != nil {
				fmt.Fprintf(stderr, "warning: mkdir run dir: %v\n", mkErr)
				// Clear runDir so the post-Run signals.log append is skipped.
				runDir = ""
				persistOK = false
			}

			if persistOK {
				if regErr := registry.WithFileLock(root.LockFile(), func() error {
					rs, lerr := registry.Load(root)
					if lerr != nil {
						return lerr
					}
					if rs.FindEntryByRunID(runID) != nil {
						return fmt.Errorf("run_id collision: %s", runID)
					}
					now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
					rs.Entries = append(rs.Entries, registry.RunningSensorEntry{
						SensorID:   envelope.SensorID,
						RunID:      runID,
						Blocking:   blocking,
						PID:        holderPID,
						PGID:       holderPID,
						WatcherPID: 0,
						StartedAt:  now,
						Command:    command,
						LogDir:     root.RelativeRunDir(envelope.SensorID, runID),
						HeldBy:     []registry.HeldByEntry{},
					})
					return registry.Save(root, rs)
				}); regErr != nil {
					fmt.Fprintf(stderr, "warning: registry insert: %v\n", regErr)
					// Remove the just-created run dir so no orphan is left on disk.
					// Best-effort: ignore removal errors.
					_ = os.RemoveAll(runDir)
					// Clear runDir so the post-Run signals.log append is skipped.
					runDir = ""
					persistOK = false
				}
			}

			// Only propagate run_id into the envelope (and therefore the
			// aggregate Signal) when the full persistence setup succeeded.
			if persistOK {
				envelope.RunID = runID
			}

			// Ensure the registry entry is removed on every exit path.
			defer func() {
				if !persistOK {
					return
				}
				_ = registry.WithFileLock(root.LockFile(), func() error {
					rs, lerr := registry.Load(root)
					if lerr != nil {
						return lerr
					}
					rs.RemoveEntryByRunID(runID)
					return registry.Save(root, rs)
				})
			}()

			start := sensor.NowFn()
			engineRes := runViaEngine(ctx, typed, projectRoot, schemasDir, v, &root, stdout)
			elapsedMS = int(sensor.NowFn().Sub(start) / time.Millisecond)
			aggVerdict = engineRes.Verdict
			aggSeverity = engineRes.Severity
			engineSteps = engineRes.Steps
			engineIndividuals = engineRes.Individuals
			commandRun = command
			stderrExcerpt = stderrFromEngineSteps(engineSteps)

			aggregateMD = map[string]interface{}{
				"kind":        "aggregate",
				"output_mode": output,
				"command":     command,
				"counts":      signal.CountVerdicts(engineRes.Individuals),
			}
			if len(engineSteps) > 0 {
				aggregateMD["steps"] = engineSteps
			}
			if blocking {
				aggregateMD["blocking"] = true
			}
			if hint, ok := buildHealHint(output, aggVerdict, stderrExcerpt); ok {
				aggregateMD["heal_hint"] = hint
			}
			if engineRes.EngineError != nil {
				fmt.Fprintln(stderr, "error: exec.Run:", engineRes.EngineError)
				aggregateMD["engine_error"] = engineRes.EngineError.Error()
			}
		}
	}

	// Phase 3: teardown (best-effort).
	tdResults := runTeardownPhase(ctx, execMap, projectRoot, timeoutMS)

	if aggregateMD == nil {
		aggregateMD = map[string]interface{}{
			"kind":        "aggregate",
			"output_mode": output,
			"command":     commandRun,
			"counts":      map[string]int{"pass": 0, "warn": 0, "fail": 0, "error": 1},
		}
	}
	if len(prepResults) > 0 || len(tdResults) > 0 {
		lc := map[string]interface{}{}
		if len(prepResults) > 0 {
			lc["prepare"] = prepResults
		}
		if len(tdResults) > 0 {
			lc["teardown"] = tdResults
		}
		aggregateMD["lifecycle"] = lc
	}

	// External termination: see RunOne for rationale.
	if ctx.Err() != nil {
		aggregateMD["terminated_externally"] = true
	}

	finished := sensor.NowFn().Format("2006-01-02T15:04:05Z")
	sig := map[string]interface{}{
		"sensor_id":   envelope.SensorID,
		"version":     envelope.Version,
		"run_id":      envelope.RunID,
		"started_at":  envelope.StartedAt,
		"finished_at": finished,
		"verdict":     aggVerdict,
		"severity":    aggSeverity,
		"confidence":  1.0,
		"evidence":    composeEvidence(prepResults, tdResults, engineIndividuals, output, aggVerdict, stderrExcerpt),
		"cost_actual": map[string]interface{}{"latency_ms": elapsedMS},
		"metadata":    aggregateMD,
	}

	if v != nil {
		if err := v.Validate(schema.TargetSignal, sig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return nil, 1
		}
	}
	if emitAggregate {
		_ = json.NewEncoder(stdout).Encode(sig)
	}

	// Persist aggregate to signals.log only when a run dir was successfully
	// created and the registry insert succeeded (runDir != ""). Both mkdir
	// failure and registry insert failure clear runDir to the empty string,
	// ensuring no write is attempted against a directory that does not exist
	// or was removed during cleanup.
	if runDir != "" {
		sigsPath := root.SignalsLogRun(envelope.SensorID, runID)
		if f, ferr := os.OpenFile(sigsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); ferr == nil {
			_ = json.NewEncoder(f).Encode(sig)
			_ = f.Close()
		} else {
			fmt.Fprintf(stderr, "warning: append signals.log: %v\n", ferr)
		}
	}

	return sig, 0
}

// runPreparePhase reads requires[kind=step] from the sensor JSON (via
// sensor.Project) and runs each step fail-fast. Per-step results are folded
// into metadata.lifecycle.prepare (the metadata key keeps its name; it is
// the phase name, not the schema field name).
// projectRoot is forwarded to StepConfig.Dir so prepare steps run in the
// user's project directory.
func runPreparePhase(ctx context.Context, sensorJSON map[string]interface{}, projectRoot string, defaultTimeoutMS int) ([]interface{}, bool) {
	steps := sensor.Project(sensorJSON, "step")
	var out []interface{}
	for _, step := range steps {
		cmd, _ := step["command"].(string)
		t := defaultTimeoutMS
		if v, ok := step["timeout_ms"]; ok {
			t = int(asNumber(v))
		}
		res, _ := subprocess.RunStep(ctx, subprocess.StepConfig{Command: cmd, TimeoutMS: t, Dir: projectRoot})
		ecMap, _ := step["exit_code_map"].([]interface{})
		verdict, severity := mapStepExitCode(res.ExitCode, ecMap, "prepare")
		entry := map[string]interface{}{
			"command":    cmd,
			"exit_code":  res.ExitCode,
			"latency_ms": res.ElapsedMS,
			"timed_out":  res.TimedOut,
			"verdict":    verdict,
			"severity":   severity,
		}
		if res.StderrExcerpt != "" {
			entry["stderr_excerpt"] = res.StderrExcerpt
		}
		out = append(out, entry)
		if verdict != "pass" {
			return out, true
		}
	}
	return out, false
}

// runTeardownPhase walks execMap["teardown"] best-effort and returns the
// per-step result entries. Teardown lives in execution.teardown[];
// sensor-local setup steps live in requires[kind=step] (consumed by
// runPreparePhase).
// projectRoot is forwarded to StepConfig.Dir so teardown steps run in the
// user's project directory.
func runTeardownPhase(ctx context.Context, execMap map[string]interface{}, projectRoot string, defaultTimeoutMS int) []interface{} {
	steps, _ := execMap["teardown"].([]interface{})
	var out []interface{}
	for _, raw := range steps {
		step, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		cmd, _ := step["command"].(string)
		t := defaultTimeoutMS
		if v, ok := step["timeout_ms"]; ok {
			t = int(asNumber(v))
		}
		res, _ := subprocess.RunStep(ctx, subprocess.StepConfig{Command: cmd, TimeoutMS: t, Dir: projectRoot})
		ecMap, _ := step["exit_code_map"].([]interface{})
		verdict, severity := mapStepExitCode(res.ExitCode, ecMap, "teardown")
		entry := map[string]interface{}{
			"command":    cmd,
			"exit_code":  res.ExitCode,
			"latency_ms": res.ElapsedMS,
			"timed_out":  res.TimedOut,
			"verdict":    verdict,
			"severity":   severity,
		}
		if res.StderrExcerpt != "" {
			entry["stderr_excerpt"] = res.StderrExcerpt
		}
		out = append(out, entry)
	}
	return out
}

// mapStepExitCode applies the step's exit_code_map (if any), or the
// default rule (0 → pass/info; non-zero → fail/high for prepare,
// warn/low for teardown).
func mapStepExitCode(code int, ecMap []interface{}, phase string) (string, string) {
	if len(ecMap) > 0 {
		v, s := signal.MapExitCode(code, ecMap)
		if v != "" {
			return v, s
		}
	}
	if code == 0 {
		return "pass", "info"
	}
	if phase == "teardown" {
		return "warn", "low"
	}
	return "fail", "high"
}

// buildLifecycleEvidence produces evidence[] entries for any non-pass
// lifecycle steps. Uses only signal.yaml's allowed evidence fields
// (rationale, excerpt).
func buildLifecycleEvidence(prep, td []interface{}) []interface{} {
	out := []interface{}{}
	for _, items := range [][]interface{}{prep, td} {
		for _, raw := range items {
			step, _ := raw.(map[string]interface{})
			verdict, _ := step["verdict"].(string)
			if verdict == "pass" {
				continue
			}
			cmd, _ := step["command"].(string)
			excerpt, _ := step["stderr_excerpt"].(string)
			rationale := fmt.Sprintf("lifecycle step %q produced verdict=%s (exit=%v)", cmd, verdict, step["exit_code"])
			ev := map[string]interface{}{"rationale": rationale}
			if excerpt != "" {
				ev["excerpt"] = excerpt
			}
			out = append(out, ev)
		}
	}
	return out
}

// buildStderrEvidence returns evidence[] entries derived from the
// subprocess's captured stderr tail. Called only when output=="single"
// AND the aggregate verdict is fail/error AND stderr is non-empty.
// Returns the last rawLogTailLines non-empty stderr lines as excerpt
// entries with a shared rationale; the entries are appended to any
// lifecycle evidence the aggregate already carries.
//
// rawLogTailLines is defined in lib/orchestrator/live_deps.go.
func buildStderrEvidence(output, verdict, stderrText string) []interface{} {
	if output != "single" {
		return nil
	}
	if verdict != "fail" && verdict != "error" {
		return nil
	}
	if stderrText == "" {
		return nil
	}
	allLines := strings.Split(stderrText, "\n")
	collected := []string{}
	for i := len(allLines) - 1; i >= 0 && len(collected) < rawLogTailLines; i-- {
		line := strings.TrimRight(allLines[i], "\r")
		if line == "" {
			continue
		}
		collected = append([]string{line}, collected...)
	}
	out := make([]interface{}, 0, len(collected))
	for _, line := range collected {
		out = append(out, map[string]interface{}{
			"rationale": "subprocess stderr/stdout tail",
			"excerpt":   line,
		})
	}
	return out
}

// composeEvidence composes the orchestrator's wrapper-aggregate
// evidence from three sources: lifecycle phase results (prepare +
// teardown), the engine's individual signals, and (for single-mode
// failures) the captured stderr tail. The shape mirrors the legacy
// pipeline so heal-sensor classifiers continue to match
// rationale/excerpt entries without code changes.
func composeEvidence(prep, td []interface{}, individuals []map[string]interface{}, output, verdict, stderrText string) []interface{} {
	out := buildLifecycleEvidence(prep, td)
	if out == nil {
		out = []interface{}{}
	}
	if verdict == "fail" || verdict == "error" {
		if extra := signal.SelectTopEvidence(individuals, 5); len(extra) > 0 {
			out = append(out, extra...)
		}
	}
	if extra := buildStderrEvidence(output, verdict, stderrText); len(extra) > 0 {
		out = append(out, extra...)
	}
	return out
}

// stderrFromEngineSteps extracts the stderr_excerpt of the first
// engine step that carries one. The orchestrator surfaces it through
// the same heal_hint / stderr-evidence helpers the legacy single-
// command pipeline used.
func stderrFromEngineSteps(steps []map[string]interface{}) string {
	for _, step := range steps {
		if s, ok := step["stderr_excerpt"].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// buildHealHint synthesises a metadata.heal_hint = "<shape>:<excerpt>"
// string when the conditions for emission are met:
//
//   - sensor.output == "single" (stream-mode aggregates already carry
//     evidence rationales the classifier matches against)
//   - aggregate verdict is fail or error
//   - stderr is non-empty and matches a curated heal pattern
//
// excerpt is the first non-empty stderr line that matched, truncated
// to healHintExcerptCap to keep the metadata block compact.
func buildHealHint(output, verdict, stderrText string) (string, bool) {
	if output != "single" {
		return "", false
	}
	if verdict != "fail" && verdict != "error" {
		return "", false
	}
	if stderrText == "" {
		return "", false
	}
	shape, ok := heal.MatchStderrPattern(stderrText)
	if !ok {
		return "", false
	}
	excerpt := firstMatchingLine(stderrText)
	if excerpt == "" {
		excerpt = strings.TrimSpace(stderrText)
	}
	if len(excerpt) > healHintExcerptCap {
		excerpt = excerpt[:healHintExcerptCap]
	}
	return string(shape) + ":" + excerpt, true
}

// firstMatchingLine returns the first non-empty stderr line that
// matches a curated heal pattern. Falls back to "" when no individual
// line matches (caller will use the trimmed full text).
func firstMatchingLine(stderrText string) string {
	for _, line := range strings.Split(stderrText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, ok := heal.MatchStderrPattern(line); ok {
			return line
		}
	}
	return ""
}

func readTimeoutMS(s map[string]interface{}) int {
	cost, _ := s["cost"].(map[string]interface{})
	if cost == nil {
		return 0
	}
	lat, _ := cost["latency"].(map[string]interface{})
	if lat == nil {
		return 0
	}
	return int(asNumber(lat["timeout_ms"]))
}

func asNumber(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}

// emitPreflightSignal validates and emits the canonical preflight-failed
// Signal produced by PreflightGate. Returns (signal, 0) on success or
// (nil, 1) when schema validation rejects the signal — both paths leave
// stderr informative.
func emitPreflightSignal(sig map[string]interface{}, v *schema.Validator, stdout, stderr io.Writer, emitAggregate bool) (map[string]interface{}, int) {
	if v != nil {
		if err := v.Validate(schema.TargetSignal, sig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return nil, 1
		}
	}
	if emitAggregate {
		_ = json.NewEncoder(stdout).Encode(sig)
	}
	return sig, 0
}
