package nut

import (
	"strconv"
	"strings"
)

// Variable holds a single NUT variable name/value pair.
// Value is always normalised to a string; callers parse as needed.
type Variable struct {
	Name  string
	Value string
}

// Poller abstracts the NUT data source so tests can inject a fake.
type Poller interface {
	Poll() ([]Variable, error)
	Close() error
}

// sentinelValues lists, per NUT variable, the values a driver reports when the
// underlying hardware does not populate that field.  They are not readings and
// must never reach MQTT: published raw, Riello's unpopulated bypass registers
// look like a 4 kV supply at 409 Hz.
//
// 4095 is 0xFFF — an all-ones 12-bit ADC word — and 409.5 is the same word
// scaled by ten, which is how the riello_usb driver reports frequency.  Both
// sit far outside any real mains reading, so a genuine value can never collide.
//
// Comparison is numeric, not textual.  upsd sends "409.50" but go.nut decodes
// values into interface{} and the client renders them back with %v, which
// drops the trailing zero to "409.5"; matching on the string would silently
// stop working depending on which side of that conversion the check runs.
var sentinelValues = map[string][]float64{
	"input.bypass.voltage":   {4095},
	"input.bypass.frequency": {409.5},
}

// isSentinel reports whether value is a known not-populated marker for name.
// Both operands originate as exact decimal literals, so equality is reliable.
func isSentinel(name, value string) bool {
	candidates, ok := sentinelValues[name]
	if !ok {
		return false
	}
	v, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return false
	}
	for _, s := range candidates {
		if v == s {
			return true
		}
	}
	return false
}

// VarsToMap converts a []Variable slice into a name→value map for downstream
// use (metrics computation, topic publishing, etc.).
//
// It is the single normalisation point for every variable entering the system,
// and applies two corrections that NUT itself does not:
//
//   - Values are trimmed.  Some drivers pad fixed-width fields out of the
//     device's own buffers — the Riello iPlug reports ups.model as
//     "USV5           " and ups.serial as pure spaces.
//   - Padding-only and sentinel values are dropped entirely, so the variable
//     is absent rather than present-but-meaningless.  An absent key publishes
//     no topic at all, which is the honest representation of "this device
//     cannot tell us".
func VarsToMap(vars []Variable) map[string]string {
	m := make(map[string]string, len(vars))
	for _, v := range vars {
		value := strings.TrimSpace(v.Value)
		if value == "" || isSentinel(v.Name, value) {
			continue
		}
		m[v.Name] = value
	}
	return m
}
