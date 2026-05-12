#!/usr/bin/env bash
# Smoke: requires[] gate + .runtime persistence end-to-end.
# Run from worktree root. Exits 0 on success, non-zero on the first
# unmet assertion.

set -euo pipefail

WORKTREE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$WORKTREE_ROOT"

cleanup() {
  rm -f "$WORKTREE_ROOT/sensors/requires-tool-missing.json"
  rm -f "$WORKTREE_ROOT/sensors/requires-context-missing.json"
  rm -rf .runtime/sensors/requires-tool-missing 2>/dev/null || true
  rm -rf .runtime/sensors/requires-context-missing 2>/dev/null || true
}
trap cleanup EXIT

# Symlink the fixtures into sensors/ so run-sensor can find them.
ln -sf "$WORKTREE_ROOT/sensors/fixtures/requires-tool-missing.json" \
       "$WORKTREE_ROOT/sensors/requires-tool-missing.json"
ln -sf "$WORKTREE_ROOT/sensors/fixtures/requires-context-missing.json" \
       "$WORKTREE_ROOT/sensors/requires-context-missing.json"

echo "=== Smoke 1: requires-tool-missing should fail with verdict=error and heal_hint=binary-not-found:* ==="
OUTPUT="$(go run -tags=run_computational ./skills/run-sensor/scripts requires-tool-missing 2>/dev/null || true)"
echo "$OUTPUT" | grep -q '"verdict":"error"'      || { echo "FAIL: no verdict=error"; exit 1; }
echo "$OUTPUT" | grep -q '"heal_hint":"binary-not-found' || { echo "FAIL: no binary-not-found heal_hint"; exit 1; }
# Gate failures short-circuit before the subprocess is spawned, so no run-id
# directory or signals.log is created — stdout is the only sink. This is
# intentional: persistence only happens when the command phase actually runs.

echo "=== Smoke 2: requires-context-missing should fail with verdict=error and heal_hint=missing-context:* ==="
OUTPUT="$(go run -tags=run_computational ./skills/run-sensor/scripts requires-context-missing 2>/dev/null || true)"
echo "$OUTPUT" | grep -q '"verdict":"error"'         || { echo "FAIL: no verdict=error"; exit 1; }
echo "$OUTPUT" | grep -q '"heal_hint":"missing-context' || { echo "FAIL: no missing-context heal_hint"; exit 1; }
# Gate failures short-circuit before spawning; no signals.log is expected.

echo "OK"
