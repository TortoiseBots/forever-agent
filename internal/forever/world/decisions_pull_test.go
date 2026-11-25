package world

// The standalone decisions read names its file after the module's own run id. The module's ids are numbers, so an
// answer that is anything else never names a file.

import "testing"

func TestNumericRunID(t *testing.T) {
	for _, id := range []string{"1", "1001", "20261124190001"} {
		if !numericRunID(id) {
			t.Errorf("%q was refused", id)
		}
	}
	for _, id := range []string{"", "r-1001", "1001x", "10 01", "../1001", "-1", "١٠٠١"} {
		if numericRunID(id) {
			t.Errorf("%q was accepted", id)
		}
	}
}
