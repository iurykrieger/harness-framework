package stack

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestLoadStackFile(t *testing.T) {
	cases := []struct {
		name       string
		fixture    string
		wantCode   int
		wantSubstr string // expected fragment in stderr when wantCode != 0
	}{
		{name: "golden", fixture: "golden-stack.yaml", wantCode: 0},
		{name: "with journeys", fixture: "golden-stack-with-journeys.yaml", wantCode: 0},
		{name: "with new component fields", fixture: "golden-stack-with-new-fields.yaml", wantCode: 0},
		{name: "missing required", fixture: "invalid-missing-required.yaml", wantCode: 1, wantSubstr: "version"},
		{name: "bad enum", fixture: "invalid-enum.yaml", wantCode: 1, wantSubstr: "format"},
		{name: "unknown role", fixture: "invalid-unknown-role.yaml", wantCode: 1, wantSubstr: "role"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			_, _, code := LoadStackFile(filepath.Join("testdata", tc.fixture), "", &stderr)
			if code != tc.wantCode {
				t.Fatalf("code = %d, want %d (stderr: %s)", code, tc.wantCode, stderr.String())
			}
			if tc.wantSubstr != "" && !bytes.Contains(stderr.Bytes(), []byte(tc.wantSubstr)) {
				t.Fatalf("stderr %q missing %q", stderr.String(), tc.wantSubstr)
			}
		})
	}
}
