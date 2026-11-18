package worldconn

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// The SMSG_CONNECT_TO of the capture names the instance socket on 127.0.0.1:8085 with the key the instance
// connection must echo.
func TestParseConnectToCapture(t *testing.T) {
	raw, err := os.ReadFile("../world/testdata/capture-packets.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Packets map[string]string `json:"packets"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	payload, err := hex.DecodeString(doc.Packets["connect_to"])
	if err != nil {
		t.Fatal(err)
	}
	ct, err := ParseConnectTo(payload)
	if err != nil {
		t.Fatal(err)
	}
	if ct.Serial != 17 || ct.Con != ConnectionInstance {
		t.Fatalf("serial %d, connection %d", ct.Serial, ct.Con)
	}
	if ct.Key != 0x1122334455667788 {
		t.Fatalf("key = 0x%X", ct.Key)
	}
	if ct.NativeRealmAddress != 0x46010046 {
		t.Fatalf("native realm address = 0x%08X", ct.NativeRealmAddress)
	}
	if got := ct.Address(); got != "127.0.0.1:8085" {
		t.Fatalf("address = %q", got)
	}
}

func TestParseConnectToRejects(t *testing.T) {
	base := []byte{1, 0, 0, 0, 1, 0, 0, 0, 1}
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"no address", []byte{0, 0, 0, 0}},
		{"too many addresses", []byte{9, 0, 0, 0}},
		{"truncated fields", []byte{1, 0, 0, 0, 1}},
		{"bad address type", append(append([]byte{}, base...), 9, 0, 0)},
		{"realm connection", append(append([]byte{}, base[:8]...), 0, 1, 127, 0, 0, 1, 0x95, 0x1f)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseConnectTo(tc.payload); err == nil {
				t.Fatal("parsed")
			}
		})
	}
}
