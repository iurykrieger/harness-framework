// Package lib holds the deterministic runtime shared by every run-sensor
// script. Scripts in the parent directory (run-computational.go,
// run-inferential.go) are thin CLI wrappers; all path resolution, schema
// validation, envelope construction, exit-code mapping, slot substitution,
// calibration, and signal assembly lives here.
package lib

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Schema URLs for cross-file $ref resolution.
const (
	schemaBaseURL = "https://harness-framework/schemas/"
	sensorURL     = schemaBaseURL + "sensor.json"
	signalURL     = schemaBaseURL + "signal.json"
)

// NowFn and NewRunIDFn are package-level overrideable hooks so tests can pin
// timestamps and run ids.
var (
	NowFn      = func() time.Time { return time.Now().UTC() }
	NewRunIDFn = NewUUIDv4
)

// Target identifies which schema an instance is checked against.
type Target string

const (
	TargetSensor Target = "sensor"
	TargetSignal Target = "signal"
)

// Validator holds the compiled sensor and signal schemas with cross-file
// $ref already resolved.
type Validator struct {
	sensor *jsonschema.Schema
	signal *jsonschema.Schema
}

// NewValidator loads sensor.json and signal.json from schemasDir.
func NewValidator(schemasDir string) (*Validator, error) {
	sensorBytes, err := os.ReadFile(filepath.Join(schemasDir, "sensor.json"))
	if err != nil {
		return nil, fmt.Errorf("read sensor.json: %w", err)
	}
	signalBytes, err := os.ReadFile(filepath.Join(schemasDir, "signal.json"))
	if err != nil {
		return nil, fmt.Errorf("read signal.json: %w", err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource(signalURL, strings.NewReader(string(signalBytes))); err != nil {
		return nil, fmt.Errorf("register signal schema: %w", err)
	}
	if err := c.AddResource(sensorURL, strings.NewReader(string(sensorBytes))); err != nil {
		return nil, fmt.Errorf("register sensor schema: %w", err)
	}
	sensor, err := c.Compile(sensorURL)
	if err != nil {
		return nil, fmt.Errorf("compile sensor schema: %w", err)
	}
	signal, err := c.Compile(signalURL)
	if err != nil {
		return nil, fmt.Errorf("compile signal schema: %w", err)
	}
	return &Validator{sensor: sensor, signal: signal}, nil
}

// Validate runs the schema for target against instance.
func (v *Validator) Validate(target Target, instance interface{}) error {
	switch target {
	case TargetSensor:
		return v.sensor.Validate(instance)
	case TargetSignal:
		return v.signal.Validate(instance)
	default:
		return fmt.Errorf("unknown target %q", target)
	}
}

// FindSchemasDir walks up from start looking for schemas/sensor.json + schemas/signal.json.
func FindSchemasDir(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(abs, "schemas")
		if hasFile(filepath.Join(candidate, "sensor.json")) && hasFile(filepath.Join(candidate, "signal.json")) {
			return candidate, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("schemas directory not found by walking up from %s", start)
		}
		abs = parent
	}
}

func hasFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// PrintValidationError writes an indented rendering of the error tree.
func PrintValidationError(w io.Writer, err *jsonschema.ValidationError, indent string) {
	path := err.InstanceLocation
	if path == "" {
		path = "<root>"
	}
	fmt.Fprintf(w, "%sINVALID at %s: %s\n", indent, path, err.Message)
	for _, c := range err.Causes {
		PrintValidationError(w, c, indent+"  ")
	}
}

// PrintValidationOrPlain prints an indented validation tree if err is a
// jsonschema.ValidationError; otherwise it prints err.Error().
func PrintValidationOrPlain(err error, stderr io.Writer) {
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		PrintValidationError(stderr, ve, "")
	} else {
		fmt.Fprintln(stderr, "INVALID:", err)
	}
}

// ResolveSensorPath strips a leading @, makes the path absolute (relative to
// baseDir), and verifies the file exists.
func ResolveSensorPath(arg, baseDir string) (string, error) {
	arg = strings.TrimPrefix(arg, "@")
	if arg == "" {
		return "", errors.New("empty path")
	}
	if !filepath.IsAbs(arg) {
		arg = filepath.Join(baseDir, arg)
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// Envelope is the run-scoped Signal scaffold.
type Envelope struct {
	SensorID   string `json:"sensor_id"`
	Version    string `json:"version"`
	RunID      string `json:"run_id"`
	StartedAt  string `json:"started_at"`
	SensorType string `json:"sensor_type"`
}

// BuildEnvelope constructs an envelope from a parsed sensor JSON.
func BuildEnvelope(sensor map[string]interface{}) (Envelope, error) {
	id, _ := sensor["id"].(string)
	version, _ := sensor["version"].(string)
	sensorType, _ := sensor["type"].(string)
	if id == "" || version == "" || sensorType == "" {
		return Envelope{}, errors.New("sensor missing id/version/type")
	}
	return Envelope{
		SensorID:   id,
		Version:    version,
		RunID:      NewRunIDFn(),
		StartedAt:  NowFn().Format("2006-01-02T15:04:05Z"),
		SensorType: sensorType,
	}, nil
}

// MapExitCode resolves an exit code via sensor.execution.exit_code_map.
// "*" is the wildcard fallback. Returns ("error", "high") if no entry matches
// and no wildcard is present.
func MapExitCode(code int, ecMap []interface{}) (verdict, severity string) {
	var fallbackV, fallbackS string
	haveFallback := false
	for _, item := range ecMap {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		switch ec := m["exit_code"].(type) {
		case float64:
			if int(ec) == code {
				v, _ := m["verdict"].(string)
				s, _ := m["severity"].(string)
				return v, s
			}
		case string:
			if ec == "*" {
				fallbackV, _ = m["verdict"].(string)
				fallbackS, _ = m["severity"].(string)
				haveFallback = true
			}
		}
	}
	if haveFallback {
		return fallbackV, fallbackS
	}
	return "error", "high"
}

// slotPattern matches {{slot_name}} (whitespace tolerated).
var slotPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

// RenderTemplate substitutes {{slot}} placeholders. Returns the rendered text
// and the deduplicated list of slots that were referenced but not bound.
func RenderTemplate(tmpl string, bindings map[string]string) (string, []string) {
	var missing []string
	seen := map[string]bool{}
	rendered := slotPattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		key := slotPattern.FindStringSubmatch(match)[1]
		if val, ok := bindings[key]; ok {
			return val
		}
		if !seen[key] {
			seen[key] = true
			missing = append(missing, key)
		}
		return match
	})
	return rendered, missing
}

// MultiFlag implements flag.Value for repeatable string flags.
type MultiFlag []string

func (m *MultiFlag) String() string     { return strings.Join(*m, ",") }
func (m *MultiFlag) Set(s string) error { *m = append(*m, s); return nil }

// NewUUIDv4 generates a RFC 4122 v4 UUID without external dependencies.
func NewUUIDv4() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// LoadValidator resolves schemasDir (walks up if empty) and returns a Validator.
func LoadValidator(schemasDir string, stderr io.Writer) (*Validator, int) {
	if schemasDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(stderr, "error: getwd:", err)
			return nil, 2
		}
		d, err := FindSchemasDir(cwd)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return nil, 2
		}
		schemasDir = d
	}
	v, err := NewValidator(schemasDir)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return nil, 2
	}
	return v, 0
}

// LoadAndValidateSensor resolves the path argument, reads, parses, and
// schema-validates the sensor JSON. Returns sensor, abs path, exit code (0 ok).
func LoadAndValidateSensor(arg, schemasDir string, stderr io.Writer) (map[string]interface{}, string, int) {
	cwd, _ := os.Getwd()
	sensorPath, err := ResolveSensorPath(arg, cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error: resolve:", err)
		return nil, "", 2
	}
	v, code := LoadValidator(schemasDir, stderr)
	if code != 0 {
		return nil, "", code
	}
	var sensor map[string]interface{}
	if code := readJSONFile(sensorPath, &sensor, stderr); code != 0 {
		return nil, "", code
	}
	if err := v.Validate(TargetSensor, sensor); err != nil {
		PrintValidationOrPlain(err, stderr)
		return nil, "", 1
	}
	return sensor, sensorPath, 0
}

func readJSONFile(path string, dst interface{}, stderr io.Writer) int {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, "error: read:", err)
		return 2
	}
	if err := json.Unmarshal(b, dst); err != nil {
		fmt.Fprintln(stderr, "error: parse:", err)
		return 2
	}
	return 0
}

func writeJSON(stdout io.Writer, v interface{}, stderr io.Writer) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	return 0
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

// ExecuteComputational runs a computational sensor end-to-end and writes the
// validated Signal to stdout. This is the full deterministic pipeline.
func ExecuteComputational(sensorPathArg, schemasDir string, stdout, stderr io.Writer) int {
	sensor, _, code := LoadAndValidateSensor(sensorPathArg, schemasDir, stderr)
	if code != 0 {
		return code
	}
	if t, _ := sensor["type"].(string); t != "computational" {
		fmt.Fprintf(stderr, "error: sensor.type=%q (run-computational requires 'computational')\n", t)
		return 2
	}
	v, code := LoadValidator(schemasDir, stderr)
	if code != 0 {
		return code
	}
	env, err := BuildEnvelope(sensor)
	if err != nil {
		fmt.Fprintln(stderr, "error: envelope:", err)
		return 2
	}

	execMap := sensor["execution"].(map[string]interface{})
	command, _ := execMap["command"].(string)
	cmdParts := strings.Fields(command)
	if len(cmdParts) == 0 {
		fmt.Fprintln(stderr, "error: execution.command is empty")
		return 2
	}
	timeoutMs := int64(asNumber(sensor["cost"].(map[string]interface{})["latency"].(map[string]interface{})["timeout_ms"]))
	timeout := time.Duration(timeoutMs) * time.Millisecond

	envList := os.Environ()
	if envObj, ok := execMap["env"].(map[string]interface{}); ok {
		for k, val := range envObj {
			envList = append(envList, fmt.Sprintf("%s=%v", k, val))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, cmdParts[0], cmdParts[1:]...)
	cmd.Env = envList
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	runErr := cmd.Run()
	elapsedMs := int(time.Since(start) / time.Millisecond)
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)

	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	verdict, severity := "error", "high"
	if !timedOut {
		ecMap, _ := execMap["exit_code_map"].([]interface{})
		verdict, severity = MapExitCode(exitCode, ecMap)
	}

	combined := stderrBuf.String() + stdoutBuf.String()
	if len(combined) > 2048 {
		combined = combined[:2048] + "...(truncated)"
	}
	rationale := combined
	if rationale == "" {
		switch {
		case timedOut:
			rationale = fmt.Sprintf("command exceeded timeout (%dms)", timeoutMs)
		case runErr != nil:
			rationale = runErr.Error()
		default:
			rationale = "(no output)"
		}
	}

	signal := map[string]interface{}{
		"sensor_id":   env.SensorID,
		"version":     env.Version,
		"run_id":      env.RunID,
		"started_at":  env.StartedAt,
		"finished_at": NowFn().Format("2006-01-02T15:04:05Z"),
		"verdict":     verdict,
		"severity":    severity,
		"confidence":  1.0,
		"evidence": []interface{}{
			map[string]interface{}{"rationale": rationale},
		},
		"cost_actual": map[string]interface{}{"latency_ms": elapsedMs},
	}

	if err := v.Validate(TargetSignal, signal); err != nil {
		PrintValidationOrPlain(err, stderr)
		return 1
	}
	return writeJSON(stdout, signal, stderr)
}

// ExecuteInferential runs an inferential sensor end-to-end via a single HTTP
// call to the Anthropic Messages API. Reads ANTHROPIC_API_KEY from env
// (passed in apiKey). Slot bindings populate {{slot}} placeholders in the
// user_prompt_template; missing slots are an error.
//
// apiBase defaults to "https://api.anthropic.com" but can be overridden for
// testing (httptest.Server).
func ExecuteInferential(sensorPathArg, schemasDir string, slots map[string]string, apiBase, apiKey string, stdout, stderr io.Writer) int {
	sensor, _, code := LoadAndValidateSensor(sensorPathArg, schemasDir, stderr)
	if code != 0 {
		return code
	}
	if t, _ := sensor["type"].(string); t != "inferential" {
		fmt.Fprintf(stderr, "error: sensor.type=%q (run-inferential requires 'inferential')\n", t)
		return 2
	}
	v, code := LoadValidator(schemasDir, stderr)
	if code != 0 {
		return code
	}
	env, err := BuildEnvelope(sensor)
	if err != nil {
		fmt.Fprintln(stderr, "error: envelope:", err)
		return 2
	}

	execMap := sensor["execution"].(map[string]interface{})
	systemPrompt, _ := execMap["system_prompt"].(string)
	userTemplate, _ := execMap["user_prompt_template"].(string)
	rendered, missing := RenderTemplate(userTemplate, slots)
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "error: unbound slots: %s (provide via --slot key=value)\n", strings.Join(missing, ", "))
		return 1
	}

	model, _ := execMap["model"].(string)
	model = strings.TrimPrefix(model, "anthropic/")
	if model == "" || strings.Contains(model, "/") {
		fmt.Fprintf(stderr, "error: only anthropic/* models supported (got %q)\n", execMap["model"])
		return 2
	}
	decoding, _ := execMap["decoding"].(map[string]interface{})
	maxTokens := int(asNumber(decoding["max_tokens"]))
	temperature := asNumber(decoding["temperature"])

	start := time.Now()
	resp, err := callAnthropicMessages(apiBase, apiKey, model, systemPrompt, rendered, maxTokens, temperature)
	elapsedMs := int(time.Since(start) / time.Millisecond)
	if err != nil {
		fmt.Fprintln(stderr, "error: anthropic call:", err)
		return 2
	}

	// The model is instructed (via the sensor's system_prompt) to emit a JSON
	// object describing the variable parts of the Signal. Parse it.
	var llmOut map[string]interface{}
	if err := json.Unmarshal([]byte(resp.text), &llmOut); err != nil {
		fmt.Fprintf(stderr, "error: model output is not valid JSON: %v\noutput:\n%s\n", err, resp.text)
		return 1
	}

	// Apply calibration: downgrade fail→warn when confidence < threshold.
	confThresh := 0.0
	if cal, ok := sensor["calibration"].(map[string]interface{}); ok {
		if t, ok := cal["confidence_threshold"].(float64); ok {
			confThresh = t
		}
	}
	verdict, _ := llmOut["verdict"].(string)
	confidence, _ := llmOut["confidence"].(float64)
	metadata, _ := llmOut["metadata"].(map[string]interface{})
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	if verdict == "fail" && confidence < confThresh {
		verdict = "warn"
		metadata["calibration_downgrade"] = true
	}

	final := map[string]interface{}{
		"sensor_id":   env.SensorID,
		"version":     env.Version,
		"run_id":      env.RunID,
		"started_at":  env.StartedAt,
		"finished_at": NowFn().Format("2006-01-02T15:04:05Z"),
		"verdict":     verdict,
		"severity":    llmOut["severity"],
		"confidence":  llmOut["confidence"],
		"evidence":    llmOut["evidence"],
	}
	if score, ok := llmOut["score"]; ok && score != nil {
		final["score"] = score
	}
	if rem, ok := llmOut["remediation"]; ok && rem != nil {
		final["remediation"] = rem
	}
	costActual, _ := llmOut["cost_actual"].(map[string]interface{})
	if costActual == nil {
		costActual = map[string]interface{}{}
	}
	if _, ok := costActual["latency_ms"]; !ok {
		costActual["latency_ms"] = elapsedMs
	}
	if _, ok := costActual["input_tokens"]; !ok && resp.inputTokens > 0 {
		costActual["input_tokens"] = resp.inputTokens
	}
	if _, ok := costActual["output_tokens"]; !ok && resp.outputTokens > 0 {
		costActual["output_tokens"] = resp.outputTokens
	}
	if _, ok := costActual["model"]; !ok {
		costActual["model"] = "anthropic/" + model
	}
	final["cost_actual"] = costActual
	if len(metadata) > 0 {
		final["metadata"] = metadata
	}

	if err := v.Validate(TargetSignal, final); err != nil {
		PrintValidationOrPlain(err, stderr)
		return 1
	}
	return writeJSON(stdout, final, stderr)
}

// anthropicResponse holds the bits of the Messages API response we care about.
type anthropicResponse struct {
	text         string
	inputTokens  int
	outputTokens int
}

// callAnthropicMessages POSTs to <apiBase>/v1/messages and returns the
// concatenated text of the first assistant response.
func callAnthropicMessages(apiBase, apiKey, model, system, user string, maxTokens int, temperature float64) (anthropicResponse, error) {
	body := map[string]interface{}{
		"model":      model,
		"max_tokens": maxTokens,
		"messages": []map[string]string{
			{"role": "user", "content": user},
		},
	}
	if system != "" {
		body["system"] = system
	}
	if temperature > 0 {
		body["temperature"] = temperature
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return anthropicResponse{}, err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(apiBase, "/")+"/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return anthropicResponse{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return anthropicResponse{}, err
	}
	defer httpResp.Body.Close()
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return anthropicResponse{}, err
	}
	if httpResp.StatusCode >= 400 {
		return anthropicResponse{}, fmt.Errorf("anthropic api %d: %s", httpResp.StatusCode, string(respBody))
	}
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return anthropicResponse{}, fmt.Errorf("parse api response: %w (body=%s)", err, string(respBody))
	}
	var sb strings.Builder
	for _, c := range parsed.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return anthropicResponse{
		text:         sb.String(),
		inputTokens:  parsed.Usage.InputTokens,
		outputTokens: parsed.Usage.OutputTokens,
	}, nil
}
