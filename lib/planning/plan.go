package planning

import (
	"fmt"
	"sort"

	"github.com/iurykrieger/harness-framework/lib/usecase"
)

// Build groups the input usecases, infers kind/type/output per
// bucket, materializes one Plan per bucket (splitting buckets that
// exceed BucketLimit), and returns the resulting slice sorted by
// sensor_id ascending.
//
// Build is the package's single entrypoint. The name "Build" is used
// because Go forbids reusing the type identifier Plan for a function;
// see lib/planning/shape.go for the type.
func Build(usecases []usecase.UseCase) []Plan {
	buckets := group(usecases)
	assignDiscriminators(buckets)
	var plans []Plan
	for _, b := range buckets {
		plans = append(plans, materialize(b)...)
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].SensorID < plans[j].SensorID })
	return plans
}

// MakeAggregate composes the closing Aggregate envelope for a Plan
// slice. Verdict is always "pass" — Plan never produces errors; the
// wrapper script is responsible for emitting error Signals when the
// input ledger fails to parse or load.
func MakeAggregate(plans []Plan) Aggregate {
	return Aggregate{
		Aggregate:        true,
		Verdict:          "pass",
		Severity:         "info",
		SensorsPlanned:   len(plans),
		UsecasesConsumed: countConsumed(plans),
	}
}

// materialize turns a bucket into 1..N Plans. Buckets above
// BucketLimit are chunked deterministically (id-sorted, by 8) so
// every chunk shares identical kind/type/output (computed once per
// bucket so two halves never disagree).
func materialize(b bucket) []Plan {
	sort.Slice(b.Usecases, func(i, j int) bool { return b.Usecases[i].ID < b.Usecases[j].ID })

	bucketKind := InferKind(b.Usecases)
	bucketType, inferentialWarn := InferType(b.Usecases)
	bucketOutput := InferOutput(b.Usecases)

	if len(b.Usecases) <= BucketLimit {
		return []Plan{buildPlan(b.Usecases, b.JourneyID, b.Shape, b.Discriminator, "", bucketKind, bucketType, bucketOutput, inferentialWarn)}
	}
	var plans []Plan
	for i, start := 1, 0; start < len(b.Usecases); i, start = i+1, start+BucketLimit {
		end := start + BucketLimit
		if end > len(b.Usecases) {
			end = len(b.Usecases)
		}
		plans = append(plans, buildPlan(b.Usecases[start:end], b.JourneyID, b.Shape, b.Discriminator, fmt.Sprintf("-part-%d", i), bucketKind, bucketType, bucketOutput, inferentialWarn))
	}
	return plans
}

func buildPlan(group []usecase.UseCase, journey, shape, discriminator, partSuffix, kind, typ, output string, inferentialWarn bool) Plan {
	useCaseIDs := make([]string, 0, len(group))
	for _, uc := range group {
		useCaseIDs = append(useCaseIDs, uc.ID)
	}

	var steps []StepOutline
	stepCounter := 1
	for _, uc := range group {
		for _, rule := range uc.Behavior.BusinessRules {
			steps = append(steps, StepOutline{
				StepID:            fmt.Sprintf("rule-%d-%s", stepCounter, Slugify(rule)),
				SourceUsecase:     uc.ID,
				SourceRule:        rule,
				SuggestedStepType: SuggestStepType(uc, rule),
				MockStrategy:      PickMockStrategy(uc),
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

	return Plan{
		SensorID:    sensorID,
		Kind:        kind,
		Type:        typ,
		Output:      output,
		UseCases:    useCaseIDs,
		StepOutline: steps,
		Rationale:   rationale,
	}
}

func countConsumed(plans []Plan) int {
	seen := map[string]struct{}{}
	for _, p := range plans {
		for _, id := range p.UseCases {
			seen[id] = struct{}{}
		}
	}
	return len(seen)
}
