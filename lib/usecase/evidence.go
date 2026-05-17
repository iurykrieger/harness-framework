package usecase

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/stack"
)

// CheckEvidenceFiles verifies every UseCase.Evidence[].File exists on
// disk relative to projectRoot. Returns a single error listing every
// missing file when any are absent.
func CheckEvidenceFiles(uc *UseCase, projectRoot string) error {
	var missing []string
	for _, ev := range uc.Evidence {
		full := filepath.Join(projectRoot, ev.File)
		if _, err := os.Stat(full); err != nil {
			missing = append(missing, ev.File)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &stack.CrossCheckError{
		Kind:    "evidence_file_missing",
		Message: fmt.Sprintf("evidence files not found under %s: %s", projectRoot, strings.Join(missing, ", ")),
	}
}

// CheckFixtureContractEvidence enforces that a UseCase whose trigger or
// expected_outcome carries a non-primitive fixture cites at least one
// evidence row with kind=contract — the typed declaration (DTO, schema,
// struct, command-flag definition) that determines the fixture's field
// names. Without it, /detect-usecases drifts to fabricating field names
// from prose, which silently corrupts every downstream /create-sensor.
//
// Primitive fixtures (nil, string, number, bool) carry no separable
// contract and the check is skipped.
func CheckFixtureContractEvidence(uc *UseCase) error {
	if !hasStructuredFixtureShape(uc.Trigger.Fixture) && !hasStructuredFixtureShape(uc.ExpectedOutcome.Fixture) {
		return nil
	}
	for _, ev := range uc.Evidence {
		if ev.Kind == EvidenceKindContract {
			return nil
		}
	}
	return &stack.CrossCheckError{
		Kind: "contract_evidence_missing",
		Message: fmt.Sprintf(
			"usecase %q has a non-primitive fixture but no evidence[] row with kind=%q; cite the DTO/schema/struct/flag declaration that defines the fixture field names",
			uc.ID, EvidenceKindContract,
		),
	}
}

// hasStructuredFixtureShape reports whether v is a map/slice/struct —
// i.e. has a shape distinct from its element values, which is the cue
// that a typed contract somewhere in the project defines the field set.
// Primitives (nil, bool, numeric, string) are returned as false.
func hasStructuredFixtureShape(v any) bool {
	switch v.(type) {
	case nil, bool, string,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return false
	default:
		return true
	}
}
