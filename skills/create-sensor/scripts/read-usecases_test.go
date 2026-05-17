//go:build read_usecases

package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/skills/create-sensor/scripts/lib/ledger"
)

func TestLoadSingleUsecaseByID(t *testing.T) {
	projectRoot := filepath.Join("testdata")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--project-root", projectRoot,
		"--usecases", "tail-sensor-no-registry",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: %d; stderr=%s; stdout=%s", code, stderr.String(), stdout.String())
	}
	var lg ledger.Ledger
	if err := json.Unmarshal(stdout.Bytes(), &lg); err != nil {
		t.Fatalf("unmarshal ledger: %v; stdout=%s", err, stdout.String())
	}
	if len(lg.Usecases) != 1 {
		t.Fatalf("want 1 usecase, got %d", len(lg.Usecases))
	}
	if lg.Usecases[0].ID != "tail-sensor-no-registry" {
		t.Fatalf("unexpected id: %s", lg.Usecases[0].ID)
	}
	if lg.Usecases[0].JourneyID != "tail-sensor" {
		t.Fatalf("unexpected journey: %s", lg.Usecases[0].JourneyID)
	}
}
