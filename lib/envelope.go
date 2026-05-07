package lib

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// NowFn and NewRunIDFn are package-level overrideable hooks so tests can pin
// timestamps and run ids.
var (
	NowFn      = func() time.Time { return time.Now().UTC() }
	NewRunIDFn = NewUUIDv4
)

// Envelope is the run-scoped Signal scaffold reused across individuals and
// the aggregate within a single sensor invocation.
type Envelope struct {
	SensorID   string `json:"sensor_id"`
	Version    string `json:"version"`
	RunID      string `json:"run_id"`
	StartedAt  string `json:"started_at"`
	SensorType string `json:"sensor_type"`
}

// BuildEnvelope constructs an envelope from a parsed sensor JSON.
func BuildEnvelope(sensor map[string]interface{}) (Envelope, error) {
	id, _ := sensor["id"].(string)
	version, _ := sensor["version"].(string)
	sensorType, _ := sensor["type"].(string)
	if id == "" || version == "" || sensorType == "" {
		return Envelope{}, errors.New("sensor missing id/version/type")
	}
	return Envelope{
		SensorID:   id,
		Version:    version,
		RunID:      NewRunIDFn(),
		StartedAt:  NowFn().Format("2006-01-02T15:04:05Z"),
		SensorType: sensorType,
	}, nil
}

// NewUUIDv4 generates a RFC 4122 v4 UUID without external dependencies.
func NewUUIDv4() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
