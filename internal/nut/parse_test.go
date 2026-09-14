package nut

import (
	"strings"
	"testing"
)

// ── parseVarLine ──────────────────────────────────────────────────────────

func TestParseVarLine_Valid(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		wantName  string
		wantValue string
	}{
		{"simple", `VAR ups battery.charge "100"`, "battery.charge", "100"},
		{"decimal", `VAR ups input.voltage "242.0"`, "input.voltage", "242.0"},
		{"value with spaces", `VAR ups ups.mfr "American Power Conversion"`,
			"ups.mfr", "American Power Conversion"},
		{"trailing spaces preserved", `VAR ups ups.model "USV5   "`, "ups.model", "USV5   "},
		{"value is only spaces", `VAR ups ups.serial "    "`, "ups.serial", "    "},
		{"empty value", `VAR ups ups.serial ""`, "ups.serial", ""},
		{"parentheses and colon", `VAR ups driver.version.usb "libusb-1.0.28 (API: 0x100010a)"`,
			"driver.version.usb", "libusb-1.0.28 (API: 0x100010a)"},
		{"escaped quote", `VAR ups ups.test "say \"hi\""`, "ups.test", `say "hi"`},
		{"escaped backslash", `VAR ups ups.test "a\\b"`, "ups.test", `a\b`},
		{"status with spaces", `VAR ups ups.status "OB DISCHRG"`, "ups.status", "OB DISCHRG"},
		{"deep variable name", `VAR ups driver.parameter.pollinterval "2"`,
			"driver.parameter.pollinterval", "2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := parseVarLine(tc.line, "ups")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.Name != tc.wantName {
				t.Errorf("name = %q, want %q", v.Name, tc.wantName)
			}
			if v.Value != tc.wantValue {
				t.Errorf("value = %q, want %q", v.Value, tc.wantValue)
			}
		})
	}
}

func TestParseVarLine_Rejected(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string // substring the error should mention
	}{
		{"wrong prefix", `SET ups battery.charge "100"`, "VAR"},
		{"no prefix at all", `battery.charge "100"`, "VAR"},
		{"empty line", ``, "VAR"},
		{"only the prefix", `VAR `, "variable name"},
		{"missing value", `VAR ups battery.charge`, "value"},
		{"unquoted value", `VAR ups battery.charge 100`, "quoted"},
		{"missing closing quote", `VAR ups battery.charge "100`, "quoted"},
		{"missing opening quote", `VAR ups battery.charge 100"`, "quoted"},
		{"lone quote", `VAR ups battery.charge "`, "quoted"},
		{"wrong ups name", `VAR otherups battery.charge "100"`, "otherups"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseVarLine(tc.line, "ups")
			if err == nil {
				t.Fatalf("expected an error for %q", tc.line)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A line for a different UPS must be rejected rather than silently attributed
// to ours: upsd multiplexes devices on one connection.
func TestParseVarLine_RejectsForeignUPS(t *testing.T) {
	if _, err := parseVarLine(`VAR otherups ups.status "OL"`, "myups"); err == nil {
		t.Fatal("a line for another UPS must not be accepted")
	}
}

// ── unquote ───────────────────────────────────────────────────────────────

func TestUnquote(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{`"abc"`, "abc", false},
		{`""`, "", false},
		{`"a b"`, "a b", false},
		{`"  "`, "  ", false},
		{`"a\"b"`, `a"b`, false},
		{`"a\\b"`, `a\b`, false},
		{`"\\"`, `\`, false},
		{`"\""`, `"`, false},
		{`"trailing\\"`, `trailing\`, false},
		{`"a\b"`, "ab", false}, // unknown escape: the backslash is dropped
		{`abc`, "", true},      // not quoted
		{`"abc`, "", true},     // no closing quote
		{`abc"`, "", true},     // no opening quote
		{`"`, "", true},        // single quote is too short
		{``, "", true},         // empty
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := unquote(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("unquote(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unquote(%q) returned %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("unquote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ── protocolError ─────────────────────────────────────────────────────────

func TestProtocolError(t *testing.T) {
	if err := protocolError("OK"); err != nil {
		t.Errorf("OK should not be an error, got %v", err)
	}
	if err := protocolError("BEGIN LIST VAR ups"); err != nil {
		t.Errorf("a normal line should not be an error, got %v", err)
	}
	err := protocolError("ERR DATA-STALE")
	if err == nil {
		t.Fatal("ERR line should produce an error")
	}
	if !strings.Contains(err.Error(), "DATA-STALE") {
		t.Errorf("error = %v, want it to carry the code", err)
	}
	// "ERROR" is not the ERR prefix — upsd's marker is "ERR " with a space.
	if err := protocolError("ERROR-ish value"); err != nil {
		t.Errorf("%q should not be treated as a protocol error", "ERROR-ish value")
	}
}

// ── operations without a connection ───────────────────────────────────────

func TestSendAndRead_WithoutConnection(t *testing.T) {
	c := &Client{}
	if err := c.send("LIST VAR ups"); err == nil {
		t.Error("send on an unconnected client should fail")
	}
	if _, err := c.readLine(); err == nil {
		t.Error("readLine on an unconnected client should fail")
	}
}

func TestTimeout_DefaultsWhenUnset(t *testing.T) {
	c := &Client{}
	if got := c.timeout(); got != DefaultTimeout {
		t.Errorf("timeout() = %v, want %v", got, DefaultTimeout)
	}
	c.Timeout = 42
	if got := c.timeout(); got != 42 {
		t.Errorf("timeout() = %v, want the configured 42", got)
	}
}
