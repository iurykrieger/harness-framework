---
name: create-sensor
description: Use when the user invokes /create-sensor or asks to create a single sensor that validates a specific acceptance criterion, functional requirement, or use case. Takes a free-text requirement as input, runs an interactive clarification dialogue, composes existing sensors as dependencies, synthesizes fixtures, and persists one new assertion sensor to <project>/.harness/sensors/<id>.json via the schema validator. Distinct from /detect-sensors, which sweeps the whole project; /create-sensor produces exactly one targeted sensor per invocation.
---

# create-sensor

(Body is written in Task 5 once the scripts exist.)
