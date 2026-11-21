package bnet

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// realmFlagVersionMismatch is RealmFlags::VersionMismatch: the realm is listed, but not for this client's build
// (src/server/shared/Realm/Realm.h:33-42).
const realmFlagVersionMismatch = 0x01

// defaultLoginDeadline bounds one Login call from the first request to the join answer. The per-call Timeout only
// bounds one request, so a server that keeps a call alive with traffic would otherwise hold the login forever.
const defaultLoginDeadline = 2 * time.Minute

// DefaultServerName is the name inside the login server's certificate (LoginREST.ExternalAddress of the server setup).
const DefaultServerName = "trinity.actual.battle.net"

// Options configures a login run. Password is never logged, never written anywhere and never sent as a flag value.
type Options struct {
	RESTAddr    string                           // host:port of the login REST port
	RPCAddr     string                           // host:port of the Battle.net RPC port
	CAFile      string                           // PEM file of the root CA that signed the login server's certificate
	ServerName  string                           // name to verify in the certificate (default DefaultServerName)
	Account     string                           // account e-mail or name as typed into the login form
	Password    string                           // password; case is significant for SRP v2 accounts
	ClientBuild uint32                           // client build to claim; 0 = the newest build this client knows that the realm accepts (see Login)
	Timeout     time.Duration                    // one RPC call or one HTTP request (0 = 30 s)
	Deadline    time.Duration                    // the whole login, from the form to the join answer (0 = 2 min)
	Logf        func(format string, args ...any) // one line per completed step; may be nil

	// WorldAccept, when set, authenticates the world socket for a login and says whether the claimed build works
	// there. The realm list is not proof: a realm may accept several builds (Realm.AlsoAcceptBuilds) while the
	// world server has a build_info row for one. Login calls WorldAccept after every login the realm accepted; with
	// ClientBuild 0 a *worldconn.Refused whose BadVersion is set moves on to the next known build only when the
	// refusal is attributable to the build claim — the socket sent a build auth key, or WorldSkipBuildAuthKeyCheck
	// says the server does not check one. Any other error, and a 0x1C that cannot be attributed, ends the login and
	// is returned (see WorldSkipBuildAuthKeyCheck). A non-nil *worldconn.Realm is handed to the caller in
	// Result.WorldConn; a callback that returns an error must close the socket it opened.
	WorldAccept func(ctx context.Context, result *Result) (*worldconn.Realm, error)

	// WorldSkipBuildAuthKeyCheck states that the world server runs with Network.SkipBuildAuthKeyCheck = 1, the
	// setting doc/ForeverSetup.md requires for this client. It is what makes a world ERROR_BAD_VERSION attributable
	// to the build claim: with the check skipped, a ticket variant without a build_auth_key row is not refused
	// (WorldSocket.cpp:756-772), so 0x1C can only be the missing build_info row (:739-746) and the automatic build
	// selection may try the next known build. With the check on, 0x1C is also what a missing build_auth_key row
	// produces (:760-768), and this client's 24 zero digest bytes can never authenticate there — a server that has
	// the row compares the digest and answers ERROR_DENIED (:775-791), one that has no row refuses. A retry cannot
	// succeed in that configuration, so Login does not guess: it returns the refusal and says both causes. Off by
	// default; a caller that has not established the setting must not have its login retried on an assumption.
	WorldSkipBuildAuthKeyCheck bool

	// BeforeAttempt, when set, is called before EVERY login attempt: the one-build login, each build probe of the
	// automatic selection (ClientBuild 0 logs in once per build the realm refuses) and so each re-login of a caller
	// that logs in again. A caller that keeps a login rate (a window of logins per IP, a fleet's shared budget) waits
	// for its turn and books it there, so one reservation never covers several real logins. It runs on the caller's
	// context, outside the attempt's deadline: with a hook the Deadline bounds each attempt (the login, the realm join
	// and WorldAccept) instead of the whole selection, so a wait for a turn cannot eat it. An error ends the login and
	// is returned as it is.
	BeforeAttempt func(ctx context.Context) error

	// srpPrivate pins the client private to a test vector's value. It is unexported so no caller can weaken the
	// randomness; only the package's own tests set it.
	srpPrivate *big.Int
}

// Result is everything the login service handed out. The secrets are kept for the world-socket round; this client
// does not derive the world session key yet.
type Result struct {
	LoginTicket   string // "TC-" + 40 hex, for reference only; treat as a secret
	AccountID     uint64 // Battle.net account id
	GameAccountID uint64 // game account id (low half of the EntityId)
	SessionKey    []byte // 64 bytes from OnLogonComplete
	ClientSecret  []byte // 32 bytes this client put into Param_ClientInfo
	JoinSecret    []byte // 32 bytes: the server's half of the key the world socket will need

	JoinTicket   []byte // RealmJoinTicket JSON, the blob CMSG_AUTH_SESSION carries
	RealmName    string
	RealmID      uint32 // realmlist id (auth.realmlist.id)
	RealmAddress uint32 // virtual realm address, (region << 24) | (site << 16) | realm
	SubRegion    string // "region-site-realm"
	ServerIP     string // world server address from Param_ServerAddresses
	ServerPort   uint32

	ClientBuild uint32 // the build this login claimed (WorldSocket::_clientBuild)
	RealmBuild  uint32 // the build the realm list showed for that claim (FillRealmEntry: the claim itself when the realm accepts it)

	// WorldConn is the world socket Options.WorldAccept authenticated for this build; the caller owns it (world.Enter
	// adopts it, or the caller closes it). Nil when no WorldAccept was given or it accepted without a socket.
	WorldConn *worldconn.Realm
}

// RealmError marks a failure after the account was logged in (realm list, realm join), so a caller can tell "the
// login was refused" from "the account has no realm to join".
type RealmError struct{ Err error }

func (e *RealmError) Error() string { return e.Err.Error() }
func (e *RealmError) Unwrap() error { return e.Err }

// RealmMismatch is the realm list's VersionMismatch flag: the realm does not accept the build the login claimed
// (RealmList.cpp: FillRealmEntry sets the flag and shows the realm's own build). Login's automatic-build path uses
// RealmBuild as the hint for the next claim; an explicit build returns it as the login's error.
type RealmMismatch struct {
	Realm      string // the realm's name as the server sent it; sanitise before printing
	Claimed    uint32 // the build the login claimed
	RealmBuild uint32 // the build the realm list showed instead
}

func (e *RealmMismatch) Error() string {
	return fmt.Sprintf("bnet: realm %q does not accept client build %d (the realm is build %d)",
		sanitizeServerText(e.Realm), e.Claimed, e.RealmBuild)
}

// supportedBuildList is the builds this client knows, in the message's order.
func supportedBuildList() string {
	builds := wire.SupportedClientBuilds()
	parts := make([]string, len(builds))
	for i, b := range builds {
		parts[i] = strconv.FormatUint(uint64(b), 10)
	}
	if len(parts) < 2 {
		return strings.Join(parts, "")
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func (o *Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// LoadRoots reads the root CA file. An empty or unreadable file is an error: this client has no trust-on-first-use
// fallback and never skips the certificate check.
func LoadRoots(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("bnet: root CA %s: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("bnet: root CA %s holds no PEM certificate", path)
	}
	return pool, nil
}

// Login runs the whole login: REST SRP login and ticket, RPC connect and logon, realm list ticket, sub-regions,
// realm list, realm join. It stops at the join answer unless Options.WorldAccept is set, in which case that
// callback also authenticates the world socket and the login only returns once the world server accepts the build.
//
// With ClientBuild 0 it claims the newest build this client knows that the realm accepts (the builds of
// wire.SupportedClientBuilds, newest first). The realm list's build field is only a hint: for a build the realm
// accepts it echoes the claim itself (RealmList.cpp: FillRealmEntry shows clientBuild when RealmAcceptsBuild is
// true), so it says nothing about the realm's own build, and a realm may accept several builds (Realm.AlsoAcceptBuilds)
// while the world server only has build info for one of them. A claim that a realm does not accept comes back as a
// VersionMismatch with the realm's own build; that build is then tried next when it is one this client knows, and the
// remaining known builds are tried newest first otherwise. A build the realm accepts but the world server refuses
// with ERROR_BAD_VERSION is left to Options.WorldAccept, whose refusal this loop moves on from only while 0x1C is
// attributable to the build claim rather than to a build auth key the server wanted (Options.WorldSkipBuildAuthKeyCheck);
// unattributable, the refusal is returned with both causes named, because no retry can pass a key check this client
// cannot satisfy. The realm list and the realm join both read the build of the logon, not of the realm list ticket
// (GameUtilitiesService.cpp:312 and :344 pass session->GetBuild()), so every attempt is the whole sequence again. A
// non-zero ClientBuild always wins and is claimed as given (one world authentication, no retry).
func Login(ctx context.Context, opts Options) (*Result, error) {
	if opts.ServerName == "" {
		opts.ServerName = DefaultServerName
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.Deadline <= 0 {
		opts.Deadline = defaultLoginDeadline
	}
	// one absolute deadline for the whole login; the caller's context may be earlier and wins. With a BeforeAttempt
	// hook the deadline is each attempt's instead (see begin).
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, opts.Deadline)
	defer cancel()
	// begin starts one attempt: the hook runs first, on the caller's context, then the attempt gets its context.
	begin := func() (context.Context, context.CancelFunc, error) {
		if opts.BeforeAttempt == nil {
			return ctx, func() {}, nil
		}
		if err := opts.BeforeAttempt(parent); err != nil {
			return nil, nil, err
		}
		actx, done := context.WithTimeout(parent, opts.Deadline)
		return actx, done, nil
	}
	if opts.ClientBuild != 0 {
		actx, done, err := begin()
		if err != nil {
			return nil, err
		}
		defer done()
		result, err := loginOnce(actx, opts)
		if err != nil {
			return nil, err
		}
		return acceptWorld(actx, opts, result)
	}
	supported := wire.SupportedClientBuilds()
	// newest first: prefer the newest build this client knows, then older ones
	remaining := make([]uint32, 0, len(supported))
	for i := len(supported) - 1; i >= 0; i-- {
		remaining = append(remaining, supported[i])
	}
	var hint uint32 // the realm's own build, from the last mismatch
	var realmName string
	var attempts []buildAttempt
	worldRefusals := 0
	for len(remaining) > 0 {
		build := remaining[0]
		if hint != 0 {
			if i := indexOfBuild(remaining, hint); i >= 0 {
				build, remaining = hint, append(remaining[:i], remaining[i+1:]...)
			} else {
				remaining = remaining[1:]
			}
		} else {
			remaining = remaining[1:]
		}
		probe := opts
		probe.ClientBuild = build
		actx, done, err := begin()
		if err != nil {
			return nil, err
		}
		result, err := loginOnce(actx, probe)
		if err == nil {
			realmName = result.RealmName
			if opts.WorldAccept == nil {
				done()
				return result, nil
			}
			result, err = acceptWorld(actx, opts, result)
			done()
			if err == nil {
				return result, nil
			}
			var refused *worldconn.Refused
			if !errors.As(err, &refused) || !refused.BadVersion() {
				return nil, err
			}
			// ERROR_BAD_VERSION alone is not a verdict on the build: the code also answers a missing build_auth_key
			// row when the server checks one (WorldSocket.cpp:760-768), and the claim has no way to pass that check.
			if !refused.BuildAuthKeyUsed() && !opts.WorldSkipBuildAuthKeyCheck {
				return nil, ambiguousWorldRefusal(build, refused)
			}
			worldRefusals++
			attempts = append(attempts, buildAttempt{build, "the world server refused it"})
			opts.logf("client build: the world server does not accept build %d (ERROR_BAD_VERSION); trying the next known build", build)
			continue
		}
		done()
		var mismatch *RealmMismatch
		if !errors.As(err, &mismatch) {
			return nil, err
		}
		hint, realmName = mismatch.RealmBuild, mismatch.Realm
		attempts = append(attempts, buildAttempt{build, "the realm does not accept it"})
		opts.logf("client build: the realm does not accept build %d (the realm is build %d)", build, hint)
	}
	if worldRefusals > 0 {
		// Every build the realm let in was refused by the world server with ERROR_BAD_VERSION, which the check
		// above attributed to the build_info lookup, so the choice ended where it had to: name every build tried,
		// with the side that refused it.
		tried := make([]string, len(attempts))
		for i, a := range attempts {
			tried[i] = a.String()
		}
		msg := fmt.Sprintf("bnet: realm %q: no client build this client knows was accepted (%s)",
			sanitizeServerText(realmName), strings.Join(tried, "; "))
		if hint != 0 {
			msg += fmt.Sprintf("; the realm is build %d", hint)
		}
		return nil, &RealmError{Err: errors.New(msg + "; pass --build N to claim a build anyway")}
	}
	return nil, &RealmError{Err: fmt.Errorf(
		"bnet: realm %q accepts none of the client builds this client knows (%s); the realm is build %d; pass --build N to claim a build anyway",
		sanitizeServerText(realmName), supportedBuildList(), hint)}
}

// acceptWorld runs Options.WorldAccept for a login the realm accepted and stores the socket it hands back.
func acceptWorld(ctx context.Context, opts Options, result *Result) (*Result, error) {
	if opts.WorldAccept == nil {
		return result, nil
	}
	conn, err := opts.WorldAccept(ctx, result)
	if err != nil {
		return nil, err
	}
	result.WorldConn = conn
	return result, nil
}

// ambiguousWorldRefusal is the login's error for a world ERROR_BAD_VERSION that nothing attributes to the build
// claim: the client sent no build auth key and has not established that the server skips the check. The refusal is
// kept as the cause (it is still what the server answered), and the text names both checks it can come from, so
// neither the build nor the realm is blamed for what may be a missing key row.
func ambiguousWorldRefusal(build uint32, refused *worldconn.Refused) error {
	return fmt.Errorf("bnet: %w; the code does not by itself name the check: the world server answers "+
		"ERROR_BAD_VERSION both for an account build with no build_info row (WorldSocket.cpp:739-746) and for a "+
		"join-ticket variant with no build_auth_key row while Network.SkipBuildAuthKeyCheck is off (:760-768), "+
		"and this login sent a zero digest, so build %d is not thereby unsupported; the selection did not try "+
		"another build, which could not pass that check either; pass --build N to claim a build anyway",
		refused, build)
}

// buildAttempt is one claimed build and which side refused it, for the error of an exhausted selection.
type buildAttempt struct {
	build  uint32
	reason string
}

func (a buildAttempt) String() string { return fmt.Sprintf("build %d: %s", a.build, a.reason) }

// indexOfBuild is the position of b in builds, or -1.
func indexOfBuild(builds []uint32, b uint32) int {
	for i, c := range builds {
		if c == b {
			return i
		}
	}
	return -1
}

// loginOnce is one complete login with the build it claims.
func loginOnce(ctx context.Context, opts Options) (*Result, error) {
	if opts.RESTAddr == "" {
		return nil, errors.New("bnet: no REST address")
	}
	if opts.RPCAddr == "" {
		return nil, errors.New("bnet: no RPC address")
	}
	if opts.Account == "" {
		return nil, errors.New("bnet: no account")
	}
	if opts.Password == "" {
		return nil, errors.New("bnet: no password")
	}

	roots, err := LoadRoots(opts.CAFile)
	if err != nil {
		return nil, err
	}

	// ---- REST: form, SRP challenge, proof, login ticket
	rest, err := newRESTClient(opts.RESTAddr, opts.ServerName, opts.Account, roots, opts.Timeout)
	if err != nil {
		return nil, err
	}
	defer rest.close()

	path, err := rest.form(ctx)
	if err != nil {
		return nil, err
	}
	opts.logf("login form: LOGIN_FORM, SRP form at %s", sanitizeServerText(path))

	challenge, err := rest.challenge(ctx, path, opts.Account)
	if err != nil {
		return nil, err
	}
	srp, err := newSRPClient(*challenge, opts.Password, opts.srpPrivate)
	if err != nil {
		return nil, err
	}
	opts.logf("srp challenge: version %d, %s, %d iterations, modulus %d bytes, salt 32 bytes",
		challenge.Version, sanitizeServerText(challenge.HashFunction), challenge.Iterations, len(challenge.Modulus)/2)

	login, err := rest.proof(ctx, opts.Account, srp)
	if err != nil {
		return nil, err
	}
	if err := srp.verify(login.ServerEvidenceM2); err != nil {
		return nil, err
	}
	opts.logf("srp proof: client evidence M1 %d bytes accepted, server evidence M2 %d bytes verified",
		len(srp.M1), len(srp.M2))
	opts.logf("login ticket: %d bytes", len(login.LoginTicket))

	// ---- RPC: connect, logon
	rpc, err := dialRPC(ctx, opts.RPCAddr, opts.ServerName, roots, opts.Timeout)
	if err != nil {
		return nil, err
	}
	defer rpc.Close()
	rpc.logf = opts.logf

	connect, err := rpc.connect(ctx)
	if err != nil {
		return nil, err
	}
	// lengths only: the server id, the server time and the ciid identify one live run, and a step log is meant to
	// be pasted into a report
	opts.logf("rpc connect: server id label %d digits, epoch %d digits, server time %d digits, ciid %d chars, bindless %t",
		digits(uint64(connect.ServerID.Label)), digits(uint64(connect.ServerID.Epoch)), digits(connect.ServerTime),
		len(connect.Ciid), connect.UseBindlessRPC)

	logon, err := rpc.sendLogon(ctx, login.LoginTicket, opts.ClientBuild)
	if err != nil {
		return nil, err
	}
	if len(logon.GameAccountIDs) == 0 {
		return nil, errors.New("bnet: logon succeeded but the account has no game account")
	}
	gameAccount := logon.GameAccountIDs[0]
	if gameAccount.Low > math.MaxUint32 {
		// the realm list ticket identity carries a uint32 game account id
		return nil, fmt.Errorf("bnet: game account id %d does not fit the realm list request", gameAccount.Low)
	}
	opts.logf("logon: build %d, account id %d, %d game account(s), session key %d bytes",
		opts.ClientBuild, logon.AccountID.Low, len(logon.GameAccountIDs), len(logon.SessionKey))

	result := &Result{
		LoginTicket:   login.LoginTicket,
		AccountID:     logon.AccountID.Low,
		GameAccountID: gameAccount.Low,
		SessionKey:    logon.SessionKey,
	}

	// ---- RPC: realm list ticket (identity + client information with the 32-byte client secret)
	result.ClientSecret = make([]byte, 32)
	if _, err := rand.Read(result.ClientSecret); err != nil {
		return nil, fmt.Errorf("bnet: client secret: %w", err)
	}

	ticketBlob, err := rpc.realmListTicket(ctx, uint32(gameAccount.Low), result.ClientSecret, opts.ClientBuild)
	if err != nil {
		return nil, &RealmError{Err: err}
	}
	opts.logf("realm list ticket: %d bytes", len(ticketBlob))

	// ---- RPC: sub-regions and realm list
	subRegions, err := rpc.subRegions(ctx)
	if err != nil {
		return nil, &RealmError{Err: err}
	}
	if len(subRegions) == 0 {
		return nil, errors.New("bnet: the server named no sub-region")
	}
	result.SubRegion = subRegions[0]
	opts.logf("sub-region: %s", sanitizeServerText(result.SubRegion))

	realm, err := rpc.realmList(ctx, result.SubRegion)
	if err != nil {
		return nil, &RealmError{Err: err}
	}
	opts.logf("realm list: realm %d %q, address 0x%08X, build %d, population %d, flags 0x%02X",
		realm.CfgRealmsID, sanitizeServerText(realm.Name), realm.WowRealmAddress, realm.Version.Build, realm.PopulationState, realm.Flags)
	if realm.PopulationState == 0 {
		// RealmPopulationState::Offline (Realm.h:61-71): listed, but JoinRealm refuses it
		return nil, &RealmError{Err: &realmOfflineError{text: fmt.Sprintf("bnet: realm %q is offline; its world server has not written the realmlist row", sanitizeServerText(realm.Name))}}
	}
	if realm.Flags&realmFlagVersionMismatch != 0 {
		return nil, &RealmError{Err: &RealmMismatch{Realm: realm.Name, Claimed: opts.ClientBuild, RealmBuild: realm.Version.Build}}
	}
	result.RealmName = realm.Name
	result.RealmID = realm.CfgRealmsID
	result.RealmAddress = realm.WowRealmAddress
	result.ClientBuild = opts.ClientBuild
	result.RealmBuild = realm.Version.Build
	opts.logf("client build: claimed %d, the realm list shows %d", opts.ClientBuild, realm.Version.Build)

	// ---- RPC: realm join
	join, err := rpc.joinRealm(ctx, realm.WowRealmAddress)
	if err != nil {
		return nil, &RealmError{Err: err}
	}
	var ticket realmJoinTicket
	if err := json.Unmarshal(join.ticket, &ticket); err != nil {
		return nil, decodeError("realm join ticket", err)
	}
	if ticket.GameAccount == "" {
		return nil, errors.New("bnet: realm join ticket has no game account")
	}
	var addresses realmListServerIPAddresses
	if err := decodeCompressed(join.addresses, "JSONRealmListServerIPAddresses", &addresses); err != nil {
		return nil, err
	}
	if len(addresses.Families) == 0 || len(addresses.Families[0].Addresses) == 0 {
		return nil, errors.New("bnet: realm join answer has no server address")
	}
	world := addresses.Families[0].Addresses[0]
	if len(join.secret) != 32 {
		return nil, fmt.Errorf("bnet: realm join secret is %d bytes, want 32", len(join.secret))
	}

	result.JoinTicket = join.ticket
	result.JoinSecret = join.secret
	result.ServerIP = world.IP
	result.ServerPort = world.Port
	opts.logf("realm join: join ticket %d bytes, world server %s:%d, join secret %d bytes",
		len(join.ticket), sanitizeServerText(world.IP), world.Port, len(join.secret))
	return result, nil
}

// Summary is the one-line description a command prints on stdout. RealmName and ServerIP are kept raw in Result
// (the world socket needs the address as the server sent it); here every server-supplied field goes through the
// sanitiser, so a realm name with control bytes or an ANSI escape cannot rewrite the terminal.
func (r *Result) Summary() string {
	return fmt.Sprintf("realm: %s (id %d, address 0x%08X, world %s:%d)",
		sanitizeServerText(r.RealmName), r.RealmID, r.RealmAddress, sanitizeServerText(r.ServerIP), r.ServerPort)
}

// digits is the number of decimal digits of n. The step log prints it instead of a server id or a server time:
// those values name one live run and must not end up in a report that is pasted around.
func digits(n uint64) int {
	d := 1
	for n >= 10 {
		n /= 10
		d++
	}
	return d
}

// realmListTicket sends Command_RealmListTicketRequest_v1 with Param_Identity and Param_ClientInfo and returns the
// ticket blob the server answers with (src/server/bnetserver/Services/GameUtilitiesService.cpp:240-301).
func (s *rpcSession) realmListTicket(ctx context.Context, gameAccountID uint32, secret []byte, clientBuild uint32) ([]byte, error) {
	identity, err := json.Marshal(realmListTicketIdentity{GameAccountID: gameAccountID, GameAccountRegion: 2})
	if err != nil {
		return nil, err
	}
	now := uint32(time.Now().Unix())
	info, err := json.Marshal(realmListTicketClientInformation{Info: clientInformation{
		Platform:         fourCCWn64,
		BuildVariant:     "Win",
		Type:             fourCCWoW,
		TimeZone:         "UTC",
		CurrentTime:      now,
		TextLocale:       fourCCEnUS,
		AudioLocale:      fourCCEnUS,
		VersionDataBuild: clientBuild,
		Version:          clientVersion{Major: 1, Minor: 60, Revis: 1, Build: clientBuild},
		Secret:           jsonBytes(secret),
		ClientArch:       fourCCTextX64,
		PlatformType:     fourCCWin,
		SystemArch:       fourCCTextX64,
	}})
	if err != nil {
		return nil, err
	}

	req := clientRequest{Attributes: []attribute{
		attrString("Command_RealmListTicketRequest_v1_b9", "AuthRealmListTicket"),
		attrBlob("Param_Identity", append([]byte("JSONRealmListTicketIdentity:"), identity...)),
		attrBlob("Param_ClientInfo", append([]byte("JSONRealmListTicketClientInformation:"), info...)),
	}}
	body, err := s.call(ctx, gameUtilitiesServiceHash, gameUtilitiesProcessRequest, req.marshal())
	if err != nil {
		return nil, fmt.Errorf("bnet: realm list ticket: %w", err)
	}
	resp, err := unmarshalClientResponse(body)
	if err != nil {
		return nil, fmt.Errorf("bnet: realm list ticket response: %w", err)
	}
	ticket, ok := resp.blob("Param_RealmListTicket")
	if !ok {
		return nil, errors.New("bnet: realm list ticket response carries no Param_RealmListTicket")
	}
	if string(ticket) != "AuthRealmListTicket\x00" {
		return nil, fmt.Errorf("bnet: realm list ticket is %d bytes, not \"AuthRealmListTicket\\0\"", len(ticket))
	}
	return ticket, nil
}

// subRegions asks Command_RealmListRequest_v1 for the sub-regions the account may use
// (src/server/bnetserver/Services/GameUtilitiesService.cpp:132-151).
func (s *rpcSession) subRegions(ctx context.Context) ([]string, error) {
	req := getAllValuesForAttributeRequest{AttributeKey: "Command_RealmListRequest_v1_b9"}
	body, err := s.call(ctx, gameUtilitiesServiceHash, gameUtilitiesGetAllValuesMethod, req.marshal())
	if err != nil {
		return nil, fmt.Errorf("bnet: sub-regions: %w", err)
	}
	resp, err := unmarshalGetAllValuesForAttributeResponse(body)
	if err != nil {
		return nil, fmt.Errorf("bnet: sub-regions response: %w", err)
	}
	var out []string
	for _, v := range resp.Values {
		if v.StringValue != nil {
			out = append(out, *v.StringValue)
		}
	}
	return out, nil
}

// realmList asks Command_RealmListRequest_v1 for the realms of a sub-region and picks the realm to join
// (src/server/bnetserver/Services/GameUtilitiesService.cpp:304-334).
func (s *rpcSession) realmList(ctx context.Context, subRegion string) (*realmEntry, error) {
	req := clientRequest{Attributes: []attribute{
		attrString("Command_RealmListRequest_v1_b9", subRegion),
	}}
	body, err := s.call(ctx, gameUtilitiesServiceHash, gameUtilitiesProcessRequest, req.marshal())
	if err != nil {
		return nil, fmt.Errorf("bnet: realm list: %w", err)
	}
	resp, err := unmarshalClientResponse(body)
	if err != nil {
		return nil, fmt.Errorf("bnet: realm list response: %w", err)
	}
	listBlob, ok := resp.blob("Param_RealmList")
	if !ok {
		return nil, errors.New("bnet: realm list response carries no Param_RealmList")
	}
	var list realmListUpdates
	if err := decodeCompressed(listBlob, "JSONRealmListUpdates", &list); err != nil {
		return nil, err
	}
	if _, ok := resp.blob("Param_CharacterCountList"); !ok {
		return nil, errors.New("bnet: realm list response carries no Param_CharacterCountList")
	}

	var first *realmEntry
	for i := range list.Updates {
		part := &list.Updates[i]
		if part.Deleting || part.Update == nil || part.Update.Name == "" {
			continue
		}
		if first == nil {
			first = part.Update
		}
		// PopulationState 0 is Offline (Realm.h:61-71); joining it is refused, so prefer a realm that is up
		if part.Update.PopulationState != 0 {
			return part.Update, nil
		}
	}
	if first == nil {
		return nil, fmt.Errorf("bnet: no realm in sub-region %s", sanitizeServerText(subRegion))
	}
	return first, nil
}

type joinAnswer struct {
	ticket    []byte
	addresses []byte
	secret    []byte
}

// joinRealm sends Command_RealmJoinRequest_v1 and returns the three blobs of the answer
// (src/server/bnetserver/Services/GameUtilitiesService.cpp:336-356, RealmList.cpp:446-506).
func (s *rpcSession) joinRealm(ctx context.Context, realmAddress uint32) (*joinAnswer, error) {
	req := clientRequest{Attributes: []attribute{
		attrString("Command_RealmJoinRequest_v1_b9", "AuthRealmJoinTicket"),
		attrUint("Param_RealmAddress", uint64(realmAddress)),
	}}
	body, err := s.call(ctx, gameUtilitiesServiceHash, gameUtilitiesProcessRequest, req.marshal())
	if err != nil {
		return nil, fmt.Errorf("bnet: realm join: %w", err)
	}
	resp, err := unmarshalClientResponse(body)
	if err != nil {
		return nil, fmt.Errorf("bnet: realm join response: %w", err)
	}
	answer := &joinAnswer{}
	var ok bool
	if answer.ticket, ok = resp.blob("Param_RealmJoinTicket"); !ok {
		return nil, errors.New("bnet: realm join answer carries no Param_RealmJoinTicket")
	}
	if answer.addresses, ok = resp.blob("Param_ServerAddresses"); !ok {
		return nil, errors.New("bnet: realm join answer carries no Param_ServerAddresses")
	}
	if answer.secret, ok = resp.blob("Param_JoinSecret"); !ok {
		return nil, errors.New("bnet: realm join answer carries no Param_JoinSecret")
	}
	return answer, nil
}

// ------------------------------------------------------------------------------------------------ attribute helpers

func attrString(name, value string) attribute {
	return attribute{Name: name, Value: variant{StringValue: &value}}
}

func attrBlob(name string, value []byte) attribute {
	return attribute{Name: name, Value: variant{BlobValue: value}}
}

func attrUint(name string, value uint64) attribute {
	return attribute{Name: name, Value: variant{UintValue: &value}}
}

func (r clientResponse) blob(name string) ([]byte, bool) {
	for _, a := range r.Attributes {
		if a.Name == name && a.Value.BlobValue != nil {
			return a.Value.BlobValue, true
		}
	}
	return nil, false
}
