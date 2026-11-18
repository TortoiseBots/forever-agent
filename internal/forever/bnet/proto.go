package bnet

// Hand-written protobuf messages for the Battle.net RPC frames this client needs.
//
// The core keeps the .proto sources of the RPC protocol outside the tree (only the generated *.pb.cc/*.pb.h under
// src/server/proto/Client are shipped), so protoc cannot be pointed at them. Each message below is transcribed from
// that generated code (field numbers and types from the "// required/optional <type> <name> = N;" comments) and its
// encoder has byte fixtures in rpc_test.go, hand-checked against the generated serialization. Only the messages of
// this round exist here; anything the client does not send or read is absent on purpose.
//
// proto2 semantics: required fields are always emitted, even with value 0; optional fields only when set.

import (
	"errors"
	"fmt"
	"math"

	"google.golang.org/protobuf/encoding/protowire"
)

// Header is bgs.protocol.Header (rpc_types.pb.h:707-892; the server's own layout in Session.cpp SendRequest).
type Header struct {
	ServiceID   uint32 // 1, required
	MethodID    uint32 // 2, optional
	Token       uint32 // 3, required
	Size        uint32 // 5, optional (size of the message that follows)
	Status      uint32 // 6, optional (0 = ERROR_OK)
	ServiceHash uint32 // 11, optional fixed32
	Ciid        string // 13, optional
}

func (h Header) marshal() []byte {
	var b []byte
	b = putVarint(b, 1, uint64(h.ServiceID))
	if h.MethodID != 0 {
		b = putVarint(b, 2, uint64(h.MethodID))
	}
	b = putVarint(b, 3, uint64(h.Token))
	if h.Size != 0 {
		b = putVarint(b, 5, uint64(h.Size))
	}
	if h.Status != 0 {
		b = putVarint(b, 6, uint64(h.Status))
	}
	if h.ServiceHash != 0 {
		b = putFixed32(b, 11, h.ServiceHash)
	}
	if h.Ciid != "" {
		b = putString(b, 13, h.Ciid)
	}
	return b
}

func unmarshalHeader(b []byte) (Header, error) {
	var h Header
	var haveServiceID, haveToken bool
	err := eachField(b, func(n protowire.Number, t protowire.Type, val []byte) error {
		switch n {
		case 1:
			if err := checkType(n, t, protowire.VarintType); err != nil {
				return err
			}
			h.ServiceID = uint32(uintOf(val))
			haveServiceID = true
		case 2:
			if err := checkType(n, t, protowire.VarintType); err != nil {
				return err
			}
			h.MethodID = uint32(uintOf(val))
		case 3:
			if err := checkType(n, t, protowire.VarintType); err != nil {
				return err
			}
			h.Token = uint32(uintOf(val))
			haveToken = true
		case 5:
			if err := checkType(n, t, protowire.VarintType); err != nil {
				return err
			}
			h.Size = uint32(uintOf(val))
		case 6:
			if err := checkType(n, t, protowire.VarintType); err != nil {
				return err
			}
			h.Status = uint32(uintOf(val))
		case 11:
			if err := checkType(n, t, protowire.Fixed32Type); err != nil {
				return err
			}
			h.ServiceHash = fixed32Of(val)
		case 13:
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			h.Ciid = string(bytesOf(val))
		}
		return nil
	})
	if err != nil {
		return Header{}, err
	}
	// bgs.protocol.Header declares service_id and token required; a frame without them cannot be routed
	if !haveServiceID || !haveToken {
		return Header{}, errors.New("bnet: rpc header without a service id or a token")
	}
	return h, nil
}

// ProcessID is bgs.protocol.ProcessId (rpc_types.pb.h: required label = 1, required epoch = 2).
type ProcessID struct {
	Label uint32
	Epoch uint32
}

func (p ProcessID) marshal() []byte {
	var b []byte
	b = putVarint(b, 1, uint64(p.Label))
	b = putVarint(b, 2, uint64(p.Epoch))
	return b
}

func unmarshalProcessID(b []byte) (ProcessID, error) {
	var p ProcessID
	err := eachField(b, func(n protowire.Number, t protowire.Type, val []byte) error {
		switch n {
		case 1:
			if err := checkType(n, t, protowire.VarintType); err != nil {
				return err
			}
			p.Label = uint32(uintOf(val))
		case 2:
			if err := checkType(n, t, protowire.VarintType); err != nil {
				return err
			}
			p.Epoch = uint32(uintOf(val))
		}
		return nil
	})
	return p, err
}

// EntityID is bgs.protocol.EntityId (entity_types.pb.h: optional fixed64 high = 1, optional fixed64 low = 2).
type EntityID struct {
	High uint64
	Low  uint64
}

func (e EntityID) marshal() []byte {
	var b []byte
	if e.High != 0 {
		b = putFixed64(b, 1, e.High)
	}
	if e.Low != 0 {
		b = putFixed64(b, 2, e.Low)
	}
	return b
}

func unmarshalEntityID(b []byte) (EntityID, error) {
	var e EntityID
	err := eachField(b, func(n protowire.Number, t protowire.Type, val []byte) error {
		switch n {
		case 1:
			if err := checkType(n, t, protowire.Fixed64Type); err != nil {
				return err
			}
			e.High = fixed64Of(val)
		case 2:
			if err := checkType(n, t, protowire.Fixed64Type); err != nil {
				return err
			}
			e.Low = fixed64Of(val)
		}
		return nil
	})
	return e, err
}

// ConnectRequest is connection.v1.ConnectRequest (connection_service.pb.h: client_id = 1, bind_request = 2,
// use_bindless_rpc = 3 [default true]).
type ConnectRequest struct {
	ClientID       *ProcessID
	UseBindlessRPC bool
}

func (m ConnectRequest) marshal() []byte {
	var b []byte
	if m.ClientID != nil {
		b = putBytes(b, 1, m.ClientID.marshal())
	}
	if m.UseBindlessRPC {
		b = putVarint(b, 3, 1)
	}
	return b
}

// ConnectResponse is connection.v1.ConnectResponse (server_id = 1, client_id = 2, bind_result = 3,
// bind_response = 4, content_handle_array = 5, server_time = 6, use_bindless_rpc = 7, binary_content_handle_array = 8,
// ciid = 9, connected_region = 10).
type ConnectResponse struct {
	ServerID       ProcessID
	ClientID       ProcessID
	ServerTime     uint64
	UseBindlessRPC bool
	Ciid           string
}

func unmarshalConnectResponse(b []byte) (ConnectResponse, error) {
	var m ConnectResponse
	err := eachField(b, func(n protowire.Number, t protowire.Type, val []byte) error {
		switch n {
		case 1:
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			id, err := unmarshalProcessID(bytesOf(val))
			if err != nil {
				return err
			}
			m.ServerID = id
		case 2:
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			id, err := unmarshalProcessID(bytesOf(val))
			if err != nil {
				return err
			}
			m.ClientID = id
		case 6:
			if err := checkType(n, t, protowire.VarintType); err != nil {
				return err
			}
			m.ServerTime = uintOf(val)
		case 7:
			if err := checkType(n, t, protowire.VarintType); err != nil {
				return err
			}
			m.UseBindlessRPC = uintOf(val) != 0
		case 9:
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			m.Ciid = string(bytesOf(val))
		}
		return nil
	})
	return m, err
}

// LogonRequest is authentication.v1.LogonRequest (authentication_service.pb.h: program = 1, platform = 2,
// locale = 3, application_version = 6, cached_web_credentials = 12, device_id = 15).
type LogonRequest struct {
	Program              string
	Platform             string
	Locale               string
	ApplicationVersion   int32
	CachedWebCredentials []byte
	DeviceID             string
}

func (m LogonRequest) marshal() []byte {
	var b []byte
	if m.Program != "" {
		b = putString(b, 1, m.Program)
	}
	if m.Platform != "" {
		b = putString(b, 2, m.Platform)
	}
	if m.Locale != "" {
		b = putString(b, 3, m.Locale)
	}
	if m.ApplicationVersion != 0 {
		b = putVarint(b, 6, uint64(uint32(m.ApplicationVersion)))
	}
	if m.CachedWebCredentials != nil {
		b = putBytes(b, 12, m.CachedWebCredentials)
	}
	if m.DeviceID != "" {
		b = putString(b, 15, m.DeviceID)
	}
	return b
}

// LogonResult is authentication.v1.LogonResult, the body of the OnLogonComplete push
// (authentication_service.pb.h: error_code = 1, account_id = 2, game_account_id = 3, battle_tag = 7,
// geoip_country = 8, session_key = 9).
type LogonResult struct {
	ErrorCode      uint32
	AccountID      EntityID
	GameAccountIDs []EntityID
	BattleTag      string
	GeoIPCountry   string
	SessionKey     []byte
}

func unmarshalLogonResult(b []byte) (LogonResult, error) {
	var m LogonResult
	err := eachField(b, func(n protowire.Number, t protowire.Type, val []byte) error {
		switch n {
		case 1:
			if err := checkType(n, t, protowire.VarintType); err != nil {
				return err
			}
			m.ErrorCode = uint32(uintOf(val))
		case 2:
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			id, err := unmarshalEntityID(bytesOf(val))
			if err != nil {
				return err
			}
			m.AccountID = id
		case 3:
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			id, err := unmarshalEntityID(bytesOf(val))
			if err != nil {
				return err
			}
			m.GameAccountIDs = append(m.GameAccountIDs, id)
		case 7:
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			m.BattleTag = string(bytesOf(val))
		case 8:
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			m.GeoIPCountry = string(bytesOf(val))
		case 9:
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			m.SessionKey = append([]byte(nil), bytesOf(val)...)
		}
		return nil
	})
	return m, err
}

// Variant is bgs.protocol.Variant (attribute_types.pb.h: bool_value = 2, int_value = 3, float_value = 4,
// string_value = 5, blob_value = 6, uint_value = 9). Field 1 is not used by the server's generated code.
type Variant struct {
	BoolValue   *bool
	IntValue    *int64
	FloatValue  *float64
	StringValue *string
	BlobValue   []byte
	UintValue   *uint64
}

func (v Variant) marshal() []byte {
	var b []byte
	switch {
	case v.BoolValue != nil:
		if *v.BoolValue {
			b = putVarint(b, 2, 1)
		} else {
			b = putVarint(b, 2, 0)
		}
	case v.IntValue != nil:
		b = putVarint(b, 3, uint64(*v.IntValue))
	case v.FloatValue != nil:
		b = putFixed64(b, 4, mathBits(*v.FloatValue))
	case v.StringValue != nil:
		b = putString(b, 5, *v.StringValue)
	case v.BlobValue != nil:
		b = putBytes(b, 6, v.BlobValue)
	case v.UintValue != nil:
		b = putVarint(b, 9, *v.UintValue)
	}
	return b
}

func unmarshalVariant(b []byte) (Variant, error) {
	var v Variant
	err := eachField(b, func(n protowire.Number, t protowire.Type, val []byte) error {
		switch n {
		case 2:
			if err := checkType(n, t, protowire.VarintType); err != nil {
				return err
			}
			on := uintOf(val) != 0
			v.BoolValue = &on
		case 3:
			if err := checkType(n, t, protowire.VarintType); err != nil {
				return err
			}
			i := int64(uintOf(val))
			v.IntValue = &i
		case 4:
			if err := checkType(n, t, protowire.Fixed64Type); err != nil {
				return err
			}
			f := mathFloat(fixed64Of(val))
			v.FloatValue = &f
		case 5:
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			s := string(bytesOf(val))
			v.StringValue = &s
		case 6:
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			v.BlobValue = append([]byte(nil), bytesOf(val)...)
		case 9:
			if err := checkType(n, t, protowire.VarintType); err != nil {
				return err
			}
			u := uintOf(val)
			v.UintValue = &u
		}
		return nil
	})
	return v, err
}

// Attribute is bgs.protocol.Attribute (attribute_types.pb.h: required name = 1, required value = 2).
type Attribute struct {
	Name  string
	Value Variant
}

func (a Attribute) marshal() []byte {
	var b []byte
	b = putString(b, 1, a.Name)
	b = putBytes(b, 2, a.Value.marshal())
	return b
}

func unmarshalAttribute(b []byte) (Attribute, error) {
	var a Attribute
	err := eachField(b, func(n protowire.Number, t protowire.Type, val []byte) error {
		switch n {
		case 1:
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			a.Name = string(bytesOf(val))
		case 2:
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			v, err := unmarshalVariant(bytesOf(val))
			if err != nil {
				return err
			}
			a.Value = v
		}
		return nil
	})
	return a, err
}

// ClientRequest is game_utilities.v1.ClientRequest (game_utilities_service.pb.h: attribute = 1, account_id = 3,
// game_account_id = 4, program = 5, client_info = 6).
type ClientRequest struct {
	Attributes []Attribute
}

func (m ClientRequest) marshal() []byte {
	var b []byte
	for _, a := range m.Attributes {
		b = putBytes(b, 1, a.marshal())
	}
	return b
}

// ClientResponse is game_utilities.v1.ClientResponse (attribute = 1).
type ClientResponse struct {
	Attributes []Attribute
}

func unmarshalClientResponse(b []byte) (ClientResponse, error) {
	var m ClientResponse
	err := eachField(b, func(n protowire.Number, t protowire.Type, val []byte) error {
		if n == 1 {
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			a, err := unmarshalAttribute(bytesOf(val))
			if err != nil {
				return err
			}
			m.Attributes = append(m.Attributes, a)
		}
		return nil
	})
	return m, err
}

// GetAllValuesForAttributeRequest is game_utilities.v1.GetAllValuesForAttributeRequest (attribute_key = 1).
type GetAllValuesForAttributeRequest struct {
	AttributeKey string
}

func (m GetAllValuesForAttributeRequest) marshal() []byte {
	var b []byte
	if m.AttributeKey != "" {
		b = putString(b, 1, m.AttributeKey)
	}
	return b
}

// GetAllValuesForAttributeResponse is game_utilities.v1.GetAllValuesForAttributeResponse (attribute_value = 1).
type GetAllValuesForAttributeResponse struct {
	Values []Variant
}

func unmarshalGetAllValuesForAttributeResponse(b []byte) (GetAllValuesForAttributeResponse, error) {
	var m GetAllValuesForAttributeResponse
	err := eachField(b, func(n protowire.Number, t protowire.Type, val []byte) error {
		if n == 1 {
			if err := checkType(n, t, protowire.BytesType); err != nil {
				return err
			}
			v, err := unmarshalVariant(bytesOf(val))
			if err != nil {
				return err
			}
			m.Values = append(m.Values, v)
		}
		return nil
	})
	return m, err
}

// ------------------------------------------------------------------------------------------------ wire helpers

func putVarint(b []byte, n protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, n, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func putFixed32(b []byte, n protowire.Number, v uint32) []byte {
	b = protowire.AppendTag(b, n, protowire.Fixed32Type)
	return protowire.AppendFixed32(b, v)
}

func putFixed64(b []byte, n protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, n, protowire.Fixed64Type)
	return protowire.AppendFixed64(b, v)
}

func putBytes(b []byte, n protowire.Number, v []byte) []byte {
	b = protowire.AppendTag(b, n, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

func putString(b []byte, n protowire.Number, v string) []byte {
	return putBytes(b, n, []byte(v))
}

// checkType refuses a field whose wire type is not the one its message declares: reading it anyway would turn a
// length-delimited field into a varint and silently yield 0 or garbage.
func checkType(n protowire.Number, got, want protowire.Type) error {
	if got != want {
		return fmt.Errorf("bnet: protobuf field %d has wire type %d, want %d", n, got, want)
	}
	return nil
}

// eachField walks a message and calls fn with every field's raw value bytes (length-delimited content for BytesType,
// the full 4/8 bytes for the fixed types, the varint bytes for VarintType).
func eachField(b []byte, fn func(n protowire.Number, t protowire.Type, val []byte) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return fmt.Errorf("bnet: malformed protobuf tag: %w", protowire.ParseError(n))
		}
		b = b[n:]
		var val []byte
		switch typ {
		case protowire.VarintType:
			_, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return fmt.Errorf("bnet: malformed varint: %w", protowire.ParseError(m))
			}
			val, b = b[:m], b[m:]
		case protowire.Fixed32Type:
			if len(b) < 4 {
				return fmt.Errorf("bnet: truncated fixed32")
			}
			val, b = b[:4], b[4:]
		case protowire.Fixed64Type:
			if len(b) < 8 {
				return fmt.Errorf("bnet: truncated fixed64")
			}
			val, b = b[:8], b[8:]
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return fmt.Errorf("bnet: malformed bytes field: %w", protowire.ParseError(m))
			}
			val, b = v, b[m:]
		default:
			return fmt.Errorf("bnet: unsupported protobuf wire type %d", typ)
		}
		if err := fn(num, typ, val); err != nil {
			return err
		}
	}
	return nil
}

func uintOf(val []byte) uint64 {
	v, _ := protowire.ConsumeVarint(val)
	return v
}

func fixed32Of(val []byte) uint32 {
	v, _ := protowire.ConsumeFixed32(val)
	return v
}

func fixed64Of(val []byte) uint64 {
	v, _ := protowire.ConsumeFixed64(val)
	return v
}

func bytesOf(val []byte) []byte { return val }

func mathBits(f float64) uint64  { return math.Float64bits(f) }
func mathFloat(u uint64) float64 { return math.Float64frombits(u) }
