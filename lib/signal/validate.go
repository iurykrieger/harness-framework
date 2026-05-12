package signal

import (
	"fmt"
	"io"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// ValidateOrEmergency validates sig against schemas/signal.json. If the
// validation fails, it logs the error to stderr and returns an emergency
// signal (verdict=error, metadata.kind=signal_validation_failed) so the
// bug surfaces without recursion. On success it returns sig unchanged.
//
// fallbackID is used as the sensor_id of the emergency signal when the
// original signal lacks a valid id (e.g., the bug that produced the
// invalid sig also lost the sensor_id).
func ValidateOrEmergency(v *schema.Validator, sig map[string]interface{}, fallbackID string, stderr io.Writer) map[string]interface{} {
	if v == nil {
		return sig
	}
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		fmt.Fprintf(stderr, "BUG: emitted signal failed signal.json validation: %v\n", err)
		return NewBuilder(fallbackID, "0.0.0").
			WithVerdict("error", "high").
			WithKind("signal_validation_failed").
			WithRationale(fmt.Sprintf("signal_validation_failed: %v", err)).
			Build()
	}
	return sig
}
