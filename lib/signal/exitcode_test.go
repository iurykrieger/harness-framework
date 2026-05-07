package signal_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/signal"
)

func TestMapExitCode(t *testing.T) {
	ecMap := []interface{}{
		map[string]interface{}{"exit_code": 0.0, "verdict": "pass", "severity": "info"},
		map[string]interface{}{"exit_code": 1.0, "verdict": "fail", "severity": "high"},
		map[string]interface{}{"exit_code": "*", "verdict": "error", "severity": "medium"},
	}
	cases := []struct {
		code    int
		verdict string
	}{
		{0, "pass"}, {1, "fail"}, {99, "error"},
	}
	for _, c := range cases {
		v, _ := signal.MapExitCode(c.code, ecMap)
		if v != c.verdict {
			t.Errorf("MapExitCode(%d)=%q want %q", c.code, v, c.verdict)
		}
	}

	// Without wildcard fallback
	noWild := []interface{}{
		map[string]interface{}{"exit_code": 0.0, "verdict": "pass", "severity": "info"},
	}
	v, s := signal.MapExitCode(99, noWild)
	if v != "error" || s != "high" {
		t.Errorf("expected error/high default, got %s/%s", v, s)
	}
}
