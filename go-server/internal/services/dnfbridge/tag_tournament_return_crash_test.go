package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/tagtournament"
)

// 1388的现用原生读者会释放双角色holder，不能以写出24代替客户端回城。
// 使用真实op35入口确认，覆盖已完成路由在释放挑战后仍需保留恢复凭据。
func TestTagReturnRosterWaitsForNativeTownPosition(t *testing.T) {
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		for _, opcode := range []uint16{42, 132, 72} {
			for _, secondary := range []bool{false, true} {
				t.Run(fmt.Sprint(mode, opcode, secondary), func(t *testing.T) {
					s, p := tagResultActionFixture(t, mode, false)
					b, runtime := p.tagTournamentBattle, p.dungeon.runtime
					tagPlayMoveNext(t, s, p)
					if secondary {
						target := 1 - b.run.Snapshot().Active
						if err := s.handleCurrentTagTournamentTag(p, []byte{b.nativeRoleIDs[target]}); err != nil {
							t.Fatal(err)
						}
					}
					if opcode == 72 {
						for {
							tagPlayKillBoss(t, s, p)
							if b.run.Snapshot().Phase == tagtournament.Finished {
								break
							}
							tagPlayMoveNext(t, s, p)
						}
					}
					p.conn.(*bufferConn).write.Reset()
					if err := tagSecondaryExit(t, s, p, opcode); err != nil {
						t.Fatal(err)
					}
					counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
					if counts[24] != 1 || counts[1388] != 0 || counts[1389] != 0 || counts[1403] != 0 || p.tagTournamentReturnRoster != b {
						t.Fatal("真实城镇确认前销毁了战斗对象或丢弃分组归属", counts)
					}
					p.conn.(*bufferConn).write.Reset()
					var body [7]byte
					binary.LittleEndian.PutUint16(body[0:2], uint16(runtime.townReturnTransition.PositionX))
					binary.LittleEndian.PutUint16(body[2:4], uint16(runtime.townReturnTransition.PositionY))
					body[4] = runtime.townReturnTransition.Direction
					if err := s.handleTownSetUserPosition(p, body[:]); err != nil {
						t.Fatal(err)
					}
					counts = tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
					if counts[1388] != 1 || counts[24] != 0 || counts[1389] != 0 || counts[1403] != 0 || !b.returnRosterSent || p.tagTournamentReturnRoster != nil || p.dungeon.runtime != nil {
						t.Fatal("真实op35后未完成唯一的队伍恢复", counts)
					}
					p.conn.(*bufferConn).write.Reset()
					if err := s.handleTownSetUserPosition(p, body[:]); err != nil {
						t.Fatal(err)
					}
					if counts = tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes()); counts[1388] != 0 || counts[2] != 0 {
						t.Fatal("重复城镇位置重建角色或分组", counts)
					}
				})
			}
		}
	}
}

func TestTagReturnRosterRetiresWithCharacterOwner(t *testing.T) {
	s, p := tagResultActionFixture(t, tagtournament.Hard, false)
	if err := s.handleDungeonGiveupGame(p, nil); err != nil {
		t.Fatal(err)
	}
	if p.tagTournamentReturnRoster == nil {
		t.Fatal("未保留回城恢复归属")
	}
	s.retireCurrentTagTournamentBattle(p)
	if p.tagTournamentReturnRoster != nil {
		t.Fatal("选角/断线后仍保留旧队伍恢复")
	}
}
