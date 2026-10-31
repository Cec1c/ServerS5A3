package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func TestTagPrimaryReturnLiveFollowup(t *testing.T) {
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		for _, opcode := range []uint16{42, 132, 72} {
			for _, switched := range []bool{false, true} {
				for _, openRoster := range []bool{false, true} {
					t.Run(fmt.Sprint(mode, opcode, switched, openRoster), func(t *testing.T) {
						s, p := tagResultActionFixture(t, mode, false)
						b := p.tagTournamentBattle
						tagPlayMoveNext(t, s, p)
						root := b.run.Snapshot().Active
						if switched {
							if err := s.handleCurrentTagTournamentTag(p, []byte{b.nativeRoleIDs[1-root]}); err != nil || b.run.Snapshot().Active != 1-root {
								t.Fatal("切出入场角色失败", err)
							}
							later := b.now().Add(10 * time.Second)
							b.now = func() time.Time { return later }
							g, _ := s.repositoryGroup()
							owner, err := tagtournament.NewOwnerWithClock(s.tagTournamentCatalog, g, b.now)
							if err != nil {
								t.Fatal(err)
							}
							b.owner = owner
							if err := s.handleCurrentTagTournamentTag(p, []byte{b.nativeRoleIDs[root]}); err != nil {
								t.Fatal(err)
							}
						}
						if b.run.Snapshot().Active != root || b.characters[root].CharacterID != "29" {
							t.Fatal("没有回到真实入场角色")
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
						g, _ := s.repositoryGroup()
						before, _, _ := g.Character.Load(context.Background(), "29")
						mails, rewards := tagBattleMailTotals(t, g)
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
								if !seen24 || !bytes.Equal(packet.Body, want) {
									t.Fatal("入场角色回城后分组顺序或资料错误", packet)
								}
							}
							if packet.Header.MsgID == 1403 {
								t.Fatal("回城主动弹出队伍设置")
							}
						}
						if !seen24 || restored != 0 || p.tagTournamentReturnRoster != b || b.returnRosterSent {
							t.Fatal("回城尚未得到客户端确认就重建了分组，或没有保留恢复凭据", restored)
						}
						p.conn.(*bufferConn).write.Reset()
						if openRoster {
							if err = s.handleCurrentTagTournamentRosterList(p, nil); err != nil {
								t.Fatal(err)
							}
							counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
							if counts[1388] != 1 || counts[1403] != 1 {
								t.Fatal("入场角色回城后队伍框仍为空", counts)
							}
						}
						p.conn.(*bufferConn).write.Reset()
						if err = s.sendEnterSelectDungeonState(p, "入场角色回城现场补充核验", false, true); err != nil {
							t.Fatal(err)
						}
						if !b.returnRosterSent || p.tagTournamentReturnRoster != nil {
							t.Fatal("真实城镇请求后没有完成队伍恢复")
						}
						newBattle := p.tagTournamentBattle
						if newBattle == nil || newBattle == b || !newBattle.prepared.creationSent {
							t.Fatal("入场角色回城后不能免费重新创建")
						}
						if err = s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(newBattle.prepared.runtime.Request)); err != nil || p.dungeon.runtime != newBattle.prepared.runtime {
							t.Fatal("入场角色回城后再次进图失败", err)
						}
						after, _, _ := g.Character.Load(context.Background(), "29")
						actualMails, actualRewards := tagBattleMailTotals(t, g)
						if before.Stats["gold"] != after.Stats["gold"] || mails != actualMails || !reflect.DeepEqual(rewards, actualRewards) {
							t.Fatal("回城重入重复扣费或发奖")
						}
					})
				}
			}
		}
	}
}

func TestTagFirstSelectCannotConfirmUnacceptedReturn(t *testing.T) {
	for _, kind := range []string{"战斗中", "24未写出", "恢复归属缺失", "非休息室", "失效会话", "普通频道", "未认证"} {
		t.Run(kind, func(t *testing.T) {
			s, p := tagResultActionFixture(t, tagtournament.Hard, false)
			b, runtime := p.tagTournamentBattle, p.dungeon.runtime
			if kind != "战斗中" {
				if err := s.handleDungeonGiveupGame(p, nil); err != nil {
					t.Fatal(err)
				}
			}
			p.backToVillageEnterSelectPending = true
			switch kind {
			case "战斗中":
				b.returnRosterSent = true
			case "24未写出":
				runtime.townReturnOp24Sent = false
			case "恢复归属缺失":
				p.tagTournamentReturnRoster = nil
			case "非休息室":
				runtime.townReturnTransition.TownID = 1
			case "失效会话":
				b.identity.generation++
			case "普通频道":
				p.channel.Type = 0
			case "未认证":
				p.accountFromToken = false
			}
			p.conn.(*bufferConn).write.Reset()
			if err := s.confirmCurrentTagTournamentEnterSelectReturn(p); err != nil {
				t.Fatal(err)
			}
			if p.dungeon.runtime != runtime || p.tagTournamentBattle != b || !p.backToVillageEnterSelectPending || p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("无有效帝国离场证据的15确认了回城")
			}
		})
	}
}
