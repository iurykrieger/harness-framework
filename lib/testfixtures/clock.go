package testfixtures

import (
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// FreezeClock pins sensor.NowFn and sensor.NewRunIDFn for deterministic
// Signal output. Returns a restore function; defer it.
func FreezeClock(t *testing.T) func() {
	t.Helper()
	origNow, origID := sensor.NowFn, sensor.NewRunIDFn
	frozen := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	sensor.NowFn = func() time.Time { return frozen }
	sensor.NewRunIDFn = func() string { return "00000000-0000-4000-8000-000000000000" }
	return func() { sensor.NowFn = origNow; sensor.NewRunIDFn = origID }
}
