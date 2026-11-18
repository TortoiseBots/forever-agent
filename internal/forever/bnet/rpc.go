package bnet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Service hashes and method ids the login sequence uses; they are the OriginalHash/MethodId constants of the
// generated service classes (src/server/proto/Client/**/*.pb.h) and the dispatch numbers the core registers
// (docs/forever/PROTOCOL-LOGIN.md section 3.2).
const (
	serviceIDRequest  = 0x00
	serviceIDResponse = 0xFE

	connectionServiceHash   = 0x65446991
	connectionConnectMethod = 1

	authenticationServiceHash   = 0x0DECFC01 // authentication.v1.AuthenticationService
	authenticationLogonMethod   = 1
	authenticationListenerHash  = 0x71240E35 // authentication.v1.AuthenticationListener
	authenticationLogonComplete = 5

	gameUtilitiesServiceHash        = 0x3FC1274D
	gameUtilitiesProcessRequest     = 1
	gameUtilitiesGetAllValuesMethod = 10
)

// maxRPCBody is the server's own cap on one message body (src/server/bnetserver/Server/Session.cpp:78).
const maxRPCBody = 0x100000

// rpcError is a non-zero status in a response header.
type rpcError struct {
	ServiceHash uint32
	MethodID    uint32
	Status      uint32
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("rpc: service 0x%08X method %d failed with status 0x%08X (%s)",
		e.ServiceHash, e.MethodID, e.Status, rpcStatusName(e.Status))
}

// rpcStatusName names the statuses this round can produce; anything else is printed as a number.
func rpcStatusName(status uint32) string {
	switch status {
	case 0x2:
		return "ERROR_TIMED_OUT"
	case 0x3:
		return "ERROR_DENIED"
	case 0x1C:
		return "ERROR_BAD_VERSION"
	case 0x34:
		return "ERROR_GAME_ACCOUNT_BANNED"
	case 0x35:
		return "ERROR_GAME_ACCOUNT_SUSPENDED"
	case 0x4D:
		return "ERROR_BAD_PROGRAM"
	case 0x4E:
		return "ERROR_BAD_LOCALE"
	case 0x4F:
		return "ERROR_BAD_PLATFORM"
	case 0x0BC3:
		return "ERROR_RPC_INVALID_METHOD"
	case 0x0BC5:
		return "ERROR_RPC_MALFORMED_REQUEST"
	case 0x0BC7:
		return "ERROR_RPC_NOT_IMPLEMENTED"
	case 0x80000069:
		return "ERROR_UTIL_SERVER_UNKNOWN_REALM"
	case 0x8000006E:
		return "ERROR_UTIL_SERVER_INVALID_IDENTITY_ARGS"
	case 0x80000073:
		return "ERROR_UTIL_SERVER_FAILED_TO_SERIALIZE_RESPONSE"
	case 0x800000E1:
		return "ERROR_USER_SERVER_NOT_PERMITTED_ON_REALM"
	case 0x8000012C:
		return "ERROR_WOW_SERVICES_TIMED_OUT"
	case 0x80000132:
		return "ERROR_WOW_SERVICES_DENIED_REALM_LIST_TICKET"
	case 0x8000012E:
		return "ERROR_WOW_SERVICES_INVALID_JOIN_TICKET"
	}
	return "unknown status"
}

// rpcSession is one TLS connection to the Battle.net RPC port.
type rpcSession struct {
	conn    net.Conn
	timeout time.Duration
	token   uint32
	ciid    string

	// what the pushes of this round fill in
	logon *LogonResult

	logf func(string, ...any)
}

// dialRPC opens the TLS connection. The certificate is verified against roots for serverName; there is no way to
// skip that check.
func dialRPC(ctx context.Context, addr, serverName string, roots *x509.CertPool, timeout time.Duration) (*rpcSession, error) {
	dialer := &net.Dialer{Timeout: timeout}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("bnet: rpc connect %s: %w", addr, err)
	}
	conn := tls.Client(raw, &tls.Config{
		ServerName: serverName,
		RootCAs:    roots,
		MinVersion: tls.VersionTLS12,
	})
	hsCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := conn.HandshakeContext(hsCtx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("bnet: rpc TLS handshake with %s as %q: %w", addr, serverName, err)
	}
	return &rpcSession{conn: conn, timeout: timeout, logf: func(string, ...any) {}}, nil
}

func (s *rpcSession) Close() error { return s.conn.Close() }

func (s *rpcSession) log(format string, args ...any) { s.logf(format, args...) }

// setDeadline applies the caller's context deadline, or the session timeout when it has none.
func (s *rpcSession) setDeadline(ctx context.Context) {
	deadline := time.Now().Add(s.timeout)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	_ = s.conn.SetDeadline(deadline)
}

// writeFrame sends one framed message: uint16 BE header length, the Header, then body
// (src/server/bnetserver/Server/Session.cpp:341-427).
func (s *rpcSession) writeFrame(ctx context.Context, h Header, body []byte) error {
	s.setDeadline(ctx)
	header := h.marshal()
	if len(header) > 0xFFFF {
		return errors.New("bnet: rpc header does not fit the length prefix")
	}
	frame := make([]byte, 2+len(header)+len(body))
	binary.BigEndian.PutUint16(frame, uint16(len(header)))
	copy(frame[2:], header)
	copy(frame[2+len(header):], body)
	if _, err := s.conn.Write(frame); err != nil {
		return fmt.Errorf("bnet: rpc write: %w", err)
	}
	return nil
}

// readFrame reads one framed message.
func (s *rpcSession) readFrame(ctx context.Context) (Header, []byte, error) {
	s.setDeadline(ctx)
	var lenBuf [2]byte
	if _, err := io.ReadFull(s.conn, lenBuf[:]); err != nil {
		return Header{}, nil, fmt.Errorf("bnet: rpc read: %w", err)
	}
	headerLen := binary.BigEndian.Uint16(lenBuf[:])
	if headerLen == 0 {
		return Header{}, nil, errors.New("bnet: rpc frame with an empty header")
	}
	headerBuf := make([]byte, headerLen)
	if _, err := io.ReadFull(s.conn, headerBuf); err != nil {
		return Header{}, nil, fmt.Errorf("bnet: rpc header: %w", err)
	}
	h, err := unmarshalHeader(headerBuf)
	if err != nil {
		return Header{}, nil, err
	}
	if h.Size > maxRPCBody {
		return Header{}, nil, fmt.Errorf("bnet: rpc frame claims %d body bytes", h.Size)
	}
	var body []byte
	if h.Size > 0 {
		body = make([]byte, h.Size)
		if _, err := io.ReadFull(s.conn, body); err != nil {
			return Header{}, nil, fmt.Errorf("bnet: rpc body: %w", err)
		}
	}
	return h, body, nil
}

// call sends one request and returns the body of its response, handling pushes that arrive in between.
func (s *rpcSession) call(ctx context.Context, serviceHash, methodID uint32, req []byte) ([]byte, error) {
	token := s.token
	s.token++
	h := Header{ServiceID: serviceIDRequest, MethodID: methodID, Token: token, Size: uint32(len(req)), ServiceHash: serviceHash}
	if err := s.writeFrame(ctx, h, req); err != nil {
		return nil, err
	}
	for {
		got, body, err := s.readFrame(ctx)
		if err != nil {
			return nil, err
		}
		switch got.ServiceID {
		case serviceIDResponse:
			if got.Token != token {
				continue // a response to something we are not waiting for
			}
			if got.Status != 0 {
				return nil, &rpcError{ServiceHash: serviceHash, MethodID: methodID, Status: got.Status}
			}
			return body, nil
		case serviceIDRequest:
			if err := s.handleRequest(got, body); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("bnet: rpc frame with service id 0x%X", got.ServiceID)
		}
	}
}

// handleRequest deals with a server-to-client message (a push in this round's vocabulary).
func (s *rpcSession) handleRequest(h Header, body []byte) error {
	switch {
	case h.ServiceHash == authenticationListenerHash && h.MethodID == authenticationLogonComplete:
		result, err := unmarshalLogonResult(body)
		if err != nil {
			return fmt.Errorf("bnet: OnLogonComplete: %w", err)
		}
		if result.ErrorCode != 0 {
			return fmt.Errorf("bnet: OnLogonComplete reports error %d", result.ErrorCode)
		}
		if len(result.SessionKey) != 64 {
			return fmt.Errorf("bnet: OnLogonComplete carries a %d-byte session key, want 64", len(result.SessionKey))
		}
		s.logon = &result
	default:
		s.log("rpc: ignoring push service 0x%08X method %d (%d bytes)", h.ServiceHash, h.MethodID, len(body))
	}
	return nil
}

// waitLogon reads until the OnLogonComplete push arrived.
func (s *rpcSession) waitLogon(ctx context.Context) (*LogonResult, error) {
	for s.logon == nil {
		h, body, err := s.readFrame(ctx)
		if err != nil {
			return nil, err
		}
		if h.ServiceID != serviceIDRequest {
			continue
		}
		if err := s.handleRequest(h, body); err != nil {
			return nil, err
		}
	}
	return s.logon, nil
}

// connect runs ConnectionService::Connect and returns the response (ciid, server time, bindless flag).
func (s *rpcSession) connect(ctx context.Context) (ConnectResponse, error) {
	body, err := s.call(ctx, connectionServiceHash, connectionConnectMethod, ConnectRequest{UseBindlessRPC: true}.marshal())
	if err != nil {
		return ConnectResponse{}, fmt.Errorf("bnet: Connect: %w", err)
	}
	resp, err := unmarshalConnectResponse(body)
	if err != nil {
		return ConnectResponse{}, fmt.Errorf("bnet: ConnectResponse: %w", err)
	}
	s.ciid = resp.Ciid
	return resp, nil
}

// logon sends the login ticket and waits for OnLogonComplete.
func (s *rpcSession) logonRequest(ctx context.Context, ticket string, clientBuild uint32) (*LogonResult, error) {
	req := LogonRequest{
		Program:              "WoW",
		Platform:             "Wn64",
		Locale:               "enUS",
		ApplicationVersion:   int32(clientBuild),
		CachedWebCredentials: []byte(ticket),
	}
	if _, err := s.call(ctx, authenticationServiceHash, authenticationLogonMethod, req.marshal()); err != nil {
		return nil, fmt.Errorf("bnet: Logon: %w", err)
	}
	result, err := s.waitLogon(ctx)
	if err != nil {
		return nil, fmt.Errorf("bnet: Logon: %w", err)
	}
	return result, nil
}
