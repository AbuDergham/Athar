package syncer

import "testing"

func TestParseMachineID(t *testing.T) {
	for in, want := range map[string]string{
		"0123456789abcdef0123456789abcdef\n": "0123456789abcdef0123456789abcdef",
		"0123456789abcdef0123456789abcdef":   "0123456789abcdef0123456789abcdef",
		"":                                   "",
		"uninitialized\n":                    "",
		"0123456789ABCDEF0123456789ABCDEF\n": "",
		"0123456789abcdef\n":                 "",
	} {
		if got := parseMachineID(in); got != want {
			t.Errorf("parseMachineID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseIOPlatformUUID(t *testing.T) {
	out := `+-o J314sAP  <class IOPlatformExpertDevice, id 0x100000254, registered, matched, active, busy 0 (1234 ms), retain 39>
    {
      "IOPlatformSerialNumber" = "XXXXXXXXXX"
      "IOPlatformUUID" = "1A2B3C4D-5E6F-7081-92A3-B4C5D6E7F809"
      "model" = <"MacBookPro18,3">
    }
`
	if got := parseIOPlatformUUID(out); got != "1A2B3C4D-5E6F-7081-92A3-B4C5D6E7F809" {
		t.Errorf("parseIOPlatformUUID = %q", got)
	}
	if got := parseIOPlatformUUID(`"IOPlatformUUID" = "short"`); got != "" {
		t.Errorf("malformed: %q", got)
	}
}
