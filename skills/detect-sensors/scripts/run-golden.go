//go:build run_golden

// Command run-golden iterates a sensor's verification.golden_cases[],
// invokes the standard runner (run-computational for type=computational,
// run-inferential for type=inferential) once per case, captures the
// aggregate Signal (the LAST JSONL line on stdout), and compares verdict
// and severity against expected_verdict / expected_severity.
//
// Usage:
//
//	go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=run_golden \
//	  ./skills/detect-sensors/scripts --sensor=PATH
//
// Sensor path may also be provided via HARNESS_SENSOR_PATH for callers
// that prefer env-var plumbing.
//
// Exit codes: 0 every case agreed with its expectations, 1 first
// mismatch (verdict or severity), 2 usage or I/O error before any
// runner spawn.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func main() {
	fs := flag.NewFlagSet("run-golden", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var sensorPath string
	fs.StringVar(&sensorPath, "sensor", "", "path to the sensor YAML (required; falls back to HARNESS_SENSOR_PATH)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if sensorPath == "" {
		sensorPath = os.Getenv("HARNESS_SENSOR_PATH")
	}
	if sensorPath == "" {
		fmt.Fprintln(os.Stderr, "usage: run-golden --sensor=PATH (or set HARNESS_SENSOR_PATH)")
		os.Exit(2)
	}
	os.Exit(runGolden(sensorPath))
}

// runGolden is the testable entry point. Returns 0 on full agreement,
// 1 on the first verdict/severity mismatch (or a runner error that
// prevents producing an aggregate), and 2 on usage / I/O failure
// before any runner spawn.
func runGolden(sensorPath string) int {
	v, err := newValidator()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: load validator:", err)
		return 2
	}
	s, err := sensor.Load(sensorPath, v)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: load sensor:", err)
		return 2
	}
	if len(s.Verification.GoldenCases) == 0 {
		fmt.Fprintln(os.Stderr, "error: sensor declares no golden_cases")
		return 2
	}
	tag := runnerTag(s.Type)
	if tag == "" {
		fmt.Fprintf(os.Stderr, "error: unsupported sensor.type %q\n", s.Type)
		return 2
	}
	pluginRoot := os.Getenv("CLAUDE_PLUGIN_ROOT")
	if pluginRoot == "" {
		fmt.Fprintln(os.Stderr, "error: CLAUDE_PLUGIN_ROOT not set")
		return 2
	}
	// Canonical layout: <projectRoot>/.harness/sensors/<id>.yaml. The
	// standard runner discovers the project root via HARNESS_REGISTRY_ROOT.
	absSensor, err := filepath.Abs(sensorPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: abs sensor path:", err)
		return 2
	}
	projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(absSensor)))

	for i, gc := range s.Verification.GoldenCases {
		verdict, severity, err := invokeRunner(pluginRoot, projectRoot, tag, s.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "golden[%d] (%s): runner error: %v\n", i, gc.Fixture, err)
			return 1
		}
		if verdict != gc.ExpectedVerdict {
			fmt.Fprintf(os.Stderr,
				"golden[%d] (%s) verdict mismatch: got %q want %q\n",
				i, gc.Fixture, verdict, gc.ExpectedVerdict)
			return 1
		}
		if severity != gc.ExpectedSeverity {
			fmt.Fprintf(os.Stderr,
				"golden[%d] (%s) severity mismatch: got %q want %q\n",
				i, gc.Fixture, severity, gc.ExpectedSeverity)
			return 1
		}
	}
	fmt.Fprintf(os.Stdout, "all %d golden case(s) passed\n", len(s.Verification.GoldenCases))
	return 0
}

// runnerTag maps a sensor type to the matching go-run build tag. Returns
// "" for unsupported types so the caller can emit a clean usage error.
func runnerTag(t sensor.Type) string {
	switch t {
	case sensor.TypeComputational:
		return "run_computational"
	case sensor.TypeInferential:
		return "run_inferential"
	}
	return ""
}

// invokeRunner spawns `go run -C <pluginRoot> -tags=<tag>
// ./skills/run-sensor/scripts <sensorID>` with HARNESS_REGISTRY_ROOT
// pointed at the user's project tree, captures stdout, and returns the
// aggregate Signal's verdict/severity (the LAST JSONL line on stdout).
//
// The runner's exit code is intentionally ignored: a fail-verdict
// aggregate is normal output that the caller still needs to inspect.
// An I/O failure that prevents an aggregate from landing on stdout is
// reported via the err return.
func invokeRunner(pluginRoot, projectRoot, tag, sensorID string) (verdict, severity string, err error) {
	cmd := exec.Command("go", "run", "-C", pluginRoot, "-tags="+tag,
		"./skills/run-sensor/scripts", sensorID)
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(),
		"HARNESS_REGISTRY_ROOT="+projectRoot,
		"GOWORK=off",
	)
	stdout, stderr := captureBuffers()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	_ = cmd.Run() // exit code intentionally ignored; verdict drives outcome
	return parseAggregate(stdout.String(), stderr.String())
}

// parseAggregate extracts the LAST non-empty JSONL line on stdout and
// decodes its top-level verdict and severity. stderr is woven into the
// error so failed runs surface useful context to the test author.
func parseAggregate(stdout, stderr string) (verdict, severity string, err error) {
	trimmed := strings.TrimRight(stdout, "\n")
	if trimmed == "" {
		return "", "", fmt.Errorf("runner produced no stdout; stderr=%s", stderr)
	}
	lines := strings.Split(trimmed, "\n")
	last := lines[len(lines)-1]
	var agg map[string]interface{}
	if jerr := json.Unmarshal([]byte(last), &agg); jerr != nil {
		return "", "", fmt.Errorf("decode aggregate %q: %w; stderr=%s", last, jerr, stderr)
	}
	v, _ := agg["verdict"].(string)
	s, _ := agg["severity"].(string)
	if v == "" {
		return "", "", fmt.Errorf("aggregate missing verdict; raw=%s; stderr=%s", last, stderr)
	}
	return v, s, nil
}

// captureBuffers returns matched stdout/stderr capture sinks. Factored
// out so a future test can swap them for tee-style writers without
// touching invokeRunner.
func captureBuffers() (stdout, stderr *stringSink) {
	return &stringSink{}, &stringSink{}
}

// stringSink is a minimal io.Writer that accumulates bytes and exposes
// them via String. Kept local to avoid pulling bytes.Buffer's full API
// just to grow a string.
type stringSink struct{ buf []byte }

func (s *stringSink) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	return len(p), nil
}
func (s *stringSink) String() string { return string(s.buf) }

// newValidator constructs a schema validator pointed at the in-repo
// schemas/ tree. CLAUDE_PLUGIN_ROOT (set by the standard `go run -C
// ${CLAUDE_PLUGIN_ROOT} -tags=run_golden` invocation) names the plugin
// checkout; the validator loads sensor.yaml + signal.yaml + stack.yaml
// + usecase.yaml from <root>/schemas. HARNESS_SCHEMAS_DIR is an opt-in
// override for callers that need to validate against a non-canonical
// schema dir.
func newValidator() (*schema.Validator, error) {
	dir := os.Getenv("HARNESS_SCHEMAS_DIR")
	if dir == "" {
		if root := os.Getenv("CLAUDE_PLUGIN_ROOT"); root != "" {
			dir = filepath.Join(root, "schemas")
		}
	}
	if dir == "" {
		return nil, fmt.Errorf("no schemas dir: set CLAUDE_PLUGIN_ROOT or HARNESS_SCHEMAS_DIR")
	}
	return schema.NewValidator(dir)
}

// Compile-time guard: keep the io.Writer assertion in the binary so
// stringSink stays interchangeable with bytes.Buffer for *exec.Cmd's
// Stdout/Stderr fields.
var _ io.Writer = (*stringSink)(nil)
