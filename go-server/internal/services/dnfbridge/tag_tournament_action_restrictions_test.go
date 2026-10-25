package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func TestTagActionItemsCannotBypassMatchRestrictionOnEitherTransport(t *testing.T) {
	for _, transport := range []string{"legacy", "upper"} {
		for _, stage := range []string{"准备", "战斗", "回合间", "王房选择", "终局未离场"} {
			t.Run(transport+stage, func(t *testing.T) {
				s, p, g := tagPlayFixture(t)
				tagPlayChallenge(t, s, p, tagtournament.Normal)
				tagRewardPopupAdvanceClock(t, s, p)
				if stage != "准备" {
					tagPlayMoveNext(t, s, p)
				}
				if stage == "回合间" || stage == "王房选择" || stage == "终局未离场" {
					tagPlayKillBoss(t, s, p)
				}
				if stage == "王房选择" || stage == "终局未离场" {
					for round := 1; round < 4; round++ {
						tagPlayMoveNext(t, s, p)
						tagPlayKillBoss(t, s, p)
					}
				}
				if stage == "终局未离场" {
					tagPlayMoveNext(t, s, p)
					tagPlayKillBoss(t, s, p)
				}
				root, _, _ := g.Inventory.Load(context.Background(), "29")
				backup, _, _ := g.Inventory.Load(context.Background(), "60")
				character, _, _ := g.Character.Load(context.Background(), "29")
				before := p.tagTournamentBattle.run.Snapshot()
				for _, action := range []uint32{99, 118, 164, 169, 179} {
					body := make([]byte, 19)
					binary.LittleEndian.PutUint16(body, 87)
					binary.LittleEndian.PutUint32(body[7:], action)
					p.conn.(*bufferConn).write.Reset()
					var handled bool
					var err error
					if transport == "legacy" {
						handled, err = currentGameplayModules.DispatchLegacy(s, p, 515, body)
					} else {
						handled, err = currentGameplayModules.DispatchUpper(s, p, 515, 1, body)
					}
					if !handled || err != nil {
						t.Fatal(action, handled, err)
					}
					packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
					if packet.Header.MsgID != 515 || !bytes.Equal(packet.Body, []byte{0, 1, 87, 0, 0, byte(action)}) || len(rest) != 0 {
						t.Fatal("动作道具没有唯一515失败应答", action, packet, rest)
					}
				}
				for _, old := range []struct {
					id  string
					bag any
				}{{"29", root}, {"60", backup}} {
					actual, _, _ := g.Inventory.Load(context.Background(), old.id)
					if !reflect.DeepEqual(old.bag, actual) {
						t.Fatal("比赛拒绝仍消费或改变角色背包", old.id)
					}
				}
				actual, _, _ := g.Character.Load(context.Background(), "29")
				if !reflect.DeepEqual(character, actual) || !reflect.DeepEqual(before, p.tagTournamentBattle.run.Snapshot()) {
					t.Fatal("拒绝动作道具仍改变角色或帝国战斗")
				}
			})
		}
	}
}

func TestTagActionItemRestrictionKeepsLobbyAndOrdinaryChannelRouting(t *testing.T) {
	for _, other := range []string{"休息室", "普通频道"} {
		t.Run(other, func(t *testing.T) {
			s, p, _ := tagPlayFixture(t)
			if other == "普通频道" {
				tagPlayChallenge(t, s, p, tagtournament.Normal)
				p.channel.Type = 11
			}
			p.conn.(*bufferConn).write.Reset()
			handled, err := s.rejectCurrentTagTournamentMutation(p, gameplayRequest{Opcode: 515, Classification: 1, Body: make([]byte, 19)})
			if handled || err != nil || p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("非比赛动作路由受到帝国限制", handled, err)
			}
		})
	}
}

func TestTagActionItemRestrictionRejectsShortPayloadWithoutEnteringHandler(t *testing.T) {
	s, p, _ := tagPlayFixture(t)
	tagPlayChallenge(t, s, p, tagtournament.Normal)
	for _, size := range []int{0, 2, 3, 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			p.conn.(*bufferConn).write.Reset()
			handled, err := currentGameplayModules.DispatchUpper(s, p, 515, 1, make([]byte, size))
			if !handled || err != nil || p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("短正文未在比赛限制处拒绝", handled, err)
			}
		})
	}
}
