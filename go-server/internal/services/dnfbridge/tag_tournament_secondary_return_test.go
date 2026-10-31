package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func tagSecondaryReturnFixture(t *testing.T, mode tagtournament.Mode, reverse bool) (*Service, *gameSession, repo.Group) {
	t.Helper()
	s, p, g := tagPlayFixture(t)
	enableTownMoveSkillProjection(t, s, g, "29")
	ch, _, err := g.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	ch.Stats["direction"], ch.Stats["area_state"] = 0, 0
	if err = g.Character.Save(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	if reverse {
		o, err := s.currentTagTournamentOwner()
		if err != nil {
			t.Fatal(err)
		}
		if err = o.RegisterRoster(context.Background(), p.accountID, "29", [2]string{"60", "29"}); err != nil {
			t.Fatal(err)
		}
	}
	tagPlayChallenge(t, s, p, mode)
	tagPlayMoveNext(t, s, p)
	b := p.tagTournamentBattle
	target := 1 - b.run.Snapshot().Active
	if err = s.handleCurrentTagTournamentTag(p, []byte{b.nativeRoleIDs[target]}); err != nil || b.run.Snapshot().Active != target || b.characters[target].CharacterID == "29" {
		t.Fatal("未实际切到备用角色", err)
	}
	return s, p, g
}

func tagSecondaryExit(t *testing.T, s *Service, p *gameSession, opcode uint16) error {
	t.Helper()
	switch opcode {
	case 42:
		return s.handleDungeonGiveupGame(p, nil)
	case 132:
		return s.handleDungeonBackToVillage(p, nil)
	case 72:
		return s.handleDungeonEplpCommand(p, []byte{1, 2})
	}
	t.Fatal("未知测试离场请求", opcode)
	return nil
}

func TestTagSecondaryReturnRestoresSavedRosterAndCanReenter(t *testing.T) {
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		for _, opcode := range []uint16{42, 132, 72} {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprint(mode, opcode, reverse), func(t *testing.T) {
					s, p, g := tagSecondaryReturnFixture(t, mode, reverse)
					b, runtime := p.tagTournamentBattle, p.dungeon.runtime
					if opcode == 72 {
						for {
							tagPlayKillBoss(t, s, p)
							if b.run.Snapshot().Phase == tagtournament.Finished {
								break
							}
							tagPlayMoveNext(t, s, p)
						}
					}
					before, _, _ := g.Character.Load(context.Background(), "29")
					other, _, _ := g.Character.Load(context.Background(), "60")
					mailCount, rewards := tagBattleMailTotals(t, g)
					rows, server, err := s.currentTagTournamentSentRoster(p)
					if err != nil {
						t.Fatal(err)
					}
					view, err := b.owner.Lobby(context.Background(), p.accountID)
					if err != nil {
						t.Fatal(err)
					}
					want, err := buildCurrentTagTournamentSavedRosterBody(view.Characters, rows, server)
					if err != nil {
						t.Fatal(err)
					}
					p.conn.(*bufferConn).write.Reset()
					if err = tagSecondaryExit(t, s, p, opcode); err != nil {
						t.Fatal(err)
					}
					seen24, restored := false, 0
					for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
						packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
						rest = tail
						if packet.Header.MsgID == 24 {
							seen24 = true
						}
						if packet.Header.MsgID == 1388 {
							restored++
							if !seen24 || packet.Header.Classification != 0 || !bytes.Equal(packet.Body, want) {
								t.Fatal("回城分组顺序、区服或序号不符", packet)
							}
						}
						if packet.Header.MsgID == 1403 || packet.Header.MsgID == 1389 {
							t.Fatal("静默回城打开了队伍框或重建战斗角色", packet)
						}
					}
					if !seen24 || restored != 0 || b.returnRosterSent || p.tagTournamentReturnRoster != b || p.selectedCharacterID != 29 {
						t.Fatal("备用角色回城未确认就重建分组或污染登录身份")
					}
					// 先收到真实1467，原角色/HUD恢复后才允许1388；没有补35/36/1345。
					p.conn.(*bufferConn).write.Reset()
					if err = s.handleCurrentTagTournamentRosterList(p, nil); err != nil {
						t.Fatal(err)
					}
					counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
					if counts[1388] != 1 || counts[1403] != 1 || !b.returnRosterSent || p.tagTournamentReturnRoster != nil {
						t.Fatal("真实队伍框请求后未恢复分组", counts)
					}
					p.conn.(*bufferConn).write.Reset()
					if err = s.handleCurrentTagTournamentSetRoster(p, want); err != nil {
						t.Fatal(err)
					}
					ack := 0
					for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
						packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
						rest = tail
						if packet.Header.MsgID == 1457 {
							ack++
							if !bytes.Equal(packet.Body, append([]byte{1}, want...)) {
								t.Fatal("回城自动保存被跨区提示拒绝", packet)
							}
						}
						if packet.Header.MsgID == 1388 {
							t.Fatal("ACK重复推送1388，可能形成自动回写循环")
						}
					}
					if ack != 1 || p.dungeon.runtime != nil || p.tagTournamentBattle != nil || !currentTagTournamentLobbyInteractionReady(p) {
						t.Fatal("回城确认后仍阻断休息室操作")
					}
					p.conn.(*bufferConn).write.Reset()
					if err = s.handleCurrentTagTournamentRosterList(p, nil); err != nil {
						t.Fatal(err)
					}
					counts = tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
					if counts[1388] != 1 || counts[1403] != 1 || counts[2] != 0 {
						t.Fatal("回城后队伍框资料空白或重建角色", counts)
					}
					p.conn.(*bufferConn).write.Reset()
					if err = s.sendEnterSelectDungeonState(p, "帝国备用角色回城重入", false, true); err != nil {
						t.Fatal(err)
					}
					newBattle := p.tagTournamentBattle
					if newBattle == nil || newBattle == b || !newBattle.prepared.creationSent {
						t.Fatal("回城后无法免费重新准备挑战")
					}
					if err = s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(runtime.Request)); err != nil || p.dungeon.runtime != newBattle.prepared.runtime {
						t.Fatal("实际重入地图失败", err)
					}
					tagPlayMoveNext(t, s, p)
					target := 1 - newBattle.run.Snapshot().Active
					if err = s.handleCurrentTagTournamentTag(p, []byte{newBattle.nativeRoleIDs[target]}); err != nil || newBattle.run.Snapshot().Active != target {
						t.Fatal("再次入场后切换无效", err)
					}
					after, _, _ := g.Character.Load(context.Background(), "29")
					actualOther, _, _ := g.Character.Load(context.Background(), "60")
					actualMails, actualRewards := tagBattleMailTotals(t, g)
					if before.Stats["gold"] != after.Stats["gold"] || !reflect.DeepEqual(other, actualOther) || actualMails != mailCount || !reflect.DeepEqual(rewards, actualRewards) {
						t.Fatal("回城/重入重复扣费、发奖或污染备用角色")
					}
				})
			}
		}
	}
}

func TestTagReturnRosterWriteFailureKeepsReturnForRetry(t *testing.T) {
	for _, opcode := range []uint16{42, 132, 72} {
		t.Run(fmt.Sprint(opcode), func(t *testing.T) {
			s, p, _ := tagSecondaryReturnFixture(t, tagtournament.Hard, false)
			b, runtime := p.tagTournamentBattle, p.dungeon.runtime
			if opcode == 72 {
				for {
					tagPlayKillBoss(t, s, p)
					if b.run.Snapshot().Phase == tagtournament.Finished {
						break
					}
					tagPlayMoveNext(t, s, p)
				}
			}
			failure := errors.New("隔离回城分组写失败")
			p.conn = &tagReturnRosterFailConn{bufferConn: &bufferConn{}, err: failure}
			if err := tagSecondaryExit(t, s, p, opcode); err != nil {
				t.Fatal("尚未确认回城就发送了1388", err)
			}
			if b.returnRosterSent || p.tagTournamentReturnRoster != b || !runtime.townReturnOp24Sent {
				t.Fatal("没有保留延后恢复归属")
			}
			if err := s.handleCurrentTagTournamentRosterList(p, nil); !errors.Is(err, failure) {
				t.Fatal("未返回确认回城后的1388失败", err)
			}
			if b.returnRosterSent || p.dungeon.runtime != nil || p.tagTournamentReturnRoster != b {
				t.Fatal("失败后丢弃了可重试的分组恢复")
			}
			p.conn = &bufferConn{}
			if err := s.handleCurrentTagTournamentRosterList(p, nil); err != nil {
				t.Fatal(err)
			}
			counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
			if !b.returnRosterSent || counts[1388] != 1 || counts[24] != 0 || counts[1403] != 1 || p.tagTournamentReturnRoster != nil {
				t.Fatal("重试没有只补分组资料", counts)
			}
		})
	}
}

func TestTagInvalidRosterCannotConfirmSecondaryReturn(t *testing.T) {
	for _, kind := range []string{"错误区服", "越界序号", "重复队员", "非空队伍框请求", "仍在战斗"} {
		t.Run(kind, func(t *testing.T) {
			s, p, g := tagSecondaryReturnFixture(t, tagtournament.Hard, false)
			b, runtime := p.tagTournamentBattle, p.dungeon.runtime
			if kind != "仍在战斗" {
				if err := s.handleDungeonGiveupGame(p, nil); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := g.Account.Load(context.Background(), p.accountID)
			rows, server, err := s.currentTagTournamentSentRoster(p)
			if err != nil {
				t.Fatal(err)
			}
			view, err := b.owner.Lobby(context.Background(), p.accountID)
			if err != nil {
				t.Fatal(err)
			}
			body, err := buildCurrentTagTournamentSavedRosterBody(view.Characters, rows, server)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "错误区服":
				body[0]++
			case "越界序号":
				body[1] = 255
			case "重复队员":
				body[3] = body[1]
			}
			p.conn.(*bufferConn).write.Reset()
			if kind == "非空队伍框请求" {
				err = s.handleCurrentTagTournamentRosterList(p, []byte{1})
			} else {
				err = s.handleCurrentTagTournamentSetRoster(p, body)
			}
			if err != nil {
				t.Fatal(err)
			}
			after, _, _ := g.Account.Load(context.Background(), p.accountID)
			if p.dungeon.runtime != runtime || p.tagTournamentBattle != b || !reflect.DeepEqual(before, after) {
				t.Fatal("无效请求确认离场或改变了账号状态")
			}
		})
	}
}

type tagReturnRosterFailConn struct {
	*bufferConn
	err error
}

func (c *tagReturnRosterFailConn) Write(data []byte) (int, error) {
	// 本机upper头的消息号为小端u16、位于偏移1；拒绝整个1388帧。
	if len(data) >= 3 && data[1] == byte(1388&255) && data[2] == byte(1388>>8) {
		return 0, c.err
	}
	return c.bufferConn.Write(data)
}
