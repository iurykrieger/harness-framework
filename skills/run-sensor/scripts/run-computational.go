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

	"github.com/iurykrieger/harness-framework/lib"
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

	sensor, _, code := lib.LoadAndValidateSensor(rest[0], schemasDir, stderr)
	if code != 0 {
		return code
	}
	if t, _ := sensor["type"].(string); t != "computational" {
		fmt.Fprintf(stderr, "error: sensor.type=%q (run-computational requires 'computational')\n", t)
		return 2
	}
	v, code := lib.LoadValidator(schemasDir, stderr)
	if code != 0 {
		return code
	}
	envelope, err := lib.BuildEnvelope(sensor)
	if err != nil {
		fmt.Fprintln(stderr, "error: envelope:", err)
		return 2
	}

	execMap := sensor["execution"].(map[string]interface{})
	command, _ := execMap["command"].(string)

	var patterns []lib.Pattern
	if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
		raw, _ := op["patterns"].([]interface{})
		patterns, err = lib.CompilePatterns(raw)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
	}

	timeoutMS := int(asNumber(sensor["cost"].(map[string]interface{})["latency"].(map[string]interface{})["timeout_ms"]))

	envExtra := map[string]string{}
	if envObj, ok := execMap["env"].(map[string]interface{}); ok {
		for k, val := range envObj {
			envExtra[k] = fmt.Sprintf("%v", val)
		}
	}

	res, _ := lib.StreamSubprocess(context.Background(), lib.StreamConfig{
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
	exitVerd, exitSev := lib.MapExitCode(res.ExitCode, ecMap)
	streamVerd, streamSev := lib.MaxStreamVerdict(res.Individuals)
	agg := lib.Aggregate(lib.AggregateInput{
		ExitVerdict:    exitVerd,
		ExitSeverity:   exitSev,
		StreamVerdict:  streamVerd,
		StreamSeverity: streamSev,
		TimedOut:       res.TimedOut,
	})

	signal := buildAggregateSignal(envelope, res, agg, command)
	if err := v.Validate(lib.TargetSignal, signal); err != nil {
		lib.PrintValidationOrPlain(err, stderr)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(signal)
	return 0
}

func buildAggregateSignal(env lib.Envelope, res lib.StreamResult, agg lib.AggregateResult, command string) map[string]interface{} {
	finished := lib.NowFn().Format("2006-01-02T15:04:05Z")
	evidence := lib.SelectTopEvidence(res.Individuals, 20)
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
		"metadata": map[string]interface{}{
			"kind":      "aggregate",
			"command":   command,
			"exit_code": res.ExitCode,
			"timed_out": res.TimedOut,
			"counts":    lib.CountVerdicts(res.Individuals),
		},
	}
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
