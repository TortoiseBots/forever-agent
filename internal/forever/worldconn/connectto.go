package worldconn

import (
	"fmt"
	"net"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Connection types of Opcodes.h.
const (
	ConnectionRealm    uint8 = 0
	ConnectionInstance uint8 = 1
)

// ConnectTo is SMSG_CONNECT_TO (AuthenticationPackets.cpp:280-334, WorldSession.cpp:912-957): the server sends it
// on the realm socket after CMSG_PLAYER_LOGIN and the client opens the instance socket it names.
//
// The write order is Size<uint32>(Payload), Serial, Con, Key, NativeRealmAddress, Key3, then per payload
// AddressType, the address bytes, Port and the bleep token. The protocol document's field names (Signature,
// WhereBuffer, ...) belong to a newer build and do not match this server.
type ConnectTo struct {
	Serial             uint32
	Con                uint8
	Key                uint64
	NativeRealmAddress uint32
	Key3               uint32
	IP                 net.IP
	Port               uint16
}

// ParseConnectTo parses the first address of an SMSG_CONNECT_TO. The bleep token after the port is not parsed: it
// is unused by this server (no proxy), and the address is all the client needs.
func ParseConnectTo(payload []byte) (ConnectTo, error) {
	var ct ConnectTo
	b := wire.NewReader(payload)
	payloads := b.ReadU32()
	if b.Err() != nil {
		return ct, fmt.Errorf("worldconn: SMSG_CONNECT_TO is %d bytes, too short", len(payload))
	}
	if payloads == 0 || payloads > 8 {
		return ct, fmt.Errorf("worldconn: SMSG_CONNECT_TO names %d addresses", payloads)
	}
	ct.Serial = b.ReadU32()
	ct.Con = b.ReadU8()
	ct.Key = b.ReadU64()
	ct.NativeRealmAddress = b.ReadU32()
	ct.Key3 = b.ReadU32()
	if b.Err() != nil {
		return ct, fmt.Errorf("worldconn: SMSG_CONNECT_TO is %d bytes, too short", len(payload))
	}
	addressType := b.ReadU8()
	switch addressType {
	case 1: // IPv4
		p := b.ReadBytes(4)
		if p != nil {
			ct.IP = net.IPv4(p[0], p[1], p[2], p[3])
		}
	case 2: // IPv6
		p := b.ReadBytes(16)
		if p != nil {
			ct.IP = append(net.IP(nil), p...)
		}
	case 3: // NamedSocket: not supported by this server
		return ct, fmt.Errorf("worldconn: SMSG_CONNECT_TO uses a named socket, which this client does not support")
	default:
		return ct, fmt.Errorf("worldconn: SMSG_CONNECT_TO has address type %d", addressType)
	}
	ct.Port = b.ReadU16()
	if b.Err() != nil {
		return ct, fmt.Errorf("worldconn: SMSG_CONNECT_TO has no complete address")
	}
	if ct.Port == 0 {
		return ct, fmt.Errorf("worldconn: SMSG_CONNECT_TO names port 0")
	}
	if ct.Con != ConnectionInstance {
		return ct, fmt.Errorf("worldconn: SMSG_CONNECT_TO names connection type %d, only the instance connection is supported", ct.Con)
	}
	return ct, nil
}

// Address returns the host:port to dial.
func (ct ConnectTo) Address() string {
	return net.JoinHostPort(ct.IP.String(), fmt.Sprintf("%d", ct.Port))
}
