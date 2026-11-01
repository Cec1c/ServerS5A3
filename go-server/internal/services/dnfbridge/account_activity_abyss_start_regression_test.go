package dnfbridge

import (
	"context"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 现场未发送op235；用真实op39处理路径模拟两波击败，并在清末波后op42回城。
func TestAccountActivityAbyssNoStartMessageLastWaveAndTownReturn(t *testing.T) {
	for _, started := range []bool{false, true} {
		name := "未发送开始包"
		if started {
			name = "已发送开始包"
		}
		t.Run(name, func(t *testing.T) {
			s, session, _, r, scene := prepareHellPartyClearSettlementRuntime(t)
			r.HellPartyStarted = started
			g, _ := s.repositoryGroup()
			ctx := context.Background()
			q := repo.ActivityQuery{AccountID: s.accountIDForSession(session)}
			assertCount := func(want int64) {
				t.Helper()
				summary, err := g.Activity.Summary(ctx, q)
				if err != nil || summary.Metrics[repo.ActivityAbyss] != want {
					t.Fatalf("深渊完成统计=%v，期望%d，错误=%v", summary.Metrics, want, err)
				}
			}
			if currentActivityHellReady(r, scene) {
				t.Fatal("未开打就认为完成")
			}
			killDungeonActors(t, s, session, hellPartyRoomMonsterKeys(t, r)...)
			assertCount(0)
			first, last := hellPartyRewardActorKeys(t, r)
			killDungeonActors(t, s, session, first...)
			assertCount(0)
			killDungeonActors(t, s, session, last[:len(last)-1]...)
			assertCount(0)
			killDungeonActors(t, s, session, last[len(last)-1])
			assertCount(1)
			if r.HellPartyStarted != started {
				t.Fatal("统计修改了开始包状态")
			}
			if r.bossDieCheckAccepted || r.settlementEntrySent || r.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
				t.Fatal("深渊计数误触发了普通地下城通关")
			}
			townService, townSession, _ := newBackToVillageRuntime(t)
			transition, err := townService.prepareCurrentDungeonTownTransition(ctx, townSession.selectedCharacterID, townSession)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.sendCurrentDungeonReturnToTownLocked(session, r, transition, 42, "fixture_bodyless_op42_after_final_wave"); err != nil {
				t.Fatal(err)
			}
			if !r.townReturnPending || !r.townReturnOp24Sent {
				t.Fatal("没有发出原有回城投影")
			}
			assertCount(1)
			r.activityAbyssRecorded = false
			if err = s.recordCurrentActivityAbyss(session, r, r.Session.Snapshot().Scene, false); err != nil {
				t.Fatal(err)
			}
			assertCount(1)
			if err = r.Session.Abandon(); err != nil {
				t.Fatal(err)
			}
			assertCount(1)
		})
	}
}
