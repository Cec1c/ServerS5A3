package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Official24.9 room-slot rows must carry RuntimePartyMember.Slot. A member
// leaving the middle slot creates a hole: the survivor roster stays at 0/2 and
// the new leader's host-slot marker stays 2 instead of being renumbered by row
// count.
func TestOfficial249RoomTableKeepsFixedSlotHoleAfterLeave(t *testing.T) {
	f := newInviteAcceptDungeonFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	f.formPartyThroughClientAccept(t)

	repositories, ok := f.service.repositoryProvider()
	if !ok {
		t.Fatal("repository provider unavailable")
	}
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "101",
		AccountID:   "account-1",
		Level:       20,
		Stats: map[string]int64{
			"fatigue": 100, "town_id": 38, "area_id": 1,
			"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "101",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	third := &gameSession{conn: &bufferConn{}, connID: "invite-accept-third", channel: f.channel, residentChannel: f.channel}
	f.service.bindGameSessionCharacter(third, 101)
	f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 101, TownID: 38, AreaID: 1, Session: third})
	bindDungeonSelectorOriginForTestAt(t, f.service, third, 38, 1, 450, 234)

	manager := f.service.runtimePartyManagerForService()
	leaderSnapshot, found := manager.SnapshotByUser(inviteAcceptLeaderCharacterID, boundGeneration(t, f.service, f.leader))
	if !found {
		t.Fatal("initial party missing")
	}
	joined := manager.Join(leaderSnapshot.ID, party.RuntimePartyMember{
		UserID:            101,
		SessionGeneration: boundGeneration(t, f.service, third),
		State:             alignedcmd.PartyMemberState{UserID: 101, UserState: 1, HPPercent: 100, MPPercent: 100},
	})
	if !joined.OK || runtimePartySlotList(joined.Party) != "0:99,1:100,2:101" {
		t.Fatalf("third join = %+v slots=%s", joined, runtimePartySlotList(joined.Party))
	}
	f.service.publishRuntimePartySnapshot(joined.Party)
	if err := f.service.handleOnlineChangePartyHost(f.leader, []byte{2}); err != nil {
		t.Fatal(err)
	}

	left := manager.Leave(inviteAcceptFollowerCharacterID, boundGeneration(t, f.service, f.follower))
	if !left.OK || left.LeaderMoved {
		t.Fatalf("middle member leave = %+v", left)
	}
	f.service.publishRuntimePartySnapshot(left.Party)
	snapshot, found := manager.SnapshotByUser(101, boundGeneration(t, f.service, third))
	if !found || runtimePartySlotList(snapshot) != "0:99,2:101" {
		t.Fatalf("party after leave = %+v slots=%s", snapshot, runtimePartySlotList(snapshot))
	}

	for _, session := range []*gameSession{f.leader, third} {
		session.conn.(*bufferConn).write.Reset()
	}
	f.selectDungeon(t, third, 700)
	wantIDs := []uint16{inviteAcceptLeaderCharacterID, 101}
	wantSlots := []byte{0, 2}
	total := 0
	for _, session := range []*gameSession{f.leader, third} {
		wire := session.conn.(*bufferConn).write.Bytes()
		for len(wire) > 0 {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			if packet.Header.Classification != 0 || packet.Header.MsgID != 9 || len(packet.Body) < 10 || packet.Body[9] != 0 {
				continue
			}
			ids, slots, hostSlot, err := readPartyRoomOfficial249Record(packet.Body, session.channel.ID)
			if err != nil || !reflect.DeepEqual(ids, wantIDs) || !reflect.DeepEqual(slots, wantSlots) || hostSlot != 2 {
				t.Fatalf("char%d room table ids=%v slots=%v host=%d err=%v want ids=%v slots=%v host=2 body=%x",
					session.selectedCharacterID, ids, slots, hostSlot, err, wantIDs, wantSlots, packet.Body)
			}
			total++
		}
	}
	if total < 2 {
		t.Fatalf("room tables=%d want both remaining members", total)
	}
}
