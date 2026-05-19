//go:build validate_usecase

// Command validate-usecase orchestrates the per-usecase sensor bundle at
// .harness/sensors/<usecase-id>/, runs every layer entrypoint in
// alphabetical order, and emits a single confidence-report Signal on stdout.
//
// Usage:
//
//	go run -tags=validate_usecase ./skills/validate-usecase/scripts <usecase-id>
//
// Stdout: exactly one JSONL Signal whose metadata.confidence_report carries
// ceiling/coverage/realized counts, per-layer verdicts, and ratios.
// Exit codes:
//
//	0 — report emitted (verdict pass/warn/fail/error baked into Signal).
//	1 — usage or I/O error (error Signal emitted before exit).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	layerPkg "github.com/iurykrieger/harness-framework/lib/planning/layer"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

// verdictRow records the outcome of one layer entrypoint.
type verdictRow struct {
	Layer      sensor.Layer `json:"layer"`
	Verdict    string       `json:"verdict"`
	SensorID   string       `json:"sensor_id"`
	FinishedAt string       `json:"finished_at,omitempty"`
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		emitSig(stdout, signal.NewBuilder("validate-usecase", "0.1.0").
			WithVerdict("error", "high").
			WithKind("usage").
			WithRationale("usage: validate-usecase <usecase-id>").
			Build())
		return 1
	}
	usecaseID := strings.TrimSpace(args[0])

	cwd, _ := os.Getwd()
	res, err := registry.Lookup(cwd)
	if err != nil {
		emitSig(stdout, registry.DiscoveryErrorSignal(err, "validate-usecase"))
		return 1
	}
	projectRoot := res.ProjectRoot

	// Load the schema validator (empty schemasDir = auto-discover).
	var validatorErrBuf bytes.Buffer
	v, code := schema.LoadValidator("", &validatorErrBuf)
	if code != 0 {
		msg := strings.TrimSpace(validatorErrBuf.String())
		emitSig(stdout, signal.NewBuilder("validate-usecase", "0.1.0").
			WithVerdict("error", "high").
			WithKind("schema_validator_init_failed").
			WithRationale("schema validator init failed: "+msg).
			Build())
		return 1
	}

	// Load stack.yaml for applicability computation.
	stackMap := loadStack(projectRoot)
	st := decodeStack(stackMap)

	// Load the usecase for applicability computation.
	uc, ucErr := loadUsecase(projectRoot, usecaseID)
	if ucErr != nil {
		emitSig(stdout, signal.NewBuilder("validate-usecase", "0.1.0").
			WithVerdict("error", "high").
			WithKind("usecase_not_found").
			WithRationale(fmt.Sprintf("usecase %q not found under .harness/usecases/: %v", usecaseID, ucErr)).
			Build())
		return 1
	}

	// Walk .harness/sensors/<usecase-id>/ and group sensors by layer.
	bundleDir := filepath.Join(projectRoot, ".harness", "sensors", usecaseID)
	layerToSensors := walkBundle(bundleDir, v)

	// Identify the entrypoint for each layer: composite > solo.
	layerToEntrypoint := pickEntrypoints(layerToSensors)

	// Compute ceiling = count of applicable layers across all registered recipes.
	allLayers := layerPkg.AllLayers()
	var applicable []sensor.Layer
	var notApplicable []string
	for _, l := range allLayers {
		recipe := layerPkg.Get(l)
		if recipe == nil {
			continue
		}
		ok, reason := recipe.Applicable(st, uc, nil)
		if ok {
			applicable = append(applicable, l)
		} else {
			msg := string(l)
			if reason != "" {
				msg = string(l) + ": " + reason
			}
			notApplicable = append(notApplicable, msg)
		}
	}
	ceiling := len(applicable)
	coverage := len(layerToEntrypoint)

	// Run each entrypoint in alphabetical (layer slug) order.
	entrypointLayers := make([]sensor.Layer, 0, len(layerToEntrypoint))
	for l := range layerToEntrypoint {
		entrypointLayers = append(entrypointLayers, l)
	}
	sort.Slice(entrypointLayers, func(i, j int) bool {
		return string(entrypointLayers[i]) < string(entrypointLayers[j])
	})

	var verdicts []verdictRow
	for _, l := range entrypointLayers {
		ep := layerToEntrypoint[l]
		var runStdout, runStderr bytes.Buffer
		_ = orchestrator.RunWithDepsAtRoot(context.Background(), ep.path, projectRoot, "", &runStdout, &runStderr)
		verdict, finishedAt := lastAggregateVerdict(runStdout.Bytes())
		verdicts = append(verdicts, verdictRow{
			Layer:      l,
			Verdict:    verdict,
			SensorID:   ep.s.ID,
			FinishedAt: finishedAt,
		})
	}

	// Tally realized (pass count).
	realized := 0
	for _, r := range verdicts {
		if r.Verdict == "pass" {
			realized++
		}
	}

	aggregateVerdict := worstResult(verdicts)
	if coverage == 0 {
		emitSig(stdout, signal.NewBuilder("validate-usecase", "0.1.0").
			WithVerdict("error", "high").
			WithKind("no_coverage").
			WithRationale(fmt.Sprintf("usecase %q: no sensors found under .harness/sensors/%s/", usecaseID, usecaseID)).
			Build())
		return 1
	}

	report := map[string]any{
		"usecase_id":  usecaseID,
		"computed_at": time.Now().UTC().Format(time.RFC3339),
		"ceiling": map[string]any{
			"value":          ceiling,
			"applicable":     stringer(applicable),
			"not_applicable": notApplicable,
		},
		"coverage": map[string]any{
			"value":     coverage,
			"generated": layerIDs(verdicts),
		},
		"realized": map[string]any{
			"value":          realized,
			"layer_verdicts": verdicts,
		},
		"ratios": map[string]any{
			"completeness":       safeDiv(coverage, ceiling),
			"pass_rate":          safeDiv(realized, coverage),
			"executed_pass_rate": safeDiv(realized, coverage),
			"confidence":         safeDiv(realized, ceiling),
		},
		"aggregate_verdict": aggregateVerdict,
	}

	severity := "info"
	if aggregateVerdict == "fail" || aggregateVerdict == "error" {
		severity = "high"
	}

	sig := signal.NewBuilder("validate-usecase", "0.1.0").
		WithVerdict(aggregateVerdict, severity).
		WithKind("confidence_report").
		WithMetadata(map[string]interface{}{"confidence_report": report}).
		Build()
	emitSig(stdout, sig)
	return 0
}

// entrypoint pairs a loaded sensor with its on-disk path.
type entrypoint struct {
	s    sensor.Sensor
	path string
}

// walkBundle walks bundleDir and groups sensors by Layer. Files that fail
// schema validation are silently skipped.
func walkBundle(bundleDir string, v *schema.Validator) map[sensor.Layer][]entrypoint {
	out := map[sensor.Layer][]entrypoint{}
	_ = filepath.Walk(bundleDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".yaml") {
			return nil
		}
		s, loadErr := sensor.Load(path, v)
		if loadErr != nil {
			return nil // skip invalid sensors
		}
		if s.Layer == "" {
			return nil // skip sensors with no layer (core sensors at root level)
		}
		out[s.Layer] = append(out[s.Layer], entrypoint{s: *s, path: path})
		return nil
	})
	return out
}

// pickEntrypoints selects one entrypoint per layer: composite (has a type:sensor step) wins; otherwise the first sensor alphabetically.
func pickEntrypoints(layerToSensors map[sensor.Layer][]entrypoint) map[sensor.Layer]entrypoint {
	out := map[sensor.Layer]entrypoint{}
	for l, eps := range layerToSensors {
		// Sort for determinism.
		sort.Slice(eps, func(i, j int) bool { return eps[i].s.ID < eps[j].s.ID })
		chosen := eps[0]
		for _, ep := range eps {
			if isComposite(ep.s) {
				chosen = ep
				break
			}
		}
		out[l] = chosen
	}
	return out
}

// isComposite returns true when the sensor has at least one type:sensor step.
func isComposite(s sensor.Sensor) bool {
	for _, st := range s.Execution.Steps {
		if st.Type == "sensor" {
			return true
		}
	}
	return false
}

// worstResult returns the aggregate verdict from a set of rows.
func worstResult(rows []verdictRow) string {
	hasError := false
	hasFail := false
	hasWarn := false
	for _, r := range rows {
		switch r.Verdict {
		case "error":
			hasError = true
		case "fail":
			hasFail = true
		case "warn":
			hasWarn = true
		}
	}
	switch {
	case hasError:
		return "error"
	case hasFail:
		return "fail"
	case hasWarn:
		return "warn"
	}
	return "pass"
}

// lastAggregateVerdict walks lines in reverse to find the last valid JSONL
// Signal and extracts its verdict and finished_at. Falls back to ("error","")
// when no valid Signal is found.
func lastAggregateVerdict(b []byte) (verdict, finishedAt string) {
	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		v, _ := m["verdict"].(string)
		if v == "" {
			continue
		}
		fa, _ := m["finished_at"].(string)
		return v, fa
	}
	return "error", ""
}

// safeDiv divides n by d, returning 0 when d is zero.
func safeDiv(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// stringer converts a slice of Layer to a slice of string.
func stringer(layers []sensor.Layer) []string {
	out := make([]string, len(layers))
	for i, l := range layers {
		out[i] = string(l)
	}
	return out
}

// layerIDs extracts the Layer field of each verdictRow as a string slice.
func layerIDs(rows []verdictRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = string(r.Layer)
	}
	return out
}

// loadStack reads .harness/stack.yaml and returns the raw map. Returns nil
// when the file is absent or unreadable.
func loadStack(projectRoot string) map[string]any {
	body, err := os.ReadFile(filepath.Join(projectRoot, ".harness", "stack.yaml"))
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := yaml.Unmarshal(body, &out); err != nil {
		return nil
	}
	return out
}

// decodeStack normalizes a map[string]any into a typed stack.Stack via JSON round-trip.
func decodeStack(m map[string]any) stack.Stack {
	if m == nil {
		return stack.Stack{}
	}
	body, _ := json.Marshal(m)
	var out stack.Stack
	_ = json.Unmarshal(body, &out)
	return out
}

// loadUsecase walks .harness/usecases/ to find <usecaseID>.yaml and decodes it.
func loadUsecase(projectRoot, usecaseID string) (usecase.UseCase, error) {
	usecasesRoot := filepath.Join(projectRoot, ".harness", "usecases")
	var found string
	_ = filepath.Walk(usecasesRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if strings.TrimSuffix(info.Name(), ".yaml") == usecaseID {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		return usecase.UseCase{}, fmt.Errorf("not found")
	}
	body, err := os.ReadFile(found)
	if err != nil {
		return usecase.UseCase{}, err
	}
	var uc usecase.UseCase
	if err := yaml.Unmarshal(body, &uc); err != nil {
		return usecase.UseCase{}, err
	}
	return uc, nil
}

// emitSig writes a Signal as a JSONL line on stdout.
func emitSig(w io.Writer, sig map[string]interface{}) {
	body, _ := json.Marshal(sig)
	fmt.Fprintln(w, string(body))
}
