package dnfbridge

import (
	"encoding/binary"
	"errors"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

type r133FailOp31OnceConn struct {
	bufferConn
	failed            bool
	failuresRemaining int
}

func (connection *r133FailOp31OnceConn) Write(data []byte) (int, error) {
	if !connection.failed || connection.failuresRemaining > 0 {
		rest := data
		for len(rest) >= dnfproto.GameServerUpperHeaderSize {
			total := int(binary.LittleEndian.Uint32(rest[3:7]))
			if total < dnfproto.GameServerUpperHeaderSize || total > len(rest) {
				break
			}
			packet, err := dnfproto.ParseChannelPacket(rest[:total])
			if err == nil && packet.Header.MsgID == currentDungeonSettlementEntryMsgID {
				connection.failed = true
				if connection.failuresRemaining > 0 {
					connection.failuresRemaining--
				}
				return 0, errors.New("injected member op31 write failure")
			}
			rest = rest[total:]
		}
	}
	return connection.bufferConn.Write(data)
}

// The leader kills the shared final boss. The member's first bodyless op31
// write fails, but the same peer-dispatch settlement owner must retry and send
// exactly one op31. The reward and runtime-identity barriers remain closed.
func TestR133PartyMemberDispatchResumesFailedOp31(t *testing.T) {
	service, leader, member, leaderRuntime, memberRuntime := partyDungeonDeathFixture(t)
	monster := leaderRuntime.Room.Snapshot().Monsters[0]
	memberConn := &r133FailOp31OnceConn{}
	member.conn = memberConn
	leader.conn.(*bufferConn).write.Reset()

	body := make([]byte, 62)
	binary.LittleEndian.PutUint32(body[0:4], monster.ObjectKey)
	binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(leader.selectedCharacterID))
	if err := service.handleDungeonMonsterDeath(leader, body); err != nil {
		t.Fatalf("leader final-room death: %v", err)
	}
	if !memberConn.failed {
		t.Fatal("fixture did not inject the member op31 write failure")
	}
	frames := splitUpperFrames(t, memberConn.write.Bytes())
	if got := countPartyDeathTestFrames(frames, currentDungeonSettlementEntryMsgID); got != 1 {
		t.Fatalf("member received %d op31 entries after dispatch retry; want one (frames=%+v)", got, frames)
	}
	if !memberRuntime.settlementEntrySent {
		t.Fatal("member runtime did not record the retried op31")
	}
	if memberRuntime.settlementClearRewardSent {
		t.Fatal("op31 retry bypassed the reward barrier")
	}
	if member.dungeon.runtime != memberRuntime {
		t.Fatal("op31 retry replaced the member's dungeon runtime")
	}
}

func TestR133PartyMemberDeathReplayResumesTwoFailedOp31Writes(t *testing.T) {
	service, leader, member, leaderRuntime, memberRuntime := partyDungeonDeathFixture(t)
	monster := leaderRuntime.Room.Snapshot().Monsters[0]
	memberConn := &r133FailOp31OnceConn{failuresRemaining: 2}
	member.conn = memberConn
	body := make([]byte, 62)
	binary.LittleEndian.PutUint32(body[0:4], monster.ObjectKey)
	binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(leader.selectedCharacterID))
	if err := service.handleDungeonMonsterDeath(leader, body); err != nil {
		t.Fatal(err)
	}
	if memberConn.failuresRemaining != 0 || memberRuntime.settlementEntrySent || !memberRuntime.bossDieCheckAccepted {
		t.Fatalf("fixture did not stop after both writes failed: remaining=%d sent=%t accepted=%t",
			memberConn.failuresRemaining, memberRuntime.settlementEntrySent, memberRuntime.bossDieCheckAccepted)
	}
	binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(member.selectedCharacterID))
	for i := 0; i < 2; i++ {
		if err := service.handleDungeonMonsterDeath(member, body); err != nil {
			t.Fatal(err)
		}
	}
	frames := splitUpperFrames(t, memberConn.write.Bytes())
	if got := countPartyDeathTestFrames(frames, currentDungeonSettlementEntryMsgID); got != 1 {
		t.Fatalf("member received %d op31 after authoritative death replay; want one", got)
	}
	if !memberRuntime.settlementEntrySent || memberRuntime.settlementClearRewardSent || member.dungeon.runtime != memberRuntime {
		t.Fatal("replay did not recover entry or changed reward/runtime ownership")
	}
}
