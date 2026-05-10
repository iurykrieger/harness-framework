//go:build run_inferential

// Command run-inferential runs a streaming inferential sensor end-to-end. The
// sensor's execution.command spawns an LLM CLI (e.g. `claude -p ...`); the
// runner does not talk HTTP. The user_prompt_template is rendered against
// --slot bindings and exposed to the subprocess as the HARNESS_PROMPT env var.
// The subprocess may emit a single line of the form
// `HARNESS_AGGREGATE_CONFIDENCE=<float>` on stdout to influence the
// calibration downgrade decision; otherwise confidence defaults to 1.0.
//
// The runner injects an internal pattern that catches HARNESS_AGGREGATE_CONFIDENCE
// lines so the subprocess does not need to declare one explicitly. Those
// magic-line individuals are filtered out before computing the aggregate so
// they do not pollute counts or evidence.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/cli"
	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/subprocess"
	"github.com/iurykrieger/harness-framework/lib/template"
)

const harnessConfidencePrefix = "HARNESS_AGGREGATE_CONFIDENCE="

func main() {
	cwd, _ := os.Getwd()
	os.Exit(run(os.Args[1:], cwd, os.Stdout, os.Stderr))
}

// run is the testable entry point. projectRoot is the directory from which
// sensor ids are resolved (sensors/<id>.json); pass os.Getwd() for production.
func run(args []string, projectRoot string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run-inferential", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var schemasDir string
	var slots cli.MultiFlag
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	fs.Var(&slots, "slot", "key=value slot binding for user_prompt_template (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: run-inferential [--schemas-dir=DIR] [--slot k=v]... <sensor-id>")
		return 2
	}

	bindings, err := parseSlots(slots)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}

	id := rest[0]

	sensorAbsPath, err := sensor.ResolveByID(id, projectRoot)
	if err != nil {
		fmt.Fprintln(stderr, "error: resolve:", err)
		return 2
	}

	v, code := schema.LoadValidator(schemasDir, stderr)
	if code != 0 {
		return code
	}

	var sensorJSON map[string]interface{}
	if b, rerr := os.ReadFile(sensorAbsPath); rerr != nil {
		fmt.Fprintln(stderr, "error: read:", rerr)
		return 2
	} else if jerr := json.Unmarshal(b, &sensorJSON); jerr != nil {
		fmt.Fprintln(stderr, "error: parse:", jerr)
		return 2
	}
	if err := v.Validate(schema.TargetSensor, sensorJSON); err != nil {
		schema.PrintValidationOrPlain(err, stderr)
		return 1
	}

	if t, _ := sensorJSON["type"].(string); t != "inferential" {
		fmt.Fprintf(stderr, "error: sensor.type=%q (run-inferential requires 'inferential')\n", t)
		return 2
	}

	execMapTop, _ := sensorJSON["execution"].(map[string]interface{})
	if execMapTop != nil {
		if blocking, _ := execMapTop["blocking"].(bool); blocking {
			fmt.Fprintln(stderr, "error: sensor is blocking; use /start-sensor instead")
			return 2
		}
	}

	output, _ := sensorJSON["output"].(string)

	// Resolve depends_on graph. Run every dep via orchestrator.RunOne;
	// blocking deps are started/attached via AttachLiveDep and detached
	// after the requested sensor completes.
	sensorRoot := filepath.Dir(sensorAbsPath)
	rootID := id

	order, err := orchestrator.Resolve(rootID, sensorRoot)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}

	// Validate every sensor in the graph before running anything.
	for _, s := range order {
		if err := v.Validate(schema.TargetSensor, s.JSON); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
	}

	depSignals := map[string]map[string]interface{}{}
	var liveStack []string

	defer func() {
		for i := len(liveStack) - 1; i >= 0; i-- {
			orchestrator.DetachLiveDep(liveStack[i], projectRoot, rootID, v, stdout, stderr)
		}
	}()

	for _, dep := range order[:len(order)-1] {
		depExecMap, _ := dep.JSON["execution"].(map[string]interface{})
		blocking, _ := depExecMap["blocking"].(bool)
		if blocking {
			depID, aerr := orchestrator.AttachLiveDep(context.Background(), dep, projectRoot, rootID, os.Getpid(), v, stdout, stderr)
			if aerr != nil {
				fmt.Fprintln(stderr, "error: attach live dep:", aerr)
				return 1
			}
			liveStack = append(liveStack, depID)
			depSignals[dep.ID] = map[string]interface{}{"verdict": "pass"}
			continue
		}
		if blocker := orchestrator.FirstFailedDep(dep, depSignals); blocker != nil {
			cascade := orchestrator.BuildCascadeSignal(dep, blocker)
			if err := v.Validate(schema.TargetSignal, cascade); err != nil {
				schema.PrintValidationOrPlain(err, stderr)
				return 1
			}
			_ = json.NewEncoder(stdout).Encode(cascade)
			depSignals[dep.ID] = cascade
			continue
		}
		sig, depCode := orchestrator.RunOne(context.Background(), dep, schemasDir, v, stdout, stderr)
		if depCode != 0 {
			return depCode
		}
		depSignals[dep.ID] = sig
	}

	// If any dep of the requested sensor failed, cascade and stop.
	requested := order[len(order)-1]
	if blocker := orchestrator.FirstFailedDep(requested, depSignals); blocker != nil {
		cascade := orchestrator.BuildCascadeSignal(requested, blocker)
		if err := v.Validate(schema.TargetSignal, cascade); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(cascade)
		return 0
	}

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

	execMap := sensorJSON["execution"].(map[string]interface{})
	command, _ := execMap["command"].(string)
	userTemplate, _ := execMap["user_prompt_template"].(string)
	rendered, missing := template.RenderTemplate(userTemplate, bindings)
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "error: unbound slots: %s (provide via --slot key=value)\n", strings.Join(missing, ", "))
		return 1
	}

	patterns, code := compilePatternsForInferential(execMap, stderr)
	if code != 0 {
		return code
	}

	timeoutMS := int(asNumber(sensorJSON["cost"].(map[string]interface{})["latency"].(map[string]interface{})["timeout_ms"]))

	res, _ := subprocess.StreamSubprocess(context.Background(), subprocess.StreamConfig{
		Command:   command,
		Env:       map[string]string{"HARNESS_PROMPT": rendered},
		TimeoutMS: timeoutMS,
		Patterns:  patterns,
		Envelope:  envelope,
		Validator: v,
		Stdout:    stdout,
		Stderr:    stderr,
	})

	confidence, individuals := extractConfidenceAndFilter(res.Individuals, 1.0)

	// Honour a user-declared execution.exit_code_map when present; otherwise
	// fall back to the default inferential mapping (exit 0 -> pass/info,
	// non-zero -> error/high). The schema currently does not forbid
	// exit_code_map on inferential sensors, so a sensor author can override
	// this fallback when their LLM CLI uses non-trivial exit semantics.
	ecMap, _ := execMap["exit_code_map"].([]interface{})
	var exitVerd, exitSev string
	if len(ecMap) > 0 {
		exitVerd, exitSev = signal.MapExitCode(res.ExitCode, ecMap)
	} else {
		exitVerd, exitSev = defaultInferentialExit(res.ExitCode)
	}
	streamVerd, streamSev := signal.MaxStreamVerdict(individuals)
	agg := signal.Aggregate(signal.AggregateInput{
		ExitVerdict:    exitVerd,
		ExitSeverity:   exitSev,
		StreamVerdict:  streamVerd,
		StreamSeverity: streamSev,
		TimedOut:       res.TimedOut,
	})

	downgrade := false
	if cal, ok := sensorJSON["calibration"].(map[string]interface{}); ok {
		thresh, _ := cal["confidence_threshold"].(float64)
		if agg.Verdict == "fail" && confidence < thresh {
			agg.Verdict = "warn"
			agg.Severity = "low"
			downgrade = true
		}
	}

	sig := buildAggregateSignal(envelope, res, individuals, agg, command, output, confidence, downgrade)
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		schema.PrintValidationOrPlain(err, stderr)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(sig)
	return 0
}

// compilePatternsForInferential compiles the user-declared patterns and
// appends an internal "magic" pattern that catches HARNESS_AGGREGATE_CONFIDENCE
// lines so the runner can extract them later. Returns ([]Pattern, exit-code).
func compilePatternsForInferential(execMap map[string]interface{}, stderr io.Writer) ([]signal.Pattern, int) {
	var raw []interface{}
	if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
		raw, _ = op["patterns"].([]interface{})
	}
	patterns, err := signal.CompilePatterns(raw)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return nil, 1
	}
	// Append the internal HARNESS_AGGREGATE_CONFIDENCE catcher LAST so user
	// patterns take precedence; the unique prefix makes collisions unlikely.
	magic, err := signal.CompilePatterns([]interface{}{
		map[string]interface{}{
			"regex":    "^" + harnessConfidencePrefix,
			"verdict":  "pass",
			"severity": "info",
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, "error: internal pattern:", err)
		return nil, 1
	}
	return append(patterns, magic...), 0
}

// extractConfidenceAndFilter walks individuals, extracts the first
// HARNESS_AGGREGATE_CONFIDENCE=<float> line's value, and returns the parsed
// confidence (or fallback) plus a slice with magic individuals removed.
func extractConfidenceAndFilter(individuals []map[string]interface{}, fallback float64) (float64, []map[string]interface{}) {
	conf := fallback
	out := make([]map[string]interface{}, 0, len(individuals))
	for _, s := range individuals {
		md, _ := s["metadata"].(map[string]interface{})
		line, _ := md["line"].(string)
		if strings.HasPrefix(line, harnessConfidencePrefix) {
			if val, err := strconv.ParseFloat(strings.TrimPrefix(line, harnessConfidencePrefix), 64); err == nil {
				conf = val
			}
			continue
		}
		out = append(out, s)
	}
	return conf, out
}

func parseSlots(raw []string) (map[string]string, error) {
	out := map[string]string{}
	for _, s := range raw {
		i := strings.IndexByte(s, '=')
		if i <= 0 {
			return nil, fmt.Errorf("slot %q is not key=value", s)
		}
		out[s[:i]] = s[i+1:]
	}
	return out, nil
}

func buildAggregateSignal(env sensor.Envelope, res subprocess.StreamResult, filteredIndividuals []map[string]interface{}, agg signal.AggregateResult, command, outputMode string, confidence float64, downgrade bool) map[string]interface{} {
	finished := sensor.NowFn().Format("2006-01-02T15:04:05Z")
	evidence := signal.SelectTopEvidence(filteredIndividuals, 20)
	md := map[string]interface{}{
		"kind":        "aggregate",
		"output_mode": outputMode,
		"command":     command,
		"exit_code":   res.ExitCode,
		"timed_out":   res.TimedOut,
		"counts":      signal.CountVerdicts(filteredIndividuals),
	}
	if downgrade {
		md["calibration_downgrade"] = true
	}
	return map[string]interface{}{
		"sensor_id":   env.SensorID,
		"version":     env.Version,
		"run_id":      env.RunID,
		"started_at":  env.StartedAt,
		"finished_at": finished,
		"verdict":     agg.Verdict,
		"severity":    agg.Severity,
		"confidence":  confidence,
		"evidence":    evidence,
		"cost_actual": map[string]interface{}{"latency_ms": res.ElapsedMS},
		"metadata":    md,
	}
}

// defaultInferentialExit is the fallback exit-code mapping used when the
// sensor does not declare an explicit execution.exit_code_map. LLM CLIs
// typically exit 0 for every emitted judgment regardless of verdict; only a
// crash or a CLI-internal error should surface a non-zero exit. When a sensor
// declares its own exit_code_map, that takes precedence.
func defaultInferentialExit(code int) (verdict, severity string) {
	if code == 0 {
		return "pass", "info"
	}
	return "error", "high"
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
