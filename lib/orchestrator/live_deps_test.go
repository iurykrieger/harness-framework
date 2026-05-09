package orchestrator_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func TestRunOneWithLiveDeps_AttachesAndDetachesBlockingDep(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	writeConsumer(t, root, "uses-tick")

	ctx := context.Background()
	exit := orchestrator.RunWithDepsRoot(ctx, "uses-tick", root, schemasDir, io.Discard, io.Discard)
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}

	r := registry.NewRoot(root)
	rs, err := registry.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	if rs.FindEntry("blocking-tick") != nil {
		t.Fatal("blocking dep should be torn down after the consumer ran")
	}
}

func writeBlockingDep(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A blocking sensor MUST:
	// - have execution.blocking: true
	// - have output: "stream" (blocking forces stream)
	// - NOT have cost.latency.timeout_ms
	// - have execution.output_parsing.patterns (stream requires it)
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Blocking tick",
"description": "blocking tick",
"determinism": "high",
"kind": "setup",
"type": "computational",
"output": "stream",
"regulation": "behaviour",
"phase": "continuous",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50}
},
"execution": {
  "command": "while true; do echo TICK; sleep 0.1; done",
  "blocking": true,
  "graceful_timeout_ms": 200,
  "exit_code_map": [{"exit_code":"*","verdict":"pass","severity":"info"}],
  "output_parsing": {"patterns":[{"regex":"^TICK$","verdict":"pass","severity":"info"}]}
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeConsumer(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, "sensors")
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Uses tick",
"description": "consumer",
"determinism": "high",
"kind": "assertion",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "on-demand",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"depends_on": ["blocking-tick"],
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50,"timeout_ms":2000}
},
"execution": {
  "command": "echo OK",
  "exit_code_map": [{"exit_code":0,"verdict":"pass","severity":"info"},{"exit_code":"*","verdict":"fail","severity":"high"}]
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}
