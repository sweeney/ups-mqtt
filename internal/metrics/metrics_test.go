package metrics

import (
	"testing"
)

// sampleVars mirrors the actual device output from upsc.txt.
var sampleVars = map[string]string{
	"ups.load":               "8",
	"ups.realpower.nominal":  "900",
	"battery.runtime":        "4920",
	"input.voltage":          "242.0",
	"input.voltage.nominal":  "230",
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

// ---- Compute / LoadWatts --------------------------------------------------

func TestLoadWatts_Normal(t *testing.T) {
	m := Compute(sampleVars)
	if m.LoadWatts != 72 {
		t.Errorf("LoadWatts = %v, want 72", m.LoadWatts)
	}
}

func TestLoadWatts_MissingLoad(t *testing.T) {
	vars := map[string]string{"ups.realpower.nominal": "900"}
	if m := Compute(vars); m.LoadWatts != 0 {
		t.Errorf("LoadWatts = %v with missing ups.load, want 0", m.LoadWatts)
	}
}

func TestLoadWatts_MissingNominal(t *testing.T) {
	vars := map[string]string{"ups.load": "8"}
	if m := Compute(vars); m.LoadWatts != 0 {
		t.Errorf("LoadWatts = %v with missing ups.realpower.nominal, want 0", m.LoadWatts)
	}
}

func TestLoadWatts_BadLoad(t *testing.T) {
	vars := map[string]string{"ups.load": "bad", "ups.realpower.nominal": "900"}
	if m := Compute(vars); m.LoadWatts != 0 {
		t.Errorf("LoadWatts = %v with unparseable ups.load, want 0", m.LoadWatts)
	}
}

func TestLoadWatts_BadNominal(t *testing.T) {
	vars := map[string]string{"ups.load": "8", "ups.realpower.nominal": "bad"}
	if m := Compute(vars); m.LoadWatts != 0 {
		t.Errorf("LoadWatts = %v with unparseable ups.realpower.nominal, want 0", m.LoadWatts)
	}
}

// ---- BatteryRuntimeMins --------------------------------------------------

func TestBatteryRuntimeMins_Normal(t *testing.T) {
	m := Compute(sampleVars)
	if m.BatteryRuntimeMins != 82 {
		t.Errorf("BatteryRuntimeMins = %v, want 82", m.BatteryRuntimeMins)
	}
}

func TestBatteryRuntimeMins_Missing(t *testing.T) {
	if m := Compute(map[string]string{}); m.BatteryRuntimeMins != 0 {
		t.Errorf("BatteryRuntimeMins = %v with missing var, want 0", m.BatteryRuntimeMins)
	}
}

func TestBatteryRuntimeMins_Bad(t *testing.T) {
	vars := map[string]string{"battery.runtime": "notanumber"}
	if m := Compute(vars); m.BatteryRuntimeMins != 0 {
		t.Errorf("BatteryRuntimeMins = %v with bad value, want 0", m.BatteryRuntimeMins)
	}
}

// ---- BatteryRuntimeHours -------------------------------------------------

func TestBatteryRuntimeHours_Normal(t *testing.T) {
	m := Compute(sampleVars)
	// 4920 / 3600 = 1.3666... → rounds to 1.37
	if !nearlyEqual(m.BatteryRuntimeHours, 1.37) {
		t.Errorf("BatteryRuntimeHours = %v, want ~1.37", m.BatteryRuntimeHours)
	}
}

func TestBatteryRuntimeHours_Missing(t *testing.T) {
	if m := Compute(map[string]string{}); m.BatteryRuntimeHours != 0 {
		t.Errorf("BatteryRuntimeHours = %v with missing var, want 0", m.BatteryRuntimeHours)
	}
}

func TestBatteryRuntimeHours_Bad(t *testing.T) {
	vars := map[string]string{"battery.runtime": "xyz"}
	if m := Compute(vars); m.BatteryRuntimeHours != 0 {
		t.Errorf("BatteryRuntimeHours = %v with bad value, want 0", m.BatteryRuntimeHours)
	}
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
	m := Compute(sampleVars)
	// (242 - 230) / 230 * 100 = 5.2173...% → rounds to 5.22
	if !nearlyEqual(m.InputVoltageDeviationPct, 5.22) {
		t.Errorf("InputVoltageDeviationPct = %v, want ~5.22", m.InputVoltageDeviationPct)
	}
}

func TestInputVoltageDeviationPct_MissingVoltage(t *testing.T) {
	vars := map[string]string{"input.voltage.nominal": "230"}
	if m := Compute(vars); m.InputVoltageDeviationPct != 0 {
		t.Errorf("InputVoltageDeviationPct = %v with missing voltage, want 0", m.InputVoltageDeviationPct)
	}
}

func TestInputVoltageDeviationPct_MissingNominal(t *testing.T) {
	vars := map[string]string{"input.voltage": "242.0"}
	if m := Compute(vars); m.InputVoltageDeviationPct != 0 {
		t.Errorf("InputVoltageDeviationPct = %v with missing nominal, want 0", m.InputVoltageDeviationPct)
	}
}

func TestInputVoltageDeviationPct_ZeroNominal(t *testing.T) {
	vars := map[string]string{"input.voltage": "242.0", "input.voltage.nominal": "0"}
	if m := Compute(vars); m.InputVoltageDeviationPct != 0 {
		t.Errorf("InputVoltageDeviationPct = %v with zero nominal, want 0 (guard against div-by-zero)", m.InputVoltageDeviationPct)
	}
}

func TestInputVoltageDeviationPct_BadVoltage(t *testing.T) {
	vars := map[string]string{"input.voltage": "bad", "input.voltage.nominal": "230"}
	if m := Compute(vars); m.InputVoltageDeviationPct != 0 {
		t.Errorf("InputVoltageDeviationPct = %v with bad voltage, want 0", m.InputVoltageDeviationPct)
	}
}

func TestInputVoltageDeviationPct_BadNominal(t *testing.T) {
	vars := map[string]string{"input.voltage": "242.0", "input.voltage.nominal": "bad"}
	if m := Compute(vars); m.InputVoltageDeviationPct != 0 {
		t.Errorf("InputVoltageDeviationPct = %v with bad nominal, want 0", m.InputVoltageDeviationPct)
	}
}

// ---- parseFloat (via Compute) -------------------------------------------

func TestParseFloat_EmptyString(t *testing.T) {
	// parseFloat("") is covered by the "missing variable" tests above,
	// but add an explicit case to make the intent obvious.
	vars := map[string]string{"ups.load": "", "ups.realpower.nominal": "900"}
	if m := Compute(vars); m.LoadWatts != 0 {
		t.Errorf("LoadWatts = %v with empty ups.load, want 0", m.LoadWatts)
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
