package template_test

import (
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/template"
)

func ctx() template.ActionContext {
	return template.ActionContext{
		Fixtures: map[string]string{"order-valid.json": "/abs/order-valid.json"},
		Env:      map[string]string{"TARGET_URL": "https://stg.api"},
		Steps: map[string]template.ActionStep{
			"create": {
				Verdict: "pass",
				Outputs: map[string]string{"order_id": "abc-123"},
				Response: &template.ActionResponse{
					Status:  201,
					Headers: map[string]string{"content-type": "application/json"},
				},
			},
		},
	}
}

func TestRenderActions_Accessors(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"fixture", "load ${{ fixtures.order-valid.json }}", "load /abs/order-valid.json"},
		{"output", "id=${{ steps.create.outputs.order_id }}", "id=abc-123"},
		{"step verdict", "v=${{ steps.create.verdict }}", "v=pass"},
		{"response status", "s=${{ steps.create.response.status }}", "s=201"},
		{"response header", "ct=${{ steps.create.response.headers.content-type }}", "ct=application/json"},
		{"env", "u=${{ env.TARGET_URL }}", "u=https://stg.api"},
		{"plain", "no placeholder", "no placeholder"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := template.RenderActions(tc.input, ctx())
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderActions_RejectsOperators(t *testing.T) {
	inputs := []string{
		"${{ steps.create.outputs.x + 1 }}",
		"${{ steps.create.verdict == 'pass' }}",
		"${{ contains(x, y) }}",
		"${{ steps.create.outputs.x || 'fallback' }}",
	}
	for _, in := range inputs {
		if _, err := template.RenderActions(in, ctx()); err == nil {
			t.Fatalf("expected error for %q", in)
		}
	}
}

func TestRenderActions_UnknownAccessor(t *testing.T) {
	if _, err := template.RenderActions("${{ steps.missing.outputs.k }}", ctx()); err == nil {
		t.Fatalf("expected error for unknown step")
	}
	if _, err := template.RenderActions("${{ steps.create.outputs.missing }}", ctx()); err == nil {
		t.Fatalf("expected error for undeclared output")
	}
	if _, err := template.RenderActions("${{ env.MISSING }}", ctx()); err == nil {
		t.Fatalf("expected error for missing env var")
	}
}

func TestRenderActions_AllowsIdentifiersWithDashes(t *testing.T) {
	c := template.ActionContext{
		Steps: map[string]template.ActionStep{
			"create-order": {Outputs: map[string]string{"id": "ok"}},
		},
	}
	got, err := template.RenderActions("${{ steps.create-order.outputs.id }}", c)
	if err != nil || got != "ok" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestRenderActions_LiteralBraceSurvives(t *testing.T) {
	out, err := template.RenderActions("plain {{ not actions }}", ctx())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(out, "{{ not actions }}") {
		t.Fatalf("single-brace block should not be touched: %q", out)
	}
}
