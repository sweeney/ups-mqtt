// Package metrics provides pure computed/derived functions over NUT variable maps.
// There is no I/O, no external dependencies, and no side effects; all functions
// are safe to call from any goroutine.
package metrics

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// Metrics holds values derived from raw NUT variables.
//
// Numeric fields are pointers because "the UPS cannot tell us" and "the value
// is zero" are different facts, and conflating them is dangerous: a Riello
// iPlug reports no battery.runtime at all, and a flat 0 published to a
// retained topic reads downstream as a battery with no runtime left rather
// than as a UPS that does not measure runtime.  A nil field is omitted from
// the JSON state and publishes no computed/ topic.
//
// JSON tags define the canonical field names used in both the MQTT state topic
// and the per-metric computed/ topics — keeping the wire format in one place.
// When adding a new field, update Compute, AsTopicMap, and the test table.
type Metrics struct {
	LoadWatts                *float64 `json:"load_watts,omitempty"`
	BatteryRuntimeMins       *float64 `json:"battery_runtime_mins,omitempty"`
	BatteryRuntimeHours      *float64 `json:"battery_runtime_hours,omitempty"`
	OnBattery                bool     `json:"on_battery"`
	LowBattery               bool     `json:"low_battery"`
	StatusDisplay            string   `json:"status_display"`
	InputVoltageDeviationPct *float64 `json:"input_voltage_deviation_pct,omitempty"`
}

// AsTopicMap returns each known metric as a topic-name → string-payload pair,
// ready to publish as individual MQTT computed/ topics.
//
// Metrics the UPS cannot supply are omitted rather than published as zero, so
// a device that reports no runtime simply has no battery_runtime_mins topic.
//
// This is the single authoritative source for metric names and their
// string formatting.  Adding a new field to Metrics requires adding one
// entry here; the JSON state topic picks it up automatically via the
// struct tags above.
func (m Metrics) AsTopicMap() map[string]string {
	topics := map[string]string{
		"on_battery":     strconv.FormatBool(m.OnBattery),
		"low_battery":    strconv.FormatBool(m.LowBattery),
		"status_display": m.StatusDisplay,
	}
	optional := map[string]*float64{
		"load_watts":                  m.LoadWatts,
		"battery_runtime_mins":        m.BatteryRuntimeMins,
		"battery_runtime_hours":       m.BatteryRuntimeHours,
		"input_voltage_deviation_pct": m.InputVoltageDeviationPct,
	}
	for name, v := range optional {
		if v != nil {
			topics[name] = formatFloat(*v)
		}
	}
	return topics
}

// statusTokens maps NUT status tokens to human-readable labels.
var statusTokens = map[string]string{
	"OL":      "Online",
	"OB":      "On Battery",
	"LB":      "Low Battery",
	"HB":      "High Battery",
	"RB":      "Replace Battery",
	"CHRG":    "Charging",
	"DISCHRG": "Discharging",
	"BYPASS":  "Bypass",
	"CAL":     "Calibrating",
	"OFF":     "Offline",
	"OVER":    "Overloaded",
	"TRIM":    "Trimming",
	"BOOST":   "Boosting",
	"FSD":     "Forced Shutdown",
}

// Compute derives all metrics from vars, a map of NUT variable name → string value.
// Missing, unparseable or impossible variables yield nil metrics rather than
// zeroes.
func Compute(vars map[string]string) Metrics {
	return Metrics{
		LoadWatts:                computeLoadWatts(vars),
		BatteryRuntimeMins:       computeBatteryRuntime(vars, 60),
		BatteryRuntimeHours:      computeBatteryRuntime(vars, 3600),
		OnBattery:                hasStatusToken(vars["ups.status"], "OB"),
		LowBattery:               hasStatusToken(vars["ups.status"], "LB"),
		StatusDisplay:            computeStatusDisplay(vars),
		InputVoltageDeviationPct: computeInputVoltageDeviationPct(vars),
	}
}

func computeLoadWatts(vars map[string]string) *float64 {
	load, ok := parseFloat(vars["ups.load"])
	if !ok {
		return nil
	}
	nominal, ok := parseFloat(vars["ups.realpower.nominal"])
	if !ok {
		return nil
	}
	return ptr(round2(load / 100 * nominal))
}

// computeBatteryRuntime converts battery.runtime (seconds) into the unit given
// by divisor — 60 for minutes, 3600 for hours.
func computeBatteryRuntime(vars map[string]string, divisor float64) *float64 {
	runtime := BatteryRuntimeSecs(vars)
	if runtime == nil {
		return nil
	}
	return ptr(round2(*runtime / divisor))
}

// Bounds outside which a reading cannot be real.  A driver that misdecodes
// the device's reply sends such values as confidently as real ones: a Riello
// iPlug went from reporting no charge or runtime at all to battery.charge
// 3434 and battery.runtime 3308760 (38 days), and once input.voltage 24346.
// No UPS battery lasts over a week, and no supply a UPS reports, single-phase
// or a three-phase line-to-line voltage (480 V and its tolerance), reaches
// 600 V.
const (
	maxBatteryChargePct   = 100
	maxBatteryRuntimeSecs = 7 * 24 * 60 * 60
	maxInputVoltage       = 600
)

// plausible is the numeric NUT variables whose readings are range-checked,
// with the largest value each can really take.  None can be negative.
var plausible = map[string]float64{
	"battery.charge":  maxBatteryChargePct,
	"battery.runtime": maxBatteryRuntimeSecs,
	"input.voltage":   maxInputVoltage,
}

// plausibleFloat parses the named variable and returns it if it lies in its
// range, else nil: an impossible reading is unknown, not a value.
func plausibleFloat(vars map[string]string, name string) *float64 {
	v, ok := parseFloat(vars[name])
	if !ok || !(v >= 0 && v <= plausible[name]) {
		return nil
	}
	return ptr(v)
}

// BatteryChargePct returns battery.charge, or nil if it is absent, not a
// number, or outside 0–100.
func BatteryChargePct(vars map[string]string) *float64 {
	return plausibleFloat(vars, "battery.charge")
}

// BatteryRuntimeSecs returns battery.runtime, or nil if it is absent, not a
// number, negative, or longer than any UPS battery lasts.
func BatteryRuntimeSecs(vars map[string]string) *float64 {
	return plausibleFloat(vars, "battery.runtime")
}

// Implausible lists the range-checked variables that are present as numbers
// but out of range, as sorted "name=value" pairs; empty when none are.
// Their raw values still travel in state.variables; nothing is derived from
// them.
func Implausible(vars map[string]string) []string {
	var out []string
	for name := range plausible {
		if _, ok := parseFloat(vars[name]); ok && plausibleFloat(vars, name) == nil {
			out = append(out, name+"="+vars[name])
		}
	}
	sort.Strings(out)
	return out
}

func computeStatusDisplay(vars map[string]string) string {
	status := vars["ups.status"]
	if status == "" {
		return ""
	}
	tokens := strings.Fields(status)
	decoded := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if name, ok := statusTokens[t]; ok {
			decoded = append(decoded, name)
		} else {
			decoded = append(decoded, t)
		}
	}
	return strings.Join(decoded, ", ")
}

// computeInputVoltageDeviationPct expresses input.voltage as a percentage
// deviation from nominal.
//
// Not every UPS publishes input.voltage.nominal — the Riello iPlug reports
// only output.voltage.nominal — so that is used as the fallback reference.
// On a line-interactive UPS both describe the same mains specification for
// the region (230 V here), which makes the substitution sound.
func computeInputVoltageDeviationPct(vars map[string]string) *float64 {
	voltage := plausibleFloat(vars, "input.voltage")
	if voltage == nil {
		return nil
	}
	nominal, ok := parseFloat(vars["input.voltage.nominal"])
	if !ok {
		nominal, ok = parseFloat(vars["output.voltage.nominal"])
	}
	if !ok || nominal == 0 {
		return nil
	}
	return ptr(round2((*voltage - nominal) / nominal * 100))
}

// hasStatusToken reports whether the space-separated status string contains token.
func hasStatusToken(status, token string) bool {
	for _, t := range strings.Fields(status) {
		if t == token {
			return true
		}
	}
	return false
}

// parseFloat converts a NUT value string to float64.
// Returns (0, false) for empty or unparseable strings.
func parseFloat(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// round2 rounds v to two decimal places.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// ptr returns a pointer to v, marking the metric as known.
func ptr(v float64) *float64 {
	return &v
}

// formatFloat returns the shortest decimal representation of v with no
// trailing zeros (e.g. 72.0 → "72", 1.37 → "1.37").
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
