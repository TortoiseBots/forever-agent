package worldconn

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Instance is the second world socket (connection type INSTANCE): the server sends the player's world on it and
// keeps the realm socket for the character screen.
type Instance struct {
	c *conn
}

// Next returns the next server packet on the instance socket.
func (i *Instance) Next(ctx context.Context) (Packet, error) { return i.c.readFrame(ctx) }

// Send writes one client packet by its core opcode on the instance socket.
func (i *Instance) Send(ctx context.Context, core uint32, payload []byte) error {
	return i.c.sendFrame(ctx, core, payload)
}

// Close closes the instance socket.
func (i *Instance) Close() error { return i.c.close() }

// HandshakeNames returns the server packets this connection's handshake read (its auth challenge and the
// encrypted-mode packet).
func (i *Instance) HandshakeNames() []string {
	return append([]string(nil), i.c.handshakeNames...)
}

// checkInstanceTarget refuses a SMSG_CONNECT_TO that names a host other than the one the realm socket is
// connected to, unless the caller opted in. Both sides are IP addresses by the time they are compared: the realm's
// from the accepted TCP connection, the packet's from the address bytes the server sent. The port may differ (the
// instance socket listens on another port by design).
func (r *Realm) checkInstanceTarget(ct ConnectTo) error {
	remote, ok := r.c.nc.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("worldconn: the realm socket's remote address %s is not an IP, so the instance socket at %s cannot be checked; refusing", r.c.nc.RemoteAddr(), ct.Address())
	}
	if ct.IP.Equal(remote.IP) {
		return nil
	}
	if r.allowInstanceRedirect {
		r.c.log("world instance: SMSG_CONNECT_TO redirects from %s to %s (allowed by --allow-instance-redirect)", remote, ct.Address())
		return nil
	}
	return fmt.Errorf("worldconn: refusing the instance socket at %s because the realm socket is connected to %s; a clustered server needs --allow-instance-redirect", ct.Address(), remote)
}

// DialInstance opens the instance socket SMSG_CONNECT_TO named, authenticates with CMSG_AUTH_CONTINUED_SESSION and
// switches encryption on. The digest is always checked by the server (unlike the realm's), so this is where a
// wrong session key shows up.
func (r *Realm) DialInstance(ctx context.Context, ct ConnectTo) (*Instance, error) {
	// one operation, one deadline: the stated default from now (the context's own end still cancels it, since a
	// child context inherits its parent's). The realm's handshake deadline is not reused, so a long-lived Realm can
	// open an instance socket an hour later.
	opCtx, cancel := context.WithDeadline(ctx, operationDeadline(ctx))
	defer cancel()
	if err := r.checkInstanceTarget(ct); err != nil {
		return nil, err
	}

	nc, err := (&net.Dialer{}).DialContext(opCtx, "tcp", ct.Address())
	if err != nil {
		return nil, fmt.Errorf("worldconn: connecting to the instance socket at %s: %w", ct.Address(), contextError(opCtx, err))
	}
	if tcp, ok := nc.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	c := &conn{
		nc:       nc,
		table:    r.c.table,
		inflater: wire.NewInflater(),
		logf:     r.c.logf,
	}
	challenge, err := c.exchangeBanners(opCtx)
	if err != nil {
		_ = c.close()
		return nil, err
	}
	localChallenge := make([]byte, 32)
	if _, err := rand.Read(localChallenge); err != nil {
		_ = c.close()
		return nil, fmt.Errorf("worldconn: local challenge: %w", err)
	}
	digest := continuedSessionDigest(r.sessionKey[:], ct.Key, localChallenge, challenge)

	// AuthenticationPackets.cpp AuthContinuedSession::Read: DosResponse, LocalChallenge, Digest, Key,
	// NativeRealmAddress, Key3 (80 bytes). The protocol document orders Key before the challenge; the server reads
	// this order, and the real client's captured packet is 80 bytes.
	b := wire.NewWriter()
	b.WriteU64(0) // DosResponse: unchecked
	b.WriteBytes(localChallenge)
	b.WriteBytes(digest[:])
	b.WriteU64(ct.Key)
	b.WriteU32(ct.NativeRealmAddress)
	b.WriteU32(0) // Key3
	if err := c.sendFrame(opCtx, wire.CMSGAuthContinuedSession, b.Bytes()); err != nil {
		_ = c.close()
		return nil, err
	}
	c.log("world instance: socket opened, continued session sent (serial %d, digest %d bytes)", ct.Serial, len(digest))

	encryptKey := encryptKeyFrom(r.sessionKey[:], localChallenge, challenge)
	if err := c.waitEncryptedMode(opCtx, encryptKey); err != nil {
		_ = c.close()
		return nil, err
	}
	c.log("world instance: encryption on")
	c.handshakeDone = true
	return &Instance{c: c}, nil
}
