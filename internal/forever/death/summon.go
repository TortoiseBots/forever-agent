package death

// The summoning offer a portal makes: SMSG_SUMMON_REQUEST and the client's answer CMSG_SUMMON_RESPONSE. Both
// layouts are the server's reader and writer, field group by field group (MovementPackets.cpp:1079-1110).

import (
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// SummonOffer is SMSG_SUMMON_REQUEST (SummonRequest::Write, MovementPackets.cpp:1100-1110): the summoner, the area
// the portal is in, the reason (a spell or a scenario) and the client's SkipStartingArea bit. The virtual realm
// address between the GUID and the area is read and dropped: it names the realm, which the client does not use.
type SummonOffer struct {
	Summoner         wire.GUID
	AreaID           int32
	Reason           uint8
	SkipStartingArea bool
}

// ParseSummonRequest reads SMSG_SUMMON_REQUEST: packed GUID summoner, uint32 virtual realm address, int32 area,
// uint8 reason, the SkipStartingArea bit, flush. A short packet or one with bytes left after the layout is an error,
// so a layout change cannot be read as a plausible offer.
func ParseSummonRequest(p []byte) (SummonOffer, error) {
	b := wire.NewReader(p)
	o := SummonOffer{
		Summoner: guid(b),
	}
	b.ReadU32() // SummonerVirtualRealmAddress
	o.AreaID = b.ReadI32()
	o.Reason = b.ReadU8()
	o.SkipStartingArea = b.ReadBit()
	flush(b)
	if err := exactly(b); err != nil {
		return SummonOffer{}, err
	}
	return o, nil
}

// BuildSummonResponse is CMSG_SUMMON_RESPONSE (SummonResponse::Read, MovementPackets.cpp:1079-1083): the summoner's
// packed GUID and the Accept bit.
func BuildSummonResponse(summoner wire.GUID, accept bool) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, summoner)
	b.WriteBit(accept)
	b.FlushBits()
	return b.Bytes()
}
