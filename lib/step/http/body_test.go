package http

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/step"
	"github.com/iurykrieger/harness-framework/lib/template"
)

func TestBuildBody_Nil(t *testing.T) {
	b, err := buildBody(nil, &step.ExecContext{}, template.ActionContext{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if b != nil {
		t.Fatalf("body = %q (want nil)", string(b))
	}
}

func TestBuildBody_Fixture(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	if err := os.WriteFile(p, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ec := &step.ExecContext{Fixtures: map[string]string{"f.json": p}}
	b, err := buildBody(&sensor.BodyFromConfig{Fixture: "f.json"}, ec, template.ActionContext{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(b) != `{"a":1}` {
		t.Fatalf("body = %q", string(b))
	}
}

func TestBuildBody_FixtureMissing(t *testing.T) {
	_, err := buildBody(&sensor.BodyFromConfig{Fixture: "nope"}, &step.ExecContext{Fixtures: map[string]string{}}, template.ActionContext{})
	if err == nil {
		t.Fatal("expected error for missing fixture")
	}
}

func TestBuildBody_Inline(t *testing.T) {
	b, err := buildBody(&sensor.BodyFromConfig{Inline: map[string]interface{}{"k": "v"}}, &step.ExecContext{}, template.ActionContext{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(b) != `{"k":"v"}` {
		t.Fatalf("body = %q", string(b))
	}
}

func TestBuildBody_InlineScalar(t *testing.T) {
	b, err := buildBody(&sensor.BodyFromConfig{Inline: "raw"}, &step.ExecContext{}, template.ActionContext{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(b) != `"raw"` {
		t.Fatalf("body = %q", string(b))
	}
}

func TestBuildBody_Template(t *testing.T) {
	actx := template.ActionContext{
		Env: map[string]string{"X": "y"},
	}
	b, err := buildBody(&sensor.BodyFromConfig{Template: `{"x":"${{ env.X }}"}`}, &step.ExecContext{}, actx)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(b) != `{"x":"y"}` {
		t.Fatalf("body = %q", string(b))
	}
}

func TestBuildBody_RejectsMultipleFields(t *testing.T) {
	_, err := buildBody(&sensor.BodyFromConfig{Fixture: "a", Inline: "b"}, &step.ExecContext{Fixtures: map[string]string{"a": "/x"}}, template.ActionContext{})
	if err == nil {
		t.Fatal("expected error for multiple body_from fields")
	}
}
