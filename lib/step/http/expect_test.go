package http

import (
	"net/http"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/signal"
)

func TestEvalExpect_NilDefaultsByStatus(t *testing.T) {
	cases := []struct {
		status int
		want   signal.Verdict
	}{
		{204, signal.VerdictPass},
		{301, signal.VerdictPass},
		{404, signal.VerdictFail},
		{500, signal.VerdictFail},
		{100, signal.VerdictError},
	}
	for _, c := range cases {
		got, _ := evalExpect(nil, c.status, nil, nil)
		if got != c.want {
			t.Errorf("status=%d: got %v want %v", c.status, got, c.want)
		}
	}
}

func TestEvalExpect_StatusEquals(t *testing.T) {
	exp := map[string]interface{}{"status": map[string]interface{}{"equals": 201}}
	if v, _ := evalExpect(exp, 201, nil, nil); v != signal.VerdictPass {
		t.Errorf("equals hit: got %v", v)
	}
	if v, _ := evalExpect(exp, 200, nil, nil); v != signal.VerdictFail {
		t.Errorf("equals miss: got %v", v)
	}
}

func TestEvalExpect_HeaderContains(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	exp := map[string]interface{}{
		"headers": map[string]interface{}{
			"Content-Type": map[string]interface{}{"contains": "json"},
		},
	}
	if v, _ := evalExpect(exp, 200, nil, h); v != signal.VerdictPass {
		t.Errorf("header hit: got %v", v)
	}
	exp2 := map[string]interface{}{
		"headers": map[string]interface{}{
			"Content-Type": map[string]interface{}{"contains": "xml"},
		},
	}
	if v, _ := evalExpect(exp2, 200, nil, h); v != signal.VerdictFail {
		t.Errorf("header miss: got %v", v)
	}
}

func TestEvalExpect_BodySingleMatcher(t *testing.T) {
	body := []byte(`{"id":"abc"}`)
	exp := map[string]interface{}{
		"body": map[string]interface{}{"jsonpath": "$.id", "matches": "^[a-z]+$"},
	}
	if v, _ := evalExpect(exp, 200, body, nil); v != signal.VerdictPass {
		t.Errorf("body single hit: got %v", v)
	}
}

func TestEvalExpect_BodyArrayAllPass(t *testing.T) {
	body := []byte(`{"id":"abc","items":[1,2]}`)
	exp := map[string]interface{}{
		"body": []interface{}{
			map[string]interface{}{"jsonpath": "$.id"},
			map[string]interface{}{"jsonpath": "$.items"},
		},
	}
	if v, _ := evalExpect(exp, 200, body, nil); v != signal.VerdictPass {
		t.Errorf("body array all hit: got %v", v)
	}
}

func TestEvalExpect_BodyArrayOneFails(t *testing.T) {
	body := []byte(`{"id":"abc"}`)
	exp := map[string]interface{}{
		"body": []interface{}{
			map[string]interface{}{"jsonpath": "$.id"},
			map[string]interface{}{"jsonpath": "$.missing"},
		},
	}
	if v, _ := evalExpect(exp, 200, body, nil); v != signal.VerdictFail {
		t.Errorf("body array one miss: got %v", v)
	}
}

func TestEvalExpect_MalformedShape(t *testing.T) {
	if v, _ := evalExpect("oops", 200, nil, nil); v != signal.VerdictError {
		t.Errorf("malformed: got %v", v)
	}
}

func TestMatcherFrom_BareScalar(t *testing.T) {
	m := matcherFrom(42)
	if m.Equals == nil {
		t.Fatalf("bare scalar should collapse to Equals")
	}
}

func TestMatcherFrom_MapWithGteLte(t *testing.T) {
	m := matcherFrom(map[string]interface{}{"gte": 100, "lte": float64(200)})
	if m.Gte == nil || *m.Gte != 100 {
		t.Fatalf("gte = %v", m.Gte)
	}
	if m.Lte == nil || *m.Lte != 200 {
		t.Fatalf("lte = %v", m.Lte)
	}
}
