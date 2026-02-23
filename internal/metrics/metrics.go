// Package metrics provides pure computed/derived functions over NUT variable maps.
// There is no I/O, no external dependencies, and no side effects; all functions
// are safe to call from any goroutine.
package metrics

import (
	"math"
	"strconv"
	"strings"
)

// Metrics holds values derived from raw NUT variables.
//
// JSON tags define the canonical field names used in both the MQTT state topic
// and the per-metric computed/ topics — keeping the wire format in one place.
// When adding a new field, update Compute, AsTopicMap, and the test table.
type Metrics struct {
	LoadWatts                float64 `json:"load_watts"`
	BatteryRuntimeMins       float64 `json:"battery_runtime_mins"`
	BatteryRuntimeHours      float64 `json:"battery_runtime_hours"`
	OnBattery                bool    `json:"on_battery"`
	LowBattery               bool    `json:"low_battery"`
	StatusDisplay            string  `json:"status_display"`
	InputVoltageDeviationPct float64 `json:"input_voltage_deviation_pct"`
}

// AsTopicMap returns each metric as a topic-name → string-payload pair,
// ready to publish as individual MQTT computed/ topics.
//
// This is the single authoritative source for metric names and their
// string formatting.  Adding a new field to Metrics requires adding one
// entry here; the JSON state topic picks it up automatically via the
// struct tags above.
func (m Metrics) AsTopicMap() map[string]string {
	return map[string]string{
		"load_watts":                  formatFloat(m.LoadWatts),
		"battery_runtime_mins":        formatFloat(m.BatteryRuntimeMins),
		"battery_runtime_hours":       formatFloat(m.BatteryRuntimeHours),
		"on_battery":                  strconv.FormatBool(m.OnBattery),
		"low_battery":                 strconv.FormatBool(m.LowBattery),
		"status_display":              m.StatusDisplay,
		"input_voltage_deviation_pct": formatFloat(m.InputVoltageDeviationPct),
	}
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
// Missing or unparseable variables gracefully produce zero values rather than panics.
func Compute(vars map[string]string) Metrics {
	return Metrics{
		LoadWatts:                computeLoadWatts(vars),
		BatteryRuntimeMins:       computeBatteryRuntimeMins(vars),
		BatteryRuntimeHours:      computeBatteryRuntimeHours(vars),
		OnBattery:                hasStatusToken(vars["ups.status"], "OB"),
		LowBattery:               hasStatusToken(vars["ups.status"], "LB"),
		StatusDisplay:            computeStatusDisplay(vars),
		InputVoltageDeviationPct: computeInputVoltageDeviationPct(vars),
	}
}

func computeLoadWatts(vars map[string]string) float64 {
	load, ok := parseFloat(vars["ups.load"])
	if !ok {
		return 0
	}
	nominal, ok := parseFloat(vars["ups.realpower.nominal"])
	if !ok {
		return 0
	}
	return math.Round(load/100*nominal*100) / 100
}

func computeBatteryRuntimeMins(vars map[string]string) float64 {
	runtime, ok := parseFloat(vars["battery.runtime"])
	if !ok {
		return 0
	}
	return math.Round(runtime/60*100) / 100
}

func computeBatteryRuntimeHours(vars map[string]string) float64 {
	runtime, ok := parseFloat(vars["battery.runtime"])
	if !ok {
		return 0
	}
	return math.Round(runtime/3600*100) / 100
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

func computeInputVoltageDeviationPct(vars map[string]string) float64 {
	voltage, ok := parseFloat(vars["input.voltage"])
	if !ok {
		return 0
	}
	nominal, ok := parseFloat(vars["input.voltage.nominal"])
	if !ok || nominal == 0 {
		return 0
	}
	return math.Round((voltage-nominal)/nominal*100*100) / 100
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

// formatFloat returns the shortest decimal representation of v with no
// trailing zeros (e.g. 72.0 → "72", 1.37 → "1.37").
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
