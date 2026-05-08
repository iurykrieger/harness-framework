package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/subprocess"
)

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

	timeoutMS := readTimeoutMS(s.JSON)

	// Phase 1: prepare (fail-fast).
	prepResults, prepFailed := runLifecyclePhase(ctx, execMap, "prepare", timeoutMS, true)

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
		longRunning, _ := execMap["long_running"].(bool)
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
			LongRunning:    longRunning,
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
		if longRunning {
			aggregateMD["long_running"] = true
		}
	}

	// Phase 3: teardown (best-effort, runs regardless of prepare/command outcome).
	tdResults, _ := runLifecyclePhase(ctx, execMap, "teardown", timeoutMS, false)

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

// runLifecyclePhase walks execMap[phase] (prepare or teardown), runs each
// step via subprocess.RunStep, and returns a slice of result maps shaped
// for inclusion under metadata.lifecycle. On failFast=true, the first
// non-pass step short-circuits (returns prepFailed=true).
func runLifecyclePhase(ctx context.Context, execMap map[string]interface{}, phase string, defaultTimeoutMS int, failFast bool) ([]interface{}, bool) {
	steps, _ := execMap[phase].([]interface{})
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
		verdict, severity := mapStepExitCode(res.ExitCode, ecMap, phase)
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
		if failFast && verdict != "pass" {
			return out, true
		}
	}
	return out, false
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
