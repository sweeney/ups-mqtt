package nut

import "testing"

// The Riello iPlug pads fixed-width identity fields out of the device's own
// buffers; those pads must never reach MQTT.
func TestVarsToMap_TrimsPaddedValues(t *testing.T) {
	m := VarsToMap([]Variable{
		{Name: "ups.model", Value: "USV5           "},
		{Name: "ups.mfr", Value: "RPS S.p.a."},
		{Name: "device.model", Value: "  USV5  "},
	})

	if got := m["ups.model"]; got != "USV5" {
		t.Errorf("ups.model = %q, want %q", got, "USV5")
	}
	if got := m["device.model"]; got != "USV5" {
		t.Errorf("device.model = %q, want %q", got, "USV5")
	}
	if got := m["ups.mfr"]; got != "RPS S.p.a." {
		t.Errorf("ups.mfr = %q, want unchanged", got)
	}
}

// A field that is nothing but padding carries no information, so the variable
// should be absent rather than present-and-empty.
func TestVarsToMap_DropsBlankValues(t *testing.T) {
	m := VarsToMap([]Variable{
		{Name: "ups.serial", Value: "                 "},
		{Name: "device.serial", Value: ""},
		{Name: "ups.status", Value: "OL"},
	})

	for _, k := range []string{"ups.serial", "device.serial"} {
		if v, ok := m[k]; ok {
			t.Errorf("%s should be dropped, got %q", k, v)
		}
	}
	if m["ups.status"] != "OL" {
		t.Errorf("ups.status = %q, want OL", m["ups.status"])
	}
	if len(m) != 1 {
		t.Errorf("map has %d entries, want 1", len(m))
	}
}

// Unpopulated bypass registers read as 0xFFF; published raw they would look
// like a 4 kV supply at 409 Hz.
func TestVarsToMap_DropsSentinelValues(t *testing.T) {
	m := VarsToMap([]Variable{
		{Name: "input.bypass.voltage", Value: "4095"},
		{Name: "input.bypass.frequency", Value: "409.50"},
		{Name: "input.voltage", Value: "250"},
		{Name: "input.frequency", Value: "50.10"},
	})

	for _, k := range []string{"input.bypass.voltage", "input.bypass.frequency"} {
		if v, ok := m[k]; ok {
			t.Errorf("%s should be dropped as a sentinel, got %q", k, v)
		}
	}
	if m["input.voltage"] != "250" {
		t.Errorf("input.voltage = %q, want 250", m["input.voltage"])
	}
	if m["input.frequency"] != "50.10" {
		t.Errorf("input.frequency = %q, want 50.10", m["input.frequency"])
	}
}

// The sentinel filter is keyed per variable: the same number arriving on a
// different variable is a real reading and must survive.
func TestVarsToMap_SentinelIsPerVariable(t *testing.T) {
	m := VarsToMap([]Variable{
		{Name: "input.voltage", Value: "4095"},
		{Name: "ups.realpower.nominal", Value: "409.5"},
	})

	if m["input.voltage"] != "4095" {
		t.Errorf("input.voltage = %q, want 4095 kept (not a sentinel for this variable)", m["input.voltage"])
	}
	if m["ups.realpower.nominal"] != "409.5" {
		t.Errorf("ups.realpower.nominal = %q, want kept", m["ups.realpower.nominal"])
	}
}

// Trimming happens before the sentinel check, so a padded sentinel is caught.
func TestVarsToMap_TrimsBeforeSentinelCheck(t *testing.T) {
	m := VarsToMap([]Variable{{Name: "input.bypass.voltage", Value: "  4095 "}})
	if v, ok := m["input.bypass.voltage"]; ok {
		t.Errorf("padded sentinel should be dropped, got %q", v)
	}
}

// Regression: upsd sends "409.50", but go.nut decodes values into interface{}
// and the client renders them with %v, so the driver actually hands us
// "409.5".  A textual match missed it on the real device; every spelling of
// the same number must be caught.
func TestVarsToMap_SentinelMatchesNumerically(t *testing.T) {
	for _, spelling := range []string{"409.50", "409.5", "409.500", "4.095e2"} {
		m := VarsToMap([]Variable{{Name: "input.bypass.frequency", Value: spelling}})
		if v, ok := m["input.bypass.frequency"]; ok {
			t.Errorf("sentinel spelled %q should be dropped, got %q", spelling, v)
		}
	}
	for _, spelling := range []string{"4095", "4095.0", "4.095e3"} {
		m := VarsToMap([]Variable{{Name: "input.bypass.voltage", Value: spelling}})
		if v, ok := m["input.bypass.voltage"]; ok {
			t.Errorf("sentinel spelled %q should be dropped, got %q", spelling, v)
		}
	}
}

// A near-miss is a real reading and must survive.
func TestVarsToMap_SentinelDoesNotOverreach(t *testing.T) {
	m := VarsToMap([]Variable{
		{Name: "input.bypass.frequency", Value: "409.4"},
		{Name: "input.bypass.voltage", Value: "4094"},
	})
	if m["input.bypass.frequency"] != "409.4" {
		t.Errorf("input.bypass.frequency = %q, want 409.4 kept", m["input.bypass.frequency"])
	}
	if m["input.bypass.voltage"] != "4094" {
		t.Errorf("input.bypass.voltage = %q, want 4094 kept", m["input.bypass.voltage"])
	}
}

// A non-numeric value on a sentinel-bearing variable is passed through rather
// than dropped, since it cannot be compared.
func TestVarsToMap_SentinelIgnoresNonNumeric(t *testing.T) {
	m := VarsToMap([]Variable{{Name: "input.bypass.voltage", Value: "N/A"}})
	if m["input.bypass.voltage"] != "N/A" {
		t.Errorf("input.bypass.voltage = %q, want \"N/A\" kept", m["input.bypass.voltage"])
	}
}
