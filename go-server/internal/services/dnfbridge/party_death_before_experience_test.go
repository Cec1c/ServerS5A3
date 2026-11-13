package dnfbridge

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

// The old order put the recipient's experience transaction ahead of its
// committed death notice. Test real handler output for either party killer.
func TestPartyDeathVisibleBeforeExperienceForEitherKiller(t *testing.T) {
	for _, queued := range []bool{false, true} {
		for _, memberKills := range []bool{false, true} {
			s, leader, member := partyMonsterExperienceFixture(t)
			killer, peer := leader, member
			if memberKills {
				killer, peer = member, leader
			}
			var release func()
			if queued {
				release = holdPartyDeathRecipient(t, s, peer, nil)
			}
			monster := killer.dungeon.runtime.Room.Snapshot().Monsters[0]
			request := make([]byte, dungeoncmd.DieMonsterRequestSize)
			binary.LittleEndian.PutUint32(request, monster.ObjectKey)
			binary.LittleEndian.PutUint16(request[4:], killer.selectedCharacterID)
			if err := s.handleDungeonMonsterDeath(killer, request); err != nil {
				t.Fatal(err)
			}
			if queued {
				release()
				drainPartyDeathRecipient(t, s, peer)
			}
			frames := splitUpperFrames(t, peer.conn.(*bufferConn).write.Bytes())
			death, experience, deathCount := -1, -1, 0
			for i, frame := range frames {
				if frame.msgID == uint16(dnfenum.CmdPacketNotifyDieMonster) {
					death = i
					deathCount++
				}
				if frame.msgID == currentDungeonCharacterStateMsgID && experience < 0 {
					experience = i
				}
			}
			if deathCount != 1 || death < 0 || experience < 0 || death >= experience {
				t.Fatalf("queued=%t memberKills=%t death count=%d frame=%d experience frame=%d", queued, memberKills, deathCount, death, experience)
			}
		}
	}
}
