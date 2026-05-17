//go:build plan_sensors

// Command plan-sensors reads a ledger from stdin and emits a JSONL plan
// on stdout (one Plan line per proposed sensor, then one Aggregate
// signal as the last line).
//
// Determinism: no rand, no time.Now(). Sort orders are explicit in code
// and depend only on input data. See spec §Grouping heuristic.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/skills/create-sensor/scripts/lib/ledger"
)

const bucketLimit = 8

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr))
}

func run(stdin io.Reader, stdout, stderr io.Writer) int {
	body, err := io.ReadAll(stdin)
	if err != nil {
		emit(stdout, errSignal("usage", "read stdin: "+err.Error()))
		return 2
	}
	var lg ledger.Ledger
	if err := json.Unmarshal(body, &lg); err != nil {
		emit(stdout, errSignal("usage", "parse ledger: "+err.Error()))
		return 2
	}

	buckets := group(lg.Usecases)
	assignDiscriminators(buckets)
	var plans []ledger.Plan
	for _, b := range buckets {
		plans = append(plans, materialize(b)...)
	}
	// Sort plans by sensor_id ascending for deterministic output.
	sort.Slice(plans, func(i, j int) bool { return plans[i].SensorID < plans[j].SensorID })

	for _, p := range plans {
		emit(stdout, p)
	}
	emit(stdout, ledger.Aggregate{
		Aggregate:        true,
		Verdict:          "pass",
		Severity:         "info",
		SensorsPlanned:   len(plans),
		UsecasesConsumed: countConsumed(plans),
	})
	return 0
}

// bucket is a tentative grouping of usecases sharing journey+shape.
type bucket struct {
	journeyID     string
	shape         string
	usecases      []ledger.Usecase
	discriminator string // non-empty only when the journey has multiple buckets
}

// group partitions usecases by (journey_id, trigger.shape). Tag overlap
// further splits — usecases with disjoint tag sets in the same journey+
// shape go to different sensors. Evidence-directory proximity tightens
// further; usecases whose evidence files share a common directory (or
// 1-level-up) stay together.
func group(usecases []ledger.Usecase) []bucket {
	// Step 1: partition by journey+shape.
	keyed := map[string][]ledger.Usecase{}
	var order []string
	for _, uc := range usecases {
		key := uc.JourneyID + "|" + uc.Trigger.Shape
		if _, ok := keyed[key]; !ok {
			order = append(order, key)
		}
		keyed[key] = append(keyed[key], uc)
	}
	sort.Strings(order)

	// Step 2: within each (journey, shape) partition, split by
	// disjoint-tag clusters and evidence-directory clusters.
	var out []bucket
	for _, k := range order {
		parts := strings.SplitN(k, "|", 2)
		clusters := splitByTagsAndEvidence(keyed[k])
		for _, c := range clusters {
			sort.Slice(c, func(i, j int) bool { return c[i].ID < c[j].ID })
			out = append(out, bucket{journeyID: parts[0], shape: parts[1], usecases: c})
		}
	}
	return out
}

func splitByTagsAndEvidence(in []ledger.Usecase) [][]ledger.Usecase {
	if len(in) <= 1 {
		return [][]ledger.Usecase{in}
	}
	// Union-find by (tag overlap OR evidence-dir proximity).
	parent := make([]int, len(in))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	for i := 0; i < len(in); i++ {
		for j := i + 1; j < len(in); j++ {
			if shareTag(in[i], in[j]) || evidenceProximate(in[i], in[j]) {
				union(i, j)
			}
		}
	}
	// Bucket by root.
	groups := map[int][]ledger.Usecase{}
	for i, uc := range in {
		r := find(i)
		groups[r] = append(groups[r], uc)
	}
	// Stable order.
	var rootOrder []int
	for r := range groups {
		rootOrder = append(rootOrder, r)
	}
	sort.Ints(rootOrder)
	var out [][]ledger.Usecase
	for _, r := range rootOrder {
		out = append(out, groups[r])
	}
	return out
}

func shareTag(a, b ledger.Usecase) bool {
	set := map[string]struct{}{}
	for _, t := range a.Tags {
		set[t] = struct{}{}
	}
	for _, t := range b.Tags {
		if _, ok := set[t]; ok {
			return true
		}
	}
	return false
}

func evidenceProximate(a, b ledger.Usecase) bool {
	if len(a.Evidence) == 0 || len(b.Evidence) == 0 {
		return false
	}
	dirA := filepath.Dir(a.Evidence[0].File)
	dirB := filepath.Dir(b.Evidence[0].File)
	if dirA == dirB {
		return true
	}
	// 1-level-up match.
	if filepath.Dir(dirA) == filepath.Dir(dirB) && dirA != "." && dirB != "." {
		return true
	}
	return false
}

// assignDiscriminators populates bucket.discriminator for every bucket
// that belongs to a journey with more than one bucket. The discriminator
// is deterministic and computed in priority order:
//   1. Dominant tag — a tag present in EVERY usecase of the bucket. If
//      multiple such tags exist, the alphabetically first is chosen.
//   2. Slugified trigger.shape (e.g. "cli-invocation", "http-request").
//   3. Last-resort stable "cluster-N" index reflecting emergence order
//      from group() (1-based, scoped to the journey).
//
// Single-bucket journeys leave discriminator empty so the simple
// "<prefix>-<journey>" sensor_id shape is preserved.
func assignDiscriminators(buckets []bucket) {
	counts := map[string]int{}
	for _, b := range buckets {
		counts[b.journeyID]++
	}
	seenInJourney := map[string]int{}
	for i := range buckets {
		j := buckets[i].journeyID
		if counts[j] <= 1 {
			continue
		}
		seenInJourney[j]++
		buckets[i].discriminator = computeDiscriminator(buckets[i], seenInJourney[j])
	}
}

func computeDiscriminator(b bucket, clusterIdx int) string {
	if tag := dominantTag(b.usecases); tag != "" {
		return slugify(tag)
	}
	if shape := slugify(b.shape); shape != "" {
		return shape
	}
	return fmt.Sprintf("cluster-%d", clusterIdx)
}

// dominantTag returns the alphabetically-first tag shared by EVERY
// usecase in the bucket, or "" when no such tag exists. A usecase with
// no tags cannot share any tag, so it forces the empty result.
func dominantTag(ucs []ledger.Usecase) string {
	if len(ucs) == 0 {
		return ""
	}
	// Seed the intersection from the first usecase's tags.
	intersect := map[string]struct{}{}
	for _, t := range ucs[0].Tags {
		intersect[t] = struct{}{}
	}
	if len(intersect) == 0 {
		return ""
	}
	for _, uc := range ucs[1:] {
		present := map[string]struct{}{}
		for _, t := range uc.Tags {
			if _, ok := intersect[t]; ok {
				present[t] = struct{}{}
			}
		}
		intersect = present
		if len(intersect) == 0 {
			return ""
		}
	}
	var shared []string
	for t := range intersect {
		shared = append(shared, t)
	}
	sort.Strings(shared)
	return shared[0]
}

// materialize turns a bucket into 1..N plans, applying the
// bucket-too-large fission rule (sort by id ascending, chunk by 8).
func materialize(b bucket) []ledger.Plan {
	// Sort usecases by id ascending — required for deterministic split.
	sort.Slice(b.usecases, func(i, j int) bool { return b.usecases[i].ID < b.usecases[j].ID })

	if len(b.usecases) <= bucketLimit {
		return []ledger.Plan{buildPlan(b.usecases, b.journeyID, b.shape, b.discriminator, "")}
	}
	// Fission by id-sorted chunks.
	var plans []ledger.Plan
	for i, start := 1, 0; start < len(b.usecases); i, start = i+1, start+bucketLimit {
		end := start + bucketLimit
		if end > len(b.usecases) {
			end = len(b.usecases)
		}
		plans = append(plans, buildPlan(b.usecases[start:end], b.journeyID, b.shape, b.discriminator, fmt.Sprintf("-part-%d", i)))
	}
	return plans
}

func buildPlan(group []ledger.Usecase, journey, shape, discriminator, partSuffix string) ledger.Plan {
	kind := inferKind(group)
	typ, inferentialWarn := inferType(group)
	output := inferOutput(group)

	useCaseIDs := make([]string, 0, len(group))
	for _, uc := range group {
		useCaseIDs = append(useCaseIDs, uc.ID)
	}

	var steps []ledger.StepOutline
	stepCounter := 1
	for _, uc := range group {
		for _, rule := range uc.Behavior.BusinessRules {
			steps = append(steps, ledger.StepOutline{
				StepID:            fmt.Sprintf("rule-%d-%s", stepCounter, slugify(rule)),
				SourceUsecase:     uc.ID,
				SourceRule:        rule,
				SuggestedStepType: suggestStepType(uc, rule),
				MockStrategy:      pickMockStrategy(uc),
				Evidence:          uc.Evidence,
			})
			stepCounter++
		}
	}

	rationale := fmt.Sprintf(
		"Grouped by journey_id=%s + trigger.shape=%s. %d usecases × business_rules → %d steps.",
		journey, shape, len(group), len(steps),
	)
	if inferentialWarn {
		rationale += " WARN: inferential — calibration must be supplied by user."
	}
	if partSuffix != "" {
		rationale += " WARN: bucket_too_large — chunked by id-sorted split."
	}

	prefix := map[string]string{
		"assertion":   "assert",
		"observation": "observe",
		"setup":       "setup",
	}[kind]
	if prefix == "" {
		prefix = "assert"
	}

	sensorID := fmt.Sprintf("%s-%s", prefix, journey)
	if discriminator != "" {
		sensorID = fmt.Sprintf("%s-%s", sensorID, discriminator)
	}
	sensorID += partSuffix

	return ledger.Plan{
		SensorID:    sensorID,
		Kind:        kind,
		Type:        typ,
		Output:      output,
		UseCases:    useCaseIDs,
		StepOutline: steps,
		Rationale:   rationale,
	}
}

func inferKind(group []ledger.Usecase) string {
	for _, uc := range group {
		shape := strings.ToLower(uc.Trigger.Shape)
		summary := strings.ToLower(uc.Behavior.Summary)
		if strings.Contains(shape, "setup") || strings.Contains(summary, "idempotent") {
			return "setup"
		}
	}
	for _, uc := range group {
		shape := strings.ToLower(uc.ExpectedOutcome.Shape)
		summary := strings.ToLower(uc.ExpectedOutcome.Summary)
		if strings.Contains(shape, "stream") || strings.Contains(summary, "log lines while running") {
			return "observation"
		}
	}
	return "assertion"
}

func inferType(group []ledger.Usecase) (string, bool) {
	semanticAdjectives := []string{
		"semantically equivalent",
		"team voice",
		"no pii",
		"no personally identifiable",
	}
	for _, uc := range group {
		for _, rule := range uc.Behavior.BusinessRules {
			r := strings.ToLower(rule)
			for _, adj := range semanticAdjectives {
				if strings.Contains(r, adj) {
					return "inferential", true
				}
			}
		}
	}
	return "computational", false
}

func inferOutput(group []ledger.Usecase) string {
	for _, uc := range group {
		shape := strings.ToLower(uc.ExpectedOutcome.Shape)
		if strings.Contains(shape, "stream") || strings.Contains(shape, "log lines") || strings.Contains(shape, "one line per") {
			return "stream"
		}
	}
	// ≥2 independent rules → stream.
	totalRules := 0
	for _, uc := range group {
		totalRules += len(uc.Behavior.BusinessRules)
	}
	if totalRules >= 2 {
		return "stream"
	}
	return "single"
}

func suggestStepType(uc ledger.Usecase, rule string) string {
	if len(uc.Evidence) == 0 {
		return "shell"
	}
	file := uc.Evidence[0].File
	if strings.Contains(strings.ToLower(file), "http") {
		return "http"
	}
	return "shell"
}

func pickMockStrategy(uc ledger.Usecase) string {
	if len(uc.Evidence) == 0 {
		return "stub-deterministic"
	}
	file := uc.Evidence[0].File
	if strings.HasPrefix(file, "lib/") && strings.HasSuffix(file, ".go") && !strings.HasSuffix(file, "_test.go") {
		return "stub-deterministic"
	}
	if strings.Contains(strings.ToLower(file), "http") {
		return "fixture-http-step"
	}
	for _, se := range uc.ExpectedOutcome.SideEffects {
		l := strings.ToLower(se)
		if strings.Contains(l, "db write") || strings.Contains(l, "kafka") || strings.Contains(l, "external api") {
			return "setup-mock-infra"
		}
	}
	return "stub-deterministic"
}

func slugify(s string) string {
	s = strings.ToLower(s)
	var out []rune
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out = append(out, r)
		} else if r == ' ' || r == '-' {
			out = append(out, '-')
		}
	}
	slug := strings.Trim(string(out), "-")
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	if len(slug) > 32 {
		slug = slug[:32]
	}
	return slug
}

func countConsumed(plans []ledger.Plan) int {
	seen := map[string]struct{}{}
	for _, p := range plans {
		for _, id := range p.UseCases {
			seen[id] = struct{}{}
		}
	}
	return len(seen)
}

func emit(w io.Writer, v any) {
	body, _ := json.Marshal(v)
	fmt.Fprintln(w, string(body))
}

func errSignal(kind, rationale string) map[string]any {
	return signal.NewBuilder("plan-sensors", "0.1.0").
		WithVerdict("error", "high").
		WithKind(kind).
		WithRationale(rationale).
		Build()
}
