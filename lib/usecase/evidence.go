package usecase

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	return fmt.Errorf("evidence files not found under %s: %s", projectRoot, strings.Join(missing, ", "))
}
