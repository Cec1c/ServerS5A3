package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/party"
)

func TestP32FiendWarRetirementClearsLocalRecordLast(t *testing.T) {
	for _, count := range []int{2, 3, 4} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := newLeaderExitFixture(t)
			f.service.options.currentClientVersion = "official-24.9"
			peers := []*gameSession{f.leader, f.member}
			for id := uint16(101); int(id) < 99+count; id++ {
				peers = append(peers, addRetirementPeer(t, f, id))
			}
			before, _ := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0)
			frozenOrder := append([]party.RuntimePartyMember(nil), before.Members...)
			f.service.retireCurrentFiendWarPartySnapshot(f.member, before, "p32-return")
			if !reflect.DeepEqual(before.Members, frozenOrder) {
				t.Fatal("publication reordered the frozen domain snapshot")
			}
			for _, peer := range peers {
				var order []uint16
				for _, frame := range splitUpperFrames(t, peer.conn.(*bufferConn).write.Bytes()) {
					if frame.msgID == 9 && len(frame.body) == 10 && frame.body[9] == 3 && frame.body[7] == 0 {
						order = append(order, binary.LittleEndian.Uint16(frame.body[4:6]))
					}
				}
				// The final town projection may use owner0 in this fixture too;
				// the frozen dungeon record clear is the first complete cohort.
				if len(order) < count || order[count-1] != peer.selectedCharacterID {
					t.Fatalf("receiver %d order=%v: own dungeon record must be last", peer.selectedCharacterID, order)
				}
				if _, exists := f.service.runtimePartyManagerForService().SnapshotByUser(peer.selectedCharacterID, 0); exists {
					t.Fatal("temporary party survived confirmed retirement")
				}
				peer.conn.(*bufferConn).write.Reset()
			}
			if f.member.dungeon.runtime != f.memberRuntime || f.leader.dungeon.runtime != f.leaderRuntime {
				t.Fatal("retirement changed a survivor dungeon runtime")
			}
			f.service.retireCurrentFiendWarPartySnapshot(f.member, before, "p32-repeat")
			for _, peer := range peers {
				if peer.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("repeated retirement published twice")
				}
			}
		})
	}
}
