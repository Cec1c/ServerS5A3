package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/tagtournament"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestTagAbandonRosterRequestConfirmsLobbyWithoutPositionCallback(t *testing.T) {
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		for _, opcode := range []uint16{42, 132} {
			for _, cleared := range []bool{false, true} {
				t.Run(fmt.Sprint(mode, opcode, cleared), func(t *testing.T) {
					s, p := tagResultActionFixture(t, mode, false)
					b := p.tagTournamentBattle
					runtime := p.dungeon.runtime
					tagPlayMoveNext(t, s, p)
					if cleared {
						tagPlayKillBoss(t, s, p)
					}
					repos, _ := s.repositoryGroup()
					before, _, _ := repos.Character.Load(context.Background(), "29")
					mails, totals := tagBattleMailTotals(t, repos)
					var err error
					if opcode == 42 {
						err = s.handleDungeonGiveupGame(p, nil)
					} else {
						err = s.handleDungeonBackToVillage(p, nil)
					}
					if err != nil || p.dungeon.runtime != runtime || !runtime.townReturnOp24Sent {
						t.Fatal("退出发送阶段未保留待确认运行态", err)
					}
					p.conn.(*bufferConn).write.Reset()
					// 现场42/24之后没有35、36、15，直接收到休息室1467。
					if err = s.handleCurrentTagTournamentRosterList(p, nil); err != nil {
						t.Fatal(err)
					}
					counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
					if p.dungeon.runtime != nil || p.tagTournamentBattle != nil || !currentTagTournamentLobbyReady(p) || counts[1403] != 1 || counts[1396] != 0 || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
						t.Fatal("中途返回后队伍资料仍被拒绝或错误记作通关", counts)
					}
					if opcode == 42 && counts[42] != 1 || opcode == 132 && counts[42] != 0 {
						t.Fatal("退出应答不属于真实请求", counts)
					}
					_, weekly, err := b.owner.Records(context.Background(), p.accountID)
					if err != nil || weekly != nil {
						t.Fatal("未通关产生个人周成绩", weekly, err)
					}
					p.conn.(*bufferConn).write.Reset()
					if err = s.handleCurrentTagTournamentRosterList(p, nil); err != nil {
						t.Fatal(err)
					}
					if counts = tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes()); counts[1403] != 1 || counts[2] != 0 || counts[42] != 0 {
						t.Fatal("再次打开队伍框重建了回城角色", counts)
					}
					if err = s.sendEnterSelectDungeonState(p, "帝国中途返回后重新挑战", false, true); err != nil || p.tagTournamentBattle == nil || p.tagTournamentBattle == b {
						t.Fatal("中途退出后不能免费重新准备挑战", err)
					}
					after, _, _ := repos.Character.Load(context.Background(), "29")
					actualMails, actualTotals := tagBattleMailTotals(t, repos)
					if !reflect.DeepEqual(before, after) || mails != actualMails || !reflect.DeepEqual(totals, actualTotals) {
						t.Fatal("回城开队伍或重新挑战重复扣费/发奖")
					}
				})
			}
		}
	}
}

func TestTagRosterRequestCannotRetireBattleWithoutAcceptedReturn(t *testing.T) {
	for _, scenario := range []string{"战斗中", "回城未写出", "非休息室落点", "其他角色地图", "过期会话", "非空正文", "普通频道"} {
		t.Run(scenario, func(t *testing.T) {
			s, p := tagResultActionFixture(t, tagtournament.Hard, false)
			b, runtime := p.tagTournamentBattle, p.dungeon.runtime
			if scenario != "战斗中" {
				if err := s.handleDungeonGiveupGame(p, nil); err != nil {
					t.Fatal(err)
				}
			}
			var body []byte
			switch scenario {
			case "回城未写出":
				runtime.townReturnOp24Sent = false
			case "非休息室落点":
				runtime.townReturnTransition.TownID = 1
			case "其他角色地图":
				runtime.Character.CharacterID = "60"
			case "过期会话":
				p.selectedCharacterID = 60
			case "非空正文":
				body = []byte{1}
			case "普通频道":
				p.channel.Type = 0
			}
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleCurrentTagTournamentRosterList(p, body); err != nil {
				t.Fatal(err)
			}
			if p.dungeon.runtime != runtime || p.tagTournamentBattle != b || p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("无有效回城证据的队伍请求释放了地图或打开队伍框")
			}
		})
	}
}

func TestTagAbandonRosterConfirmationWriteFailureCanResume(t *testing.T) {
	s, p := tagResultActionFixture(t, tagtournament.Hard, false)
	if err := s.handleDungeonGiveupGame(p, nil); err != nil {
		t.Fatal(err)
	}
	want := errors.New("隔离回城确认写失败")
	p.conn = &failNthDungeonWriteConn{failAt: 2, err: want}
	if err := s.handleCurrentTagTournamentRosterList(p, nil); !errors.Is(err, want) {
		t.Fatal("未返回回城状态重建写失败", err)
	}
	if p.dungeon.runtime != nil || !p.confirmedDungeonReturnStatePending || currentTagTournamentLobbyReady(p) {
		t.Fatal("重建失败仍放行队伍设置或没有保留重试")
	}
	p.conn = &bufferConn{}
	if err := s.handleCurrentTagTournamentRosterList(p, nil); err != nil {
		t.Fatal(err)
	}
	counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if !currentTagTournamentLobbyReady(p) || counts[1403] != 1 || counts[42] != 0 {
		t.Fatal("重试未完成原回城世代或重复退出应答", counts)
	}
}
