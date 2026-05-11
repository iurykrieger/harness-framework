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

RID_DIR="$(ls .runtime/sensors/requires-tool-missing 2>/dev/null | head -n1 || true)"
[ -n "$RID_DIR" ] || { echo "FAIL: no run_id dir for requires-tool-missing"; exit 1; }
[ -s ".runtime/sensors/requires-tool-missing/$RID_DIR/signals.log" ] || { echo "FAIL: signals.log empty"; exit 1; }

# Compare stdout vs signals.log (byte-for-byte).
# json.NewEncoder appends a trailing newline; printf '%s\n' normalizes OUTPUT to match.
EXPECTED="$(printf '%s\n' "$OUTPUT")"
ACTUAL="$(cat ".runtime/sensors/requires-tool-missing/$RID_DIR/signals.log")"
if [ "$EXPECTED" != "$ACTUAL" ]; then
  echo "FAIL: stdout != signals.log"
  echo "stdout: $EXPECTED"
  echo "signals.log: $ACTUAL"
  exit 1
fi

echo "=== Smoke 2: requires-context-missing should fail with verdict=error and heal_hint=missing-context:* ==="
OUTPUT="$(go run -tags=run_computational ./skills/run-sensor/scripts requires-context-missing 2>/dev/null || true)"
echo "$OUTPUT" | grep -q '"verdict":"error"'         || { echo "FAIL: no verdict=error"; exit 1; }
echo "$OUTPUT" | grep -q '"heal_hint":"missing-context' || { echo "FAIL: no missing-context heal_hint"; exit 1; }

RID_DIR="$(ls .runtime/sensors/requires-context-missing 2>/dev/null | head -n1 || true)"
[ -n "$RID_DIR" ] || { echo "FAIL: no run_id dir for requires-context-missing"; exit 1; }
[ -s ".runtime/sensors/requires-context-missing/$RID_DIR/signals.log" ] || { echo "FAIL: signals.log empty"; exit 1; }

echo "OK"
