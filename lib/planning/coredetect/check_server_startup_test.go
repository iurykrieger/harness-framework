package coredetect

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/stack"
)

func TestCheckServerStartupRegistered(t *testing.T) {
	if Get("check-server-startup") == nil {
		t.Fatal("expected non-nil ScaffoldFunc for check-server-startup")
	}
}

func TestCheckServerStartupNilWithoutHTTPServer(t *testing.T) {
	fn := Get("check-server-startup")
	if fn == nil {
		t.Fatal("expected non-nil ScaffoldFunc for check-server-startup")
	}
	if fn(stack.Stack{}) != nil {
		t.Fatal("expected nil Draft for check-server-startup on stack without http-server")
	}
}

func TestCheckServerStartupWithHTTPServer(t *testing.T) {
	fn := Get("check-server-startup")
	if fn == nil {
		t.Fatal("expected non-nil ScaffoldFunc for check-server-startup")
	}
	s := stack.Stack{
		Components: []stack.Component{
			{Role: stack.RoleHTTPServer, Name: "chi"},
		},
	}
	d := fn(s)
	if d == nil {
		t.Fatal("expected non-nil Draft for check-server-startup on stack with http-server")
	}
}
