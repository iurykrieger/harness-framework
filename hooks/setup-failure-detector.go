// hooks/setup-failure-detector.go
//
// Claude Code Stop hook that classifies the most-recent /run-sensor
// aggregate Signal in the conversation transcript. On setup-shaped
// failure, emits additionalContext on stdout instructing the LLM to
// invoke /heal-sensor. On no-match (passing run, sensor-design
// failure, already-healed-this-turn), prints nothing.
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
)

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr))
}

type hookInput struct {
	TranscriptPath string `json:"transcript_path"`
}

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

	scan, ok := scanTranscript(in.TranscriptPath)
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
// recent /run-sensor failure that warrants heal: the Signal to classify
// on, the sensor file path to target, whether heal already ran, and
// (for cascades) which originally-requested sensor caused the chain.
type scanResult struct {
	Signal              heal.Signal
	SensorPath          string // path of the sensor whose Signal we classify
	AlreadyHealed       bool
	OriginalRequestedID string // requested sensor's id, when ≠ SensorPath's owner (cascade)
}

// scanTranscript walks the JSONL transcript backward, finds the most
// recent /run-sensor aggregate Signal, and reports whether a
// subsequent /heal-sensor invocation already happened in this turn.
func scanTranscript(path string) (scanResult, bool) {
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

		// Find the matching /run-sensor invocation in earlier entries.
		// We always need it to derive sensors_dir (filepath.Dir).
		originalSensorPath := findRunSensorTarget(entries[:i])
		if originalSensorPath == "" {
			continue
		}

		var sensorPath, originalRequestedID string
		switch kind {
		case "aggregate":
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


func findRunSensorTarget(entries []transcriptEntry) string {
	// Walk backward: nearest user message containing "/run-sensor <path>".
	for i := len(entries) - 1; i >= 0; i-- {
		content := contentText(entries[i].Content)
		if strings.Contains(content, "/run-sensor ") {
			parts := strings.Fields(content)
			for j, p := range parts {
				if p == "/run-sensor" && j+1 < len(parts) {
					return strings.TrimPrefix(parts[j+1], "@")
				}
			}
		}
	}
	return ""
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

type sensorRequiresView struct {
	ID       string
	Requires struct {
		Env []struct {
			Name string `json:"name"`
		} `json:"env"`
		Tools   []string `json:"tools"`
		Context []string `json:"context"`
	} `json:"requires"`
}

func loadFailedSensorView(path string) (heal.FailedSensor, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return heal.FailedSensor{}, err
	}
	var v struct {
		ID       string `json:"id"`
		Requires struct {
			Env []struct {
				Name string `json:"name"`
			} `json:"env"`
			Tools   []string `json:"tools"`
			Context []string `json:"context"`
		} `json:"requires"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return heal.FailedSensor{}, err
	}
	envs := make([]string, 0, len(v.Requires.Env))
	for _, e := range v.Requires.Env {
		envs = append(envs, e.Name)
	}
	return heal.FailedSensor{ID: v.ID, EnvNames: envs, Tools: v.Requires.Tools, Context: v.Requires.Context}, nil
}

func emitInjection(stdout io.Writer, scan scanResult, sensorID string, res heal.Result) {
	var msg string
	if scan.OriginalRequestedID != "" && scan.OriginalRequestedID != sensorID {
		msg = fmt.Sprintf(
			"The previous /run-sensor invocation for sensor %q failed because dep %q failed setup-shape (rule=%s, shape=%s, detail=%q). Invoke `/heal-sensor --signal-from=transcript --sensor=%s` to heal the dep before reporting the failure to the user.",
			scan.OriginalRequestedID, sensorID, res.Rule, res.Shape, res.Detail, scan.SensorPath,
		)
	} else {
		msg = fmt.Sprintf(
			"The previous /run-sensor invocation for sensor %q produced a setup-shaped failure (rule=%s, shape=%s, detail=%q). Invoke `/heal-sensor --signal-from=transcript --sensor=%s` to attempt automatic recovery before reporting the failure to the user.",
			sensorID, res.Rule, res.Shape, res.Detail, scan.SensorPath,
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
