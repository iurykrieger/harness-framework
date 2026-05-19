---
name: validate-usecase
description: Use when the user invokes /validate-usecase or asks to exercise the per-usecase sensor bundle and report a confidence score. Reads the bundle at .harness/sensors/<usecase-id>/, identifies layer entrypoints (one per unique sensor.layer), invokes the runtime in topological order, and emits a Signal carrying ceiling/coverage/realized counts plus the worst-result aggregate verdict.
---

# validate-usecase

(Phase prose added in Task 37.)
