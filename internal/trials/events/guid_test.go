package events

import "testing"

// The expectations are Python 3.12's trl.events._norm_guid on the same strings.
func TestNormGUIDMatchesPython(t *testing.T) {
	cases := map[string]string{
		"0x-5":   "0x-5",
		"1_0":    "0x000000000000000a",
		"0x1_f":  "0x000000000000001f",
		"0x+5":   "0x+5",
		"-5":     "0x-000000000000005",
		"0x_5":   "0x0000000000000005",
		"0x":     "0x",
		"0x0":    "0x0000000000000000",
		" 0x1F ": "0x000000000000001f",
		"10":     "0x000000000000000a",
		"0x2":    "0x0000000000000002",
	}
	for in, want := range cases {
		if got := NormGUID(in); got != want {
			t.Errorf("NormGUID(%q) = %q, want %q", in, got, want)
		}
	}
}
