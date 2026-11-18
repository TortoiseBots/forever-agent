package bnet

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
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
	h := header{ServiceID: serviceIDRequest, MethodID: connectionConnectMethod, Token: 0, ServiceHash: connectionServiceHash}
	want := "080010011800" + "5D91694465" // 1:0, 2:1, 3:0, 11:fixed32 0x65446991 (little endian)
	if got := hex.EncodeToString(h.marshal()); !strings.EqualFold(got, want) {
		t.Fatalf("header = %s, want %s", got, want)
	}
}

func TestHeaderResponseFixture(t *testing.T) {
	h := header{ServiceID: serviceIDResponse, Token: 7, Size: 2, Status: 3, Ciid: "ab"}
	want := "08FE01" + "1807" + "2802" + "3003" + "6A026162"
	if got := hex.EncodeToString(h.marshal()); !strings.EqualFold(got, want) {
		t.Fatalf("header = %s, want %s", got, want)
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	for _, h := range []header{
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
	if got := hex.EncodeToString(connectRequest{UseBindlessRPC: true}.marshal()); !strings.EqualFold(got, "1801") {
		t.Fatalf("connectRequest = %s", got)
	}
	if got := len(connectRequest{}.marshal()); got != 0 {
		t.Fatalf("empty connectRequest is %d bytes", got)
	}
}

func TestLogonRequestFixture(t *testing.T) {
	req := logonRequest{
		Program: "WoW", Platform: "Wn64", Locale: "enUS",
		ApplicationVersion: build70938, CachedWebCredentials: []byte("TC-0123"),
	}
	want := "0A03576F57" + "1204576E3634" + "1A04656E5553" + "309AAA04" + "620754432D30313233"
	if got := hex.EncodeToString(req.marshal()); !strings.EqualFold(got, want) {
		t.Fatalf("logonRequest = %s, want %s", got, want)
	}
}

func TestVariantFixture(t *testing.T) {
	str := "Auth"
	cases := []struct {
		name string
		v    variant
		want string
	}{
		{"string", variant{StringValue: &str}, "2A0441757468"},
		{"blob", variant{BlobValue: []byte{1, 2, 3}}, "3203010203"},
		{"bool false", variant{BoolValue: new(bool)}, "1000"},
		{"uint", variant{UintValue: func() *uint64 { v := uint64(0x46010046); return &v }()}, "48C68084B004"},
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
	req := clientRequest{Attributes: []attribute{{Name: "Param_RealmAddress", Value: variant{UintValue: &addr}}}}
	// 1: message (0A 1C) { 1: "Param_RealmAddress" (0A 12), 2: variant { 9: varint } (12 06 48 ...) }
	want := "0A1C" + "0A12506172616D5F5265616C6D41646472657373" + "1206" + "48C68084B004"
	if got := hex.EncodeToString(req.marshal()); !strings.EqualFold(got, want) {
		t.Fatalf("clientRequest = %s, want %s", got, want)
	}
}

func TestGetAllValuesFixture(t *testing.T) {
	req := getAllValuesForAttributeRequest{AttributeKey: "Command_RealmListRequest_v1_b9"}
	want := "0A1E436F6D6D616E645F5265616C6D4C697374526571756573745F76315F6239"
	if got := hex.EncodeToString(req.marshal()); !strings.EqualFold(got, want) {
		t.Fatalf("GetAllValues = %s, want %s", got, want)
	}
	resp, err := unmarshalGetAllValuesForAttributeResponse(hexBytes(t, "0A092A0737302D312D3730")) // 1: variant{5:"70-1-70"}
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
	// 1: attribute{1:"Param_RealmListTicket", 2:variant{5:"Auth"}}
	// 1: attribute{1:"Param_JoinSecret", 2:variant{6:010203}}
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

	body := connectRequest{UseBindlessRPC: true}.marshal()
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
	if err := session.writeFrame(header{
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
	h, gotBody, err := srv.readFrame()
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
	if _, _, err := session.readFrame(); err == nil {
		t.Fatal("a body over the server's own 1 MiB cap was accepted")
	}
}

func TestFrameRejectsEmptyHeader(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() { _, _ = server.Write([]byte{0, 0}) }()
	session := &rpcSession{conn: client, timeout: 5 * time.Second, logf: func(string, ...any) {}}
	if _, _, err := session.readFrame(); err == nil {
		t.Fatal("an empty header was accepted")
	}
}

// dripServer drains one request and then writes a frame that is not the answer every interval. It returns when the
// client closes the connection or the loop runs out.
func dripServer(t *testing.T, conn net.Conn, interval time.Duration, headers func(int) header) {
	t.Helper()
	srv := &rpcSession{conn: conn, timeout: time.Minute, logf: func(string, ...any) {}}
	if _, _, err := srv.readFrame(); err != nil {
		return
	}
	for i := range 1000 {
		if err := srv.writeFrame(headers(i), nil); err != nil {
			return
		}
		time.Sleep(interval)
	}
}

// TestCallSlowDripExpires pins the one absolute deadline per call: a server that keeps a frame coming before every
// read must not be able to renew it. The per-call timeout is 200 ms; the call has to fail near that, not run on.
func TestCallSlowDripExpires(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go dripServer(t, server, 20*time.Millisecond, func(i int) header {
		return header{ServiceID: serviceIDRequest, ServiceHash: authenticationListenerHash, MethodID: 99, Token: uint32(i)}
	})

	session := &rpcSession{conn: client, timeout: 200 * time.Millisecond, logf: func(string, ...any) {}}
	start := time.Now()
	_, err := session.call(context.Background(), gameUtilitiesServiceHash, gameUtilitiesGetAllValuesMethod, nil)
	if err == nil {
		t.Fatal("a server that never answers produced a response")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the call took %s; the deadline moved with the traffic", elapsed)
	}
}

// TestCallCancelledContextUnblocksRead makes sure cancelling the context interrupts a read that has a long socket
// deadline. The server reads the request and then stays silent.
func TestCallCancelledContextUnblocksRead(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		srv := &rpcSession{conn: server, timeout: time.Minute, logf: func(string, ...any) {}}
		_, _, _ = srv.readFrame()
	}()

	session := &rpcSession{conn: client, timeout: time.Minute, logf: func(string, ...any) {}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := session.call(ctx, gameUtilitiesServiceHash, gameUtilitiesGetAllValuesMethod, nil)
	if err == nil {
		t.Fatal("a cancelled call returned a response")
	}
	// the cancellation is reported as itself, not as the i/o timeout the past socket deadline produces
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancellation took %s; the read was not interrupted", elapsed)
	}
}

// TestCallDeadlineSurfacesAsDeadlineExceeded: a context deadline that ends a call is reported as
// context.DeadlineExceeded (errors.Is), not as a bare i/o timeout, even though the watcher unblocks the read by
// setting the socket deadline in the past.
func TestCallDeadlineSurfacesAsDeadlineExceeded(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		srv := &rpcSession{conn: server, timeout: time.Minute, logf: func(string, ...any) {}}
		_, _, _ = srv.readFrame() // the request, then silence
	}()

	session := &rpcSession{conn: client, timeout: time.Minute, logf: func(string, ...any) {}}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := session.call(ctx, gameUtilitiesServiceHash, gameUtilitiesGetAllValuesMethod, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

// TestArmDeadlineWatcherStopsWithTheCall pins stop's authority: after stop returns, a late cancellation must not
// set a past deadline on the connection. A write on the live deadline must still go through.
func TestArmDeadlineWatcherStopsWithTheCall(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() { _, _ = server.Read(make([]byte, 1)) }()

	session := &rpcSession{conn: client, timeout: time.Minute, logf: func(string, ...any) {}}
	ctx, cancel := context.WithCancel(context.Background())
	stop := session.armDeadline(ctx, time.Now().Add(time.Minute))
	stop()
	cancel()
	// give the watcher, were it still alive, time to run after stop returned
	time.Sleep(50 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := client.Write([]byte("x"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the stopped watcher left a past deadline on the connection: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the write blocked; the connection is not usable after stop")
	}
}

// TestCancellationLeavesTheSessionUsable: a call cancelled while it waits for its answer must not poison the
// connection for the calls that follow it.
func TestCancellationLeavesTheSessionUsable(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		srv := &rpcSession{conn: server, timeout: time.Minute, logf: func(string, ...any) {}}
		for {
			h, _, err := srv.readFrame()
			if err != nil {
				return
			}
			if h.Token == 0 {
				// the first call is meant to wait for its context; only later tokens are answered
				continue
			}
			if err := srv.writeFrame(header{ServiceID: serviceIDResponse, Token: h.Token}, nil); err != nil {
				return
			}
		}
	}()

	session := &rpcSession{conn: client, timeout: time.Minute, logf: func(string, ...any) {}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := session.call(ctx, gameUtilitiesServiceHash, gameUtilitiesGetAllValuesMethod, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	for i := range 50 {
		if _, err := session.call(context.Background(), gameUtilitiesServiceHash, gameUtilitiesGetAllValuesMethod, nil); err != nil {
			t.Fatalf("call %d after the cancelled one: %v", i, err)
		}
	}
}

// TestCallCapsIgnoredFrames makes a server that answers other tokens forever end in an error, not in a loop.
func TestCallCapsIgnoredFrames(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		srv := &rpcSession{conn: server, timeout: time.Minute, logf: func(string, ...any) {}}
		if _, _, err := srv.readFrame(); err != nil {
			return
		}
		for range maxIgnoredFrames + 1 {
			if err := srv.writeFrame(header{ServiceID: serviceIDResponse, Token: 999}, nil); err != nil {
				return
			}
		}
	}()

	session := &rpcSession{conn: client, timeout: 5 * time.Second, logf: func(string, ...any) {}}
	_, err := session.call(context.Background(), gameUtilitiesServiceHash, gameUtilitiesGetAllValuesMethod, nil)
	if err == nil || !strings.Contains(err.Error(), "not the answer") {
		t.Fatalf("err = %v", err)
	}
}

// TestHeaderRequiresServiceIDAndToken: bgs.protocol.Header declares both required, and a frame without them cannot
// be routed or matched to a call.
func TestHeaderRequiresServiceIDAndToken(t *testing.T) {
	if _, err := unmarshalHeader(hexBytes(t, "08FE01")); err == nil {
		t.Fatal("a header without a token was accepted")
	}
	if _, err := unmarshalHeader(hexBytes(t, "1807")); err == nil {
		t.Fatal("a header without a service id was accepted")
	}
	if _, err := unmarshalHeader(hexBytes(t, "08001800")); err != nil {
		t.Fatalf("a minimal header (service id 0, token 0) was rejected: %v", err)
	}
}

// TestUnmarshalChecksWireTypes: a field read as an integer must actually be a varint, and a nested message must be
// length-delimited; otherwise the decoder would read 0 and pretend the message was fine.
func TestUnmarshalChecksWireTypes(t *testing.T) {
	if _, err := unmarshalHeader(hexBytes(t, "0A0100"+"1800")); err == nil { // 1 as bytes
		t.Fatal("a bytes field 1 in a header was accepted")
	}
	if _, err := unmarshalHeader(hexBytes(t, "0800"+"1800"+"5801")); err == nil { // 11 as varint
		t.Fatal("a varint service hash was accepted")
	}
	if _, err := unmarshalEntityID(hexBytes(t, "0A0100")); err == nil { // 1 as bytes
		t.Fatal("a bytes field 1 in an EntityId was accepted")
	}
	if _, err := unmarshalLogonResult(hexBytes(t, "0A0100")); err == nil { // 1 as bytes
		t.Fatal("a bytes error_code was accepted")
	}
	if _, err := unmarshalAttribute(hexBytes(t, "0A001001")); err == nil { // 2 as varint
		t.Fatal("a varint attribute value was accepted")
	}
	// and a nested message that is not a message is an error, not a zero value
	if _, err := unmarshalConnectResponse(append(putBytes(nil, 1, []byte{0x0A, 0x01, 0xFF}), putVarint(nil, 6, 1)...)); err == nil {
		t.Fatal("a nested ProcessId with a bytes label was accepted")
	}
}
