---
name: create-sensors
description: Use when the user invokes /create-sensors or asks to author the per-usecase sensor bundle that validates one or more usecases from multiple angles. Resolves the input to one or more usecase ids (by id, journey, path, or free-text match), applies the closed-enum layer matrix in lib/planning/layer/, auto-creates missing platform primitives via lib/planning/coredetect/, and persists each draft to .harness/sensors/<usecase-id>/<sensor-id>.yaml. Distinct from /detect-sensors (sweeps the project for root-tier primitives) and from /validate-usecase (orchestrates the bundle and reports confidence).
---

# create-sensors

Take one or more usecase ids (or a journey id, or a usecase file path, or a free-text requirement) as input and produce, per usecase, the multi-layer bundle of narrow + composite sensors that validates it through every applicable lens. Uses deterministic Go scripts for catalog walking, layer application, and persistence; LLM judgment only when synthesizing fixtures the layer recipe cannot infer from evidence alone.

## Invocation

```
/create-sensors [usecase-id | journey-id | path/to/usecase.yaml | "<free text>"]
```

(Phase prose added in Task 34.)
