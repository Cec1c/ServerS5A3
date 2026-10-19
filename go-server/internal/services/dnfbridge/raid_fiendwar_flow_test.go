package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	town "longheng.io/server/internal/modules/dnf/town"
	"strconv"
	"testing"
	"time"
)

func TestRealPVFCurrentFiendWarEightActorsTwoBossesAndRewards(t *testing.T) {
	testCurrentFiendWarTwoBossesAndRewards(t, true)
}

func TestRealPVFCurrentFiendWarBossDeathWithoutMechanismReports(t *testing.T) {
	testCurrentFiendWarTwoBossesAndRewards(t, false)
}

func TestRealPVFCurrentFiendWarBossDeathWithStaleHP(t *testing.T) {
	testCurrentFiendWarTwoBossesAndRewards(t, true, false)
}

func testCurrentFiendWarTwoBossesAndRewards(t *testing.T, reports bool, hpReports ...bool) {
	t.Helper()
	f, peers := actualCurrentFiendWar(t, 8)
	f.state.Started = true
	b := f.state.Battle
	now := time.Now()
	b.EnterAfter = now.Add(-time.Minute)
	b.Deadline = now.Add(time.Hour)
	clear := func(area int, party []*gameSession) {
		t.Helper()
		d := uint32(b.FiendWar.Config.Areas[area].Dungeon)
		runs := make([]*runtimeDungeonState, len(party))
		followers := []*runtimePartyPreparedDungeonEntry{}
		for i, p := range party {
			b.FiendWar.MemberAreas[p.selectedCharacterID] = area
			setCurrentFiendWarTestOrigin(p, area)
			runs[i] = raidCompletedRuntimeFixture(t, p, d)
			if i > 0 {
				followers = append(followers, &runtimePartyPreparedDungeonEntry{session: p, runtime: runs[i]})
			}
		}
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(party[0], runs[0], followers); err != nil {
			t.Fatal(area, err)
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(party[0], runs[0], now); err != nil || !changed {
			t.Fatal(area, changed, err)
		}
		for i := 1; i < len(party); i++ {
			if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(party[i], runs[i], now); err != nil || changed {
				t.Fatal("多人同场重复归账", area, i, changed, err)
			}
		}
	}
	clear(27, peers[:4])
	clear(28, peers[:4])
	clear(29, peers[4:])
	if !b.FiendWar.AreaOpen(30) || !b.FiendWar.AreaOpen(31) {
		t.Fatal("兵营通关未打开两个首领")
	}
	parties := [][]*gameSession{peers[:4], peers[4:]}
	bossRuns := make([][]*runtimeDungeonState, 2)
	for side, party := range parties {
		area := 30 + side
		d := uint32(b.FiendWar.Config.Areas[area].Dungeon)
		followers := []*runtimePartyPreparedDungeonEntry{}
		for i, p := range party {
			b.FiendWar.MemberAreas[p.selectedCharacterID] = area
			setCurrentFiendWarTestOrigin(p, area)
			r := raidCompletedRuntimeFixture(t, p, d)
			bossRuns[side] = append(bossRuns[side], r)
			if i > 0 {
				followers = append(followers, &runtimePartyPreparedDungeonEntry{session: p, runtime: r})
			}
		}
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(party[0], bossRuns[side][0], followers); err != nil {
			t.Fatal(err)
		}
	}
	for _, mapID := range []int{306000463, 306000460} {
		if err := b.FiendWar.Apply(fiendwar.FiendWarEvent{Kind: "on enter map", ID: mapID}, now); err != nil {
			t.Fatal(err)
		}
	}
	for turn := 0; reports && turn < 8; turn++ {
		changed := false
		for _, side := range []struct {
			name string
			area int
		}{{"top boss", 30}, {"bot boss", 31}} {
			state := b.FiendWar.Value(side.name + " state")
			if state >= 5 && state <= 8 {
				if err := b.FiendWar.BossReport(b.FiendWar.Config.Areas[side.area].Dungeon, 1, state, now); err != nil {
					t.Fatal(err)
				}
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	for side, party := range parties {
		area := 30 + side
		d := b.FiendWar.Config.Areas[area].Dungeon
		if reports {
			hp := 0
			if len(hpReports) > 0 && !hpReports[0] {
				hp = 75
			}
			if err := b.FiendWar.BossReport(d, 0, hp, now); err != nil {
				t.Fatal(err)
			}
		} else {
			r := bossRuns[side][0]
			r.bossDieCheckAccepted = false
			if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(party[0], r, now); err == nil || changed {
				t.Fatal("未验证副本死亡就推进罗什通关", side)
			}
			r.bossDieCheckAccepted = true
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(party[0], bossRuns[side][0], now); err != nil || !changed {
			t.Fatal("首领结算失败", side, changed, err)
		}
	}
	if !b.PhaseCompleted || !b.FiendWar.Complete || b.RewardFlow[0] == nil || len(b.RewardFlow[0].Recipients) != 8 {
		t.Fatal("未冻结8位符合门槛的实际参战者")
	}
	flow := b.RewardFlow[0]
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) > 0 || len(flow.Party) != 0 {
		t.Fatal("周通关提交前发放物品", errs)
	}
	if commands, errs := f.service.commitCurrentRaidWeeklyIntents(now); len(errs) != 0 || len(commands) != 1 || commands[0].Mode != 3 || commands[0].Limit != 2 {
		t.Fatal("未按PVF提交超时空周通关", commands, errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) > 0 {
		t.Fatal(errs)
	}
	for _, p := range peers {
		if !flow.Party[p.selectedCharacterID].Committed || !flow.Squad[p.selectedCharacterID].Committed {
			t.Fatal("八人奖励漏发", p.selectedCharacterID)
		}
		actor, found, err := f.repos.Character.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
		policy, policyErr := currentRaidWeeklyPolicy()
		used, countErr := raid.WeeklyClears(actor, 3, now, policy)
		if !found || err != nil || policyErr != nil || countErr != nil || used != 1 {
			t.Fatal("每位实际达标成员周次数应为一次", found, err, countErr, used)
		}
	}
	if commands, errs := f.service.commitCurrentRaidWeeklyIntents(now.Add(time.Minute)); len(commands) != 0 || len(errs) != 0 {
		t.Fatal("周通关回放重复提交", commands, errs)
	}
}

func TestRealPVFCurrentFiendWarWaitingReturnAndMoveRollback(t *testing.T) {
	f, p := actualCurrentFiendWar(t, 2)
	b := f.state.Battle
	f.state.Started = true
	b.EnterAfter = time.Now().Add(-time.Minute)
	b.Deadline = time.Now().Add(time.Hour)
	table, err := town.Load(context.Background(), f.service.initialEquipmentArchive, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.service.townCatalog = table
	waiting := b.FiendWar.Config.WaitingArea
	if _, _, err := f.service.currentFiendWarLanding(b.FiendWar.Config.WaitingTown, waiting); err != nil {
		t.Fatal(err)
	}
	target := b.FiendWar.Config.Areas[waiting].Next[0]
	for _, peer := range p {
		b.FiendWar.MemberAreas[peer.selectedCharacterID] = waiting
	}
	before, err := f.service.moveCurrentFiendWarEncounterRoster(*f.state, f.state.Members, target)
	if err != nil {
		t.Fatal(err)
	}
	f.service.rollbackCurrentFiendWarEncounterRoster(*f.state, before, target)
	for _, peer := range p {
		if b.FiendWar.MemberArea(peer.selectedCharacterID, b.MemberGroups[peer.selectedCharacterID]) != waiting {
			t.Fatal("失败准备未恢复区域")
		}
	}
	r := raidCompletedRuntimeFixture(t, p[0], 4978)
	r.raidEntry = &currentRaidDungeonEntry{RaidKey: f.state.RaidKey, InstanceID: b.InstanceID, ChannelType: 42}
	b.PhaseCompleted, b.FiendWar.Complete = true, true
	transition, err := f.service.prepareCurrentFiendWarReturn(p[0], r)
	if err != nil {
		t.Fatal(err)
	}
	if int(transition.AreaID) != waiting || transition.PositionX == 0 && transition.PositionY == 0 {
		t.Fatal("首领退场落在无坐标的传送站", transition)
	}
	r.townReturnPending, r.townReturnOp24Sent = true, true
	r.townReturnTransition = transition
	if _, err := f.service.prepareCurrentFiendWarMove(p[0], currentTownSetUserAreaRequest{TownID: transition.TownID, AreaID: transition.AreaID}); err != nil {
		t.Fatal("终局拦截了已发送回城的确认", err)
	}
}

func TestRealPVFCurrentFiendWarFailureRefundOnceWithoutClearRewards(t *testing.T) {
	f, p := actualCurrentFiendWar(t, 2)
	b := f.state.Battle
	f.state.Started = true
	if err := f.service.consumeCurrentFiendWarEntry(p[0], *f.state, b); err != nil {
		t.Fatal(err)
	}
	if !b.FiendWarConfig.Retry {
		t.Fatal("当前真实PVF未声明失败返票")
	}
	now := time.Now()
	b.TimedOut = true
	b.FiendWarEndedAt = now
	if err := f.service.freezeCurrentFiendWarRetryLocked(*f.state, now); err != nil {
		t.Fatal(err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(now, func(int64) (int64, error) { t.Fatal("返票不应抽取通关奖励"); return 0, nil }); len(errs) > 0 {
		t.Fatal(errs)
	}
	before := int64(b.FiendWarConfig.EntryCost.Count) + 2
	for _, id := range []string{"19", "20"} {
		bag, _, err := f.repos.Inventory.Load(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		var count int64
		for _, stack := range bag.Slots {
			if stack.ItemID == int64(b.FiendWarConfig.RetryItem.ItemID) {
				count += stack.Count
			}
		}
		if count != before {
			t.Fatal("返票数量错误", id, count, before)
		}
	}
	if b.RewardFlow[0] != nil || b.PhaseCompleted || len(f.service.raidWeeklyIntents) != 0 {
		t.Fatal("失败生成通关奖励或周记录")
	}
	if err := f.service.freezeCurrentFiendWarRetryLocked(*f.state, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(now.Add(time.Second), zeroAntonRoll); len(errs) > 0 {
		t.Fatal(errs)
	}
	if !f.service.resetCurrentFiendWarFailureLocked(f.state, now.Add(time.Hour)) || f.state.Started || f.state.Battle != nil {
		t.Fatal("返票完成并退出副本后没有恢复招募")
	}
}

func setCurrentFiendWarTestOrigin(p *gameSession, area int) {
	p.townSelectorOriginBound = true
	p.townSelectorOriginSnapshot = currentTownPositionSnapshot{CharacterID: p.selectedCharacterID, TownID: 98, AreaID: byte(area), PositionX: 500, PositionY: 250, PositionValid: true}
}
