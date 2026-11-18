package bnet

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// FourCC values of the client variant the server stores in the join ticket (ClientBuildInfo.h: FourCC packs the
// characters big-endian, "Win" -> 0x0057696E).
const (
	fourCCWin     = 0x0057696E // platform type
	fourCCWn64    = 0x576E3634 // platform
	fourCCTextX64 = 0x783634   // architecture
	fourCCWoW     = 0x00576F57 // title
	fourCCEnUS    = 0x656E5553 // locale
)

// jsonBytes is a protobuf bytes field in the server's JSON: an array of uint32, one per byte
// (src/server/shared/JSON/ProtobufJSON.cpp:110-121).
type jsonBytes []byte

func (b jsonBytes) MarshalJSON() ([]byte, error) {
	out := make([]uint32, len(b))
	for i, v := range b {
		out[i] = uint32(v)
	}
	return json.Marshal(out)
}

func (b *jsonBytes) UnmarshalJSON(data []byte) error {
	var in []uint32
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	out := make([]byte, len(in))
	for i, v := range in {
		if v > 0xFF {
			return fmt.Errorf("bnet: byte array value %d out of range", v)
		}
		out[i] = byte(v)
	}
	*b = out
	return nil
}

// realmListTicketIdentity is JSON.RealmList.RealmListTicketIdentity (RealmList.proto:13-17).
type realmListTicketIdentity struct {
	GameAccountID     uint32 `json:"gameAccountID"`
	GameAccountRegion uint32 `json:"gameAccountRegion"`
}

// clientVersion is JSON.RealmList.ClientVersion (RealmList.proto:19-24).
type clientVersion struct {
	Major uint32 `json:"versionMajor"`
	Minor uint32 `json:"versionMinor"`
	Revis uint32 `json:"versionRevision"`
	Build uint32 `json:"versionBuild"`
}

// clientInformation is JSON.RealmList.ClientInformation (RealmList.proto:26-51). Only the fields the server reads
// are set by this client; JSON::Serialize on the server writes exactly those.
type clientInformation struct {
	Platform         uint32        `json:"platform"`
	BuildVariant     string        `json:"buildVariant"`
	Type             uint32        `json:"type"`
	TimeZone         string        `json:"timeZone"`
	CurrentTime      uint32        `json:"currentTime"`
	TextLocale       uint32        `json:"textLocale"`
	AudioLocale      uint32        `json:"audioLocale"`
	VersionDataBuild uint32        `json:"versionDataBuild"`
	Version          clientVersion `json:"version"`
	Secret           jsonBytes     `json:"secret"`
	ClientArch       uint32        `json:"clientArch"`
	SystemVersion    string        `json:"systemVersion"`
	PlatformType     uint32        `json:"platformType"`
	SystemArch       uint32        `json:"systemArch"`
}

// realmListTicketClientInformation wraps the information the way the server parses it (RealmList.proto:53-55).
type realmListTicketClientInformation struct {
	Info clientInformation `json:"info"`
}

// realmJoinTicket is JSON.RealmList.RealmJoinTicket: the JSON the world socket later carries in CMSG_AUTH_SESSION
// (RealmList.proto:136-141).
type realmJoinTicket struct {
	GameAccount string `json:"gameAccount"`
	Platform    uint32 `json:"platform"`
	Type        uint32 `json:"type"`
	ClientArch  uint32 `json:"clientArch"`
}

// realmListUpdates is JSON.RealmList.RealmListUpdates (RealmList.proto:105-126).
type realmListUpdates struct {
	Updates []realmListUpdatePart `json:"updates"`
}

type realmListUpdatePart struct {
	WowRealmAddress uint32      `json:"wowRealmAddress"`
	Update          *realmEntry `json:"update"`
	Deleting        bool        `json:"deleting"`
}

type realmEntry struct {
	WowRealmAddress uint32        `json:"wowRealmAddress"`
	CfgTimezonesID  uint32        `json:"cfgTimezonesID"`
	PopulationState uint32        `json:"populationState"`
	CfgCategoriesID uint32        `json:"cfgCategoriesID"`
	Version         clientVersion `json:"version"`
	CfgRealmsID     uint32        `json:"cfgRealmsID"`
	Flags           uint32        `json:"flags"`
	Name            string        `json:"name"`
	CfgConfigsID    uint32        `json:"cfgConfigsID"`
	CfgLanguagesID  uint32        `json:"cfgLanguagesID"`
	CfgContentSetID uint32        `json:"cfgContentSetID"`
	UseBleepChance  float32       `json:"useBleepChance"`
	SuperDistrictID uint32        `json:"superDistrictID"` // added by the server for the Classic client, not in the .proto
}

// realmListServerIPAddresses is JSON.RealmList.RealmListServerIPAddresses (RealmList.proto:128-134).
type realmListServerIPAddresses struct {
	Families []realmIPAddressFamily `json:"families"`
}

type realmIPAddressFamily struct {
	Family    uint32      `json:"family"`
	Addresses []ipAddress `json:"addresses"`
}

type ipAddress struct {
	IP   string `json:"ip"`
	Port uint32 `json:"port"`
}

// decompressJSON reads a GameUtilities blob: uint32 LE uncompressed length (which counts the trailing NUL), zlib
// data, then "<TypePrefix>:<json>\0" (src/server/bnetserver/Services/GameUtilitiesService.cpp:48-60).
func decompressJSON(blob []byte) (string, []byte, error) {
	const maxJSON = 1 << 20
	if len(blob) < 5 {
		return "", nil, fmt.Errorf("bnet: compressed blob of %d bytes is too short", len(blob))
	}
	size := binary.LittleEndian.Uint32(blob[:4])
	if size == 0 || size > maxJSON {
		return "", nil, fmt.Errorf("bnet: compressed blob claims %d bytes", size)
	}
	zr, err := zlib.NewReader(bytes.NewReader(blob[4:]))
	if err != nil {
		return "", nil, fmt.Errorf("bnet: compressed blob: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(zr, int64(size)+1))
	if closeErr := zr.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", nil, fmt.Errorf("bnet: compressed blob: %w", err)
	}
	if uint32(len(raw)) != size {
		return "", nil, fmt.Errorf("bnet: compressed blob holds %d bytes, header says %d", len(raw), size)
	}
	raw = bytes.TrimSuffix(raw, []byte{0})
	prefix, body, ok := bytes.Cut(raw, []byte(":"))
	if !ok || len(prefix) == 0 || len(body) == 0 {
		return "", nil, errors.New("bnet: compressed blob has no <prefix>:<json> shape")
	}
	return string(prefix), body, nil
}

// decodeCompressed unmarshals a compressed blob into v and checks its type prefix.
func decodeCompressed(blob []byte, wantPrefix string, v any) error {
	prefix, body, err := decompressJSON(blob)
	if err != nil {
		return err
	}
	if prefix != wantPrefix {
		return fmt.Errorf("bnet: blob prefix %q, want %q", sanitizeServerText(prefix), wantPrefix)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("bnet: %s: %w", wantPrefix, err)
	}
	return nil
}
