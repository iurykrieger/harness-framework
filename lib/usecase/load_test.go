package usecase

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestLoadUseCaseFile(t *testing.T) {
	cases := []struct {
		name       string
		fixture    string
		wantCode   int
		wantSubstr string
	}{
		{name: "canonical", fixture: "canonical-usecase.json", wantCode: 0},
		{name: "missing journey_id", fixture: "invalid-missing-journey-id.json", wantCode: 1, wantSubstr: "journey_id"},
		{name: "empty evidence", fixture: "invalid-empty-evidence.json", wantCode: 1, wantSubstr: "evidence"},
		{name: "bad id pattern", fixture: "invalid-bad-id-pattern.json", wantCode: 1, wantSubstr: "id"},
		{name: "bad version", fixture: "invalid-bad-version-format.json", wantCode: 1, wantSubstr: "version"},
		{name: "missing trigger fixture", fixture: "invalid-missing-trigger-fixture.json", wantCode: 1, wantSubstr: "fixture"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			_, _, code := LoadUseCaseFile(filepath.Join("testdata", tc.fixture), "", &stderr)
			if code != tc.wantCode {
				t.Fatalf("code=%d want=%d stderr=%s", code, tc.wantCode, stderr.String())
			}
			if tc.wantSubstr != "" && !bytes.Contains(stderr.Bytes(), []byte(tc.wantSubstr)) {
				t.Errorf("stderr %q missing %q", stderr.String(), tc.wantSubstr)
			}
		})
	}
}
