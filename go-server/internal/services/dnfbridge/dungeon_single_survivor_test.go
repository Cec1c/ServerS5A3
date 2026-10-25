package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"testing"
)

func TestDungeonLastRemoteDepartureClearsHUDKeepsSurvivorInstance(t *testing.T) {
	for _, departing := range []uint16{99, 100} {
		t.Run(fmt.Sprint(departing), func(t *testing.T) {
			f := newLeaderExitFixture(t)
			f.service.options.currentClientVersion = "official-24.9"
			manager := f.service.runtimePartyManagerForService()
			before, _ := manager.SnapshotByUser(99, 0)
			round69AlignInstancePartyID(t, f, before.ID)
			leaver, survivor, retained := f.member, f.leader, f.leaderRuntime
			if departing == 99 {
				// Keep the survivor in its real slot 1, as in the reported case.
				result := manager.TransferLeader(99, f.leader.characterGeneration, 100, f.member.characterGeneration)
				if !result.OK {
					t.Fatal(result.Reason)
				}
				f.service.publishRuntimePartySnapshot(result.Party)
				leaver, survivor, retained = f.leader, f.member, f.memberRuntime
			}
			leaver.dungeon.runtime = nil
			if err := f.service.leaveRuntimePartyOnMemberGiveup(leaver, f.instance); err != nil {
				t.Fatal(err)
			}
			frames := splitUpperFrames(t, survivor.conn.(*bufferConn).write.Bytes())
			var keys []uint16
			removed, hp := false, false
			for _, frame := range frames {
				switch frame.msgID {
				case 2, 11:
					t.Fatal("survivor actor or network was rebuilt")
				case 9:
					if len(frame.body) != 10 || frame.body[9] != 3 || removed {
						t.Fatal("survivor HUD redeclared or record retired after actor")
					}
					keys = append(keys, binary.LittleEndian.Uint16(frame.body[4:6]))
				case currentTownUserLeaveMsgID:
					if len(keys) != 2 || binary.LittleEndian.Uint16(frame.body[len(frame.body)-2:]) != departing {
						t.Fatal("wrong active actor removal")
					}
					removed = true
				case 153:
					if !removed || len(frame.body) != 1 || frame.body[0] != 0 {
						t.Fatal("single survivor received nonempty HP roster")
					}
					hp = true
				}
			}
			if len(keys) != 2 || keys[0] != departing || keys[1] != survivor.selectedCharacterID || !removed || !hp {
				t.Fatalf("incomplete retirement keys=%v remove=%v hp=%v", keys, removed, hp)
			}
			if survivor.dungeon.runtime != retained || retained.partyInstance != f.instance {
				t.Fatal("survivor instance changed")
			}
			after, found := manager.SnapshotByUser(survivor.selectedCharacterID, 0)
			if !found || after.ID != before.ID || len(after.Members) != 1 {
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
