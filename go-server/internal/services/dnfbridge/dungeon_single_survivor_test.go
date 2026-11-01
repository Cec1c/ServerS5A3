package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"testing"
)

func assertDungeonSurvivorRetainsParty(t *testing.T, survivor *gameSession, departing uint16) {
	t.Helper()
	local, departure, restored, removed, hp := -1, -1, -1, -1, -1
	for i, frame := range splitUpperFrames(t, survivor.conn.(*bufferConn).write.Bytes()) {
		switch frame.msgID {
		case 2, 11:
			t.Fatal("survivor actor or network was rebuilt")
		case 9:
			if len(frame.body) < 10 || frame.body[9] == 3 {
				t.Fatal("surviving party received disband record")
			}
			id := binary.LittleEndian.Uint16(frame.body[4:6])
			switch frame.body[9] {
			case 0:
				rows := departureRowsInSelectedChannel(t, frame.body, currentClientSelectedChannelID(survivor))
				if id != survivor.selectedCharacterID || len(rows) != 1 || rows[0].uid != survivor.selectedCharacterID {
					t.Fatal("surviving singleton roster lost")
				}
				if local < 0 {
					local = i
				}
				restored = i
			case 2:
				if id != departing || len(frame.body) != 15 || frame.body[10] != 0 {
					t.Fatal("wrong departing record")
				}
				departure = i
			}
		case currentTownUserLeaveMsgID:
			if binary.LittleEndian.Uint16(frame.body[len(frame.body)-2:]) != departing {
				t.Fatal("wrong active actor removal")
			}
			removed = i
		case 153:
			if len(frame.body) <= 1 || frame.body[0] != 1 {
				t.Fatal("surviving party realtime roster was cleared")
			}
			hp = i
		}
	}
	if local < 0 || departure <= local || restored <= departure || removed <= restored || hp <= removed {
		t.Fatalf("invalid departure order: roster=%d departing=%d restored=%d actor=%d realtime=%d", local, departure, restored, removed, hp)
	}
}

func TestDungeonLastRemoteDepartureKeepsSurvivorPartyAndInstance(t *testing.T) {
	for _, departing := range []uint16{99, 100} {
		t.Run(fmt.Sprint(departing), func(t *testing.T) {
			f := newLeaderExitFixture(t)
			f.service.options.currentClientVersion = "official-24.9"
			f.leader.channel.ID, f.member.channel.ID = 11, 11
			f.leader.townActorOwnerChannel, f.member.townActorOwnerChannel = 0, 0
			manager := f.service.runtimePartyManagerForService()
			before, _ := manager.SnapshotByUser(99, 0)
			leaver, survivor, retained := f.member, f.leader, f.leaderRuntime
			if departing == 99 {
				leaver, survivor, retained = f.leader, f.member, f.memberRuntime
			}
			leaver.dungeon.runtime = nil
			if err := f.service.leaveRuntimePartyOnMemberGiveup(leaver, f.instance); err != nil {
				t.Fatal(err)
			}
			assertDungeonSurvivorRetainsParty(t, survivor, departing)
			if survivor.dungeon.runtime != retained || retained.partyInstance != f.instance {
				t.Fatal("survivor instance changed")
			}
			after, found := manager.SnapshotByUser(survivor.selectedCharacterID, 0)
			if !found || after.ID != before.ID || len(after.Members) != 1 || after.Leader != survivor.selectedCharacterID {
				t.Fatal("server instance party identity lost")
			}
			for _, old := range before.Members {
				if old.UserID == survivor.selectedCharacterID && after.Members[0].Slot != old.Slot {
					t.Fatal("survivor slot moved")
				}
			}
			survivor.conn.(*bufferConn).write.Reset()
			if err := f.service.leaveRuntimePartyOnMemberGiveup(leaver, f.instance); err != nil {
				t.Fatal(err)
			}
			if survivor.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("duplicate departure recreated projection")
			}
		})
	}
}
