package planning

import (
	"strings"

	"github.com/iurykrieger/harness-framework/lib/usecase"
)

// InferKind decides the planned sensor's kind by scanning every
// usecase in group for textual hints. Resolution order:
//
//  1. setup     — trigger.shape contains "setup" OR
//                 behavior.summary contains "idempotent"
//  2. observation — expected_outcome.shape contains "stream" OR
//                   expected_outcome.summary contains
//                   "log lines while running"
//  3. assertion (default).
func InferKind(group []usecase.UseCase) string {
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

// InferType returns ("inferential", true) when any business_rule
// across group reads as a semantic / judgmental check (e.g.,
// "semantically equivalent", "team voice", "no pii"). Otherwise
// returns ("computational", false). The boolean flag signals the
// caller to attach an "inferential — calibration required" warn to
// the plan rationale.
func InferType(group []usecase.UseCase) (string, bool) {
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

// InferOutput returns "stream" when at least one usecase's
// expected_outcome.shape signals line-by-line output, OR when the
// group declares ≥2 independent business rules (each rule maps to one
// step, and ≥2 steps means individual signals are worth surfacing).
// Otherwise "single".
func InferOutput(group []usecase.UseCase) string {
	for _, uc := range group {
		shape := strings.ToLower(uc.ExpectedOutcome.Shape)
		if strings.Contains(shape, "stream") || strings.Contains(shape, "log lines") || strings.Contains(shape, "one line per") {
			return "stream"
		}
	}
	totalRules := 0
	for _, uc := range group {
		totalRules += len(uc.Behavior.BusinessRules)
	}
	if totalRules >= 2 {
		return "stream"
	}
	return "single"
}

// SuggestStepType picks "http" when the first evidence file path
// mentions "http" (case-insensitive), otherwise "shell". The rule
// argument is currently unused but kept on the signature so future
// per-rule heuristics can be added without touching every call site.
func SuggestStepType(uc usecase.UseCase, rule string) string {
	if len(uc.Evidence) == 0 {
		return "shell"
	}
	file := uc.Evidence[0].File
	if strings.Contains(strings.ToLower(file), "http") {
		return "http"
	}
	return "shell"
}

// PickMockStrategy picks one of {stub-deterministic, fixture-http-step,
// setup-mock-infra} per the cascade:
//
//  1. No evidence → stub-deterministic.
//  2. Evidence under lib/*.go (non-test) → stub-deterministic.
//  3. Evidence file mentions "http" → fixture-http-step.
//  4. Any side-effect mentions "db write" / "kafka" / "external api" →
//     setup-mock-infra.
//  5. Default → stub-deterministic.
func PickMockStrategy(uc usecase.UseCase) string {
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

// Slugify lower-cases s, drops non-alphanumeric runes (preserving
// hyphens and converting spaces to hyphens), collapses runs of "--",
// trims leading/trailing "-", and truncates to 32 chars. Used for
// sensor-id construction and step-id construction.
func Slugify(s string) string {
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
