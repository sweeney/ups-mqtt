package metrics

import (
	"testing"
)

// sampleVars mirrors the actual device output from upsc.txt (CyberPower).
var sampleVars = map[string]string{
	"ups.load":              "8",
	"ups.realpower.nominal": "900",
	"battery.runtime":       "4920",
	"input.voltage":         "242.0",
	"input.voltage.nominal": "230",
	"ups.status":            "OL",
}

// rielloVars mirrors a real poll of a Riello iPlug (RPS S.p.a. USV5).  It
// reports neither battery.runtime nor battery.charge nor
// input.voltage.nominal, which is exactly why the numeric metrics are
// optional.
var rielloVars = map[string]string{
	"ups.load":               "0",
	"ups.realpower.nominal":  "480",
	"input.voltage":          "250",
	"output.voltage.nominal": "230",
	"battery.voltage":        "13.5",
	"ups.status":             "OL",
}

// nearlyEqual checks that two float64 values are equal to two decimal places.
func nearlyEqual(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 0.005
}

// wantValue asserts that got is present and carries the expected value.
func wantValue(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %v", name, want)
	}
	if !nearlyEqual(*got, want) {
		t.Errorf("%s = %v, want %v", name, *got, want)
	}
}

// wantAbsent asserts that a metric the UPS cannot supply is nil rather than 0.
func wantAbsent(t *testing.T, name string, got *float64) {
	t.Helper()
	if got != nil {
		t.Errorf("%s = %v, want nil (absent, not zero)", name, *got)
	}
}

// ---- Compute / LoadWatts --------------------------------------------------

func TestLoadWatts_Normal(t *testing.T) {
	wantValue(t, "LoadWatts", Compute(sampleVars).LoadWatts, 72)
}

func TestLoadWatts_ZeroLoadIsStillKnown(t *testing.T) {
	// A real 0 % load must stay present — only unknowns become nil.
	wantValue(t, "LoadWatts", Compute(rielloVars).LoadWatts, 0)
}

func TestLoadWatts_MissingLoad(t *testing.T) {
	vars := map[string]string{"ups.realpower.nominal": "900"}
	wantAbsent(t, "LoadWatts", Compute(vars).LoadWatts)
}

func TestLoadWatts_MissingNominal(t *testing.T) {
	vars := map[string]string{"ups.load": "8"}
	wantAbsent(t, "LoadWatts", Compute(vars).LoadWatts)
}

func TestLoadWatts_BadLoad(t *testing.T) {
	vars := map[string]string{"ups.load": "bad", "ups.realpower.nominal": "900"}
	wantAbsent(t, "LoadWatts", Compute(vars).LoadWatts)
}

func TestLoadWatts_BadNominal(t *testing.T) {
	vars := map[string]string{"ups.load": "8", "ups.realpower.nominal": "bad"}
	wantAbsent(t, "LoadWatts", Compute(vars).LoadWatts)
}

// ---- BatteryRuntimeMins --------------------------------------------------

func TestBatteryRuntimeMins_Normal(t *testing.T) {
	wantValue(t, "BatteryRuntimeMins", Compute(sampleVars).BatteryRuntimeMins, 82)
}

func TestBatteryRuntimeMins_Missing(t *testing.T) {
	wantAbsent(t, "BatteryRuntimeMins", Compute(map[string]string{}).BatteryRuntimeMins)
}

func TestBatteryRuntimeMins_Bad(t *testing.T) {
	vars := map[string]string{"battery.runtime": "notanumber"}
	wantAbsent(t, "BatteryRuntimeMins", Compute(vars).BatteryRuntimeMins)
}

// ---- BatteryRuntimeHours -------------------------------------------------

func TestBatteryRuntimeHours_Normal(t *testing.T) {
	// 4920 / 3600 = 1.3666... → rounds to 1.37
	wantValue(t, "BatteryRuntimeHours", Compute(sampleVars).BatteryRuntimeHours, 1.37)
}

func TestBatteryRuntimeHours_Missing(t *testing.T) {
	wantAbsent(t, "BatteryRuntimeHours", Compute(map[string]string{}).BatteryRuntimeHours)
}

func TestBatteryRuntimeHours_Bad(t *testing.T) {
	vars := map[string]string{"battery.runtime": "xyz"}
	wantAbsent(t, "BatteryRuntimeHours", Compute(vars).BatteryRuntimeHours)
}

// A UPS that reports no runtime at all must yield no runtime metrics, rather
// than claiming zero minutes remaining.
func TestBatteryRuntime_AbsentOnRiello(t *testing.T) {
	m := Compute(rielloVars)
	wantAbsent(t, "BatteryRuntimeMins", m.BatteryRuntimeMins)
	wantAbsent(t, "BatteryRuntimeHours", m.BatteryRuntimeHours)
}

// ---- OnBattery / LowBattery ----------------------------------------------

func TestOnBattery_False(t *testing.T) {
	m := Compute(sampleVars) // status = "OL"
	if m.OnBattery {
		t.Error("OnBattery should be false for status OL")
	}
}

func TestOnBattery_True(t *testing.T) {
	vars := map[string]string{"ups.status": "OB"}
	if m := Compute(vars); !m.OnBattery {
		t.Error("OnBattery should be true for status OB")
	}
}

func TestLowBattery_False(t *testing.T) {
	m := Compute(sampleVars)
	if m.LowBattery {
		t.Error("LowBattery should be false for status OL")
	}
}

func TestLowBattery_True(t *testing.T) {
	vars := map[string]string{"ups.status": "LB"}
	if m := Compute(vars); !m.LowBattery {
		t.Error("LowBattery should be true for status LB")
	}
}

func TestOnBattery_LowBattery_BothTrue(t *testing.T) {
	vars := map[string]string{"ups.status": "OB LB"}
	m := Compute(vars)
	if !m.OnBattery {
		t.Error("OnBattery should be true for status OB LB")
	}
	if !m.LowBattery {
		t.Error("LowBattery should be true for status OB LB")
	}
}

func TestOnBattery_EmptyStatus(t *testing.T) {
	vars := map[string]string{"ups.status": ""}
	m := Compute(vars)
	if m.OnBattery || m.LowBattery {
		t.Error("OnBattery and LowBattery should be false for empty status")
	}
}

// ---- StatusDisplay -------------------------------------------------------

func TestStatusDisplay_Online(t *testing.T) {
	m := Compute(sampleVars)
	if m.StatusDisplay != "Online" {
		t.Errorf("StatusDisplay = %q, want %q", m.StatusDisplay, "Online")
	}
}

func TestStatusDisplay_Empty(t *testing.T) {
	vars := map[string]string{"ups.status": ""}
	if m := Compute(vars); m.StatusDisplay != "" {
		t.Errorf("StatusDisplay = %q with empty status, want empty", m.StatusDisplay)
	}
}

func TestStatusDisplay_MultipleTokens(t *testing.T) {
	vars := map[string]string{"ups.status": "OL CHRG"}
	m := Compute(vars)
	if m.StatusDisplay != "Online, Charging" {
		t.Errorf("StatusDisplay = %q, want %q", m.StatusDisplay, "Online, Charging")
	}
}

func TestStatusDisplay_UnknownToken(t *testing.T) {
	vars := map[string]string{"ups.status": "OL NEWTOKEN"}
	m := Compute(vars)
	if m.StatusDisplay != "Online, NEWTOKEN" {
		t.Errorf("StatusDisplay = %q, want %q", m.StatusDisplay, "Online, NEWTOKEN")
	}
}

func TestStatusDisplay_AllKnownTokens(t *testing.T) {
	tokens := []struct {
		token string
		label string
	}{
		{"OL", "Online"},
		{"OB", "On Battery"},
		{"LB", "Low Battery"},
		{"HB", "High Battery"},
		{"RB", "Replace Battery"},
		{"CHRG", "Charging"},
		{"DISCHRG", "Discharging"},
		{"BYPASS", "Bypass"},
		{"CAL", "Calibrating"},
		{"OFF", "Offline"},
		{"OVER", "Overloaded"},
		{"TRIM", "Trimming"},
		{"BOOST", "Boosting"},
		{"FSD", "Forced Shutdown"},
	}
	for _, tc := range tokens {
		t.Run(tc.token, func(t *testing.T) {
			vars := map[string]string{"ups.status": tc.token}
			m := Compute(vars)
			if m.StatusDisplay != tc.label {
				t.Errorf("StatusDisplay(%q) = %q, want %q", tc.token, m.StatusDisplay, tc.label)
			}
		})
	}
}

// ---- InputVoltageDeviationPct --------------------------------------------

func TestInputVoltageDeviationPct_Normal(t *testing.T) {
	// (242 - 230) / 230 * 100 = 5.2173...% → rounds to 5.22
	wantValue(t, "InputVoltageDeviationPct", Compute(sampleVars).InputVoltageDeviationPct, 5.22)
}

// The Riello publishes no input.voltage.nominal, so the output nominal stands
// in for it: (250 - 230) / 230 * 100 = 8.6956...% → 8.7
func TestInputVoltageDeviationPct_FallsBackToOutputNominal(t *testing.T) {
	wantValue(t, "InputVoltageDeviationPct", Compute(rielloVars).InputVoltageDeviationPct, 8.7)
}

// An unparseable input nominal should still fall through to the output nominal.
func TestInputVoltageDeviationPct_BadInputNominalUsesOutput(t *testing.T) {
	vars := map[string]string{
		"input.voltage":          "242.0",
		"input.voltage.nominal":  "bad",
		"output.voltage.nominal": "230",
	}
	wantValue(t, "InputVoltageDeviationPct", Compute(vars).InputVoltageDeviationPct, 5.22)
}

func TestInputVoltageDeviationPct_MissingVoltage(t *testing.T) {
	vars := map[string]string{"input.voltage.nominal": "230"}
	wantAbsent(t, "InputVoltageDeviationPct", Compute(vars).InputVoltageDeviationPct)
}

func TestInputVoltageDeviationPct_NoNominalAnywhere(t *testing.T) {
	vars := map[string]string{"input.voltage": "242.0"}
	wantAbsent(t, "InputVoltageDeviationPct", Compute(vars).InputVoltageDeviationPct)
}

func TestInputVoltageDeviationPct_ZeroNominal(t *testing.T) {
	vars := map[string]string{"input.voltage": "242.0", "input.voltage.nominal": "0"}
	wantAbsent(t, "InputVoltageDeviationPct", Compute(vars).InputVoltageDeviationPct)
}

func TestInputVoltageDeviationPct_ZeroFallbackNominal(t *testing.T) {
	vars := map[string]string{"input.voltage": "242.0", "output.voltage.nominal": "0"}
	wantAbsent(t, "InputVoltageDeviationPct", Compute(vars).InputVoltageDeviationPct)
}

func TestInputVoltageDeviationPct_BadVoltage(t *testing.T) {
	vars := map[string]string{"input.voltage": "bad", "input.voltage.nominal": "230"}
	wantAbsent(t, "InputVoltageDeviationPct", Compute(vars).InputVoltageDeviationPct)
}

func TestInputVoltageDeviationPct_BadNominalNoFallback(t *testing.T) {
	vars := map[string]string{"input.voltage": "242.0", "input.voltage.nominal": "bad"}
	wantAbsent(t, "InputVoltageDeviationPct", Compute(vars).InputVoltageDeviationPct)
}

// On battery the Riello reports input.voltage as a near-zero residual (4 V),
// which is a real reading and must survive as a large negative deviation.
func TestInputVoltageDeviationPct_OnBatteryResidual(t *testing.T) {
	vars := map[string]string{"input.voltage": "4", "output.voltage.nominal": "230"}
	wantValue(t, "InputVoltageDeviationPct", Compute(vars).InputVoltageDeviationPct, -98.26)
}

// ---- parseFloat (via Compute) -------------------------------------------

func TestParseFloat_EmptyString(t *testing.T) {
	vars := map[string]string{"ups.load": "", "ups.realpower.nominal": "900"}
	wantAbsent(t, "LoadWatts", Compute(vars).LoadWatts)
}

// ---- formatFloat ---------------------------------------------------------

func TestFormatFloat_NoTrailingZeros(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{72.0, "72"},
		{1.37, "1.37"},
		{0, "0"},
		{-98.26, "-98.26"},
	}
	for _, tc := range cases {
		if got := formatFloat(tc.in); got != tc.want {
			t.Errorf("formatFloat(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---- AsTopicMap ----------------------------------------------------------

func TestAsTopicMap(t *testing.T) {
	m := Compute(sampleVars)
	tm := m.AsTopicMap()

	cases := []struct {
		key  string
		want string
	}{
		{"load_watts", "72"},
		{"battery_runtime_mins", "82"},
		{"battery_runtime_hours", "1.37"},
		{"on_battery", "false"},
		{"low_battery", "false"},
		{"status_display", "Online"},
		{"input_voltage_deviation_pct", "5.22"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			got, ok := tm[tc.key]
			if !ok {
				t.Fatalf("key %q missing from AsTopicMap()", tc.key)
			}
			if got != tc.want {
				t.Errorf("AsTopicMap()[%q] = %q, want %q", tc.key, got, tc.want)
			}
		})
	}

	// Verify key count matches struct field count to catch any future drift.
	if len(tm) != 7 {
		t.Errorf("AsTopicMap() returned %d keys, want 7", len(tm))
	}
}

// Unknown metrics must publish no topic at all, so a stale retained value is
// never confused for a fresh reading.
func TestAsTopicMap_OmitsAbsentMetrics(t *testing.T) {
	tm := Compute(rielloVars).AsTopicMap()

	for _, absent := range []string{"battery_runtime_mins", "battery_runtime_hours"} {
		if v, ok := tm[absent]; ok {
			t.Errorf("AsTopicMap() should omit %q for a UPS that cannot report it, got %q", absent, v)
		}
	}
	for _, present := range []string{"load_watts", "input_voltage_deviation_pct", "on_battery", "low_battery", "status_display"} {
		if _, ok := tm[present]; !ok {
			t.Errorf("AsTopicMap() should include %q", present)
		}
	}
	if len(tm) != 5 {
		t.Errorf("AsTopicMap() returned %d keys for Riello vars, want 5", len(tm))
	}
}

func TestAsTopicMap_AllAbsent(t *testing.T) {
	tm := Compute(map[string]string{}).AsTopicMap()
	// Only the three always-present fields survive.
	if len(tm) != 3 {
		t.Errorf("AsTopicMap() with no vars returned %d keys, want 3", len(tm))
	}
}
