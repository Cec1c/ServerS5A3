package dnfbridge

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func TestTagPreviouslyRewardedRoundsSkipPopupAndStillComplete(t *testing.T) {
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		for _, priorRounds := range []int{2, 99} {
			t.Run(fmt.Sprintf("模式%d/已领%d", mode, priorRounds), func(t *testing.T) {
				s, p := tagResultActionFixture(t, mode, false)
				g, _ := s.repositoryGroup()
				old := p.tagTournamentBattle
				request := old.prepared.runtime.Request
				if priorRounds > len(old.plan.Encounters) {
					priorRounds = len(old.plan.Encounters)
				}
				for stage := 0; stage < priorRounds; stage++ {
					tagPlayMoveNext(t, s, p)
					p.conn.(*bufferConn).write.Reset()
					tagPlayKillBoss(t, s, p)
					if counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes()); counts[1397] != 1 {
						t.Fatal("首次发奖缺少K.O奖励通知", stage, counts)
					}
				}
				beforeCount, beforeTotals := tagBattleMailTotals(t, g)
				if err := s.handleDungeonGiveupGame(p, nil); err != nil {
					t.Fatal(err)
				}
				if err := s.commitPendingDungeonReturnBeforeTownEnterSelect(p, "已领奖重复挑战回归"); err != nil {
					t.Fatal(err)
				}
				p.conn.(*bufferConn).write.Reset()
				if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(request)); err != nil || p.tagTournamentBattle == nil {
					t.Fatal("当日免费重入失败", err)
				}
				b := p.tagTournamentBattle
				for stage := range b.plan.Encounters {
					tagPlayMoveNext(t, s, p)
					p.conn.(*bufferConn).write.Reset()
					tagPlayKillBoss(t, s, p)
					counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
					wantPopup := 1
					if stage < priorRounds {
						wantPopup = 0
					}
					if counts[1397] != wantPopup || b.roundProjectionPending || b.projectedRounds != stage+1 {
						t.Fatal("已领奖回合弹窗或投影状态阻断后续过图", stage, counts)
					}
					if (counts[1396] == 1) != (stage == len(b.plan.Encounters)-1) {
						t.Fatal("无奖励回合破坏终局计时收尾", stage, counts)
					}
					if stage < priorRounds {
						count, totals := tagBattleMailTotals(t, g)
						if count != beforeCount || !reflect.DeepEqual(totals, beforeTotals) {
							t.Fatal("重复挑战再次发放已领奖材料", stage, count, totals)
						}
					}
				}
				if b.run.Snapshot().Phase != tagtournament.Finished || !b.completionSent {
					t.Fatal("重复挑战无法正常通关")
				}
			})
		}
	}
}

func TestTagRewardPopupWriteRetrySurvivesRepeatedZeroGrantSettlement(t *testing.T) {
	s, p, g, now := tagBattleBridgeFixture(t, tagtournament.Normal)
	b := p.tagTournamentBattle
	e, _, err := s.beginCurrentTagTournamentRound(p, false)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(1)
	failure := errors.New("隔离奖励窗口写失败")
	c := &failNthDungeonWriteConn{failAt: 2, err: failure}
	p.conn = c
	if err := s.confirmCurrentTagTournamentRound(p, e.MapID, []uint32{e.MonsterID}, true); !errors.Is(err, failure) {
		t.Fatal("未命中奖励通知写失败", err)
	}
	if !b.roundRewardGranted || !b.roundProjectionPending || len(b.pendingRoundRewards) == 0 {
		t.Fatal("已提交邮件但丢失待重试窗口")
	}
	count, totals := tagBattleMailTotals(t, g)
	p.conn = &bufferConn{}
	if err := s.confirmCurrentTagTournamentRound(p, e.MapID, []uint32{e.MonsterID}, true); err != nil {
		t.Fatal(err)
	}
	if counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes()); counts[1397] != 1 || b.roundRewardGranted || b.roundProjectionPending {
		t.Fatal("重复幂等结算吞掉了上次已提交奖励的待补通知", counts)
	}
	gotCount, gotTotals := tagBattleMailTotals(t, g)
	if count != gotCount || !reflect.DeepEqual(totals, gotTotals) {
		t.Fatal("补投窗口重复发奖")
	}
}
