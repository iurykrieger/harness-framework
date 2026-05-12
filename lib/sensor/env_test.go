package sensor_test

import (
	"reflect"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func withFakeEnv(t *testing.T, env map[string]string) {
	t.Helper()
	prev := sensor.LookupEnvFn
	sensor.LookupEnvFn = func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
	t.Cleanup(func() { sensor.LookupEnvFn = prev })
}

func TestCheckRequiredEnv_NoRequires(t *testing.T) {
	got := sensor.CheckRequiredEnv(map[string]interface{}{})
	if got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestCheckRequiredEnv_EmptyEnv(t *testing.T) {
	got := sensor.CheckRequiredEnv(map[string]interface{}{
		"requires": []interface{}{},
	})
	if got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestCheckRequiredEnv_RequiredMissing(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	got := sensor.CheckRequiredEnv(map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "GITHUB_TOKEN", "description": "PAT"},
			map[string]interface{}{"kind": "env", "name": "GCP_PROJECT"},
		},
	})
	want := []sensor.MissingEnv{
		{Name: "GITHUB_TOKEN", Description: "PAT"},
		{Name: "GCP_PROJECT"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestCheckRequiredEnv_RequiredPresent(t *testing.T) {
	withFakeEnv(t, map[string]string{"GITHUB_TOKEN": "ghp_xxx"})
	got := sensor.CheckRequiredEnv(map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "GITHUB_TOKEN"},
		},
	})
	if got != nil {
		t.Fatalf("expected nil (env present), got %#v", got)
	}
}

func TestCheckRequiredEnv_OptionalMissingIsIgnored(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	got := sensor.CheckRequiredEnv(map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "env", "name": "DEBUG", "optional": true},
			map[string]interface{}{"kind": "env", "name": "REGION"},
		},
	})
	if len(got) != 1 || got[0].Name != "REGION" {
		t.Fatalf("expected only REGION missing, got %+v", got)
	}
}

func TestCheckRequiredEnv_MalformedEntriesIgnored(t *testing.T) {
	withFakeEnv(t, map[string]string{})
	got := sensor.CheckRequiredEnv(map[string]interface{}{
		"requires": []interface{}{
			"not-an-object",
			map[string]interface{}{"kind": "env"},
			map[string]interface{}{"kind": "env", "name": ""},
			map[string]interface{}{"kind": "env", "name": "REAL_ONE"},
			map[string]interface{}{"kind": "env", "description": "orphan"},
		},
	})
	if len(got) != 1 || got[0].Name != "REAL_ONE" {
		t.Fatalf("expected only REAL_ONE, got %+v", got)
	}
}
