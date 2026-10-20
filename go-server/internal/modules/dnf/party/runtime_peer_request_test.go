package party

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func TestRuntimeOnlinePeerRequestPinsOwnerAndSessionGenerations(t *testing.T) {
	manager := NewRuntimePartyManager()
	owner := RuntimePartyMember{UserID: 47, SessionGeneration: 4, State: alignedcmd.PartyMemberState{UserID: 47}}
	created := manager.Create(owner, alignedcmd.PartyState{MaxMembers: 4})
	if !created.OK {
		t.Fatal(created.Reason)
	}
	request, ok := manager.RecordOnlinePeerRequest(47, 4, 6, 5, 0)
	if !ok || request.AnchorID != 47 || request.JoinerID != 6 || request.PartyID != created.Party.ID {
		t.Fatalf("join direction=%+v ok=%t", request, ok)
	}
	if _, ok := manager.ConsumePeerRequest(47, 40, 6, 5, 0); ok {
		t.Fatal("wrong receiver generation consumed request")
	}
	if _, ok := manager.ConsumePeerRequest(47, 4, 6, 50, 0); ok {
		t.Fatal("wrong requester generation consumed request")
	}
	if _, ok := manager.ConsumePeerRequest(47, 4, 6, 5, 12); ok {
		t.Fatal("wrong mode consumed request")
	}
	got, ok := manager.ConsumePeerRequest(47, 4, 6, 5, 0)
	if !ok || got != request {
		t.Fatalf("correct request lost: %+v ok=%t", got, ok)
	}
	if _, ok := manager.ConsumePeerRequest(47, 4, 6, 5, 0); ok {
		t.Fatal("request replay accepted")
	}
}

func TestRuntimeOnlineTradeAndForcedInviteKeepExplicitDirection(t *testing.T) {
	manager := NewRuntimePartyManager()
	owner := RuntimePartyMember{UserID: 47, SessionGeneration: 4, State: alignedcmd.PartyMemberState{UserID: 47}}
	created := manager.Create(owner, alignedcmd.PartyState{MaxMembers: 4})
	if !created.OK {
		t.Fatal(created.Reason)
	}
	trade, ok := manager.RecordOnlinePeerRequest(47, 4, 6, 5, 1)
	if !ok || trade.AnchorID != 6 || trade.JoinerID != 47 {
		t.Fatalf("trade direction changed: %+v", trade)
	}
	if !manager.RecordInvite(47, 4, 6, 5, 0, 0) {
		t.Fatal("forced invite registration")
	}
	forced, ok := manager.ConsumePeerRequest(47, 4, 6, 5, 0)
	if !ok || forced.AnchorID != 6 || forced.JoinerID != 47 {
		t.Fatalf("forced invite direction changed: %+v", forced)
	}
}
