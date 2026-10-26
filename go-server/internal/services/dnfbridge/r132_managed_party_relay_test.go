package dnfbridge

import (
	"context"
	"net"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type r132RelayTestConn struct {
	bufferConn
	remote net.TCPAddr
}

func (c *r132RelayTestConn) RemoteAddr() net.Addr { return &c.remote }

func r132RelayFixture(t *testing.T) (*inviteAcceptDungeonFixture, *gameSession) {
	t.Helper()
	fixture := newInviteAcceptDungeonFixture(t)
	fixture.leader.conn = &r132RelayTestConn{remote: net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40101}}
	fixture.follower.conn = &r132RelayTestConn{remote: net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 40102}}

	repos, ok := fixture.service.repositoryGroup()
	if !ok {
		t.Fatal("repository group unavailable")
	}
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "101", AccountID: "account-1", Level: 20,
		Stats: map[string]int64{"town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "101", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	third := &gameSession{
		conn:            &r132RelayTestConn{remote: net.TCPAddr{IP: net.ParseIP("127.0.0.3"), Port: 40103}},
		connID:          "r132-third",
		channel:         fixture.channel,
		residentChannel: fixture.channel,
	}
	fixture.service.bindGameSessionCharacter(third, 101)
	fixture.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 101, TownID: 38, AreaID: 1, Session: third})

	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 64, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.partyUDPRelay = relay
	t.Cleanup(func() { _ = relay.Close() })
	return fixture, third
}

func r132RelayLegCount(relay *currentPartyUDPRelay) int {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return len(relay.legs)
}

func TestR132ManagedPartyRebuildDoesNotLeakRelayPorts(t *testing.T) {
	fixture, _ := r132RelayFixture(t)
	state := runtimeRaidState{
		RaidKey: 23<<16 | 99, LeaderID: inviteAcceptLeaderCharacterID,
		Members: []runtimeRaidMemberState{
			{CharID: inviteAcceptLeaderCharacterID, GroupIndex: 1, SlotOrder: 0},
			{CharID: inviteAcceptFollowerCharacterID, GroupIndex: 1, SlotOrder: 1},
		},
	}
	for attempt := 1; attempt <= 40; attempt++ {
		if err := fixture.service.applyRuntimeRaidParties(state); err != nil {
			t.Fatalf("replacement %d failed: %v", attempt, err)
		}
		if legs := r132RelayLegCount(fixture.service.partyUDPRelay); legs != 2 {
			t.Fatalf("replacement %d retained %d relay legs, want 2", attempt, legs)
		}
	}
}

func TestR132ManagedPartyReplacementKeepsThirdOldPartyMember(t *testing.T) {
	fixture, third := r132RelayFixture(t)
	manager := fixture.service.runtimePartyManagerForService()
	state, _, ok := fixture.service.createOrJoinManagedRuntimeParty(fixture.follower, fixture.leader)
	if !ok {
		t.Fatal("initial two-member party failed")
	}
	state, _, ok = fixture.service.createOrJoinManagedRuntimeParty(third, fixture.leader)
	if !ok || len(runtimePartyMembers(state)) != 3 {
		t.Fatalf("three-member party failed: %+v", state)
	}
	if err := fixture.service.sendManagedRuntimePartySnapshots(state); err != nil {
		t.Fatal(err)
	}
	oldID := uint16(0)
	for _, candidate := range manager.Snapshots() {
		if len(candidate.Members) != 3 || candidate.ID != uint16(state.PartyID) {
			continue
		}
		for _, member := range candidate.Members {
			if member.UserID == 101 {
				oldID = candidate.ID
			}
		}
	}
	if oldID == 0 {
		t.Fatalf("three-member manager party %d missing third member", state.PartyID)
	}

	raid := state
	raid.UserID = inviteAcceptLeaderCharacterID
	raid.IsLeader = true
	raid.Members = nil
	for _, member := range state.Members {
		if member.UserID == inviteAcceptLeaderCharacterID || member.UserID == inviteAcceptFollowerCharacterID {
			raid.Members = append(raid.Members, member)
		}
	}
	thirdConn := third.conn.(*r132RelayTestConn)
	thirdConn.write.Reset()
	if _, ok := fixture.service.replaceManagedRuntimeParty(raid); !ok {
		t.Fatal("two-member replacement failed")
	}
	if retired, found := manager.SnapshotByID(oldID); found {
		t.Fatalf("old party %d was not retired after its leader left: %+v", oldID, retired)
	}
	kept, found := manager.SnapshotByUser(101, 0)
	if !found || kept.ID == oldID || len(kept.Members) != 1 || kept.Members[0].UserID != 101 {
		t.Fatalf("old party survivor=%+v found=%t, want member 101 under a replacement party after %d", kept, found, oldID)
	}
	if thirdConn.write.Len() == 0 {
		t.Fatal("remaining old-party member received no roster/leave notification")
	}
}
