package fixture_test

import (
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/fixture"
)

func TestFindOnDisk(t *testing.T) {
	base := "testdata/sample"
	abs := func(rel string) string {
		a, err := filepath.Abs(filepath.Join(base, rel))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	cases := []struct {
		name         string
		role         string
		searchPaths  []string
		wantSource   string // "disk" expected; "" expected nil sample
		wantBasename string
	}{
		{
			name:         "exact role basename",
			role:         "trigger",
			searchPaths:  []string{abs("exact_trigger")},
			wantSource:   "disk",
			wantBasename: "trigger.json",
		},
		{
			name:         "alias basename for trigger role",
			role:         "trigger",
			searchPaths:  []string{abs("alias_request")},
			wantSource:   "disk",
			wantBasename: "request.json",
		},
		{
			name:         "exact beats alias in same dir",
			role:         "trigger",
			searchPaths:  []string{abs("two_candidates_same_dir")},
			wantSource:   "disk",
			wantBasename: "trigger.json",
		},
		{
			name:         "earlier searchPath wins",
			role:         "trigger",
			searchPaths:  []string{abs("across_search_paths/first"), abs("across_search_paths/second")},
			wantSource:   "disk",
			wantBasename: "trigger.json",
		},
		{
			name:        "no match returns nil sample",
			role:        "trigger",
			searchPaths: []string{abs("no_match")},
			wantSource:  "",
		},
		{
			name:        "no searchPaths returns nil sample",
			role:        "trigger",
			searchPaths: nil,
			wantSource:  "",
		},
		{
			name:        "non-existent searchPath skipped silently",
			role:        "trigger",
			searchPaths: []string{abs("does_not_exist")},
			wantSource:  "",
		},
		{
			name:         "outcome role matches response.json alias",
			role:         "outcome",
			searchPaths:  []string{abs("role_outcome")},
			wantSource:   "disk",
			wantBasename: "response.json",
		},
		{
			name:         "body role matches payload.json alias",
			role:         "body",
			searchPaths:  []string{abs("role_body")},
			wantSource:   "disk",
			wantBasename: "payload.json",
		},
		{
			name:         "log-line role matches sample.log alias",
			role:         "log-line",
			searchPaths:  []string{abs("role_log_line")},
			wantSource:   "disk",
			wantBasename: "sample.log",
		},
		{
			name:         "event role matches message.json alias",
			role:         "event",
			searchPaths:  []string{abs("role_event")},
			wantSource:   "disk",
			wantBasename: "message.json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := fixture.FindOnDisk(fixture.Hint{Role: tc.role}, tc.searchPaths)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantSource == "" {
				if s != nil {
					t.Fatalf("expected nil sample, got %+v", s)
				}
				return
			}
			if s == nil {
				t.Fatalf("expected sample, got nil")
			}
			if s.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", s.Source, tc.wantSource)
			}
			if got := filepath.Base(s.SourcePath); got != tc.wantBasename {
				t.Errorf("Basename = %q, want %q", got, tc.wantBasename)
			}
		})
	}
}
