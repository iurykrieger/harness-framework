package http_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/step"
	httpstep "github.com/iurykrieger/harness-framework/lib/step/http"
)

func TestHTTP_2xxDefaultPass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	defer srv.Close()
	s, err := httpstep.New(sensor.StepConfig{
		ID: "p", Type: "http", Method: "GET", URL: srv.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v (err=%v)", res.Verdict, res.Err)
	}
	if res.Response == nil || res.Response.Status != 204 {
		t.Fatalf("response missing or wrong status: %+v", res.Response)
	}
	if len(res.Signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(res.Signals))
	}
	md, _ := res.Signals[0]["metadata"].(map[string]interface{})
	if md == nil || md["kind"] != "http_observation" {
		t.Fatalf("metadata.kind not http_observation: %+v", res.Signals[0])
	}
}

func TestHTTP_4xxDefaultFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	s, _ := httpstep.New(sensor.StepConfig{
		ID: "p", Type: "http", Method: "GET", URL: srv.URL,
	})
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictFail {
		t.Fatalf("verdict = %v", res.Verdict)
	}
}

func TestHTTP_ExpectStatusEquals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		fmt.Fprint(w, `{"id":"abc","items":[1]}`)
	}))
	defer srv.Close()
	s, _ := httpstep.New(sensor.StepConfig{
		ID: "p", Type: "http", Method: "POST", URL: srv.URL,
		Expect: map[string]interface{}{
			"status": map[string]interface{}{"equals": 201},
			"body": []interface{}{
				map[string]interface{}{"jsonpath": "$.id", "matches": `^[a-z]+$`},
				map[string]interface{}{"jsonpath": "$.items"},
			},
		},
	})
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v stdout=%q outputs=%+v err=%v", res.Verdict, res.Stdout, res.Outputs, res.Err)
	}
}

func TestHTTP_ExpectStatusEqualsFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	s, _ := httpstep.New(sensor.StepConfig{
		ID: "p", Type: "http", Method: "GET", URL: srv.URL,
		Expect: map[string]interface{}{
			"status": map[string]interface{}{"equals": 201},
		},
	})
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictFail {
		t.Fatalf("verdict = %v (want fail)", res.Verdict)
	}
}

func TestHTTP_ExpectHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
	}))
	defer srv.Close()
	s, _ := httpstep.New(sensor.StepConfig{
		ID: "p", Type: "http", Method: "GET", URL: srv.URL,
		Expect: map[string]interface{}{
			"headers": map[string]interface{}{
				"Content-Type": map[string]interface{}{"contains": "json"},
			},
		},
	})
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v err=%v", res.Verdict, res.Err)
	}
}

func TestHTTP_BodyFromFixture(t *testing.T) {
	seen := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 64)
		n, _ := r.Body.Read(b)
		seen = string(b[:n])
		w.WriteHeader(201)
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := filepath.Join(dir, "order-valid.json")
	if err := os.WriteFile(f, []byte(`{"sku":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := httpstep.New(sensor.StepConfig{
		ID: "p", Type: "http", Method: "POST", URL: srv.URL,
		BodyFrom: &sensor.BodyFromConfig{Fixture: "order-valid.json"},
	})
	res := s.Execute(context.Background(), &step.ExecContext{
		Fixtures: map[string]string{"order-valid.json": f},
		Env:      map[string]string{},
	})
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v err=%v", res.Verdict, res.Err)
	}
	if seen != `{"sku":"x"}` {
		t.Fatalf("server saw %q", seen)
	}
}

func TestHTTP_BodyFromInlineJSON(t *testing.T) {
	seen := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 128)
		n, _ := r.Body.Read(b)
		seen = string(b[:n])
		w.WriteHeader(201)
	}))
	defer srv.Close()

	s, _ := httpstep.New(sensor.StepConfig{
		ID: "p", Type: "http", Method: "POST", URL: srv.URL,
		BodyFrom: &sensor.BodyFromConfig{
			Inline: map[string]interface{}{"sku": "x"},
		},
	})
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v err=%v", res.Verdict, res.Err)
	}
	if seen != `{"sku":"x"}` {
		t.Fatalf("server saw %q", seen)
	}
}

func TestHTTP_BodyFromTemplate(t *testing.T) {
	seen := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 128)
		n, _ := r.Body.Read(b)
		seen = string(b[:n])
		w.WriteHeader(200)
	}))
	defer srv.Close()

	s, _ := httpstep.New(sensor.StepConfig{
		ID: "p", Type: "http", Method: "POST", URL: srv.URL,
		BodyFrom: &sensor.BodyFromConfig{
			Template: `{"x":"${{ env.SKU }}"}`,
		},
	})
	res := s.Execute(context.Background(), &step.ExecContext{
		Env: map[string]string{"SKU": "abc"},
	})
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v err=%v", res.Verdict, res.Err)
	}
	if seen != `{"x":"abc"}` {
		t.Fatalf("server saw %q (want template-rendered body)", seen)
	}
}

func TestHTTP_RendersURLAndHeaders(t *testing.T) {
	gotPath := ""
	gotHeader := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Get("X-Tenant")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	s, _ := httpstep.New(sensor.StepConfig{
		ID: "p", Type: "http", Method: "GET",
		URL:     srv.URL + "/${{ env.TENANT }}",
		Headers: map[string]string{"X-Tenant": "${{ env.TENANT }}"},
	})
	res := s.Execute(context.Background(), &step.ExecContext{
		Env: map[string]string{"TENANT": "acme"},
	})
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v err=%v", res.Verdict, res.Err)
	}
	if gotPath != "/acme" {
		t.Fatalf("path rendered as %q", gotPath)
	}
	if gotHeader != "acme" {
		t.Fatalf("header rendered as %q", gotHeader)
	}
}

func TestHTTP_OutputsFromResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		fmt.Fprint(w, `{"id":"abc-123"}`)
	}))
	defer srv.Close()

	s, _ := httpstep.New(sensor.StepConfig{
		ID: "p", Type: "http", Method: "POST", URL: srv.URL,
		Outputs: map[string]sensor.OutputSpec{
			"order_id": {From: "response.body", JSONPath: "$.id"},
			"code":     {From: "response.status"},
		},
	})
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v err=%v", res.Verdict, res.Err)
	}
	if res.Outputs["order_id"] != "abc-123" {
		t.Fatalf("order_id = %q", res.Outputs["order_id"])
	}
	if res.Outputs["code"] != "201" {
		t.Fatalf("code = %q", res.Outputs["code"])
	}
}

func TestHTTP_NetworkError(t *testing.T) {
	s, _ := httpstep.New(sensor.StepConfig{
		ID: "p", Type: "http", Method: "GET",
		// Closed loopback port; connection refused.
		URL: "http://127.0.0.1:1",
	})
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictError {
		t.Fatalf("verdict = %v (want error)", res.Verdict)
	}
	if len(res.Signals) != 1 {
		t.Fatalf("expected one error signal, got %d", len(res.Signals))
	}
}

func TestHTTP_New_RejectsNonHTTPType(t *testing.T) {
	if _, err := httpstep.New(sensor.StepConfig{ID: "x", Type: "shell"}); err == nil {
		t.Fatal("expected error for non-http type")
	}
}

func TestHTTP_New_RejectsMissingMethodOrURL(t *testing.T) {
	if _, err := httpstep.New(sensor.StepConfig{ID: "x", Type: "http", URL: "http://x"}); err == nil {
		t.Fatal("expected error for missing method")
	}
	if _, err := httpstep.New(sensor.StepConfig{ID: "x", Type: "http", Method: "GET"}); err == nil {
		t.Fatal("expected error for missing url")
	}
}
