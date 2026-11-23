package world

// accept_invite's result names the invite's inviter, not the party leader the roster may show: a scenario needs to
// tell which bot offered the group (#112).

import (
	"encoding/json"
	"testing"
	"time"
)

// TestServeAcceptInviteNamesInviter: the invite says "Botname" while the roster the accept brings names its leader
// "Inviter", so the result must carry the invite's name and GUID, not the roster leader's.
func TestServeAcceptInviteNamesInviter(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveInvite = true
	f.serveAccept = "roster"
	f.serveInviteName = "Botname"
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)

	// The invite the fake sent is in the model before the accept is called.
	deadline := time.Now().Add(5 * time.Second)
	var party ServePartyInfo
	for time.Now().Before(deadline) {
		json.Unmarshal(a.mustCall(t, "party", nil), &party)
		if party.PendingInvite != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if party.PendingInvite != "Botname" {
		t.Fatalf("pending invite %q, want Botname\n%s", party.PendingInvite, steps.joined())
	}

	var got ServeAcceptInviteResult
	json.Unmarshal(a.mustCall(t, "accept_invite", nil), &got)
	if got.Inviter != "Botname" {
		t.Fatalf("inviter %q, want the invite's Botname, not the party leader\n%s", got.Inviter, steps.joined())
	}
	if got.InviterGUID != GUID(serveInviterGUID) {
		t.Fatalf("inviter_guid %s, want %s\n%s", got.InviterGUID, GUID(serveInviterGUID), steps.joined())
	}
	json.Unmarshal(a.mustCall(t, "party", nil), &party)
	if !party.InGroup {
		t.Fatalf("party after the accept %+v\n%s", party, steps.joined())
	}
	a.stop(t)
	f.wait(t)
}

// TestServeAcceptInviteNoInviteUnchanged pins the error a call without a held invite still answers, word for word.
func TestServeAcceptInviteNoInviteUnchanged(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	a.mustFail(t, "accept_invite", nil, -32003, "no pending group invite")
	a.stop(t)
	f.wait(t)
}
