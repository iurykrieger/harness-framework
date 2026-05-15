//go:build !error_autofiler

// hooks/setup-failure-detector.go
//
// Claude Code Stop hook that classifies the most-recent /run-sensor,
// /start-sensor, or /stop-sensor aggregate Signal in the conversation
// transcript. On setup-shaped failure, emits additionalContext on
// stdout instructing the LLM to invoke /heal-sensor.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/heal"
	"github.com/iurykrieger/harness-framework/lib/heal/rules"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/transcript"
)

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr))
}

type hookInput struct {
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
}

var sensorCommands = map[string]bool{
	"/run-sensor":   true,
	"/start-sensor": true,
	"/stop-sensor":  true,
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

	entries, err := transcript.Scan(in.TranscriptPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 0
	}

	scan, ok := findFailingInvocation(entries, in.Cwd)
	if !ok {
		return 0
	}
	if anyHealAfter(entries[scan.index+1:]) {
		return 0
	}

	failed, err := loadFailedSensorView(scan.SensorPath)
	if err != nil {
		return 0
	}

	res, matched := heal.ClassifyWith(rules.Registered(), scan.Signal, failed)
	if !matched {
		return 0
	}
	emitInjection(stdout, scan, failed.ID, res)
	return 0
}

type scanResult struct {
	Signal              heal.Signal
	SensorPath          string
	Command             string
	OriginalRequestedID string
	index               int // index into entries[] of the tool_result that produced this Signal
}

// findFailingInvocation walks entries backward and returns the most
// recent sensor-command failure that warrants heal classification.
func findFailingInvocation(entries []transcript.Entry, cwd string) (scanResult, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		for _, tr := range entries[i].ToolResults() {
			lines := strings.Split(strings.TrimRight(tr.ResultText(), "\n"), "\n")
			if len(lines) == 0 {
				continue
			}
			var sigMap map[string]interface{}
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &sigMap); err != nil {
				continue
			}
			md, _ := sigMap["metadata"].(map[string]interface{})
			if md == nil {
				continue
			}
			kind, _ := md["kind"].(string)

			inv := findSensorInvocation(entries[:i], cwd)
			if inv.SensorPath == "" {
				continue
			}

			var sensorPath, originalRequestedID string
			switch kind {
			case "aggregate", "start_failed", "failed":
				sensorPath = inv.SensorPath
			case "cascade":
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
				sensorPath = filepath.Join(filepath.Dir(inv.SensorPath), failedDepID+".yaml")
			default:
				continue
			}
			return scanResult{
				Signal:              signalFromMap(sigMap),
				SensorPath:          sensorPath,
				Command:             inv.Command,
				OriginalRequestedID: originalRequestedID,
				index:               i,
			}, true
		}
	}
	return scanResult{}, false
}

func findDepAggregate(lines []string, failedDepID string) (map[string]interface{}, bool) {
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

type sensorInvocation struct {
	Command    string
	SensorPath string
}

// findSensorInvocation walks entries backward for the most recent
// sensor slash command (preferred) or, as a fallback, a Bash tool_use
// whose command literally contains "/run-sensor " etc. The slash-command
// path handles user-typed invocations; the fallback covers agent-driven
// invocations from sub-agents.
func findSensorInvocation(entries []transcript.Entry, cwd string) sensorInvocation {
	for i := len(entries) - 1; i >= 0; i-- {
		// Preferred: slash-command form in a user entry.
		name, args, ok := transcript.ParseSlashCommand(entries[i].Text())
		if ok && sensorCommands[name] {
			arg := firstPositionalArg(strings.Fields(args))
			if arg == "" {
				continue
			}
			return sensorInvocation{
				Command:    name,
				SensorPath: resolveSensorTarget(arg, cwd),
			}
		}
		// Fallback: literal "/run-sensor X" string inside any block.
		for cmd := range sensorCommands {
			needle := cmd + " "
			text := entries[i].Text()
			if idx := strings.Index(text, needle); idx >= 0 {
				rest := strings.Fields(text[idx+len(needle):])
				arg := firstPositionalArg(rest)
				if arg == "" {
					continue
				}
				return sensorInvocation{Command: cmd, SensorPath: resolveSensorTarget(arg, cwd)}
			}
			// Also look inside assistant tool_use Bash commands.
			for _, tu := range entries[i].ToolUses() {
				if tu.Name != "Bash" {
					continue
				}
				var ti struct{ Command string }
				_ = json.Unmarshal(tu.Input, &ti)
				if strings.Contains(ti.Command, needle) {
					idx := strings.Index(ti.Command, needle)
					rest := strings.Fields(ti.Command[idx+len(needle):])
					arg := firstPositionalArg(rest)
					if arg != "" {
						return sensorInvocation{Command: cmd, SensorPath: resolveSensorTarget(arg, cwd)}
					}
				}
			}
		}
	}
	return sensorInvocation{}
}

func firstPositionalArg(parts []string) string {
	for _, p := range parts {
		if strings.HasPrefix(p, "-") {
			continue
		}
		return p
	}
	return ""
}

func resolveSensorTarget(arg, cwd string) string {
	arg = strings.TrimPrefix(arg, "@")
	if strings.ContainsAny(arg, "/\\") || strings.HasSuffix(arg, ".yaml") || strings.HasSuffix(arg, ".yml") {
		return arg
	}
	if cwd == "" {
		return arg
	}
	return filepath.Join(cwd, ".harness", "sensors", arg+".yaml")
}

func anyHealAfter(entries []transcript.Entry) bool {
	for _, e := range entries {
		if name, _, ok := transcript.ParseSlashCommand(e.Text()); ok && name == "/heal-sensor" {
			return true
		}
		if strings.Contains(e.Text(), "/heal-sensor") {
			return true
		}
	}
	return false
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
	body, err := schema.ReadAsJSON(path)
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
