package bnet

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// build70938 is the client build of the Forever client; the server accepts the login for it
// (src/server/shared/Realm/ClientBuildInfo.cpp).
const build70938 = 70938

// DefaultServerName is the name inside the login server's certificate (LoginREST.ExternalAddress of the server setup).
const DefaultServerName = "trinity.actual.battle.net"

// Options configures a login run. Password is never logged, never written anywhere and never sent as a flag value.
type Options struct {
	RESTAddr    string // host:port of the login REST port
	RPCAddr     string // host:port of the Battle.net RPC port
	CAFile      string // PEM file of the root CA that signed the login server's certificate
	ServerName  string // name to verify in the certificate (default DefaultServerName)
	Account     string // account e-mail or name as typed into the login form
	Password    string // password; case is significant for SRP v2 accounts
	ClientBuild uint32 // client build to claim (0 = build70938); a realm only lets in the builds it accepts
	Timeout     time.Duration
	Logf        func(format string, args ...any) // one line per completed step; may be nil
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
}

// RealmError marks a failure after the account was logged in (realm list, realm join), so a caller can tell "the
// login was refused" from "the account has no realm to join".
type RealmError struct{ Err error }

func (e *RealmError) Error() string { return e.Err.Error() }
func (e *RealmError) Unwrap() error { return e.Err }

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
// realm list, realm join. It stops at the join answer; the world socket is a later round.
func Login(ctx context.Context, opts Options) (*Result, error) {
	if opts.ServerName == "" {
		opts.ServerName = DefaultServerName
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.ClientBuild == 0 {
		opts.ClientBuild = build70938
	}
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
	rest, err := newRESTClient(opts.RESTAddr, opts.ServerName, roots, opts.Timeout)
	if err != nil {
		return nil, err
	}
	defer rest.close()

	path, err := rest.form(ctx)
	if err != nil {
		return nil, err
	}
	opts.logf("login form: LOGIN_FORM, SRP form at %s", path)

	challenge, err := rest.challenge(ctx, path, opts.Account)
	if err != nil {
		return nil, err
	}
	srp, err := newSRPClient(*challenge, opts.Password, nil)
	if err != nil {
		return nil, err
	}
	opts.logf("srp challenge: version %d, %s, %d iterations, modulus %d bytes, salt 32 bytes",
		challenge.Version, challenge.HashFunction, challenge.Iterations, len(challenge.Modulus)/2)

	login, err := rest.proof(ctx, opts.Account, srp)
	if err != nil {
		return nil, err
	}
	if err := srp.verify(login.ServerEvidenceM2); err != nil {
		return nil, err
	}
	opts.logf("srp proof: client evidence M1 %d bytes accepted, server evidence M2 %d bytes verified",
		len(srp.M1), len(login.ServerEvidenceM2)/2)
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
	opts.logf("rpc connect: server id %d/%d, server time %d, ciid %q, bindless %t",
		connect.ServerID.Label, connect.ServerID.Epoch, connect.ServerTime, connect.Ciid, connect.UseBindlessRPC)

	logon, err := rpc.logonRequest(ctx, login.LoginTicket, opts.ClientBuild)
	if err != nil {
		return nil, err
	}
	if len(logon.GameAccountIDs) == 0 {
		return nil, errors.New("bnet: logon succeeded but the account has no game account")
	}
	gameAccount := logon.GameAccountIDs[0]
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
	opts.logf("sub-region: %s", result.SubRegion)

	realm, err := rpc.realmList(ctx, result.SubRegion)
	if err != nil {
		return nil, &RealmError{Err: err}
	}
	opts.logf("realm list: realm %d %q, address 0x%08X, build %d, population %d, flags 0x%02X",
		realm.CfgRealmsID, realm.Name, realm.WowRealmAddress, realm.Version.Build, realm.PopulationState, realm.Flags)
	result.RealmName = realm.Name
	result.RealmID = realm.CfgRealmsID
	result.RealmAddress = realm.WowRealmAddress

	// ---- RPC: realm join
	join, err := rpc.joinRealm(ctx, realm.WowRealmAddress)
	if err != nil {
		return nil, &RealmError{Err: err}
	}
	var ticket realmJoinTicket
	if err := json.Unmarshal(join.ticket, &ticket); err != nil {
		return nil, fmt.Errorf("bnet: realm join ticket: %w", err)
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
		len(join.ticket), world.IP, world.Port, len(join.secret))
	return result, nil
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

	req := ClientRequest{Attributes: []Attribute{
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
	req := GetAllValuesForAttributeRequest{AttributeKey: "Command_RealmListRequest_v1_b9"}
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
	req := ClientRequest{Attributes: []Attribute{
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
		return nil, fmt.Errorf("bnet: no realm in sub-region %s", subRegion)
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
	req := ClientRequest{Attributes: []Attribute{
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

func attrString(name, value string) Attribute {
	return Attribute{Name: name, Value: Variant{StringValue: &value}}
}

func attrBlob(name string, value []byte) Attribute {
	return Attribute{Name: name, Value: Variant{BlobValue: value}}
}

func attrUint(name string, value uint64) Attribute {
	return Attribute{Name: name, Value: Variant{UintValue: &value}}
}

func (r ClientResponse) blob(name string) ([]byte, bool) {
	for _, a := range r.Attributes {
		if a.Name == name && a.Value.BlobValue != nil {
			return a.Value.BlobValue, true
		}
	}
	return nil, false
}
