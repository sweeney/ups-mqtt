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

// Regression: a textual match on "409.50" missed the sentinel on a real
// device, because the go.nut-based client decoded values and re-rendered them
// with %v, handing us "409.5".  The current client keeps upsd's bytes
// verbatim, but drivers and NUT versions still differ in how many decimals
// they emit, so every spelling of the same number must be caught.
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

// ── Variables a driver fakes as a constant zero ───────────────────────────

var rielloLoadVars = []string{
	"ups.load", "output.power.percent",
	"output.L1.current", "output.L1.power", "output.L1.realpower",
	"output.L2.current", "output.L2.power", "output.L2.realpower",
	"output.L3.current", "output.L3.power", "output.L3.realpower",
}

// End to end through the real protocol client, against the captured iPlug
// reply: every load figure goes, everything the device does measure stays.
func TestVarsToMap_RielloIPlugFixture_DropsUnimplementedLoad(t *testing.T) {
	s := newFakeUPSD(t)
	s.UPSName = upsNameFromFixture(t, "listvar_riello.txt")
	s.Lines = varLinesFromFixture(t, "listvar_riello.txt")
	s.start(t)

	vars, err := s.client(t, "", "").Poll()
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	raw := make(map[string]string, len(vars))
	for _, v := range vars {
		raw[v.Name] = v.Value
	}
	for _, name := range rielloLoadVars {
		if raw[name] != "0" {
			t.Fatalf("fixture premise: %s = %q, want the device's constant \"0\"", name, raw[name])
		}
	}

	m := VarsToMap(vars)
	for _, name := range rielloLoadVars {
		if v, ok := m[name]; ok {
			t.Errorf("%s = %q survived; the iPlug does not measure it", name, v)
		}
	}
	for name, want := range map[string]string{
		"input.voltage":         "251",
		"output.voltage":        "245",
		"battery.voltage":       "13.5",
		"ups.realpower.nominal": "480",
		"ups.status":            "OL",
	} {
		if got := m[name]; got != want {
			t.Errorf("%s = %q, want %q (real readings must be untouched)", name, got, want)
		}
	}
}

// A larger riello_usb unit (the VSD3000 in NUT's device library) reports a real
// ups.load, so load percent is dropped by model, not for the whole driver.
func TestVarsToMap_RielloOtherModel_KeepsLoadPercent(t *testing.T) {
	m := VarsToMap([]Variable{
		{Name: "driver.name", Value: "riello_usb"},
		{Name: "device.model", Value: "ULC3           "},
		{Name: "ups.load", Value: "10"},
		{Name: "output.power.percent", Value: "10"},
		{Name: "output.L1.realpower", Value: "0"},
	})
	if m["ups.load"] != "10" || m["output.power.percent"] != "10" {
		t.Errorf("load percent dropped for a model that implements it: %v", m)
	}
	if _, ok := m["output.L1.realpower"]; ok {
		t.Error("RE fields are unimplemented across riello_usb and should be dropped")
	}
}

// The iDialog 800 (UNV1) shares the iPlug firmware's behaviour.
func TestVarsToMap_RielloIDialog_DropsLoadPercent(t *testing.T) {
	m := VarsToMap([]Variable{
		{Name: "driver.name", Value: "riello_usb"},
		{Name: "device.model", Value: "UNV1"},
		{Name: "ups.load", Value: "0"},
	})
	if _, ok := m["ups.load"]; ok {
		t.Error("ups.load should be dropped for UNV1")
	}
}

// device.model is preferred, but ups.model identifies the device when a
// driver does not set device.model.
func TestVarsToMap_UnimplementedFallsBackToUPSModel(t *testing.T) {
	m := VarsToMap([]Variable{
		{Name: "driver.name", Value: "riello_usb"},
		{Name: "ups.model", Value: "USV5           "},
		{Name: "ups.load", Value: "0"},
	})
	if _, ok := m["ups.load"]; ok {
		t.Error("ups.load should be dropped when only ups.model identifies a USV5")
	}
}

// Another driver's zero is a genuine reading — the CyberPower reports 0 % at
// loads below its 9 W step — and must be left alone.
func TestVarsToMap_OtherDriver_KeepsZeroLoad(t *testing.T) {
	m := VarsToMap([]Variable{
		{Name: "driver.name", Value: "usbhid-ups"},
		{Name: "device.model", Value: "USV5"},
		{Name: "ups.load", Value: "0"},
		{Name: "output.L1.realpower", Value: "0"},
	})
	if m["ups.load"] != "0" || m["output.L1.realpower"] != "0" {
		t.Errorf("zeros from another driver were dropped: %v", m)
	}
}

// Without driver.name nothing can be attributed, so nothing is dropped.
func TestVarsToMap_NoDriverName_DropsNothing(t *testing.T) {
	m := VarsToMap([]Variable{
		{Name: "device.model", Value: "USV5"},
		{Name: "ups.load", Value: "0"},
	})
	if m["ups.load"] != "0" {
		t.Error("ups.load dropped with no driver.name to attribute it to")
	}
}
