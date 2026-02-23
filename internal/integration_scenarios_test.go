// Package integration_test — scenario tests derived from a live power-cut test
// recorded on 2026-02-23 against a CyberPower CP1500EPFCLCD (900W).
//
// Four real NUT variable snapshots were captured at key moments:
//
//	snapshotNormal   — steady-state on mains  (16:40:18, ups.status=OL)
//	snapshotOnBattery — first OB poll         (16:40:28, ups.status=OB DISCHRG)
//	snapshotOLDischrg — transitional state    (16:43:08, ups.status=OL DISCHRG)
//	snapshotCharging  — first charging poll   (16:43:18, ups.status=OL CHRG)
//
// TestPowerCutSequence exercises a FakePoller whose Sequence steps through all
// four snapshots, verifying the full status machine in a single test.
package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/sweeney/ups-mqtt/internal/metrics"
	"github.com/sweeney/ups-mqtt/internal/nut"
	"github.com/sweeney/ups-mqtt/internal/publisher"
)

// ---------------------------------------------------------------------------
// Captured snapshots
// ---------------------------------------------------------------------------

// snapshotNormal is the last complete poll before mains were cut (16:40:18).
var snapshotNormal = []nut.Variable{
	{Name: "driver.debug", Value: "0"},
	{Name: "input.transfer.low", Value: "170"},
	{Name: "ups.beeper.status", Value: "false"},
	{Name: "ups.model", Value: "CP1500EPFCLCD"},
	{Name: "battery.mfr.date", Value: "CPS"},
	{Name: "battery.runtime.low", Value: "300"},
	{Name: "driver.state", Value: "quiet"},
	{Name: "driver.version.usb", Value: "libusb-1.0.28 (API: 0x100010a)"},
	{Name: "ups.delay.start", Value: "30"},
	{Name: "ups.vendorid", Value: "764"},
	{Name: "driver.flag.allow_killpower", Value: "0"},
	{Name: "driver.parameter.pollinterval", Value: "2"},
	{Name: "input.transfer.high", Value: "260"},
	{Name: "input.voltage.nominal", Value: "230"},
	{Name: "ups.mfr", Value: "CPS"},
	{Name: "ups.serial", Value: "CRXKS2000211"},
	{Name: "ups.status", Value: "OL"},
	{Name: "battery.voltage", Value: "24"},
	{Name: "driver.name", Value: "usbhid-ups"},
	{Name: "driver.version", Value: "2.8.1"},
	{Name: "driver.version.internal", Value: "0.52"},
	{Name: "ups.delay.shutdown", Value: "20"},
	{Name: "battery.charge.warning", Value: "20"},
	{Name: "battery.type", Value: "PbAcid"},
	{Name: "driver.parameter.port", Value: "auto"},
	{Name: "ups.timer.shutdown", Value: "-60"},
	{Name: "ups.timer.start", Value: "-60"},
	{Name: "device.mfr", Value: "CPS"},
	{Name: "driver.parameter.pollfreq", Value: "30"},
	{Name: "driver.version.data", Value: "CyberPower HID 0.8"},
	{Name: "battery.charge", Value: "100"},
	{Name: "device.model", Value: "CP1500EPFCLCD"},
	{Name: "input.voltage", Value: "241"},
	{Name: "output.voltage", Value: "241"},
	{Name: "ups.load", Value: "8"},
	{Name: "ups.realpower.nominal", Value: "900"},
	{Name: "ups.test.result", Value: "No test initiated"},
	{Name: "device.serial", Value: "CRXKS2000211"},
	{Name: "driver.parameter.synchronous", Value: "auto"},
	{Name: "ups.productid", Value: "501"},
	{Name: "battery.charge.low", Value: "10"},
	{Name: "battery.runtime", Value: "4890"},
	{Name: "battery.voltage.nominal", Value: "24"},
	{Name: "device.type", Value: "ups"},
}

// snapshotOnBattery is the first poll after mains were lost (16:40:28).
// ups.status = "OB DISCHRG"; input.voltage still shows 241 (one poll lag).
var snapshotOnBattery = []nut.Variable{
	{Name: "battery.charge", Value: "100"},
	{Name: "driver.state", Value: "quiet"},
	{Name: "battery.charge.low", Value: "10"},
	{Name: "battery.mfr.date", Value: "CPS"},
	{Name: "device.model", Value: "CP1500EPFCLCD"},
	{Name: "device.type", Value: "ups"},
	{Name: "driver.flag.allow_killpower", Value: "0"},
	{Name: "driver.parameter.pollfreq", Value: "30"},
	{Name: "driver.parameter.pollinterval", Value: "2"},
	{Name: "driver.version.internal", Value: "0.52"},
	{Name: "driver.parameter.port", Value: "auto"},
	{Name: "driver.parameter.synchronous", Value: "auto"},
	{Name: "driver.version.usb", Value: "libusb-1.0.28 (API: 0x100010a)"},
	{Name: "output.voltage", Value: "241"},
	{Name: "ups.delay.start", Value: "30"},
	{Name: "ups.mfr", Value: "CPS"},
	{Name: "ups.model", Value: "CP1500EPFCLCD"},
	{Name: "ups.status", Value: "OB DISCHRG"},
	{Name: "battery.charge.warning", Value: "20"},
	{Name: "battery.runtime.low", Value: "300"},
	{Name: "battery.type", Value: "PbAcid"},
	{Name: "battery.voltage", Value: "24"},
	{Name: "driver.version.data", Value: "CyberPower HID 0.8"},
	{Name: "input.voltage.nominal", Value: "230"},
	{Name: "ups.serial", Value: "CRXKS2000211"},
	{Name: "ups.test.result", Value: "No test initiated"},
	{Name: "device.mfr", Value: "CPS"},
	{Name: "driver.name", Value: "usbhid-ups"},
	{Name: "input.transfer.low", Value: "170"},
	{Name: "ups.productid", Value: "501"},
	{Name: "battery.runtime", Value: "4090"},
	{Name: "battery.voltage.nominal", Value: "24"},
	{Name: "device.serial", Value: "CRXKS2000211"},
	{Name: "driver.debug", Value: "0"},
	{Name: "input.voltage", Value: "241"},
	{Name: "ups.timer.shutdown", Value: "-60"},
	{Name: "input.transfer.high", Value: "260"},
	{Name: "ups.timer.start", Value: "-60"},
	{Name: "ups.vendorid", Value: "764"},
	{Name: "driver.version", Value: "2.8.1"},
	{Name: "ups.beeper.status", Value: "false"},
	{Name: "ups.delay.shutdown", Value: "20"},
	{Name: "ups.load", Value: "8"},
	{Name: "ups.realpower.nominal", Value: "900"},
}

// snapshotOLDischrg is the brief transitional poll as mains were restored
// (16:43:08). ups.status = "OL DISCHRG"; input.voltage reads 0 — the UPS
// firmware reports zero for one poll while reconnecting to mains.
var snapshotOLDischrg = []nut.Variable{
	{Name: "input.transfer.low", Value: "170"},
	{Name: "ups.model", Value: "CP1500EPFCLCD"},
	{Name: "ups.productid", Value: "501"},
	{Name: "ups.status", Value: "OL DISCHRG"},
	{Name: "battery.charge.low", Value: "10"},
	{Name: "device.model", Value: "CP1500EPFCLCD"},
	{Name: "driver.debug", Value: "0"},
	{Name: "driver.flag.allow_killpower", Value: "0"},
	{Name: "driver.parameter.pollinterval", Value: "2"},
	{Name: "driver.version.data", Value: "CyberPower HID 0.8"},
	{Name: "input.transfer.high", Value: "260"},
	{Name: "input.voltage", Value: "0"},
	{Name: "battery.mfr.date", Value: "CPS"},
	{Name: "driver.parameter.synchronous", Value: "auto"},
	{Name: "driver.version", Value: "2.8.1"},
	{Name: "driver.version.usb", Value: "libusb-1.0.28 (API: 0x100010a)"},
	{Name: "ups.delay.shutdown", Value: "20"},
	{Name: "ups.delay.start", Value: "30"},
	{Name: "ups.serial", Value: "CRXKS2000211"},
	{Name: "ups.timer.shutdown", Value: "-60"},
	{Name: "battery.runtime.low", Value: "300"},
	{Name: "ups.mfr", Value: "CPS"},
	{Name: "ups.realpower.nominal", Value: "900"},
	{Name: "driver.parameter.pollfreq", Value: "30"},
	{Name: "ups.timer.start", Value: "-60"},
	{Name: "battery.charge", Value: "87"},
	{Name: "battery.voltage", Value: "24"},
	{Name: "battery.voltage.nominal", Value: "24"},
	{Name: "driver.name", Value: "usbhid-ups"},
	{Name: "driver.state", Value: "quiet"},
	{Name: "output.voltage", Value: "230"},
	{Name: "device.serial", Value: "CRXKS2000211"},
	{Name: "driver.version.internal", Value: "0.52"},
	{Name: "input.voltage.nominal", Value: "230"},
	{Name: "ups.vendorid", Value: "764"},
	{Name: "battery.charge.warning", Value: "20"},
	{Name: "battery.runtime", Value: "3588"},
	{Name: "device.type", Value: "ups"},
	{Name: "driver.parameter.port", Value: "auto"},
	{Name: "ups.beeper.status", Value: "false"},
	{Name: "ups.load", Value: "7"},
	{Name: "ups.test.result", Value: "No test initiated"},
	{Name: "battery.type", Value: "PbAcid"},
	{Name: "device.mfr", Value: "CPS"},
}

// snapshotCharging is the first poll with active charging (16:43:18).
// ups.status = "OL CHRG"; input.voltage restored to 242.
var snapshotCharging = []nut.Variable{
	{Name: "ups.timer.start", Value: "-60"},
	{Name: "battery.charge.low", Value: "10"},
	{Name: "driver.flag.allow_killpower", Value: "0"},
	{Name: "driver.name", Value: "usbhid-ups"},
	{Name: "driver.version.internal", Value: "0.52"},
	{Name: "driver.version.usb", Value: "libusb-1.0.28 (API: 0x100010a)"},
	{Name: "ups.mfr", Value: "CPS"},
	{Name: "ups.model", Value: "CP1500EPFCLCD"},
	{Name: "battery.runtime", Value: "3586"},
	{Name: "driver.debug", Value: "0"},
	{Name: "ups.beeper.status", Value: "false"},
	{Name: "ups.timer.shutdown", Value: "-60"},
	{Name: "input.transfer.low", Value: "170"},
	{Name: "input.voltage.nominal", Value: "230"},
	{Name: "ups.productid", Value: "501"},
	{Name: "ups.vendorid", Value: "764"},
	{Name: "battery.charge", Value: "88"},
	{Name: "battery.runtime.low", Value: "300"},
	{Name: "driver.parameter.pollinterval", Value: "2"},
	{Name: "driver.state", Value: "quiet"},
	{Name: "driver.version", Value: "2.8.1"},
	{Name: "ups.delay.shutdown", Value: "20"},
	{Name: "battery.charge.warning", Value: "20"},
	{Name: "battery.mfr.date", Value: "CPS"},
	{Name: "device.mfr", Value: "CPS"},
	{Name: "device.serial", Value: "CRXKS2000211"},
	{Name: "device.type", Value: "ups"},
	{Name: "driver.parameter.pollfreq", Value: "30"},
	{Name: "driver.parameter.port", Value: "auto"},
	{Name: "ups.load", Value: "8"},
	{Name: "battery.voltage", Value: "24"},
	{Name: "device.model", Value: "CP1500EPFCLCD"},
	{Name: "input.voltage", Value: "242"},
	{Name: "output.voltage", Value: "242"},
	{Name: "ups.delay.start", Value: "30"},
	{Name: "ups.realpower.nominal", Value: "900"},
	{Name: "ups.serial", Value: "CRXKS2000211"},
	{Name: "ups.status", Value: "OL CHRG"},
	{Name: "battery.type", Value: "PbAcid"},
	{Name: "battery.voltage.nominal", Value: "24"},
	{Name: "driver.parameter.synchronous", Value: "auto"},
	{Name: "driver.version.data", Value: "CyberPower HID 0.8"},
	{Name: "input.transfer.high", Value: "260"},
	{Name: "ups.test.result", Value: "No test initiated"},
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

var defaultCfg = publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower", Retained: true}

// pollOnce runs a single FakePoller→metrics→FakePublisher pipeline and
// returns the computed metrics and publisher so the caller can make assertions.
func pollOnce(t *testing.T, vars []nut.Variable) (metrics.Metrics, *publisher.FakePublisher) {
	t.Helper()
	fp := &nut.FakePoller{Variables: vars}
	fpub := &publisher.FakePublisher{}

	polled, err := fp.Poll()
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	varMap := nut.VarsToMap(polled)
	m := metrics.Compute(varMap)
	if err := publisher.PublishAll(varMap, m, defaultCfg, fpub); err != nil {
		t.Fatalf("PublishAll: %v", err)
	}
	return m, fpub
}

func requireTopic(t *testing.T, fpub *publisher.FakePublisher, topic, wantPayload string) {
	t.Helper()
	msg, ok := fpub.Find(topic)
	if !ok {
		t.Errorf("topic %q not published", topic)
		return
	}
	if msg.Payload != wantPayload {
		t.Errorf("topic %q: payload = %q, want %q", topic, msg.Payload, wantPayload)
	}
}

// ---------------------------------------------------------------------------
// Scenario: Normal (OL)
// ---------------------------------------------------------------------------

func TestScenario_Normal_ComputedMetrics(t *testing.T) {
	// input.voltage=241, nominal=230 → deviation = (241-230)/230*100 ≈ 4.78%
	// battery.runtime=4890 → 81.5 min / 1.36 hr
	// ups.load=8, nominal=900 → 72W
	m, fpub := pollOnce(t, snapshotNormal)

	if m.OnBattery {
		t.Error("OnBattery should be false")
	}
	if m.LowBattery {
		t.Error("LowBattery should be false")
	}
	if m.LoadWatts != 72 {
		t.Errorf("LoadWatts = %v, want 72", m.LoadWatts)
	}
	if m.BatteryRuntimeMins != 81.5 {
		t.Errorf("BatteryRuntimeMins = %v, want 81.5", m.BatteryRuntimeMins)
	}
	if m.BatteryRuntimeHours != 1.36 {
		t.Errorf("BatteryRuntimeHours = %v, want 1.36", m.BatteryRuntimeHours)
	}
	if m.StatusDisplay != "Online" {
		t.Errorf("StatusDisplay = %q, want %q", m.StatusDisplay, "Online")
	}
	if m.InputVoltageDeviationPct != 4.78 {
		t.Errorf("InputVoltageDeviationPct = %v, want 4.78", m.InputVoltageDeviationPct)
	}

	requireTopic(t, fpub, "ups/cyberpower/computed/load_watts", "72")
	requireTopic(t, fpub, "ups/cyberpower/computed/battery_runtime_mins", "81.5")
	requireTopic(t, fpub, "ups/cyberpower/computed/battery_runtime_hours", "1.36")
	requireTopic(t, fpub, "ups/cyberpower/computed/on_battery", "false")
	requireTopic(t, fpub, "ups/cyberpower/computed/status_display", "Online")
	requireTopic(t, fpub, "ups/cyberpower/computed/input_voltage_deviation_pct", "4.78")
}

func TestScenario_Normal_VariableTopics(t *testing.T) {
	_, fpub := pollOnce(t, snapshotNormal)

	requireTopic(t, fpub, "ups/cyberpower/battery/charge", "100")
	requireTopic(t, fpub, "ups/cyberpower/ups/status", "OL")
	requireTopic(t, fpub, "ups/cyberpower/battery/runtime", "4890")
	requireTopic(t, fpub, "ups/cyberpower/input/voltage", "241")
}

// ---------------------------------------------------------------------------
// Scenario: On Battery (OB DISCHRG)
// ---------------------------------------------------------------------------

func TestScenario_OnBattery_Detection(t *testing.T) {
	m, fpub := pollOnce(t, snapshotOnBattery)

	if !m.OnBattery {
		t.Error("OnBattery should be true for OB DISCHRG")
	}
	if m.LowBattery {
		t.Error("LowBattery should be false (charge is 100%)")
	}
	requireTopic(t, fpub, "ups/cyberpower/computed/on_battery", "true")
	requireTopic(t, fpub, "ups/cyberpower/computed/low_battery", "false")
}

func TestScenario_OnBattery_StatusDisplay(t *testing.T) {
	m, fpub := pollOnce(t, snapshotOnBattery)

	if m.StatusDisplay != "On Battery, Discharging" {
		t.Errorf("StatusDisplay = %q, want %q", m.StatusDisplay, "On Battery, Discharging")
	}
	requireTopic(t, fpub, "ups/cyberpower/computed/status_display", "On Battery, Discharging")
}

func TestScenario_OnBattery_RuntimeMetrics(t *testing.T) {
	// battery.runtime=4090 → 68.17 min / 1.14 hr
	m, fpub := pollOnce(t, snapshotOnBattery)

	if m.BatteryRuntimeMins != 68.17 {
		t.Errorf("BatteryRuntimeMins = %v, want 68.17", m.BatteryRuntimeMins)
	}
	if m.BatteryRuntimeHours != 1.14 {
		t.Errorf("BatteryRuntimeHours = %v, want 1.14", m.BatteryRuntimeHours)
	}
	if m.LoadWatts != 72 {
		t.Errorf("LoadWatts = %v, want 72 (load unchanged during outage)", m.LoadWatts)
	}
	requireTopic(t, fpub, "ups/cyberpower/computed/battery_runtime_mins", "68.17")
	requireTopic(t, fpub, "ups/cyberpower/computed/load_watts", "72")
}

func TestScenario_OnBattery_StateJSON(t *testing.T) {
	_, fpub := pollOnce(t, snapshotOnBattery)

	msg, ok := fpub.Find("ups/cyberpower/state")
	if !ok {
		t.Fatal("state topic not published")
	}
	var state publisher.StateMessage
	if err := json.Unmarshal([]byte(msg.Payload), &state); err != nil {
		t.Fatalf("state JSON invalid: %v", err)
	}
	if !state.Computed.OnBattery {
		t.Error("state.computed.on_battery should be true")
	}
	if state.Computed.StatusDisplay != "On Battery, Discharging" {
		t.Errorf("state.computed.status_display = %q", state.Computed.StatusDisplay)
	}
	if state.Variables["ups.status"] != "OB DISCHRG" {
		t.Errorf("state.variables[ups.status] = %q, want %q",
			state.Variables["ups.status"], "OB DISCHRG")
	}
}

// ---------------------------------------------------------------------------
// Scenario: OL DISCHRG (transitional — mains just restored)
// ---------------------------------------------------------------------------

func TestScenario_OLDischrg_NotOnBattery(t *testing.T) {
	// OL DISCHRG: status token is OL, not OB — on_battery should be false
	// even though the inverter is still active.
	m, fpub := pollOnce(t, snapshotOLDischrg)

	if m.OnBattery {
		t.Error("OnBattery should be false for OL DISCHRG")
	}
	if m.StatusDisplay != "Online, Discharging" {
		t.Errorf("StatusDisplay = %q, want %q", m.StatusDisplay, "Online, Discharging")
	}
	requireTopic(t, fpub, "ups/cyberpower/computed/on_battery", "false")
	requireTopic(t, fpub, "ups/cyberpower/computed/status_display", "Online, Discharging")
}

func TestScenario_OLDischrg_ZeroInputVoltage(t *testing.T) {
	// The CyberPower firmware reports input.voltage=0 for one poll during
	// reconnect.  The daemon should handle this without panicking and report
	// the mathematically correct (if transient) deviation of -100%.
	m, _ := pollOnce(t, snapshotOLDischrg)

	if m.InputVoltageDeviationPct != -100 {
		t.Errorf("InputVoltageDeviationPct = %v with input.voltage=0, want -100", m.InputVoltageDeviationPct)
	}
}

func TestScenario_OLDischrg_ReducedLoad(t *testing.T) {
	// ups.load=7 during the transitional poll → 7% × 900W = 63W
	m, fpub := pollOnce(t, snapshotOLDischrg)

	if m.LoadWatts != 63 {
		t.Errorf("LoadWatts = %v, want 63 (ups.load=7 during transition)", m.LoadWatts)
	}
	requireTopic(t, fpub, "ups/cyberpower/computed/load_watts", "63")
}

// ---------------------------------------------------------------------------
// Scenario: Charging (OL CHRG)
// ---------------------------------------------------------------------------

func TestScenario_Charging_StatusDisplay(t *testing.T) {
	m, fpub := pollOnce(t, snapshotCharging)

	if m.StatusDisplay != "Online, Charging" {
		t.Errorf("StatusDisplay = %q, want %q", m.StatusDisplay, "Online, Charging")
	}
	if m.OnBattery {
		t.Error("OnBattery should be false while charging")
	}
	requireTopic(t, fpub, "ups/cyberpower/computed/status_display", "Online, Charging")
	requireTopic(t, fpub, "ups/cyberpower/computed/on_battery", "false")
}

func TestScenario_Charging_RestoredVoltage(t *testing.T) {
	// input.voltage back to 242 after transitional 0-reading
	// → deviation should be 5.22% again
	m, fpub := pollOnce(t, snapshotCharging)

	if m.InputVoltageDeviationPct != 5.22 {
		t.Errorf("InputVoltageDeviationPct = %v, want 5.22 (mains restored)", m.InputVoltageDeviationPct)
	}
	requireTopic(t, fpub, "ups/cyberpower/computed/input_voltage_deviation_pct", "5.22")
}

func TestScenario_Charging_RuntimeMetrics(t *testing.T) {
	// battery.runtime=3586 → 59.77 min
	m, fpub := pollOnce(t, snapshotCharging)

	if m.BatteryRuntimeMins != 59.77 {
		t.Errorf("BatteryRuntimeMins = %v, want 59.77", m.BatteryRuntimeMins)
	}
	requireTopic(t, fpub, "ups/cyberpower/computed/battery_runtime_mins", "59.77")
}

// ---------------------------------------------------------------------------
// Full power-cut sequence
// ---------------------------------------------------------------------------

// TestPowerCutSequence drives a FakePoller through all four captured snapshots
// in order and asserts the key state at each step.  This mirrors the live test
// from 2026-02-23.
func TestPowerCutSequence(t *testing.T) {
	type step struct {
		name            string
		wantStatus      string
		wantOnBattery   bool
		wantStatusLabel string
		wantLoadWatts   float64
	}

	steps := []step{
		{
			name:            "pre-outage (OL)",
			wantStatus:      "OL",
			wantOnBattery:   false,
			wantStatusLabel: "Online",
			wantLoadWatts:   72,
		},
		{
			name:            "on battery (OB DISCHRG)",
			wantStatus:      "OB DISCHRG",
			wantOnBattery:   true,
			wantStatusLabel: "On Battery, Discharging",
			wantLoadWatts:   72,
		},
		{
			name:            "transitional (OL DISCHRG)",
			wantStatus:      "OL DISCHRG",
			wantOnBattery:   false,
			wantStatusLabel: "Online, Discharging",
			wantLoadWatts:   63,
		},
		{
			name:            "charging (OL CHRG)",
			wantStatus:      "OL CHRG",
			wantOnBattery:   false,
			wantStatusLabel: "Online, Charging",
			wantLoadWatts:   72,
		},
	}

	fp := &nut.FakePoller{
		Sequence: [][]nut.Variable{
			snapshotNormal,
			snapshotOnBattery,
			snapshotOLDischrg,
			snapshotCharging,
		},
	}

	for i, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			fpub := &publisher.FakePublisher{}

			vars, err := fp.Poll()
			if err != nil {
				t.Fatalf("Poll %d: %v", i, err)
			}
			varMap := nut.VarsToMap(vars)
			m := metrics.Compute(varMap)
			if err := publisher.PublishAll(varMap, m, defaultCfg, fpub); err != nil {
				t.Fatalf("PublishAll %d: %v", i, err)
			}

			if varMap["ups.status"] != s.wantStatus {
				t.Errorf("ups.status = %q, want %q", varMap["ups.status"], s.wantStatus)
			}
			if m.OnBattery != s.wantOnBattery {
				t.Errorf("OnBattery = %v, want %v", m.OnBattery, s.wantOnBattery)
			}
			if m.StatusDisplay != s.wantStatusLabel {
				t.Errorf("StatusDisplay = %q, want %q", m.StatusDisplay, s.wantStatusLabel)
			}
			if m.LoadWatts != s.wantLoadWatts {
				t.Errorf("LoadWatts = %v, want %v", m.LoadWatts, s.wantLoadWatts)
			}
		})
	}

	if fp.CallCount != 4 {
		t.Errorf("FakePoller.CallCount = %d, want 4", fp.CallCount)
	}
}

// TestPowerCutSequence_SequenceRepeatsLastElement verifies that once the
// sequence is exhausted the FakePoller repeats the final snapshot.
func TestPowerCutSequence_SequenceRepeatsLastElement(t *testing.T) {
	fp := &nut.FakePoller{
		Sequence: [][]nut.Variable{
			snapshotOnBattery,
			snapshotCharging,
		},
	}

	// Call 1 → snapshotOnBattery
	vars, _ := fp.Poll()
	if nut.VarsToMap(vars)["ups.status"] != "OB DISCHRG" {
		t.Errorf("call 1: ups.status = %q, want OB DISCHRG", nut.VarsToMap(vars)["ups.status"])
	}
	// Call 2 → snapshotCharging
	vars, _ = fp.Poll()
	if nut.VarsToMap(vars)["ups.status"] != "OL CHRG" {
		t.Errorf("call 2: ups.status = %q, want OL CHRG", nut.VarsToMap(vars)["ups.status"])
	}
	// Call 3 → snapshotCharging (repeated — sequence exhausted)
	vars, _ = fp.Poll()
	if nut.VarsToMap(vars)["ups.status"] != "OL CHRG" {
		t.Errorf("call 3: ups.status = %q, want OL CHRG (last repeated)", nut.VarsToMap(vars)["ups.status"])
	}
}
