package usecase

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/stack"
)

func TestCheckFixtureRefExists(t *testing.T) {
	root := t.TempDir()
	fxPath := filepath.Join(root, ".harness/fixtures/framework/x/trigger.json")
	if err := os.MkdirAll(filepath.Dir(fxPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fxPath, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		triggerFx any
		outcomeFx any
		wantErr   string // substring; "" means expect nil
	}{
		{
			name:      "ref present and exists",
			triggerFx: map[string]any{"ref": "framework/x/trigger.json"},
		},
		{
			name:      "ref missing returns fixture_not_found",
			triggerFx: map[string]any{"ref": "framework/x/missing.json"},
			wantErr:   "fixture_not_found",
		},
		{
			name:      "ref pointing at a directory rejected",
			triggerFx: map[string]any{"ref": "framework/x"},
			wantErr:   "fixture_not_found",
		},
		{
			name:      "outcome ref also checked",
			triggerFx: map[string]any{"inline": "ok"},
			outcomeFx: map[string]any{"ref": "framework/x/nope.json"},
			wantErr:   "fixture_not_found",
		},
		{
			name:      "inline-only skipped",
			triggerFx: map[string]any{"inline": "ok"},
			outcomeFx: map[string]any{"inline": map[string]any{"exit_code": float64(0)}},
		},
		{
			name:      "both refs missing names both files",
			triggerFx: map[string]any{"ref": "framework/x/t.json"},
			outcomeFx: map[string]any{"ref": "framework/x/o.json"},
			wantErr:   "framework/x/t.json, framework/x/o.json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uc := &UseCase{
				ID:              "u1",
				Trigger:         Trigger{Fixture: tc.triggerFx},
				ExpectedOutcome: ExpectedOutcome{Fixture: tc.outcomeFx},
			}
			err := CheckFixtureRefExists(uc, root)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
			var cce *stack.CrossCheckError
			if !errors.As(err, &cce) {
				t.Fatalf("expected *stack.CrossCheckError, got %T", err)
			}
		})
	}
}
