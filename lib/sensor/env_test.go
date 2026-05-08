package sensor

import (
	"reflect"
	"testing"
	"time"
)

func stableNow() time.Time {
	return time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
}

func withFakeEnv(t *testing.T, env map[string]string) {
	t.Helper()
	prev := LookupEnvFn
	LookupEnvFn = func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
	t.Cleanup(func() { LookupEnvFn = prev })
}

func TestCheckRequiredEnv_NoRequires(t *testing.T) {
	got := CheckRequiredEnv(map[string]interface{}{})
	if got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestCheckRequiredEnv_EmptyEnv(t *testing.T) {
	got := CheckRequiredEnv(map[string]interface{}{
		"requires": map[string]interface{}{"env": []interface{}{}},
	})
	if got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestCheckRequiredEnv_RequiredMissing(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	got := CheckRequiredEnv(map[string]interface{}{
		"requires": map[string]interface{}{
			"env": []interface{}{
				map[string]interface{}{"name": "GITHUB_TOKEN", "description": "PAT"},
				map[string]interface{}{"name": "GCP_PROJECT"},
			},
		},
	})
	want := []MissingEnv{
		{Name: "GITHUB_TOKEN", Description: "PAT"},
		{Name: "GCP_PROJECT"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestCheckRequiredEnv_RequiredPresent(t *testing.T) {
	withFakeEnv(t, map[string]string{"GITHUB_TOKEN": "ghp_xxx"})
	got := CheckRequiredEnv(map[string]interface{}{
		"requires": map[string]interface{}{
			"env": []interface{}{
				map[string]interface{}{"name": "GITHUB_TOKEN"},
			},
		},
	})
	if got != nil {
		t.Fatalf("expected nil (env present), got %#v", got)
	}
}

func TestCheckRequiredEnv_OptionalMissingIsIgnored(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	got := CheckRequiredEnv(map[string]interface{}{
		"requires": map[string]interface{}{
			"env": []interface{}{
				map[string]interface{}{"name": "DEBUG", "optional": true},
				map[string]interface{}{"name": "REGION"},
			},
		},
	})
	if len(got) != 1 || got[0].Name != "REGION" {
		t.Fatalf("expected only REGION missing, got %+v", got)
	}
}

func TestCheckRequiredEnv_MalformedEntriesIgnored(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	got := CheckRequiredEnv(map[string]interface{}{
		"requires": map[string]interface{}{
			"env": []interface{}{
				"not-an-object",
				map[string]interface{}{},                      // no name
				map[string]interface{}{"name": ""},            // empty name
				map[string]interface{}{"name": "REAL_ONE"},    // counted
				map[string]interface{}{"description": "orphan"}, // missing name
			},
		},
	})
	if len(got) != 1 || got[0].Name != "REAL_ONE" {
		t.Fatalf("expected only REAL_ONE, got %+v", got)
	}
}

func TestBuildErrorSignal_ShapeAndRemediation(t *testing.T) {
	prev := NowFn
	defer func() { NowFn = prev }()
	NowFn = stableNow

	env := Envelope{
		SensorID: "x", Version: "0.1.0", RunID: "abc",
		StartedAt: "2026-05-08T00:00:00Z", SensorType: "computational",
	}
	sig := BuildErrorSignal(env, "single", "missing required env var GITHUB_TOKEN", "export GITHUB_TOKEN and re-run")

	if sig["verdict"] != "error" || sig["severity"] != "high" {
		t.Fatalf("verdict/severity mismatch: %v %v", sig["verdict"], sig["severity"])
	}
	rem, ok := sig["remediation"].(map[string]interface{})
	if !ok {
		t.Fatalf("remediation missing")
	}
	if rem["instructions"] != "export GITHUB_TOKEN and re-run" {
		t.Fatalf("remediation.instructions=%v", rem["instructions"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "aggregate" || md["output_mode"] != "single" {
		t.Fatalf("metadata wrong: %+v", md)
	}
}

func TestBuildErrorSignal_OmitsRemediationWhenEmpty(t *testing.T) {
	env := Envelope{SensorID: "x", Version: "0.1.0", RunID: "r", StartedAt: "2026-05-08T00:00:00Z"}
	sig := BuildErrorSignal(env, "stream", "rationale", "")
	if _, ok := sig["remediation"]; ok {
		t.Fatalf("remediation should be omitted when empty")
	}
}
