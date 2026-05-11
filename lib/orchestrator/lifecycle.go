package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
//  2. execution.command (existing streaming pipeline; emits individual
//     JSONL Signals)
//  3. teardown[] (best-effort, silent — every step runs regardless of
//     prepare/command outcome)
//
// On exit, RunOne emits exactly one aggregate Signal as a JSONL line on
// stdout. Per-step lifecycle results are folded into metadata.lifecycle.
// Teardown failures contribute warn evidence but do NOT downgrade the
// aggregate verdict.
//
// Returns (signal, exitCode). exitCode is 0 unless schema validation
// fails (1) or input is malformed (2).
func RunOne(ctx context.Context, s Sensor, schemasDir string, v *schema.Validator, stdout, stderr io.Writer) (map[string]interface{}, int) {
	envelope, err := sensor.BuildEnvelope(s.JSON)
	if err != nil {
		fmt.Fprintln(stderr, "error: envelope:", err)
		return nil, 2
	}
	execMap, _ := s.JSON["execution"].(map[string]interface{})
	output, _ := s.JSON["output"].(string)

	// Phase 0: enforce requires[kind=env] BEFORE prepare runs.
	// A missing non-optional env var means the sensor cannot run at all —
	// skip prepare, command, and teardown entirely and emit a single
	// verdict=error aggregate Signal whose per-var evidence rationale is
	// shaped to match the heal classifier's missing-env rule. This is the
	// canonical entry point for the heal loop's documented happy path:
	// without it, the rule fires only for inferential sensors (which is
	// the bug we are fixing here).
	if missing := sensor.CheckRequiredEnv(s.JSON); len(missing) > 0 {
		sig := sensor.BuildMissingEnvSignal(envelope, output, missing)
		if v != nil {
			if err := v.Validate(schema.TargetSignal, sig); err != nil {
				schema.PrintValidationOrPlain(err, stderr)
				return nil, 1
			}
		}
		_ = json.NewEncoder(stdout).Encode(sig)
		return sig, 0
	}

	timeoutMS := readTimeoutMS(s.JSON)

	// Phase 1: prepare (fail-fast). Reads requires[kind=step] via sensor.Project().
	prepResults, prepFailed := runPreparePhase(ctx, s.JSON, timeoutMS)

	var aggregateMD map[string]interface{}
	var aggVerdict, aggSeverity string
	var commandRun string
	var elapsedMS int

	if prepFailed {
		// Skip command. Build degraded aggregate.
		aggVerdict, aggSeverity = "error", "high"
		commandRun, _ = execMap["command"].(string)
	} else {
		// Phase 2: command (existing streaming pipeline).
		command, _ := execMap["command"].(string)
		blocking, _ := execMap["blocking"].(bool)
		envExtra := readEnvMap(execMap)

		var patterns []signal.Pattern
		if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
			raw, _ := op["patterns"].([]interface{})
			ps, perr := signal.CompilePatterns(raw)
			if perr != nil {
				fmt.Fprintln(stderr, "error:", perr)
				return nil, 1
			}
			patterns = ps
		}

		res, _ := subprocess.StreamSubprocess(ctx, subprocess.StreamConfig{
			Command:   command,
			Env:       envExtra,
			TimeoutMS: timeoutMS,
			Patterns:  patterns,
			Envelope:  envelope,
			Validator: v,
			Stdout:    stdout,
			Stderr:    stderr,
		})

		ecMap, _ := execMap["exit_code_map"].([]interface{})
		exitVerd, exitSev := signal.MapExitCode(res.ExitCode, ecMap)
		streamVerd, streamSev := signal.MaxStreamVerdict(res.Individuals)
		agg := signal.Aggregate(signal.AggregateInput{
			ExitVerdict:    exitVerd,
			ExitSeverity:   exitSev,
			StreamVerdict:  streamVerd,
			StreamSeverity: streamSev,
			TimedOut:       res.TimedOut,
			Blocking:       blocking,
		})
		aggVerdict, aggSeverity = agg.Verdict, agg.Severity
		commandRun = command
		elapsedMS = res.ElapsedMS

		aggregateMD = map[string]interface{}{
			"kind":        "aggregate",
			"output_mode": output,
			"command":     command,
			"exit_code":   res.ExitCode,
			"timed_out":   res.TimedOut,
			"counts":      signal.CountVerdicts(res.Individuals),
		}
		if blocking {
			aggregateMD["blocking"] = true
		}
		// Single-mode setup-shape heuristic: when the command failed
		// and stderr matches a curated heal pattern, surface a
		// metadata.heal_hint = "<shape>:<excerpt>" so the heal
		// classifier's fast path (rule_heal_hint) can fire.
		// Stream-mode failures already surface evidence rationales
		// that rule_stderr_pattern matches against, so we skip them
		// here to avoid double-classification noise.
		if hint, ok := buildHealHint(output, aggVerdict, res.StderrExcerpt); ok {
			aggregateMD["heal_hint"] = hint
		}
	}

	// Phase 3: teardown (best-effort, runs regardless of prepare/command outcome).
	tdResults := runTeardownPhase(ctx, execMap, timeoutMS)

	if aggregateMD == nil {
		aggregateMD = map[string]interface{}{
			"kind":        "aggregate",
			"output_mode": output,
			"command":     commandRun,
			"exit_code":   nil,
			"timed_out":   false,
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
	// cancellation), exec.CommandContext will have SIGKILLed the
	// subprocess. Surface this on the aggregate so downstream agents
	// can distinguish a clean failure from a forced shutdown.
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
		"evidence":    buildLifecycleEvidence(prepResults, tdResults),
		"cost_actual": map[string]interface{}{"latency_ms": elapsedMS},
		"metadata":    aggregateMD,
	}

	if v != nil {
		if err := v.Validate(schema.TargetSignal, sig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return nil, 1
		}
	}
	_ = json.NewEncoder(stdout).Encode(sig)
	return sig, 0
}

// RunOneWithRoot is RunOne plus runtime persistence. When root is non-nil:
//
//   - After cmd.Start() returns, compute run_id = <pid>-<short-uuid8>
//   - mkdir <run-id>/, open raw.log and signals.log (via subprocess.StreamHandle)
//   - Insert a RunningSensorEntry with blocking=<sensor.execution.blocking>
//   - defer remove the entry on any exit path
//
// When root is nil, behavior is identical to RunOne.
func RunOneWithRoot(
	ctx context.Context, s Sensor, schemasDir string, v *schema.Validator,
	root *registry.Root, stdout, stderr io.Writer,
) (map[string]interface{}, int) {
	if root == nil {
		return RunOne(ctx, s, schemasDir, v, stdout, stderr)
	}
	return runOneWithPersistence(ctx, s, schemasDir, v, *root, stdout, stderr)
}

// runOneWithPersistence mirrors RunOne but persists a <run-id>/ directory
// and a running_sensors.json entry around the streaming subprocess. The
// entry is best-effort removed on every exit path (mkdir failure,
// registry insert failure, normal exit). Aggregate Signals are written
// to both stdout and <run-id>/signals.log.
func runOneWithPersistence(
	ctx context.Context, s Sensor, schemasDir string, v *schema.Validator,
	root registry.Root, stdout, stderr io.Writer,
) (map[string]interface{}, int) {
	envelope, err := sensor.BuildEnvelope(s.JSON)
	if err != nil {
		fmt.Fprintln(stderr, "error: envelope:", err)
		return nil, 2
	}
	execMap, _ := s.JSON["execution"].(map[string]interface{})
	output, _ := s.JSON["output"].(string)

	// Phase 0: requires[kind=env] guard. Same fast-path as RunOne — no
	// subprocess to manage, so no persistence required.
	if missing := sensor.CheckRequiredEnv(s.JSON); len(missing) > 0 {
		sig := sensor.BuildMissingEnvSignal(envelope, output, missing)
		if v != nil {
			if err := v.Validate(schema.TargetSignal, sig); err != nil {
				schema.PrintValidationOrPlain(err, stderr)
				return nil, 1
			}
		}
		_ = json.NewEncoder(stdout).Encode(sig)
		return sig, 0
	}

	timeoutMS := readTimeoutMS(s.JSON)

	// Phase 1: prepare (fail-fast). No subprocess yet, no persistence.
	prepResults, prepFailed := runPreparePhase(ctx, s.JSON, timeoutMS)

	var aggregateMD map[string]interface{}
	var aggVerdict, aggSeverity string
	var commandRun string
	var elapsedMS int
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
		envExtra := readEnvMap(execMap)

		var patterns []signal.Pattern
		if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
			raw, _ := op["patterns"].([]interface{})
			ps, perr := signal.CompilePatterns(raw)
			if perr != nil {
				fmt.Fprintln(stderr, "error:", perr)
				return nil, 1
			}
			patterns = ps
		}

		cfg := subprocess.StreamConfig{
			Command:   command,
			Env:       envExtra,
			TimeoutMS: timeoutMS,
			Patterns:  patterns,
			Envelope:  envelope,
			Validator: v,
			Stdout:    stdout,
			Stderr:    stderr,
		}
		handle, startErr := subprocess.Start(ctx, cfg)
		if startErr != nil {
			// Couldn't even spawn — fall through to degraded aggregate.
			aggVerdict, aggSeverity = "error", "high"
			commandRun = command
			fmt.Fprintf(stderr, "error: stream start: %v\n", startErr)
		} else {
			// PID known: synthesize run_id and prepare <run-id>/ on disk.
			// envelope.RunID is updated only after BOTH mkdir and registry
			// insert succeed (persistOK becomes true). This ensures the
			// aggregate Signal never claims a run_id whose <run-id>/ directory
			// does not exist on disk.
			runID = fmt.Sprintf("%d-%s", handle.PID, uuid.NewString()[:8])
			runDir = root.RunDir(envelope.SensorID, runID)

			persistOK := true
			if mkErr := os.MkdirAll(runDir, 0o755); mkErr != nil {
				fmt.Fprintf(stderr, "warning: mkdir run dir: %v\n", mkErr)
				_ = handle.Kill()
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
						PID:        handle.PID,
						PGID:       handle.PGID,
						WatcherPID: 0,
						StartedAt:  now,
						Command:    command,
						LogDir:     filepath.Join(".runtime", "sensors", envelope.SensorID, runID),
						HeldBy:     []registry.HeldByEntry{},
					})
					return registry.Save(root, rs)
				}); regErr != nil {
					fmt.Fprintf(stderr, "warning: registry insert: %v\n", regErr)
					_ = handle.Kill()
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

			if persistOK {
				handle.SetRunDir(runDir)
				handle.SetEnvelope(envelope)
			}

			res := handle.Run()
			ecMap, _ := execMap["exit_code_map"].([]interface{})
			exitVerd, exitSev := signal.MapExitCode(res.ExitCode, ecMap)
			streamVerd, streamSev := signal.MaxStreamVerdict(res.Individuals)
			agg := signal.Aggregate(signal.AggregateInput{
				ExitVerdict:    exitVerd,
				ExitSeverity:   exitSev,
				StreamVerdict:  streamVerd,
				StreamSeverity: streamSev,
				TimedOut:       res.TimedOut,
				Blocking:       blocking,
			})
			aggVerdict, aggSeverity = agg.Verdict, agg.Severity
			commandRun = command
			elapsedMS = res.ElapsedMS

			aggregateMD = map[string]interface{}{
				"kind":        "aggregate",
				"output_mode": output,
				"command":     command,
				"exit_code":   res.ExitCode,
				"timed_out":   res.TimedOut,
				"counts":      signal.CountVerdicts(res.Individuals),
			}
			if blocking {
				aggregateMD["blocking"] = true
			}
			if hint, ok := buildHealHint(output, aggVerdict, res.StderrExcerpt); ok {
				aggregateMD["heal_hint"] = hint
			}
		}
	}

	// Phase 3: teardown (best-effort).
	tdResults := runTeardownPhase(ctx, execMap, timeoutMS)

	if aggregateMD == nil {
		aggregateMD = map[string]interface{}{
			"kind":        "aggregate",
			"output_mode": output,
			"command":     commandRun,
			"exit_code":   nil,
			"timed_out":   false,
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
		"evidence":    buildLifecycleEvidence(prepResults, tdResults),
		"cost_actual": map[string]interface{}{"latency_ms": elapsedMS},
		"metadata":    aggregateMD,
	}

	if v != nil {
		if err := v.Validate(schema.TargetSignal, sig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return nil, 1
		}
	}
	_ = json.NewEncoder(stdout).Encode(sig)

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
func runPreparePhase(ctx context.Context, sensorJSON map[string]interface{}, defaultTimeoutMS int) ([]interface{}, bool) {
	steps := sensor.Project(sensorJSON, "step")
	var out []interface{}
	for _, step := range steps {
		cmd, _ := step["command"].(string)
		t := defaultTimeoutMS
		if v, ok := step["timeout_ms"]; ok {
			t = int(asNumber(v))
		}
		res, _ := subprocess.RunStep(ctx, subprocess.StepConfig{Command: cmd, TimeoutMS: t})
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
func runTeardownPhase(ctx context.Context, execMap map[string]interface{}, defaultTimeoutMS int) []interface{} {
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
		res, _ := subprocess.RunStep(ctx, subprocess.StepConfig{Command: cmd, TimeoutMS: t})
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
// lifecycle steps. Uses only signal.json's allowed evidence fields
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

func readEnvMap(execMap map[string]interface{}) map[string]string {
	out := map[string]string{}
	if envObj, ok := execMap["env"].(map[string]interface{}); ok {
		for k, val := range envObj {
			out[k] = fmt.Sprintf("%v", val)
		}
	}
	return out
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
