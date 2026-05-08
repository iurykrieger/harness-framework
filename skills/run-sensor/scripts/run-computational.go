//go:build run_computational

// Command run-computational runs a streaming computational sensor end-to-end.
//
// Usage:
//
//	go run -tags=run_computational ./skills/run-sensor/scripts <sensor-path>
//
// Stdout is JSONL: one Signal per matched output line, terminated by the
// aggregate Signal. Exit codes: 0 ok (Signals printed), 1 schema/pattern
// failure, 2 usage or I/O error.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/subprocess"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run-computational", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var schemasDir string
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: run-computational [--schemas-dir=DIR] <sensor-path>")
		return 2
	}

	sensorJSON, _, code := sensor.LoadAndValidateSensor(rest[0], schemasDir, stderr)
	if code != 0 {
		return code
	}
	if t, _ := sensorJSON["type"].(string); t != "computational" {
		fmt.Fprintf(stderr, "error: sensor.type=%q (run-computational requires 'computational')\n", t)
		return 2
	}
	output, _ := sensorJSON["output"].(string)
	v, code := schema.LoadValidator(schemasDir, stderr)
	if code != 0 {
		return code
	}
	envelope, err := sensor.BuildEnvelope(sensorJSON)
	if err != nil {
		fmt.Fprintln(stderr, "error: envelope:", err)
		return 2
	}

	if missing := sensor.CheckRequiredEnv(sensorJSON); len(missing) > 0 {
		output, _ := sensorJSON["output"].(string)
		sig := sensor.BuildErrorSignal(envelope, output, missingEnvRationale(missing), missingEnvRemediation(missing))
		if err := v.Validate(schema.TargetSignal, sig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(sig)
		return 0
	}

	execMap := sensorJSON["execution"].(map[string]interface{})
	command, _ := execMap["command"].(string)
	longRunning, _ := execMap["long_running"].(bool)

	var patterns []signal.Pattern
	if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
		raw, _ := op["patterns"].([]interface{})
		patterns, err = signal.CompilePatterns(raw)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
	}

	timeoutMS := int(asNumber(sensorJSON["cost"].(map[string]interface{})["latency"].(map[string]interface{})["timeout_ms"]))

	envExtra := map[string]string{}
	if envObj, ok := execMap["env"].(map[string]interface{}); ok {
		for k, val := range envObj {
			envExtra[k] = fmt.Sprintf("%v", val)
		}
	}

	res, _ := subprocess.StreamSubprocess(context.Background(), subprocess.StreamConfig{
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

	sig := buildAggregateSignal(envelope, res, agg, command, output, longRunning)
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		schema.PrintValidationOrPlain(err, stderr)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(sig)
	return 0
}

func buildAggregateSignal(env sensor.Envelope, res subprocess.StreamResult, agg signal.AggregateResult, command, outputMode string, longRunning bool) map[string]interface{} {
	finished := sensor.NowFn().Format("2006-01-02T15:04:05Z")
	evidence := signal.SelectTopEvidence(res.Individuals, 20)
	md := map[string]interface{}{
		"kind":        "aggregate",
		"output_mode": outputMode,
		"command":     command,
		"exit_code":   res.ExitCode,
		"timed_out":   res.TimedOut,
		"counts":      signal.CountVerdicts(res.Individuals),
	}
	if longRunning {
		md["long_running"] = true
	}
	return map[string]interface{}{
		"sensor_id":   env.SensorID,
		"version":     env.Version,
		"run_id":      env.RunID,
		"started_at":  env.StartedAt,
		"finished_at": finished,
		"verdict":     agg.Verdict,
		"severity":    agg.Severity,
		"confidence":  1.0,
		"evidence":    evidence,
		"cost_actual": map[string]interface{}{"latency_ms": res.ElapsedMS},
		"metadata":    md,
	}
}

func missingEnvRationale(missing []sensor.MissingEnv) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sensor cannot run: %d required env var(s) missing from the runner's environment.\n", len(missing))
	for _, m := range missing {
		if m.Description != "" {
			fmt.Fprintf(&b, "  - %s — %s\n", m.Name, m.Description)
		} else {
			fmt.Fprintf(&b, "  - %s\n", m.Name)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func missingEnvRemediation(missing []sensor.MissingEnv) string {
	names := make([]string, 0, len(missing))
	for _, m := range missing {
		names = append(names, m.Name)
	}
	return "Set the following env var(s) before invoking /run-sensor: " + strings.Join(names, ", ") + ". Source them from your shell, a .env file, or the secret manager backing this project."
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
