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
// Comparison is numeric, not textual.  The textual form is not stable: upsd
// sends "409.50", and a client that decodes and re-renders the value (as the
// previous go.nut-based one did, via %v) turns it into "409.5".  Drivers and
// NUT versions also differ in how many decimal places they emit.  Matching on
// the string silently stopped working once, which is how this was found.
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

// unimplementedRule names variables a driver reports as a constant zero because
// the device firmware does not measure them.  A zero is a plausible reading,
// so unlike a sentinel it cannot be recognised by value: the rule is keyed on
// the driver, and optionally the model, that is known to fake it.
type unimplementedRule struct {
	driver string
	models []string // empty matches every device on the driver
	vars   []string
}

// unimplementedVariables are dropped for matching devices, so they publish no
// topic rather than a zero that reads downstream as "nothing plugged in".
//
// riello_usb fills the GPSER "RE" fields (per-phase output current, W and VA)
// with zeros on every device on record — the iPlug, the iDialog 800 and a
// VSD3000 carrying ~270 W — so those go for the whole driver.
//
// Load percent is narrower.  The VSD3000 reports a real ups.load, but the small
// USV5 (iPlug) and UNV1 (iDialog) firmwares send a literal 0 even with a whole
// network on the output, on mains and on battery alike.  Two units in NUT's
// device library show the same, annotated "unimplemented?".
var unimplementedVariables = []unimplementedRule{
	{
		driver: "riello_usb",
		vars: []string{
			"output.L1.current", "output.L1.power", "output.L1.realpower",
			"output.L2.current", "output.L2.power", "output.L2.realpower",
			"output.L3.current", "output.L3.power", "output.L3.realpower",
		},
	},
	{
		driver: "riello_usb",
		models: []string{"USV5", "UNV1"},
		vars:   []string{"ups.load", "output.power.percent"},
	},
}

// matches reports whether the rule applies to the device described by m,
// whose values must already be trimmed.
func (r unimplementedRule) matches(m map[string]string) bool {
	if m["driver.name"] != r.driver {
		return false
	}
	if len(r.models) == 0 {
		return true
	}
	model := m["device.model"]
	if model == "" {
		model = m["ups.model"]
	}
	for _, want := range r.models {
		if model == want {
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
//   - Variables a known driver/model fakes as a constant zero are dropped for
//     the same reason; see unimplementedVariables.
func VarsToMap(vars []Variable) map[string]string {
	m := make(map[string]string, len(vars))
	for _, v := range vars {
		value := strings.TrimSpace(v.Value)
		if value == "" || isSentinel(v.Name, value) {
			continue
		}
		m[v.Name] = value
	}
	for _, rule := range unimplementedVariables {
		if !rule.matches(m) {
			continue
		}
		for _, name := range rule.vars {
			delete(m, name)
		}
	}
	return m
}
