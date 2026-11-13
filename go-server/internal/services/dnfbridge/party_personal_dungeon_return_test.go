package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func TestPartyDungeonLeaveRequestReturnsOnlyTheLeavingMemberToTown(t *testing.T) {
	for _, opcode := range []uint16{13, 42} {
		for _, leavingID := range []uint16{99, 100} {
			t.Run(strconv.Itoa(int(opcode))+"/"+strconv.Itoa(int(leavingID)), func(t *testing.T) {
				f := newLeaderExitFixture(t)
				f.service.options.currentClientVersion = "official-24.9"
				leaving, staying := f.member, f.leader
				if leavingID == 99 {
					leaving, staying = f.leader, f.member
				}
				repositories, _ := f.service.repositoryGroup()
				c, _, _ := repositories.Character.Load(context.Background(), strconv.Itoa(int(leavingID)))
				for key, value := range map[string]int64{"town_id": 7, "area_id": 3, "pos_x": 474, "pos_y": 234, "direction": 5, "area_state": 3} {
					c.Stats[key] = value
				}
				if err := repositories.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
				leaving.dungeon.runtime.Character = c
				enableTownMoveSkillProjection(t, f.service, repositories, c.CharacterID)
				bindDungeonSelectorOriginForTestAt(t, f.service, leaving, 7, 3, 474, 234)
				if err := freezeCurrentDungeonTownReturnOrigin(leaving, leaving.dungeon.runtime); err != nil {
					t.Fatal(err)
				}
				// The live failure occurs after ordinary room navigation changes seed.
				advanced := f.instance.key
				advanced.Seed += 37
				advanced.RoomX++
				advanced.MapID++
				leaving.dungeonPartyRun, leaving.dungeonPartyRunValid = advanced, true
				before, _ := f.service.runtimePartyManagerForService().SnapshotByUser(leavingID, 0)
				stayingRuntime := staying.dungeon.runtime
				var err error
				if opcode == 13 {
					err = f.service.handleOnlineLeaveParty(leaving, uint16(dnfenum.CmdPacketLeaveParty))
				} else {
					err = f.service.handleDungeonGiveupGame(leaving, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				if leaving.dungeon.runtime == nil || !leaving.dungeon.runtime.townReturnPending || !leaving.dungeon.runtime.townReturnOp24Sent {
					t.Fatal("leave request did not initiate the leaving member's personal town transition")
				}
				if err := f.service.commitPendingDungeonReturnForSceneRequest(leaving, "test_personal_party_leave_town_confirmed"); err != nil {
					t.Fatal(err)
				}
				if leaving.dungeon.runtime != nil {
					t.Fatal("returned member retained dungeon runtime")
				}
				assertPartyProjectionEmpty(t, "returned member", leaving)
				assertDepartureRetiresSelfFirst(t, leaving)

				if _, found := f.service.runtimePartyManagerForService().SnapshotByUser(leavingID, 0); found {
					t.Fatal("returned member retained central party membership")
				}
				after, found := f.service.runtimePartyManagerForService().SnapshotByUser(staying.selectedCharacterID, 0)
				if !found || after.ID != before.ID || after.Leader != staying.selectedCharacterID || len(after.Members) != 1 {
					t.Fatalf("remaining party was disbanded or replaced: %+v", after)
				}
				if staying.dungeon.runtime != stayingRuntime || stayingRuntime.partyInstance != f.instance {
					t.Fatal("remaining member was removed from its dungeon instance")
				}
			})
		}
	}
}

func TestPartyLeaveAfterClearRetainsDeferredExitOwner(t *testing.T) {
	s, runtime, session, conn := cardTownExitFixture(t, 0)
	s.options.currentClientVersion = "official-24.9"
	if err := s.handleCurrentDungeonTownExitRequest(session, nil, 13); err != nil {
		t.Fatal(err)
	}
	if runtime.settlementTownReturnRequestMsgID != 13 || conn.write.Len() != 0 {
		t.Fatal("party leave bypassed card barrier or lost request owner")
	}
	for _, opcode := range []uint16{69, 70} {
		if err := cardTownExitRequest(t, s, session, opcode); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.townReturnRequestMsgID != 13 || session.dungeon.runtime != nil {
		t.Fatal("deferred party leave did not finish personal return")
	}
}

func TestPartyKickInsideDungeonReturnsOnlyTarget(t *testing.T) {
	f := newLeaderExitFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	repositories, _ := f.service.repositoryGroup()
	c, _, _ := repositories.Character.Load(context.Background(), "100")
	for key, value := range map[string]int64{"town_id": 7, "area_id": 3, "pos_x": 474, "pos_y": 234, "direction": 5, "area_state": 3} {
		c.Stats[key] = value
	}
	if err := repositories.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	f.memberRuntime.Character = c
	enableTownMoveSkillProjection(t, f.service, repositories, c.CharacterID)
	bindDungeonSelectorOriginForTestAt(t, f.service, f.member, 7, 3, 474, 234)
	if err := freezeCurrentDungeonTownReturnOrigin(f.member, f.memberRuntime); err != nil {
		t.Fatal(err)
	}
	if err := f.service.handleOnlineWalkoutPartyMember(f.leader, uint16(dnfenum.CmdPacketWalkoutPartyMember), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if !f.memberRuntime.townReturnPending || !f.memberRuntime.townReturnOp24Sent {
		t.Fatal("kick did not initiate target's personal return")
	}
	for _, frame := range splitUpperFrames(t, f.memberConn.write.Bytes()) {
		if frame.msgID == 9 && len(frame.body) >= 10 && frame.body[9] == 3 {
			t.Fatal("kick sent target a disband record")
		}
	}
	assertDungeonSurvivorRetainsParty(t, f.leader, 100)
	if err := f.service.commitPendingDungeonReturnForSceneRequest(f.member, "test_kick_town_confirmed"); err != nil {
		t.Fatal(err)
	}
	if f.member.dungeon.runtime != nil || f.leader.dungeon.runtime != f.leaderRuntime || f.leaderRuntime.partyInstance != f.instance {
		t.Fatal("kick disturbed remaining dungeon owner")
	}
	assertPartyProjectionEmpty(t, "kicked member", f.member)
}

// NOTI9's native slot removal also clears the local actor's party pointer when
// removing a peer record. Self must be retired before any peer record or the
// client loses its before/after transition and leaves the party HUD displayed.
func assertDepartureRetiresSelfFirst(t *testing.T, leaving *gameSession) {
	t.Helper()
	assertDepartureStreamRetiresSelfFirst(t, leaving, leaving.conn.(*bufferConn).write.Bytes())
}

func assertDepartureStreamRetiresSelfFirst(t *testing.T, leaving *gameSession, stream []byte) {
	t.Helper()
	var owners []byte
	peerSeen := false
	for _, frame := range splitUpperFrames(t, stream) {
		if frame.msgID != 9 || len(frame.body) != 15 || frame.body[9] != 2 || frame.body[10] != 0 {
			continue
		}
		id := binary.LittleEndian.Uint16(frame.body[4:6])
		if id == currentSceneActorObjectKey(leaving.selectedCharacterID) {
			if peerSeen {
				t.Fatal("self departure followed peer cleanup; native HUD teardown is lost")
			}
			owners = append(owners, frame.body[7])
		} else {
			if len(owners) == 0 {
				t.Fatal("peer cleanup preceded self departure")
			}
			peerSeen = true
		}
	}
	if len(owners) == 0 || owners[0] != currentClientSelectedChannel(leaving) {
		t.Fatalf("self retirement did not start in selected channel: owners=%v", owners)
	}
	if !peerSeen {
		t.Fatal("departure did not retire former peer records")
	}
}
