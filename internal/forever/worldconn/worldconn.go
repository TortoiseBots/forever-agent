// Package worldconn is the Forever (WoW Classic 1.60.1) world-socket connection: the hello banners, SMSG_AUTH_CHALLENGE,
// CMSG_AUTH_SESSION and the key derivation from the Battle.net join, SMSG_ENTER_ENCRYPTED_MODE and its ack, the
// AES-256-GCM frame stream, and the second ("instance") connection the server directs the client to for the
// player login (SMSG_CONNECT_TO / CMSG_AUTH_CONTINUED_SESSION).
package worldconn

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The banners are 53 bytes and must match byte for byte (WorldSocket.cpp:85-86, 181-237).
const (
	ServerHello = "WORLD OF WARCRAFT CONNECTION - SERVER TO CLIENT - V2\n"
	ClientHello = "WORLD OF WARCRAFT CONNECTION - CLIENT TO SERVER - V2\n"
)

// Config is what the Battle.net login left for the world socket.
type Config struct {
	Address         string // world server host:port (from Param_ServerAddresses or realmlist.address)
	RealmID         uint32 // auth.realmlist.id; CMSG_AUTH_SESSION must carry it
	RealmAddress    uint32 // virtual realm address: (region << 24) | (site << 16) | realm
	RealmJoinTicket []byte // the raw JSON blob of Param_RealmJoinTicket
	ClientSecret    []byte // 32 bytes this client contributed to the join key
	JoinSecret      []byte // 32 bytes the server contributed
	ClientBuild     uint32 // the build claimed at the Battle.net logon (account.client_build)
	RealmBuild      uint32 // auth.realmlist.gamebuild: selects the opcode groups (ExtraGroupShift)
	// BuildAuthKey is the 16-byte auth.build_auth_key of the ticket's variant (Win/x64/WoW), when the caller has
	// it. Empty means the server's Network.SkipBuildAuthKeyCheck path: the digest is sent as 24 zero bytes and the
	// live server (which has no row for 70707/70938) never compares it. A fake server that has a key can require
	// the real digest, which is how the authDigest path is covered.
	BuildAuthKey []byte
	// AllowInstanceRedirect permits SMSG_CONNECT_TO to name a host other than the one the realm socket is
	// connected to. The default refuses it: the packet comes from the server, but a hostile one could point the
	// instance socket (and the HMAC digest sent there) at loopback, a LAN address or a metadata service.
	AllowInstanceRedirect bool
	Deadline              time.Duration // hard cap on the whole Authenticate handshake; default 5 minutes
	Logf                  func(format string, args ...any)
}

func (c *Config) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// defaultDeadline bounds a handshake when the caller set none. The callers pass a context with a shorter deadline
// for a whole run; this is the backstop.
const defaultDeadline = 5 * time.Minute

// Realm is an authenticated realm-socket connection.
type Realm struct {
	c                     *conn
	sessionKey            [40]byte
	allowInstanceRedirect bool
}

// Next returns the next server packet.
func (r *Realm) Next(ctx context.Context) (Packet, error) { return r.c.readFrame(ctx) }

// Send writes one client packet by its core opcode.
func (r *Realm) Send(ctx context.Context, core uint32, payload []byte) error {
	return r.c.sendFrame(ctx, core, payload)
}

// Close closes the realm socket.
func (r *Realm) Close() error { return r.c.close() }

// HandshakeNames returns the server packets the handshake read, in order (auth challenge, the encrypted-mode
// packet, the auth response, and anything the client skipped in between).
func (r *Realm) HandshakeNames() []string {
	return append([]string(nil), r.c.handshakeNames...)
}

// Authenticate connects, exchanges the banners, authenticates with CMSG_AUTH_SESSION, switches on encryption and
// reads SMSG_AUTH_RESPONSE. A non-zero auth result is an error carrying the code.
func Authenticate(ctx context.Context, cfg Config) (*Realm, error) {
	if cfg.Deadline <= 0 {
		cfg.Deadline = defaultDeadline
	}
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	opCtx, cancel := context.WithDeadline(ctx, time.Now().Add(cfg.Deadline))
	defer cancel()

	keyData, err := checkKeyData(cfg.ClientSecret, cfg.JoinSecret)
	if err != nil {
		return nil, err
	}

	nc, err := (&net.Dialer{}).DialContext(opCtx, "tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("worldconn: connecting to %s: %w", cfg.Address, contextError(opCtx, err))
	}
	if tcp, ok := nc.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	c := &conn{
		nc:       nc,
		table:    wire.NewOpcodeTable(cfg.ClientBuild, cfg.RealmBuild),
		inflater: wire.NewInflater(),
		logf:     cfg.Logf,
	}
	cfg.logf("world socket: connected to %s (client build %d, realm build %d, opcode group shift %d)",
		cfg.Address, cfg.ClientBuild, cfg.RealmBuild, c.table.Shift())

	challenge, err := c.exchangeBanners(opCtx)
	if err != nil {
		_ = c.close()
		return nil, err
	}
	cfg.logf("world auth challenge: 32-byte challenge read, server sent the DoS challenge (ignored: the server never checks it)")

	localChallenge, err := c.sendAuthSession(opCtx, cfg, keyData, challenge)
	if err != nil {
		_ = c.close()
		return nil, err
	}

	r := &Realm{c: c, allowInstanceRedirect: cfg.AllowInstanceRedirect}
	r.sessionKey = sessionKeyFrom(keyData[:], challenge, localChallenge)
	encryptKey := encryptKeyFrom(r.sessionKey[:], localChallenge, challenge)

	if err := c.waitEncryptedMode(opCtx, encryptKey); err != nil {
		_ = c.close()
		return nil, err
	}
	cfg.logf("world encryption: SMSG_ENTER_ENCRYPTED_MODE acknowledged, AES-256-GCM on (%d-byte session key, %d-byte encryption key; lengths only)",
		len(r.sessionKey), len(encryptKey))

	if err := c.expectAuthResponse(opCtx); err != nil {
		_ = c.close()
		return nil, err
	}
	c.handshakeDone = true
	return r, nil
}

func validateConfig(cfg *Config) error {
	switch {
	case cfg.Address == "":
		return fmt.Errorf("worldconn: no world server address")
	case len(cfg.ClientSecret) != 32 || len(cfg.JoinSecret) != 32:
		return fmt.Errorf("worldconn: the join needs a 32-byte client secret and a 32-byte join secret")
	case len(cfg.RealmJoinTicket) == 0:
		return fmt.Errorf("worldconn: no realm join ticket")
	case len(cfg.RealmJoinTicket) > 4096:
		return fmt.Errorf("worldconn: realm join ticket of %d bytes is too large", len(cfg.RealmJoinTicket))
	case cfg.ClientBuild == 0:
		return fmt.Errorf("worldconn: no client build")
	case len(cfg.BuildAuthKey) != 0 && len(cfg.BuildAuthKey) != 16:
		return fmt.Errorf("worldconn: build auth key is %d bytes, want 16", len(cfg.BuildAuthKey))
	}
	return nil
}

// exchangeBanners reads the 53-byte server banner, sends the client's, and reads the first frame, which must be
// SMSG_AUTH_CHALLENGE. It returns the 32-byte challenge (the DoS challenge and zero bits are not used: the server
// never validates DosResponse).
func (c *conn) exchangeBanners(ctx context.Context) ([]byte, error) {
	banner, err := c.readExact(ctx, len(ServerHello))
	if err != nil {
		return nil, err
	}
	if string(banner) != ServerHello {
		return nil, fmt.Errorf("worldconn: unexpected server banner")
	}
	if err := c.writeAll(ctx, []byte(ClientHello)); err != nil {
		return nil, err
	}
	c.log("world socket: hello banners exchanged (V2)")
	p, err := c.readFrame(ctx)
	if err != nil {
		return nil, err
	}
	if p.Core != wire.SMSGAuthChallenge {
		return nil, fmt.Errorf("worldconn: expected SMSG_AUTH_CHALLENGE, got %s (wire 0x%06X)", packetName(p), p.Wire)
	}
	if len(p.Payload) != 65 {
		return nil, fmt.Errorf("worldconn: SMSG_AUTH_CHALLENGE is %d bytes, want 65", len(p.Payload))
	}
	challenge := make([]byte, 32)
	copy(challenge, p.Payload[32:64])
	return challenge, nil
}

// sendAuthSession builds and sends CMSG_AUTH_SESSION and returns the 32-byte local challenge. The digest is
// authDigest when the caller has the 16-byte build auth key; without it the 24 bytes are zero, which is what the
// live server accepts because Network.SkipBuildAuthKeyCheck is set and auth.build_auth_key has no row for the
// claim's variant, so the comparison is skipped altogether (WorldSocket.cpp:756-772). Which of the two went on
// the wire is kept on the connection: it is what makes a later ERROR_BAD_VERSION attributable to the build_info
// row (Refused.BuildAuthKeyUsed). The UseIPv6 bit is false and flushed to a whole byte, like the real client.
func (c *conn) sendAuthSession(ctx context.Context, cfg Config, keyData [64]byte, serverChallenge []byte) ([]byte, error) {
	localChallenge := make([]byte, 32)
	if _, err := rand.Read(localChallenge); err != nil {
		return nil, fmt.Errorf("worldconn: local challenge: %w", err)
	}
	b := wire.NewWriter()
	b.WriteU64(0) // DosResponse: the server never reads it (WorldSocket.cpp:686-1064)
	regionID := (cfg.RealmAddress >> 24) & 0xFF
	battlegroupID := (cfg.RealmAddress >> 16) & 0xFF
	b.WriteU32(regionID)
	b.WriteU32(battlegroupID)
	b.WriteU32(cfg.RealmID)
	b.WriteBytes(localChallenge)
	digest := make([]byte, 24)
	c.buildAuthKeyUsed = len(cfg.BuildAuthKey) == 16
	if c.buildAuthKeyUsed {
		d := authDigest(keyData[:], cfg.BuildAuthKey, localChallenge, serverChallenge)
		copy(digest, d[:])
	}
	b.WriteBytes(digest)
	b.WriteBit(false) // UseIPv6
	b.FlushBits()
	b.WriteU32(uint32(len(cfg.RealmJoinTicket)))
	b.WriteBytes(cfg.RealmJoinTicket)
	if err := c.sendFrame(ctx, wire.CMSGAuthSession, b.Bytes()); err != nil {
		return nil, err
	}
	if len(cfg.BuildAuthKey) == 16 {
		c.log("world auth session: sent (realm %d, region %d, battlegroup %d, join ticket %d bytes, 24-byte digest from the 16-byte build auth key)",
			cfg.RealmID, regionID, battlegroupID, len(cfg.RealmJoinTicket))
	} else {
		c.log("world auth session: sent (realm %d, region %d, battlegroup %d, join ticket %d bytes, 24 zero digest bytes: no build auth key supplied)",
			cfg.RealmID, regionID, battlegroupID, len(cfg.RealmJoinTicket))
	}
	return localChallenge, nil
}

// maxIgnoredHandshakeFrames bounds how many unexpected packets one handshake loop skips: a server that keeps
// sending something else must fail the handshake, not keep the client reading.
const maxIgnoredHandshakeFrames = 64

// waitEncryptedMode reads until SMSG_ENTER_ENCRYPTED_MODE, sends the unencrypted ack, and switches encryption on.
// An SMSG_AUTH_RESPONSE before it is the server refusing the session and is reported.
func (c *conn) waitEncryptedMode(ctx context.Context, encryptKey [32]byte) error {
	pc, err := newPacketCrypt(encryptKey)
	if err != nil {
		return err
	}
	ignored := 0
	for {
		p, err := c.readFrame(ctx)
		if err != nil {
			return err
		}
		switch p.Core {
		case wire.SMSGEnterEncryptedMode:
			if len(p.Payload) != 69 {
				return fmt.Errorf("worldconn: SMSG_ENTER_ENCRYPTED_MODE is %d bytes, want 69", len(p.Payload))
			}
			regionGroup := int32(binary.LittleEndian.Uint32(p.Payload))
			signature := p.Payload[4:68]
			enabled := p.Payload[68]&0x80 != 0
			if !enabled {
				return fmt.Errorf("worldconn: SMSG_ENTER_ENCRYPTED_MODE says disabled")
			}
			// The 64-byte Ed25519 signature over HMAC-SHA512(encryptKey, 0x01 || EnableEncryptionSeed) cannot be
			// verified: the client build picks the trusted public key for this region group out of its own key
			// store, which is not in the server tree, the captures or this repository. It is parsed and reported;
			// what authenticates the server is the AES-GCM tag of SMSG_AUTH_RESPONSE, which needs the join secret.
			c.log("world encrypted mode: region group %d, signature %d bytes (not verified: no trusted public key here)", regionGroup, len(signature))
			if err := c.sendFrame(ctx, wire.CMSGEnterEncryptedModeAck, nil); err != nil {
				return err
			}
			c.crypt = pc
			c.encrypted = true
			return nil
		case wire.SMSGAuthResponse:
			return &Refused{Code: authResult(p.Payload), BeforeEncryption: true, buildAuthKeyUsed: c.buildAuthKeyUsed}
		default:
			ignored++
			c.log("world: ignoring %s before the encrypted mode", packetName(p))
			if ignored > maxIgnoredHandshakeFrames {
				return fmt.Errorf("worldconn: %d packets before SMSG_ENTER_ENCRYPTED_MODE, the last was %s", ignored, packetName(p))
			}
		}
	}
}

// expectAuthResponse reads until SMSG_AUTH_RESPONSE over the encrypted stream and checks the result.
func (c *conn) expectAuthResponse(ctx context.Context) error {
	ignored := 0
	for {
		p, err := c.readFrame(ctx)
		if err != nil {
			return err
		}
		if p.Core != wire.SMSGAuthResponse {
			ignored++
			c.log("world: ignoring %s before the auth response", packetName(p))
			if ignored > maxIgnoredHandshakeFrames {
				return fmt.Errorf("worldconn: %d packets before SMSG_AUTH_RESPONSE, the last was %s", ignored, packetName(p))
			}
			continue
		}
		if code := authResult(p.Payload); code != 0 {
			return &Refused{Code: code, buildAuthKeyUsed: c.buildAuthKeyUsed}
		}
		return nil
	}
}

// authResultBadVersion is ERROR_BAD_VERSION (src/server/proto/BattlenetRpcErrorCodes.h). The world server answers it
// for two different failures: no build_info row for the build the account claimed (WorldSocket.cpp:739-746) and no
// build_auth_key row for the join ticket's variant while Network.SkipBuildAuthKeyCheck is off (:760-768). The code
// alone therefore does not say whether the build is unsupported, and the realm list can accept either build. Refused
// carries what tells the two apart.
const authResultBadVersion = 0x1C

// Refused is an SMSG_AUTH_RESPONSE with a non-zero result: the world server rejected this session
// (WorldSocket::SendAuthResponseError, WorldSocket.cpp:1165). The handshake sends every refusal before
// SMSG_ENTER_ENCRYPTED_MODE (the switch happens only after _authed, WorldSocket.cpp:960), so BeforeEncryption
// tells a refusal during the build check from a transfer that failed after the switch.
type Refused struct {
	Code             uint32
	BeforeEncryption bool
	// buildAuthKeyUsed is whether CMSG_AUTH_SESSION carried the digest of a 16-byte build auth key. Only the
	// socket itself knows: the caller builds the Config.
	buildAuthKeyUsed bool
}

func (e *Refused) Error() string {
	if e.BeforeEncryption {
		return fmt.Sprintf("worldconn: the server refused the session before encryption: result 0x%08X (%s)", e.Code, authResultName(e.Code))
	}
	return fmt.Sprintf("world auth refused: result 0x%08X (%s)", e.Code, authResultName(e.Code))
}

// BadVersion reports that the result code is ERROR_BAD_VERSION (0x1C). The code is not by itself a statement about
// the build claim: it is also what a missing build_auth_key row produces when the server checks the digest
// (WorldSocket.cpp:760-768). BuildAuthKeyUsed and the caller's knowledge of Network.SkipBuildAuthKeyCheck decide
// which of the two checks refused the session.
func (e *Refused) BadVersion() bool { return e.Code == authResultBadVersion }

// BuildAuthKeyUsed reports whether CMSG_AUTH_SESSION carried the 24-byte digest derived from a 16-byte build auth
// key. The server compares that digest only when the build's variant has a build_auth_key row, and a mismatch is
// ERROR_DENIED, not ERROR_BAD_VERSION (WorldSocket.cpp:775-791); a socket that sent a key and was answered 0x1C was
// therefore refused by the build_info lookup, not by a missing key row.
func (e *Refused) BuildAuthKeyUsed() bool { return e.buildAuthKeyUsed }

func authResult(payload []byte) uint32 {
	if len(payload) < 4 {
		return 0xFFFFFFFF
	}
	return binary.LittleEndian.Uint32(payload)
}

// authResultName names the auth result codes a refusal of this handshake can carry
// (src/server/proto/BattlenetRpcErrorCodes.h).
func authResultName(code uint32) string {
	switch code {
	case 0:
		return "AUTH_OK"
	case 0x2:
		return "ERROR_TIMED_OUT"
	case 0x3:
		return "ERROR_DENIED"
	case 0x1C:
		return "ERROR_BAD_VERSION"
	case 0x21:
		return "ERROR_SERVER_IS_PRIVATE"
	case 0x34:
		return "ERROR_GAME_ACCOUNT_BANNED"
	case 0xA413:
		return "ERROR_RISK_ACCOUNT_LOCKED"
	case 0x800000E1:
		return "ERROR_USER_SERVER_NOT_PERMITTED_ON_REALM"
	case 0x8000012E:
		return "ERROR_WOW_SERVICES_INVALID_JOIN_TICKET"
	}
	return "unknown result"
}

// packetLabel names one wire packet for the count.
func packetLabel(wireOpcode, core uint32) string {
	if name := wire.ServerName(core); name != "" {
		return name
	}
	return fmt.Sprintf("wire 0x%06X", wireOpcode)
}

func packetName(p Packet) string {
	if p.Name != "" {
		return p.Name
	}
	return fmt.Sprintf("wire 0x%06X", p.Wire)
}
