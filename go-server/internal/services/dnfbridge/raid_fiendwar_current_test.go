package dnfbridge

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	town "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strconv"
	"testing"
	"time"
)

func actualCurrentFiendWar(t *testing.T, n int) (*raidStartGuardFixture, []*gameSession) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要当前真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, archive, n)
	f.service.options.currentClientVersion = "official-24.9"
	f.state.CurrentLayout = true
	f.state.TailFlag = 0
	f.state.CurrentUIFlags = [3]byte{}
	f.state.RouteOrRaidType = 2
	peers := make([]*gameSession, 0, n)
	c, err := f.service.loadCurrentFiendWarConfig()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		p := f.session()
		p.selectedCharacterID = uint16(19 + i)
		p.channel.ID, p.channel.Type = 184, 42
		p.loginPacketSeen, p.accountFromToken = true, true
		if i > 0 {
			p.accountID = "account-" + strconv.Itoa(19+i)
		}
		p.conn = &bufferConn{}
		f.service.bindGameSessionCharacter(p, p.selectedCharacterID)
		f.state.Members[i].GroupIndex = byte(i + 1)
		peers = append(peers, p)
		if err = f.repos.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: strconv.Itoa(19 + i), Slots: map[string]repo.ItemStack{"0:50": {ItemID: int64(c.EntryCost.ItemID), Count: int64(c.EntryCost.Count) + 2}}}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := f.service.prepareCurrentFiendWarBattle(peers[0], *f.state)
	if err != nil {
		t.Fatal(err)
	}
	if b.Mode != 3 || len(b.Dungeons) == 0 || b.Configuration.WeeklyClearLimit != 2 {
		t.Fatal("真实战场准备失败", b)
	}
	f.state.Battle = b
	return f, peers
}

func TestRealPVFCurrentFiendWarEightMemberAdmission(t *testing.T) {
	f, p := actualCurrentFiendWar(t, 8)
	b := f.state.Battle
	if err := f.service.consumeCurrentFiendWarEntry(p[0], *f.state, b); err != nil {
		t.Fatal(err)
	}
	for id := range b.MemberAccounts {
		bag, _, err := f.repos.Inventory.Load(context.Background(), strconv.Itoa(int(id)))
		if err != nil || bag.Slots["0:50"].Count != 2 {
			t.Fatal("未原子扣除每位成员门票", id, bag, err)
		}
	}
	// A ninth member cannot enter through the ordinary twenty-player ceiling.
	f.state.Members = append(f.state.Members, runtimeRaidMemberState{CharID: 99, GroupIndex: 9})
	if _, err := f.service.prepareCurrentFiendWarBattle(p[0], *f.state); err == nil {
		t.Fatal("超出8人仍允许入场")
	}
}

func TestRealPVFCurrentFiendWarEntryRoomAndRewardPools(t *testing.T) {
	f, p := actualCurrentFiendWar(t, 1)
	archive := f.service.initialEquipmentArchive
	table, err := town.Load(context.Background(), archive, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.service.townCatalog = table
	_, townID, areaID, _, path, err := f.service.currentRaidEntranceTownTransition(p[0], 19, repo.CharacterRecord{CharacterID: "19"})
	if err != nil {
		t.Fatal(err)
	}
	if int(townID) != f.state.Battle.FiendWar.Config.WaitingTown || path == "" {
		t.Fatal("没有使用超时空频道入口", townID, areaID, path)
	}
	if areaID != 0 {
		t.Fatal("未使用独立超时空大门", areaID)
	}
	if _, _, err := f.service.currentFiendWarLanding(int(townID), f.state.Battle.FiendWar.Config.WaitingArea); err != nil {
		t.Fatal("等待室落点不可用", err)
	}
	if _, err := buildCurrentFiendWarBoard(currentFiendWarProjection{RaidKey: f.state.RaidKey, Battle: f.state.Battle.FiendWar, Members: f.state.Members, Deadline: f.state.Battle.Deadline}, currentFiendWarProjection{}, 19, time.Now()); err != nil {
		t.Fatal("真实PVF战况投影失败", err)
	}
	for id, a := range f.state.Battle.FiendWar.Config.Areas {
		if a.Dungeon > 0 && a.Kind != "top boss" && a.Kind != "bot boss" {
			if area, ok := f.service.townArea(int64(townID), int64(id)); !ok || area.MapPath == "" {
				t.Fatal("作战区域地图缺失", id)
			}
		}
	}
	t.Logf("实际超时空入口 %d/%d %s", townID, areaID, path)
	phase := f.state.Battle.AntonRewards.Phases[0]
	for _, typ := range []string{"party_card", "squad_item"} {
		for rank := byte(0); rank < 4; rank++ {
			if _, total, err := phase.Pool(typ, rank); err != nil || total == 0 {
				t.Fatal(typ, rank, err)
			}
		}
	}
}

func TestRealPVFCurrentFiendWarCrossGroupClearAndReplay(t *testing.T) {
	f, p := actualCurrentFiendWar(t, 2)
	f.state.Started = true
	b := f.state.Battle
	now := time.Now()
	b.EnterAfter = now.Add(-time.Minute)
	b.Deadline = now.Add(time.Hour)
	var dungeon uint32
	for area, a := range b.FiendWar.Config.Areas {
		if b.FiendWar.AreaOpen(area) && a.Dungeon > 0 {
			dungeon = uint32(a.Dungeon)
			for _, peer := range p {
				b.FiendWar.MemberAreas[peer.selectedCharacterID] = area
				setCurrentFiendWarTestOrigin(peer, area)
			}
			break
		}
	}
	if dungeon == 0 {
		t.Fatal("PVF初始战场没有遭遇战")
	}
	r0, r1 := raidCompletedRuntimeFixture(t, p[0], dungeon), raidCompletedRuntimeFixture(t, p[1], dungeon)
	if err := f.service.freezeCurrentRaidDungeonGroupEntry(p[0], r0, []*runtimePartyPreparedDungeonEntry{{session: p[1], runtime: r1}}); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(p[0], r0, now); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(p[1], r1, now); err != nil || changed {
		t.Fatal("同场重复结算未幂等", changed, err)
	}
	for _, peer := range p {
		if b.MemberClears[peer.selectedCharacterID] != 1 {
			t.Fatal("跨编制参战者漏记", peer.selectedCharacterID)
		}
	}
}

func TestRealPVFCurrentFiendWarFrozenRewards(t *testing.T) {
	f, _ := actualCurrentFiendWar(t, 2)
	b := f.state.Battle
	f.state.Started = true
	b.EnterAfter = time.Now().Add(-time.Minute)
	b.Deadline = time.Now().Add(time.Hour)
	b.PhaseCompleted, b.FiendWar.Complete = true, true
	b.PhaseCompletedAt = time.Now()
	minimum := b.AntonRewards.Phases[0].MinimumClears
	b.ClearedRuns = map[string]currentRaidClearedRun{}
	for i := uint32(0); i < minimum; i++ {
		b.ClearedRuns[strconv.Itoa(int(i))] = currentRaidClearedRun{DungeonID: 4978, Attempt: 1, Participants: []uint16{19, 20}}
	}
	if err := f.service.freezeCurrentFiendWarRewardFlowLocked(*f.state); err != nil {
		t.Fatal(err)
	}
	flow := b.RewardFlow[0]
	if flow == nil || flow.Mode != 3 || len(flow.Recipients) != 2 {
		t.Fatal("超时空资格未冻结")
	}
	if _, errs := f.service.commitCurrentRaidWeeklyIntents(b.PhaseCompletedAt); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) > 0 {
		t.Fatal(errs)
	}
	if !flow.Party[19].Committed || !flow.Party[20].Committed || !flow.Squad[19].Committed || len(f.service.raidWeeklyIntents) != 1 {
		t.Fatal("奖励发放或超时空周次数登记错误")
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(time.Minute), func(int64) (int64, error) { t.Fatal("已冻结奖励再次抽取"); return 0, nil }); len(errs) > 0 {
		t.Fatal(errs)
	}
	if !flow.BreakUntil.IsZero() {
		t.Fatal("单阶段超时空进入普通团本阶段休整")
	}
}
