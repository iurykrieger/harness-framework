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
// expected_outcome carries a fixture envelope whose inner payload is
// non-primitive cites at least one evidence row with kind=contract.
//
// The fixture field is always one of:
//
//	{"ref":   "path/under/.harness/fixtures"}   — payload on disk
//	{"inline": <value>}                         — payload inline
//
// For the `ref` arm we ALWAYS require a contract row, because the file
// on disk is presumed to be structured.
//
// For the `inline` arm we look inside the wrapper; primitives skip the
// check, structured payloads require a contract row.
func CheckFixtureContractEvidence(uc *UseCase) error {
	if !envelopeRequiresContract(uc.Trigger.Fixture) &&
		!envelopeRequiresContract(uc.ExpectedOutcome.Fixture) {
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

// envelopeRequiresContract reports whether v (a FixtureRef envelope from
// usecase.yaml) carries a payload that needs a contract citation.
//
// nil returns false.
// {ref: ...} returns true: the file on disk is always non-primitive at this layer.
// {inline: x}: x is unwrapped and inspected; primitives and empty/all-primitive
// objects return false; nested maps and non-empty arrays return true.
func envelopeRequiresContract(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	if _, has := m["ref"]; has {
		return true
	}
	inline, has := m["inline"]
	if !has {
		return false
	}
	return isStructuredPayload(inline)
}

func isStructuredPayload(v any) bool {
	switch x := v.(type) {
	case nil, bool, string,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return false
	case map[string]any:
		// A flat map whose values are all scalar primitives does not carry a
		// schema-defined shape on its own (e.g. {"exit_code": 0, "status": "ok"})
		// and does not require a contract citation. A map that contains at least
		// one non-scalar value (a nested map or array) has a typed shape and
		// requires one.
		for _, vv := range x {
			switch vv.(type) {
			case nil, bool, string,
				int, int8, int16, int32, int64,
				uint, uint8, uint16, uint32, uint64,
				float32, float64:
				// scalar — keep scanning
			default:
				return true
			}
		}
		return false
	case []any:
		return len(x) > 0
	default:
		return true
	}
}
