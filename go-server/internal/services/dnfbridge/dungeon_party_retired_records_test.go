package dnfbridge

import (
	"encoding/binary"
	"testing"
)

func TestDungeonDisbandClearsEveryRetiredRecordForBothReceivers(t *testing.T) {
	f := newLeaderExitFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	f.leader.townActorOwnerChannel = 10
	f.member.townActorOwnerChannel = 10
	// The leader has committed its town return; the other member stays in the
	// original instance. Both clients can still own both dungeon party records.
	f.leader.dungeon.runtime = nil
	if got := f.service.disbandRuntimePartyOnDungeonLeaderExit(f.leader, f.instance, 99, "retired_record_test"); got != 1 {
		t.Fatalf("peer removal count=%d", got)
	}
	for name, conn := range map[string]*bufferConn{"leader": f.leaderConn, "member": f.memberConn} {
		seen := map[uint16]int{}
		for _, frame := range splitUpperFrames(t, conn.write.Bytes()) {
			if frame.msgID == 2 {
				t.Fatalf("%s received an appearance redraw during disband", name)
			}
			if frame.msgID == 9 && len(frame.body) == 10 && frame.body[9] == 3 && frame.body[6] == 0 && frame.body[7] == 0 {
				seen[binary.LittleEndian.Uint16(frame.body[4:6])]++
			}
		}
		for _, key := range []uint16{99, 100} {
			if seen[key] != 1 {
				t.Errorf("%s retired record key=%d clear count=%d; want exactly one", name, key, seen[key])
			}
		}
	}
	if f.member.dungeon.runtime != f.memberRuntime || f.memberRuntime.partyInstance != f.instance {
		t.Fatal("survivor lost its current dungeon instance")
	}
	for _, id := range []uint16{99, 100} {
		if _, ok := f.service.runtimePartyManagerForService().SnapshotByUser(id, 0); ok {
			t.Fatalf("member %d retained authoritative party", id)
		}
	}
}

func TestDungeonDisbandRetiredRecordsKeepOtherProfileWire(t *testing.T) {
	f := newLeaderExitFixture(t)
	f.service.options.currentClientVersion = "legacy"
	f.leader.townActorOwnerChannel = 10
	f.member.townActorOwnerChannel = 10
	f.leader.dungeon.runtime = nil
	f.service.disbandRuntimePartyOnDungeonLeaderExit(f.leader, f.instance, 99, "legacy_retired_record_test")
	for name, conn := range map[string]*bufferConn{"leader": f.leaderConn, "member": f.memberConn} {
		count := 0
		for _, frame := range splitUpperFrames(t, conn.write.Bytes()) {
			if frame.msgID == 9 && len(frame.body) == 10 && frame.body[7] == 0 {
				count++
			}
		}
		want := 0
		if name == "member" {
			want = 1
		}
		if count != want {
			t.Fatalf("%s changed legacy scene-context removals: %d want %d", name, count, want)
		}
	}
}
