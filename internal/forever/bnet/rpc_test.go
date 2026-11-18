package bnet

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func hexBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad fixture hex: %v", err)
	}
	return b
}

// The fixtures below are hand-built from the .proto field numbers in proto.go's comments; each one names the fields
// in order so a reader can check the tags (field<<3 | wire type) without running anything.

func TestHeaderRequestFixture(t *testing.T) {
	h := Header{ServiceID: serviceIDRequest, MethodID: connectionConnectMethod, Token: 0, ServiceHash: connectionServiceHash}
	want := "080010011800" + "5D91694465" // 1:0, 2:1, 3:0, 11:fixed32 0x65446991 (little endian)
	if got := hex.EncodeToString(h.marshal()); !strings.EqualFold(got, want) {
		t.Fatalf("header = %s, want %s", got, want)
	}
}

func TestHeaderResponseFixture(t *testing.T) {
	h := Header{ServiceID: serviceIDResponse, Token: 7, Size: 2, Status: 3, Ciid: "ab"}
	want := "08FE01" + "1807" + "2802" + "3003" + "6A026162"
	if got := hex.EncodeToString(h.marshal()); !strings.EqualFold(got, want) {
		t.Fatalf("header = %s, want %s", got, want)
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	for _, h := range []Header{
		{},
		{ServiceID: serviceIDResponse, MethodID: 5, Token: 0xFFFFFFFF, Size: 0x100000, Status: 0x8000012C, ServiceHash: 0x3FC1274D, Ciid: "00112233-4455"},
	} {
		got, err := unmarshalHeader(h.marshal())
		if err != nil {
			t.Fatal(err)
		}
		if got != h {
			t.Fatalf("round trip: %+v, want %+v", got, h)
		}
	}
}

func TestConnectRequestFixture(t *testing.T) {
	// 3: varint 1 (use_bindless_rpc)
	if got := hex.EncodeToString(ConnectRequest{UseBindlessRPC: true}.marshal()); !strings.EqualFold(got, "1801") {
		t.Fatalf("ConnectRequest = %s", got)
	}
	if got := len(ConnectRequest{}.marshal()); got != 0 {
		t.Fatalf("empty ConnectRequest is %d bytes", got)
	}
}

func TestLogonRequestFixture(t *testing.T) {
	req := LogonRequest{
		Program: "WoW", Platform: "Wn64", Locale: "enUS",
		ApplicationVersion: build70938, CachedWebCredentials: []byte("TC-0123"),
	}
	want := "0A03576F57" + "1204576E3634" + "1A04656E5553" + "309AAA04" + "620754432D30313233"
	if got := hex.EncodeToString(req.marshal()); !strings.EqualFold(got, want) {
		t.Fatalf("LogonRequest = %s, want %s", got, want)
	}
}

func TestVariantFixture(t *testing.T) {
	str := "Auth"
	cases := []struct {
		name string
		v    Variant
		want string
	}{
		{"string", Variant{StringValue: &str}, "2A0441757468"},
		{"blob", Variant{BlobValue: []byte{1, 2, 3}}, "3203010203"},
		{"bool false", Variant{BoolValue: new(bool)}, "1000"},
		{"uint", Variant{UintValue: func() *uint64 { v := uint64(0x46010046); return &v }()}, "48C68084B004"},
	}
	for _, c := range cases {
		if got := hex.EncodeToString(c.v.marshal()); !strings.EqualFold(got, c.want) {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
		back, err := unmarshalVariant(hexBytes(t, c.want))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if back.StringValue != nil && *back.StringValue != "Auth" {
			t.Errorf("%s: string value %q", c.name, *back.StringValue)
		}
	}
}

func TestClientRequestFixture(t *testing.T) {
	addr := uint64(0x46010046)
	req := ClientRequest{Attributes: []Attribute{{Name: "Param_RealmAddress", Value: Variant{UintValue: &addr}}}}
	// 1: message (0A 1C) { 1: "Param_RealmAddress" (0A 12), 2: Variant { 9: varint } (12 06 48 ...) }
	want := "0A1C" + "0A12506172616D5F5265616C6D41646472657373" + "1206" + "48C68084B004"
	if got := hex.EncodeToString(req.marshal()); !strings.EqualFold(got, want) {
		t.Fatalf("ClientRequest = %s, want %s", got, want)
	}
}

func TestGetAllValuesFixture(t *testing.T) {
	req := GetAllValuesForAttributeRequest{AttributeKey: "Command_RealmListRequest_v1_b9"}
	want := "0A1E436F6D6D616E645F5265616C6D4C697374526571756573745F76315F6239"
	if got := hex.EncodeToString(req.marshal()); !strings.EqualFold(got, want) {
		t.Fatalf("GetAllValues = %s, want %s", got, want)
	}
	resp, err := unmarshalGetAllValuesForAttributeResponse(hexBytes(t, "0A092A0737302D312D3730")) // 1: Variant{5:"70-1-70"}
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Values) != 1 || resp.Values[0].StringValue == nil || *resp.Values[0].StringValue != "70-1-70" {
		t.Fatalf("values = %+v", resp.Values)
	}
}

func TestConnectResponseFixture(t *testing.T) {
	// 1: ProcessId{1:5,2:6}, 6: server_time, 7: use_bindless_rpc true, 9: ciid
	want := "0A0408051006" + "3088EF99ABC5E88C9111" + "3801" + "4A0463696964"
	resp, err := unmarshalConnectResponse(hexBytes(t, want))
	if err != nil {
		t.Fatal(err)
	}
	if resp.ServerID.Label != 5 || resp.ServerID.Epoch != 6 {
		t.Errorf("server id %+v", resp.ServerID)
	}
	if resp.ServerTime != 0x1122334455667788 {
		t.Errorf("server time %#x", resp.ServerTime)
	}
	if !resp.UseBindlessRPC || resp.Ciid != "ciid" {
		t.Errorf("bindless %t ciid %q", resp.UseBindlessRPC, resp.Ciid)
	}
}

func TestLogonResultFixture(t *testing.T) {
	// 1: error_code 0; 2: EntityId{high 0x0100...00, low 42}; 3: EntityId{high 0x0200000200576F57, low 9};
	// 9: session key 4 bytes
	want := "0800" +
		"1212" + "090000000000000001" + "112A00000000000000" +
		"1A12" + "09576F570002000002" + "110900000000000000" +
		"4A04DEADBEEF"
	res, err := unmarshalLogonResult(hexBytes(t, want))
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != 0 {
		t.Errorf("error code %d", res.ErrorCode)
	}
	if res.AccountID.High != 0x0100000000000000 || res.AccountID.Low != 42 {
		t.Errorf("account id %+v", res.AccountID)
	}
	if len(res.GameAccountIDs) != 1 || res.GameAccountIDs[0].High != 0x0200000200576F57 || res.GameAccountIDs[0].Low != 9 {
		t.Errorf("game accounts %+v", res.GameAccountIDs)
	}
	if !bytes.Equal(res.SessionKey, []byte{0xDE, 0xAD, 0xBE, 0xEF}) {
		t.Errorf("session key %X", res.SessionKey)
	}
}

func TestClientResponseFixture(t *testing.T) {
	// 1: Attribute{1:"Param_RealmListTicket", 2:Variant{5:"Auth"}}
	// 1: Attribute{1:"Param_JoinSecret", 2:Variant{6:010203}}
	want := "0A1F0A15506172616D5F5265616C6D4C6973745469636B657412062A0441757468" +
		"0A190A10506172616D5F4A6F696E53656372657412053203010203"
	resp, err := unmarshalClientResponse(hexBytes(t, want))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Attributes) != 2 {
		t.Fatalf("%d attributes", len(resp.Attributes))
	}
	if b, ok := resp.blob("Param_JoinSecret"); !ok || !bytes.Equal(b, []byte{1, 2, 3}) {
		t.Fatalf("join secret %v %t", b, ok)
	}
	if resp.Attributes[0].Name != "Param_RealmListTicket" || resp.Attributes[0].Value.StringValue == nil || *resp.Attributes[0].Value.StringValue != "Auth" {
		t.Fatalf("first attribute %+v", resp.Attributes[0])
	}
}

func TestAttributeRoundTrip(t *testing.T) {
	a := attrString("Command_RealmListRequest_v1_b9", "70-1-70")
	back, err := unmarshalAttribute(a.marshal())
	if err != nil {
		t.Fatal(err)
	}
	if back.Name != a.Name || back.Value.StringValue == nil || *back.Value.StringValue != "70-1-70" {
		t.Fatalf("round trip %+v", back)
	}
}

// TestFrameRoundTrip checks the frame layout on the wire: uint16 BE header length, the header, the body.
func TestFrameRoundTrip(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	session := &rpcSession{conn: client, timeout: 5 * time.Second, logf: func(string, ...any) {}}
	srv := &rpcSession{conn: server, timeout: 5 * time.Second, logf: func(string, ...any) {}}

	body := ConnectRequest{UseBindlessRPC: true}.marshal()
	// 00 0D | header {1:0, 2:1, 3:0, 5:2, 11:hash} | body {3:1}
	wantOnWire := "000D" + "08001001180028025D91694465" + "1801"

	gotWire := make(chan []byte, 1)
	go func() {
		raw := make([]byte, len(wantOnWire)/2)
		if _, err := io.ReadFull(server, raw); err != nil {
			gotWire <- nil
			return
		}
		gotWire <- raw
	}()
	if err := session.writeFrame(context.Background(), Header{
		ServiceID: serviceIDRequest, MethodID: connectionConnectMethod, Token: 0, Size: uint32(len(body)), ServiceHash: connectionServiceHash,
	}, body); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(<-gotWire); !strings.EqualFold(got, wantOnWire) {
		t.Fatalf("wire = %s, want %s", got, wantOnWire)
	}

	// and the same bytes parse back on the other side
	go func() {
		_, _ = client.Write(hexBytes(t, wantOnWire))
	}()
	h, gotBody, err := srv.readFrame(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h.ServiceID != serviceIDRequest || h.MethodID != connectionConnectMethod || h.Size != 2 || h.ServiceHash != connectionServiceHash {
		t.Fatalf("header %+v", h)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("body %X, want %X", gotBody, body)
	}
}

func TestFrameRejectsOversizedBody(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	// header {1:0xFE, 3:1, 5:0x100001}
	frame := "0009" + "08FE01" + "1801" + "28818040"
	go func() { _, _ = server.Write(hexBytes(t, frame)) }()
	session := &rpcSession{conn: client, timeout: 5 * time.Second, logf: func(string, ...any) {}}
	if _, _, err := session.readFrame(context.Background()); err == nil {
		t.Fatal("a body over the server's own 1 MiB cap was accepted")
	}
}

func TestFrameRejectsEmptyHeader(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() { _, _ = server.Write([]byte{0, 0}) }()
	session := &rpcSession{conn: client, timeout: 5 * time.Second, logf: func(string, ...any) {}}
	if _, _, err := session.readFrame(context.Background()); err == nil {
		t.Fatal("an empty header was accepted")
	}
}
