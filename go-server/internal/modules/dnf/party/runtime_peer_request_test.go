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

// 队友走光后剩下的人还挂在单人残留队里；多人队长邀请他必须能登记，
// 接受后他从残留队移入邀请方队伍，残留队被删除。
func TestRuntimeOnlinePeerRequestAdmitsLoneLeftoverIntoMultiMemberParty(t *testing.T) {
	manager := NewRuntimePartyManager()
	member := func(id uint16, gen uint64) RuntimePartyMember {
		return RuntimePartyMember{UserID: id, SessionGeneration: gen, State: alignedcmd.PartyMemberState{UserID: id}}
	}
	// 残留队：14 曾与 5 同队，5 离开后 14 独自留在队里。
	leftover := manager.Create(member(14, 1), alignedcmd.PartyState{MaxMembers: 4})
	if !leftover.OK || !manager.Join(leftover.Party.ID, member(5, 2)).OK {
		t.Fatal("leftover setup")
	}
	if left := manager.Leave(5, 2); !left.OK || left.Disbanded {
		t.Fatalf("leftover leave=%+v", left)
	}
	// 邀请方：22 带 24 的两人队。
	host := manager.Create(member(22, 3), alignedcmd.PartyState{MaxMembers: 4})
	if !host.OK || !manager.Join(host.Party.ID, member(24, 4)).OK {
		t.Fatal("host setup")
	}
	request, ok := manager.RecordOnlinePeerRequest(14, 1, 22, 3, 0)
	if !ok || request.JoinerID != 14 || request.AnchorID != 22 || request.PartyID != host.Party.ID {
		t.Fatalf("lone leftover invite=%+v ok=%t", request, ok)
	}
	consumed, ok := manager.ConsumePeerRequest(14, 1, 22, 3, 0)
	if !ok || consumed != request {
		t.Fatalf("consume=%+v ok=%t", consumed, ok)
	}
	joined := manager.Join(request.PartyID, member(14, 1))
	if !joined.OK || joined.PriorLeave == nil || !joined.PriorLeave.Disbanded {
		t.Fatalf("join=%+v", joined)
	}
	snapshot, found := manager.SnapshotByUser(14, 1)
	if !found || snapshot.ID != host.Party.ID || len(snapshot.Members) != 3 {
		t.Fatalf("after join=%+v found=%t", snapshot, found)
	}
}

// 目标在真正的多人队里（或不是残留队队长）时仍然拒绝，不能把人从别队抢走。
func TestRuntimeOnlinePeerRequestStillRejectsMemberOfAnotherRealParty(t *testing.T) {
	manager := NewRuntimePartyManager()
	member := func(id uint16, gen uint64) RuntimePartyMember {
		return RuntimePartyMember{UserID: id, SessionGeneration: gen, State: alignedcmd.PartyMemberState{UserID: id}}
	}
	other := manager.Create(member(14, 1), alignedcmd.PartyState{MaxMembers: 4})
	if !other.OK || !manager.Join(other.Party.ID, member(5, 2)).OK {
		t.Fatal("other setup")
	}
	host := manager.Create(member(22, 3), alignedcmd.PartyState{MaxMembers: 4})
	if !host.OK || !manager.Join(host.Party.ID, member(24, 4)).OK {
		t.Fatal("host setup")
	}
	if _, ok := manager.RecordOnlinePeerRequest(14, 1, 22, 3, 0); ok {
		t.Fatal("leader of a real two-member party was invited away")
	}
	if _, ok := manager.RecordOnlinePeerRequest(5, 2, 22, 3, 0); ok {
		t.Fatal("member of a real two-member party was invited away")
	}
	// 非队长不能邀请。
	if _, ok := manager.RecordOnlinePeerRequest(14, 1, 24, 4, 0); ok {
		t.Fatal("non-leader invite accepted")
	}
}
