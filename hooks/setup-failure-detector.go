//go:build !error_autofiler

// hooks/setup-failure-detector.go
//
// Claude Code Stop hook that classifies the most-recent
// /run-sensor, /start-sensor, or /stop-sensor aggregate Signal in the
// conversation transcript. On setup-shaped failure, emits
// additionalContext on stdout instructing the LLM to invoke
// /heal-sensor. On no-match (passing run, sensor-design failure,
// already-healed-this-turn), prints nothing.
//
// Input (JSON on stdin):
//
//	{ "transcript_path": "/abs/path/to/transcript.jsonl", ... }
//
// Output (JSON on stdout, when triggering):
//
//	{
//	  "hookSpecificOutput": {
//	    "hookEventName": "Stop",
//	    "additionalContext": "..."
//	  }
//	}
//
// Exit codes: 0 always (per Claude Code hook protocol; signal nothing
// to do via empty stdout) except 2 for usage errors.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/heal"
	"github.com/iurykrieger/harness-framework/lib/heal/rules"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr))
}

type hookInput struct {
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
}

// sensorCommands lists the slash commands whose failures the hook
// classifies for self-healing. /tail-sensor and /list-sensors are
// excluded — they are read-only and never produce setup-shaped
// failures.
var sensorCommands = []string{"/run-sensor", "/start-sensor", "/stop-sensor"}

type transcriptEntry struct {
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
	Input   json.RawMessage `json:"input"`
}

func run(stdin io.Reader, stdout, stderr io.Writer) int {
	body, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "read stdin:", err)
		return 2
	}
	var in hookInput
	if err := json.Unmarshal(body, &in); err != nil {
		fmt.Fprintln(stderr, "parse hook input:", err)
		return 2
	}
	if in.TranscriptPath == "" {
		fmt.Fprintln(stderr, "transcript_path missing")
		return 2
	}

	scan, ok := scanTranscript(in.TranscriptPath, in.Cwd)
	if !ok {
		return 0
	}
	if scan.AlreadyHealed {
		return 0
	}

	failed, err := loadFailedSensorView(scan.SensorPath)
	if err != nil {
		// Sensor file gone or unreadable — no-op rather than crash.
		return 0
	}

	res, matched := heal.ClassifyWith(rules.Registered(), scan.Signal, failed)
	if !matched {
		return 0
	}
	emitInjection(stdout, scan, failed.ID, res)
	return 0
}

// scanResult bundles everything scanTranscript discovers about the most
// recent sensor-command failure that warrants heal: the Signal to
// classify on, the sensor file path to target, whether heal already
// ran, the slash command that produced the failure, and (for cascades)
// which originally-requested sensor caused the chain.
type scanResult struct {
	Signal              heal.Signal
	SensorPath          string // path of the sensor whose Signal we classify
	AlreadyHealed       bool
	Command             string // "/run-sensor", "/start-sensor", or "/stop-sensor"
	OriginalRequestedID string // requested sensor's id, when ≠ SensorPath's owner (cascade)
}

// scanTranscript walks the JSONL transcript backward, finds the most
// recent /run-sensor, /start-sensor, or /stop-sensor aggregate Signal,
// and reports whether a subsequent /heal-sensor invocation already
// happened in this turn. cwd is the user's working directory (passed
// by Claude Code in the hook input) and is used to resolve bare
// sensor ids to their on-disk paths.
func scanTranscript(path, cwd string) (scanResult, bool) {
	f, err := os.Open(path)
	if err != nil {
		return scanResult{}, false
	}
	defer f.Close()

	var entries []transcriptEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for scanner.Scan() {
		var e transcriptEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}

	// Walk backward: find the most recent tool_result whose last JSONL
	// line parses as an aggregate Signal AND whose preceding context
	// shows a /run-sensor invocation.
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Type != "tool_result" {
			continue
		}
		content := contentText(e.Content)
		if content == "" {
			continue
		}
		lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
		var sigMap map[string]interface{}
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &sigMap); err != nil {
			continue
		}
		md, _ := sigMap["metadata"].(map[string]interface{})
		if md == nil {
			continue
		}
		kind, _ := md["kind"].(string)

		// Find the matching sensor-command invocation in earlier
		// entries. We always need it to derive sensors_dir
		// (filepath.Dir) and to label which command failed.
		invocation := findSensorInvocation(entries[:i], cwd)
		if invocation.SensorPath == "" {
			continue
		}
		originalSensorPath := invocation.SensorPath

		var sensorPath, originalRequestedID string
		switch kind {
		case "aggregate", "start_failed":
			// /run-sensor and /stop-sensor produce metadata.kind=aggregate;
			// /start-sensor produces metadata.kind=start_failed when prepare,
			// schema validation, or fork+exec fails before the sensor is
			// registered. Both are candidates for setup-shape healing.
			sensorPath = originalSensorPath
		case "cascade":
			// Walk backward through earlier JSONL lines in the same
			// tool_result content to find the dep's real aggregate
			// matching metadata.failed_dep_id.
			failedDepID, _ := md["failed_dep_id"].(string)
			if failedDepID == "" {
				continue
			}
			depMap, found := findDepAggregate(lines, failedDepID)
			if !found {
				continue
			}
			originalRequestedID, _ = sigMap["sensor_id"].(string)
			sigMap = depMap
			sensorPath = filepath.Join(filepath.Dir(originalSensorPath), failedDepID+".json")
		default:
			continue
		}

		return scanResult{
			Signal:              signalFromMap(sigMap),
			SensorPath:          sensorPath,
			AlreadyHealed:       anyHealAfter(entries[i+1:]),
			Command:             invocation.Command,
			OriginalRequestedID: originalRequestedID,
		}, true
	}
	return scanResult{}, false
}

// findDepAggregate walks the JSONL stream of one tool_result content
// (excluding the final cascade line) backward, returning the first
// aggregate Signal whose sensor_id matches failedDepID and whose verdict
// is error or fail.
func findDepAggregate(lines []string, failedDepID string) (map[string]interface{}, bool) {
	// Skip the last line (the cascade itself) and walk backward.
	for i := len(lines) - 2; i >= 0; i-- {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(lines[i]), &m); err != nil {
			continue
		}
		md, _ := m["metadata"].(map[string]interface{})
		if md == nil || md["kind"] != "aggregate" {
			continue
		}
		sid, _ := m["sensor_id"].(string)
		if sid != failedDepID {
			continue
		}
		v, _ := m["verdict"].(string)
		if v != "error" && v != "fail" {
			continue
		}
		return m, true
	}
	return nil, false
}


// sensorInvocation captures the most recent slash-command invocation
// that may have produced a failing aggregate Signal: which command
// (/run-sensor, /start-sensor, /stop-sensor) and the resolved sensor
// file path.
type sensorInvocation struct {
	Command    string
	SensorPath string
}

// findSensorInvocation walks the transcript backward and returns the
// most recent /run-sensor, /start-sensor, or /stop-sensor invocation,
// resolving its first non-flag argument to a sensor file path.
//
// Path-style arguments (containing "/" or "\", or starting with "@",
// or ending in ".json") are returned with the leading "@" stripped.
// Bare-id arguments (e.g., "watch-logs") are resolved against
// <cwd>/sensors/<id>.json.
func findSensorInvocation(entries []transcriptEntry, cwd string) sensorInvocation {
	for i := len(entries) - 1; i >= 0; i-- {
		content := contentText(entries[i].Content)
		for _, cmd := range sensorCommands {
			if !strings.Contains(content, cmd+" ") {
				continue
			}
			parts := strings.Fields(content)
			for j, p := range parts {
				if p != cmd {
					continue
				}
				arg := firstPositionalArg(parts[j+1:])
				if arg == "" {
					break
				}
				return sensorInvocation{
					Command:    cmd,
					SensorPath: resolveSensorTarget(arg, cwd),
				}
			}
		}
	}
	return sensorInvocation{}
}

// firstPositionalArg returns the first element of parts that doesn't
// start with "-" (i.e., is not a flag). Returns "" if none.
func firstPositionalArg(parts []string) string {
	for _, p := range parts {
		if strings.HasPrefix(p, "-") {
			continue
		}
		return p
	}
	return ""
}

// resolveSensorTarget converts a slash-command argument into an on-disk
// path. Path-shaped inputs (containing separators, ending in .json, or
// "@"-prefixed) pass through; bare ids are resolved to
// <cwd>/.harness/sensors/<id>.json.
func resolveSensorTarget(arg, cwd string) string {
	arg = strings.TrimPrefix(arg, "@")
	if strings.ContainsAny(arg, "/\\") || strings.HasSuffix(arg, ".json") {
		return arg
	}
	if cwd == "" {
		return arg // best-effort fallback; loadFailedSensorView will fail and the hook silently no-ops.
	}
	return filepath.Join(cwd, ".harness", "sensors", arg+".json")
}

func anyHealAfter(entries []transcriptEntry) bool {
	for _, e := range entries {
		if strings.Contains(contentText(e.Content), "/heal-sensor") {
			return true
		}
	}
	return false
}

// contentText extracts text from a Claude Code transcript entry's
// content field, which may serialize as either a plain JSON string or
// an array of {type, text, ...} content blocks.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return s
	}
	var arr []map[string]interface{}
	if err := json.Unmarshal(raw, &arr); err == nil {
		var b strings.Builder
		for _, item := range arr {
			if item["type"] == "text" {
				if t, ok := item["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	return ""
}

func signalFromMap(m map[string]interface{}) heal.Signal {
	var s heal.Signal
	if v, ok := m["verdict"].(string); ok {
		s.Verdict = v
	}
	if v, ok := m["severity"].(string); ok {
		s.Severity = v
	}
	if ev, ok := m["evidence"].([]interface{}); ok {
		for _, e := range ev {
			em, _ := e.(map[string]interface{})
			if em == nil {
				continue
			}
			r, _ := em["rationale"].(string)
			ex, _ := em["excerpt"].(string)
			s.Evidence = append(s.Evidence, heal.SignalEvidence{Rationale: r, Excerpt: ex})
		}
	}
	if md, ok := m["metadata"].(map[string]interface{}); ok {
		if h, ok := md["heal_hint"].(string); ok {
			s.Metadata.HealHint = h
		}
		if ec, ok := md["exit_code"].(float64); ok {
			n := int(ec)
			s.Metadata.ExitCode = &n
		}
		if lc, ok := md["lifecycle"].(map[string]interface{}); ok {
			if pp, ok := lc["prepare"].([]interface{}); ok {
				for _, p := range pp {
					pm, _ := p.(map[string]interface{})
					if pm == nil {
						continue
					}
					cmd, _ := pm["command"].(string)
					vrd, _ := pm["verdict"].(string)
					s.Metadata.Lifecycle.Prepare = append(s.Metadata.Lifecycle.Prepare, heal.SignalLifecycleStep{Command: cmd, Verdict: vrd})
				}
			}
		}
	}
	return s
}

func loadFailedSensorView(path string) (heal.FailedSensor, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return heal.FailedSensor{}, err
	}
	var s map[string]interface{}
	if err := json.Unmarshal(body, &s); err != nil {
		return heal.FailedSensor{}, err
	}
	id, _ := s["id"].(string)

	var envs []string
	for _, e := range sensor.Project(s, "env") {
		if name, ok := e["name"].(string); ok && name != "" {
			envs = append(envs, name)
		}
	}
	var tools []string
	for _, t := range sensor.Project(s, "tool") {
		if name, ok := t["name"].(string); ok && name != "" {
			tools = append(tools, name)
		}
	}
	var contexts []string
	for _, c := range sensor.Project(s, "context") {
		if p, ok := c["path"].(string); ok && p != "" {
			contexts = append(contexts, p)
		}
	}
	return heal.FailedSensor{ID: id, EnvNames: envs, Tools: tools, Context: contexts}, nil
}

func emitInjection(stdout io.Writer, scan scanResult, sensorID string, res heal.Result) {
	cmd := scan.Command
	if cmd == "" {
		cmd = "/run-sensor"
	}
	var msg string
	if scan.OriginalRequestedID != "" && scan.OriginalRequestedID != sensorID {
		msg = fmt.Sprintf(
			"The previous %s invocation for sensor %q failed because dep %q failed setup-shape (rule=%s, shape=%s, detail=%q). Invoke `/heal-sensor --signal-from=transcript --sensor=%s` to heal the dep before reporting the failure to the user.",
			cmd, scan.OriginalRequestedID, sensorID, res.Rule, res.Shape, res.Detail, scan.SensorPath,
		)
	} else {
		msg = fmt.Sprintf(
			"The previous %s invocation for sensor %q produced a setup-shaped failure (rule=%s, shape=%s, detail=%q). Invoke `/heal-sensor --signal-from=transcript --sensor=%s` to attempt automatic recovery before reporting the failure to the user.",
			cmd, sensorID, res.Rule, res.Shape, res.Detail, scan.SensorPath,
		)
	}
	out := map[string]interface{}{
		"hookSpecificOutput": map[string]interface{}{
			"hookEventName":     "Stop",
			"additionalContext": msg,
		},
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
