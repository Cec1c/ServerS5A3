package dnfbridge

import "testing"

func TestPartyTownReturnWaitsForAreaRegistration(t *testing.T) {
	_, service, leader, member, leaderConn, memberConn := newTownCoPresenceTestService(t)
	storeRuntimePartyState(leader, townCoPresencePartyState())
	storeRuntimePartyState(member, townCoPresencePartyState())
	actor, found := service.onlinePlayers.PlayerForCharacter(member.selectedCharacterID)
	if !found {
		t.Fatal("member fixture has no town actor")
	}
	service.onlinePlayers.LeaveAreaSession(member.selectedCharacterID, member)
	if err := service.rebuildRuntimePartyTownCoPresenceAfterReturn(leader, "test_member_before_area_registration"); err != nil {
		t.Fatalf("pending peer area registration interrupted town return: %v", err)
	}
	if leaderConn.write.Len() != 0 || memberConn.write.Len() != 0 {
		t.Fatal("party projection started before every member registered its town actor")
	}
	service.onlinePlayers.EnterArea(&actor)
	if err := service.rebuildRuntimePartyTownCoPresenceAfterReturn(member, "test_member_after_area_registration"); err != nil {
		t.Fatal(err)
	}
	if leaderConn.write.Len() == 0 || memberConn.write.Len() == 0 {
		t.Fatal("party projection did not resume after the member registered its town actor")
	}
}
