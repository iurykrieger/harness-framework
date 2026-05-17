package sensor

import (
	"fmt"
	"regexp"
	"sort"
)

// maxSensorRefDepth caps the type:sensor / requires[kind=sensor] graph
// traversal. DFS deeper than this is reported as an error (rule 8), even
// when no back-edge is present, on the rationale that arbitrarily deep
// inline composition is indistinguishable from a cycle for cost-tracking
// and recursion-budget purposes.
const maxSensorRefDepth = 5

// Validate runs every cross-field rule that JSON Schema cannot enforce
// alone (see docs/superpowers/specs/2026-05-15-complex-commands-design.md
// "Validation rules"). Rule 11 (requires[kind=sensor] / type:sensor ref
// overlap) is non-fatal: it is appended to s.Warnings rather than
// returned as an error.
//
// peers is the universe of sensors visible to the caller, keyed by id.
// It is consulted only by rules 8 (cycle detection) and 9 (sensor-ref
// to blocking child). When peers is nil, those rules degrade to
// best-effort: cycles and blocking refs that cross sensor files cannot
// be detected, but every other rule still applies. Callers with project
// context (orchestrator, /detect-sensors phase 7) should populate peers;
// unit tests of a single sensor in isolation may pass nil.
func Validate(s *Sensor, peers map[string]*Sensor) error {
	rules := []func(*Sensor, map[string]*Sensor) error{
		ruleOutputSingleNoParse,
		ruleOutputStreamWithParse,
		ruleBlockingNotWithSteps,
		ruleStepIDsUnique,
		ruleWithFixturesExist,
		ruleInterpolationOrder,
		ruleInterpolationDeclared,
		ruleSensorCycles,
		ruleSensorRefNotBlocking,
		ruleAssertNoWith,
	}
	for _, r := range rules {
		if err := r(s, peers); err != nil {
			return err
		}
	}
	s.Warnings = append(s.Warnings, sensorOverlapWarnings(s)...)
	return nil
}

// ----------------------------------------------------------------------------
// Rule 1: output: single forbids any step with parse:.
// ----------------------------------------------------------------------------

func ruleOutputSingleNoParse(s *Sensor, _ map[string]*Sensor) error {
	if s.Output != OutputSingle {
		return nil
	}
	for _, st := range s.Execution.Steps {
		if st.Parse != nil && len(st.Parse.Patterns) > 0 {
			return fmt.Errorf("sensor %q: output: single is incompatible with step %q parse: block (a single-output sensor emits exactly one Signal; per-line parsing requires output: stream)", s.ID, st.ID)
		}
	}
	return nil
}

// ----------------------------------------------------------------------------
// Rule 2: output: stream + steps: requires at least one shell step with parse:.
// (The command shortcut is normalized into a synthetic step by Load, so its
// existing rule — output: stream → execution.output_parsing.patterns required —
// still applies at schema level.)
// ----------------------------------------------------------------------------

func ruleOutputStreamWithParse(s *Sensor, _ map[string]*Sensor) error {
	if s.Output != OutputStream {
		return nil
	}
	if len(s.Execution.Steps) == 0 {
		return nil
	}
	// Skip when the sensor originated from a command: shortcut — Load
	// already mapped that case through output_parsing.patterns which the
	// JSON Schema enforces. Detect by the synthetic "main" id used by
	// normalizeCommandShortcut combined with a non-empty Execution.Command.
	if s.Execution.Command != "" && len(s.Execution.Steps) == 1 && s.Execution.Steps[0].ID == "main" {
		return nil
	}
	for _, st := range s.Execution.Steps {
		if st.Type == "shell" && st.Parse != nil && len(st.Parse.Patterns) > 0 {
			return nil
		}
	}
	return fmt.Errorf("sensor %q: output: stream with steps: requires at least one shell step with a parse: block declaring how lines map to verdicts", s.ID)
}

// ----------------------------------------------------------------------------
// Rule 3: execution.blocking: true + steps: → error.
// ----------------------------------------------------------------------------

func ruleBlockingNotWithSteps(s *Sensor, _ map[string]*Sensor) error {
	if !s.Execution.Blocking {
		return nil
	}
	// A command-shortcut sensor gets a synthetic single "main" step from
	// Load; that is not a real authored steps: declaration. Detect and
	// allow it so blocking command sensors keep working.
	if s.Execution.Command != "" && len(s.Execution.Steps) == 1 && s.Execution.Steps[0].ID == "main" {
		return nil
	}
	if len(s.Execution.Steps) > 0 {
		return fmt.Errorf("sensor %q: execution.blocking: true is incompatible with steps: (blocking sensors run a single long-lived command, not a multi-step pipeline)", s.ID)
	}
	return nil
}

// ----------------------------------------------------------------------------
// Rule 4: duplicate step ids → error.
// ----------------------------------------------------------------------------

func ruleStepIDsUnique(s *Sensor, _ map[string]*Sensor) error {
	seen := map[string]struct{}{}
	for _, st := range s.Execution.Steps {
		if _, dup := seen[st.ID]; dup {
			return fmt.Errorf("sensor %q: duplicate step id %q (every execution.steps[].id must be unique within the sensor)", s.ID, st.ID)
		}
		seen[st.ID] = struct{}{}
	}
	return nil
}

// ----------------------------------------------------------------------------
// Rule 5: with: { fixture: X } where X is absent from the fixture pool → error.
// ----------------------------------------------------------------------------

func ruleWithFixturesExist(s *Sensor, _ map[string]*Sensor) error {
	for _, st := range s.Execution.Steps {
		if st.With == nil {
			continue
		}
		raw, ok := st.With["fixture"]
		if !ok {
			continue
		}
		name, ok := raw.(string)
		if !ok || name == "" {
			continue
		}
		if _, present := s.Fixtures[name]; !present {
			return fmt.Errorf("sensor %q step %q: with.fixture %q is not present under .harness/fixtures/ (run /detect-sensors after dropping the fixture into the pool, or correct the name to match an existing file)", s.ID, st.ID, name)
		}
	}
	return nil
}

// ----------------------------------------------------------------------------
// Rule 6 & 7: interpolation order and declared-output checks.
//
// Both rules walk the same set of templated strings and look for
// ${{ steps.<id>.outputs.<key> }} accessors via regex. Rule 6 fires when
// <id> is unknown or appears later in execution order; rule 7 fires when
// <id> exists earlier but never declares outputs.<key>.
// ----------------------------------------------------------------------------

// stepsOutputsAccessor captures step id (group 1) and output key (group 2)
// from a ${{ steps.<id>.outputs.<key> }} expression. Whitespace inside the
// braces is tolerated to match the renderer's parser.
var stepsOutputsAccessor = regexp.MustCompile(`\$\{\{\s*steps\.([a-zA-Z_][a-zA-Z0-9_-]*)\.outputs\.([a-zA-Z_][a-zA-Z0-9_-]*)\s*\}\}`)

func ruleInterpolationOrder(s *Sensor, _ map[string]*Sensor) error {
	idx := stepIndex(s.Execution.Steps)
	for i, st := range s.Execution.Steps {
		for _, ref := range stepOutputsRefsIn(st) {
			pos, found := idx[ref.stepID]
			if !found {
				return fmt.Errorf("sensor %q step %q: ${{ steps.%s.outputs.%s }} references step %q which does not exist in execution.steps", s.ID, st.ID, ref.stepID, ref.key, ref.stepID)
			}
			if pos >= i {
				return fmt.Errorf("sensor %q step %q: ${{ steps.%s.outputs.%s }} references step %q which appears later in execution order (interpolation can only consume prior steps' outputs)", s.ID, st.ID, ref.stepID, ref.key, ref.stepID)
			}
		}
	}
	return nil
}

func ruleInterpolationDeclared(s *Sensor, _ map[string]*Sensor) error {
	byID := map[string]*StepConfig{}
	for i := range s.Execution.Steps {
		byID[s.Execution.Steps[i].ID] = &s.Execution.Steps[i]
	}
	for _, st := range s.Execution.Steps {
		for _, ref := range stepOutputsRefsIn(st) {
			src, ok := byID[ref.stepID]
			if !ok {
				continue // rule 6 already covers unknown step id
			}
			if _, declared := src.Outputs[ref.key]; !declared {
				return fmt.Errorf("sensor %q step %q: ${{ steps.%s.outputs.%s }} references output %q which step %q does not declare under outputs:", s.ID, st.ID, ref.stepID, ref.key, ref.key, ref.stepID)
			}
		}
	}
	return nil
}

// outputsRef is the captured (stepID, key) pair from one accessor match.
type outputsRef struct {
	stepID string
	key    string
}

// stepOutputsRefsIn scans every templated string in step st for
// ${{ steps.<id>.outputs.<key> }} accessors and returns the captured
// pairs in the order they appear.
func stepOutputsRefsIn(st StepConfig) []outputsRef {
	var refs []outputsRef
	add := func(s string) {
		if s == "" {
			return
		}
		for _, m := range stepsOutputsAccessor.FindAllStringSubmatch(s, -1) {
			refs = append(refs, outputsRef{stepID: m[1], key: m[2]})
		}
	}
	add(st.Run)
	add(st.URL)
	add(st.Method)
	add(st.Timeout)
	if st.BodyFrom != nil {
		add(st.BodyFrom.Template)
		if inline, ok := st.BodyFrom.Inline.(string); ok {
			add(inline)
		}
	}
	for _, v := range st.Headers {
		add(v)
	}
	for _, v := range st.With {
		if str, ok := v.(string); ok {
			add(str)
		}
	}
	// expect.value and any nested string in the matcher: walk recursively.
	walkExpectStrings(st.Expect, add)
	return refs
}

// walkExpectStrings traverses an Expect tree (decoded as interface{} maps
// and slices) and invokes f on every string leaf. Used by rules 6 and 7
// to reach into the matcher tree where ${{ … }} accessors can appear.
func walkExpectStrings(v interface{}, f func(string)) {
	switch t := v.(type) {
	case string:
		f(t)
	case map[string]interface{}:
		for _, vv := range t {
			walkExpectStrings(vv, f)
		}
	case []interface{}:
		for _, vv := range t {
			walkExpectStrings(vv, f)
		}
	}
}

// stepIndex returns id → ordinal-position. Used by rule 6 to detect
// forward references.
func stepIndex(steps []StepConfig) map[string]int {
	out := make(map[string]int, len(steps))
	for i, st := range steps {
		// First occurrence wins; duplicate-id case is reported by rule 4
		// before this rule runs, so the choice here is moot in practice.
		if _, dup := out[st.ID]; !dup {
			out[st.ID] = i
		}
	}
	return out
}

// ----------------------------------------------------------------------------
// Rule 8: cycle detection over the combined graph of type:sensor step refs
// and requires[kind=sensor] id edges. Iterative DFS with depth cap.
// ----------------------------------------------------------------------------

func ruleSensorCycles(s *Sensor, peers map[string]*Sensor) error {
	// dfsFrame holds one frame on the explicit stack: a node id, the
	// path of ancestors that led to it, and the path's length so we can
	// enforce the depth cap without recomputing.
	type dfsFrame struct {
		id    string
		path  []string
		depth int
	}

	// Resolve outgoing edges (children) for any sensor id. Returns the
	// union of type:sensor step refs and requires[kind=sensor] ids. When
	// peers does not contain id, returns nil — the rule degrades to
	// best-effort across the visible graph.
	edges := func(id string) []string {
		peer, ok := peers[id]
		if !ok && id != s.ID {
			return nil
		}
		var src *Sensor
		if id == s.ID {
			src = s
		} else {
			src = peer
		}
		set := map[string]struct{}{}
		for _, st := range src.Execution.Steps {
			if st.Type == "sensor" && st.Ref != "" && st.Ref != id {
				set[st.Ref] = struct{}{}
			}
		}
		for _, req := range src.Requires {
			if req.Kind == RequireSensor && req.ID != "" && req.ID != id {
				set[req.ID] = struct{}{}
			}
		}
		if len(set) == 0 {
			return nil
		}
		out := make([]string, 0, len(set))
		for k := range set {
			out = append(out, k)
		}
		sort.Strings(out) // deterministic traversal for stable error messages
		return out
	}

	stack := []dfsFrame{{id: s.ID, path: []string{s.ID}, depth: 0}}
	for len(stack) > 0 {
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		for _, child := range edges(frame.id) {
			// Cycle: child reappears in the path that reached frame.id.
			for _, ancestor := range frame.path {
				if ancestor == child {
					return fmt.Errorf("sensor %q: cycle detected through type:sensor / requires[kind=sensor] edges: %v → %s", s.ID, frame.path, child)
				}
			}
			// Depth cap: depth from origin to child is frame.depth+1.
			if frame.depth+1 > maxSensorRefDepth {
				return fmt.Errorf("sensor %q: type:sensor / requires[kind=sensor] chain exceeds maximum depth %d (path: %v → %s); flatten the composition or remove the deepest hop", s.ID, maxSensorRefDepth, frame.path, child)
			}
			// No cycle and within depth: descend.
			newPath := make([]string, len(frame.path)+1)
			copy(newPath, frame.path)
			newPath[len(frame.path)] = child
			stack = append(stack, dfsFrame{id: child, path: newPath, depth: frame.depth + 1})
		}
	}
	return nil
}

// ----------------------------------------------------------------------------
// Rule 9: type: sensor ref pointing to a sensor with execution.blocking: true.
// ----------------------------------------------------------------------------

func ruleSensorRefNotBlocking(s *Sensor, peers map[string]*Sensor) error {
	if len(peers) == 0 {
		return nil
	}
	for _, st := range s.Execution.Steps {
		if st.Type != "sensor" || st.Ref == "" {
			continue
		}
		peer, ok := peers[st.Ref]
		if !ok {
			continue // best-effort when peer is not visible
		}
		if peer.Execution.Blocking {
			return fmt.Errorf("sensor %q step %q: type: sensor ref %q points at a sensor with execution.blocking: true (inline composition cannot consume a long-lived blocking sensor; declare it under requires[kind=sensor] instead)", s.ID, st.ID, st.Ref)
		}
	}
	return nil
}

// ----------------------------------------------------------------------------
// Rule 10: type: assert step with `with:` declared → error.
// ----------------------------------------------------------------------------

func ruleAssertNoWith(s *Sensor, _ map[string]*Sensor) error {
	for _, st := range s.Execution.Steps {
		if st.Type != "assert" {
			continue
		}
		if len(st.With) > 0 {
			return fmt.Errorf("sensor %q step %q: type: assert does not accept with: (assert steps have no inputs other than expect.value, which is rendered through the global interpolator)", s.ID, st.ID)
		}
	}
	return nil
}

// ----------------------------------------------------------------------------
// Rule 11 (warning): the same sensor id appears in both requires[kind=sensor]
// and a type:sensor step's ref. The DAG prerequisite runs once before steps;
// the inline step then re-runs it, which is usually unintended.
// ----------------------------------------------------------------------------

func sensorOverlapWarnings(s *Sensor) []string {
	requiredIDs := map[string]struct{}{}
	for _, req := range s.Requires {
		if req.Kind == RequireSensor && req.ID != "" {
			requiredIDs[req.ID] = struct{}{}
		}
	}
	if len(requiredIDs) == 0 {
		return nil
	}
	var warnings []string
	for _, st := range s.Execution.Steps {
		if st.Type != "sensor" || st.Ref == "" {
			continue
		}
		if _, overlap := requiredIDs[st.Ref]; overlap {
			warnings = append(warnings, fmt.Sprintf("sensor %q step %q: ref %q is also declared in requires[kind=sensor]; the prerequisite will run once before steps and again inline at this step. If that is intentional, ignore this warning; otherwise drop the requires[] entry or the step.", s.ID, st.ID, st.Ref))
		}
	}
	return warnings
}
