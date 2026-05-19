package usecase

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/stack"
)

// CheckFixtureRefExists verifies every fixture.ref path declared by the
// UseCase resolves to an existing, non-directory file under
// <projectRoot>/.harness/fixtures/. inline fixtures are skipped.
// Returns a single error listing every missing ref when any are absent.
func CheckFixtureRefExists(uc *UseCase, projectRoot string) error {
	var missing []string
	for _, role := range []struct {
		name string
		fx   any
	}{
		{"trigger", uc.Trigger.Fixture},
		{"expected_outcome", uc.ExpectedOutcome.Fixture},
	} {
		ref, ok := refFromEnvelope(role.fx)
		if !ok {
			continue
		}
		full := filepath.Join(projectRoot, ".harness", "fixtures", ref)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			missing = append(missing, ref)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &stack.CrossCheckError{
		Kind: "fixture_not_found",
		Message: fmt.Sprintf(
			"fixture_not_found: fixture files not found under %s/.harness/fixtures/: %s",
			projectRoot, strings.Join(missing, ", "),
		),
	}
}

// refFromEnvelope returns the ref string and true when v is a
// FixtureRef envelope of the form {"ref": "..."}; otherwise false.
func refFromEnvelope(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	ref, ok := m["ref"].(string)
	if !ok || ref == "" {
		return "", false
	}
	return ref, true
}
