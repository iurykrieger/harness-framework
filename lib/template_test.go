package lib

import "testing"

func TestRenderTemplate(t *testing.T) {
	cases := []struct {
		name        string
		tmpl        string
		bindings    map[string]string
		want        string
		wantMissing []string
	}{
		{"simple", "Hi {{n}}!", map[string]string{"n": "Iury"}, "Hi Iury!", nil},
		{"repeated slot", "{{a}} + {{a}} = {{b}}", map[string]string{"a": "1", "b": "2"}, "1 + 1 = 2", nil},
		{"missing once", "{{a}} {{b}} {{a}}", map[string]string{"b": "B"}, "{{a}} B {{a}}", []string{"a"}},
		{"whitespace", "{{ name  }}", map[string]string{"name": "x"}, "x", nil},
		{"no slots", "plain", map[string]string{}, "plain", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, missing := RenderTemplate(tc.tmpl, tc.bindings)
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
			if len(missing) != len(tc.wantMissing) {
				t.Errorf("missing=%v want %v", missing, tc.wantMissing)
			}
		})
	}
}
