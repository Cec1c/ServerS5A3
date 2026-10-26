package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	repo "longheng.io/server/internal/modules/dnf/repository"
	town "longheng.io/server/internal/modules/dnf/town"
)

func TestRealPVFCurrentFiendWarDailySnapshotAndAtomicNormalEntry(t *testing.T) {
	t.Setenv(raidStartEntryCostGuardEnv, raidStartEntryCostGuardEnforce)
	f, peers := actualCurrentFiendWar(t, 1)
	s, p := f.service, peers[0]
	ctx := context.Background()
	for mode, path := range map[byte]string{1: "etc/raid/anton.etc", 2: "etc/raid/luke/luke.etc"} {
		daily, err := s.currentRaidDailyRule(mode)
		if err != nil || daily == nil || daily.Limit != 10 {
			t.Fatal("真实日限配置", daily, err)
		}
		cost, err := raid.LoadAdmissionRule(s.initialEquipmentArchive, path)
		if err != nil {
			t.Fatal(err)
		}
		bag, _, _ := f.repos.Inventory.Load(ctx, "19")
		key := "0:" + strconv.Itoa(50+int(mode))
		bag.Slots[key] = repo.ItemStack{ItemID: int64(cost.ItemID), Count: 12 * cost.Count}
		if err := f.repos.Inventory.Save(ctx, bag); err != nil {
			t.Fatal(err)
		}
		if _, err := s.consumeRaidEntryCostAndDaily(ctx, []string{"19"}, "19", p.accountID, cost, daily); err != nil {
			t.Fatal(err)
		}
		record, _, err := f.repos.Settings.Load(ctx, daily.Scope)
		used, countErr := fiendwar.FiendWarDailyUsed(record, p.accountID, daily.Now)
		if err != nil || countErr != nil || used != 1 {
			t.Fatal("未在扣票事务内记录日次数", used, err, countErr)
		}
		before, _, _ := f.repos.Inventory.Load(ctx, "19")
		record.Values["account:"+p.accountID] = "10"
		if err := f.repos.Settings.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
		if _, err := s.consumeRaidEntryCostAndDaily(ctx, []string{"19"}, "19", p.accountID, cost, daily); err == nil {
			t.Fatal("达到日限仍扣票")
		}
		after, _, _ := f.repos.Inventory.Load(ctx, "19")
		if !reflect.DeepEqual(before, after) {
			t.Fatal("日限拒绝没有回滚门票")
		}
		record.Values["account:"+p.accountID] = "1"
		if err := f.repos.Settings.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
		delete(after.Slots, key)
		if err := f.repos.Inventory.Save(ctx, after); err != nil {
			t.Fatal(err)
		}
		if _, err := s.consumeRaidEntryCostAndDaily(ctx, []string{"19"}, "19", p.accountID, cost, daily); err == nil {
			t.Fatal("缺票仍接收入场")
		}
		record, _, _ = f.repos.Settings.Load(ctx, daily.Scope)
		if used, _ := fiendwar.FiendWarDailyUsed(record, p.accountID, daily.Now); used != 1 {
			t.Fatal("缺票失败消耗了日次数")
		}
	}
	// 现用档始终强制扣票；环境观察开关不能绕过事务或次数限制。
	t.Setenv(raidStartEntryCostGuardEnv, raidStartEntryCostGuardLog)
	if s.raidStartEntryCostMode() != raidStartEntryCostGuardEnforce {
		t.Fatal("环境开关绕过现用入场规则")
	}
	if err := s.consumeCurrentFiendWarEntry(p, *f.state, f.state.Battle); err != nil {
		t.Fatal(err)
	}
	p.conn.(*bufferConn).write.Reset()
	if err := s.sendCurrentRaidDailyAvailability(p, "测试真实三种账本"); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, p.conn.(*bufferConn).write.Bytes())
	want := []byte{1, 1, 10, 0, 2, 1, 10, 0, 3, 1, 10, 0}
	if len(rest) != 0 || packet.Header.MsgID != 1303 || packet.Header.Classification != 0 || !bytes.Equal(packet.Body, want) {
		t.Fatal("现用次数快照错位", packet)
	}
}

func TestRealPVFCurrentFiendWarMerchantPurchaseScopeAndExpiry(t *testing.T) {
	f, peers := actualCurrentFiendWar(t, 1)
	s, p := f.service, peers[0]
	b := f.state.Battle
	f.state.Started = true
	area := b.FiendWar.Config.MerchantPositions[0]
	b.FiendWar.MerchantArea = area
	b.FiendWar.MerchantExpiresAt = time.Now().Add(time.Minute)
	b.FiendWar.MemberAreas[p.selectedCharacterID] = area
	setCurrentFiendWarTestOrigin(p, area)
	p.townPositionSnapshot = p.townSelectorOriginSnapshot
	p.townSceneReadyCharacterID = p.selectedCharacterID
	shop, err := s.currentNPCShopCatalog()
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	actor, _, _ := f.repos.Character.Load(ctx, "19")
	if actor.Stats == nil {
		actor.Stats = map[string]int64{}
	}
	actor.Stats["gold"] = 100000000
	if err := f.repos.Character.Save(ctx, actor); err != nil {
		t.Fatal(err)
	}
	q := currentNPCShopBuyRequest{ItemID: 490007846, Count: 1, ShopContext: currentFiendWarMerchantShop, ActorContext: currentFiendWarMerchantNPC}
	if _, err := s.commitCurrentNPCShopBuy(ctx, p, shop, items, q); err != nil {
		t.Fatal("实际商品不能购买", err)
	}
	beforeBag, _, _ := f.repos.Inventory.Load(ctx, "19")
	beforeActor, _, _ := f.repos.Character.Load(ctx, "19")
	for _, bad := range []string{"过期", "外区", "终局", "伪造NPC"} {
		b.FiendWar.MerchantExpiresAt = time.Now().Add(time.Minute)
		b.FiendWar.MemberAreas[p.selectedCharacterID] = area
		b.PhaseCompleted = false
		request := q
		switch bad {
		case "过期":
			b.FiendWar.MerchantExpiresAt = time.Now().Add(-time.Second)
		case "外区":
			b.FiendWar.MemberAreas[p.selectedCharacterID] = b.FiendWar.Config.WaitingArea
		case "终局":
			b.PhaseCompleted = true
		case "伪造NPC":
			request.ActorContext, request.ShopContext = 0, 0
		}
		if _, err := s.commitCurrentNPCShopBuy(ctx, p, shop, items, request); err == nil {
			t.Fatal("错误交易被接受", bad)
		}
		bag, _, _ := f.repos.Inventory.Load(ctx, "19")
		actor, _, _ := f.repos.Character.Load(ctx, "19")
		if !reflect.DeepEqual(bag, beforeBag) || !reflect.DeepEqual(actor, beforeActor) {
			t.Fatal("拒绝后仍改资产", bad)
		}
	}
	// 同样的补给也在固定NPC20316出售，不得被动态商人的到期条件误拦。
	unlock, err := s.lockCurrentFiendWarMerchantPurchase(p, shop, currentNPCShopBuyRequest{ItemID: 490007845, Count: 1, ActorContext: 20316})
	if err != nil {
		t.Fatal("误拦固定补给商店", err)
	}
	unlock()
}

func TestRealPVFCurrentFiendWarPartyRetreatKeepsOtherEncounter(t *testing.T) {
	f, peers := actualCurrentFiendWar(t, 4)
	s, b := f.service, f.state.Battle
	f.state.Started = true
	b.EnterAfter = time.Now().Add(-time.Minute)
	b.Deadline = time.Now().Add(time.Hour)
	table, err := town.Load(context.Background(), s.initialEquipmentArchive, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog = table
	var areas []int
	for id, a := range b.FiendWar.Config.Areas {
		if a.Dungeon > 0 && b.FiendWar.AreaOpen(id) {
			areas = append(areas, id)
		}
	}
	if len(areas) < 2 {
		t.Fatal("真实PVF缺少两处遭遇战")
	}
	runs := make([]*runtimeDungeonState, 4)
	for pair := 0; pair < 2; pair++ {
		area := areas[pair]
		members := f.state.Members[pair*2 : pair*2+2]
		managed, ok := s.replaceManagedRuntimeParty(runtimeRaidGroupPartyState(*f.state, members))
		if !ok {
			t.Fatal("不能建立实际临时队伍")
		}
		instance := &dungeonPartyInstance{key: dungeonPartyRunKey{PartyID: uint16(managed.PartyID)}}
		for i := pair * 2; i < pair*2+2; i++ {
			p := peers[i]
			_, r := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true, singleMonster: true})
			r.Dungeon.ID = int64(b.FiendWar.Config.Areas[area].Dungeon)
			r.AccountID = p.accountID
			r.Character = repo.CharacterRecord{CharacterID: strconv.Itoa(int(p.selectedCharacterID)), AccountID: p.accountID}
			p.dungeon.runtime = r
			runs[i] = r
			b.FiendWar.MemberAreas[p.selectedCharacterID] = area
			setCurrentFiendWarTestOrigin(p, area)
			p.townSceneReadyCharacterID = p.selectedCharacterID
			r.partyInstance = instance
		}
		if err := s.freezeCurrentRaidDungeonGroupEntry(peers[pair*2], runs[pair*2], []*runtimePartyPreparedDungeonEntry{{session: peers[pair*2+1], runtime: runs[pair*2+1]}}); err != nil {
			t.Fatal(err)
		}
	}
	b.Participation = map[uint16]currentRaidParticipation{}
	for i, p := range peers {
		b.Participation[p.selectedCharacterID] = currentRaidParticipation{RunID: runs[i].raidEntry.RunID, Runtime: runs[i], Session: p}
	}
	runs[1].townReturnOp24Sent = true
	if err := s.beginCurrentFiendWarPartyRetreatLocked(peers[1], runs[1], time.Now()); err != nil || len(b.FiendWarRetreatRuns) != 0 {
		t.Fatal("队员退出带退全队", err)
	}
	runs[1].townReturnOp24Sent = false
	runs[0].townReturnOp24Sent = true
	if err := s.beginCurrentFiendWarPartyRetreatLocked(peers[0], runs[0], time.Now()); err != nil {
		t.Fatal(err)
	}
	first, second := runs[0].raidEntry.RunID, runs[2].raidEntry.RunID
	if !b.FiendWarRetreatRuns[first] || b.FiendWarRetreatRuns[second] || !runs[0].fiendWarRetreatKeepsParty {
		t.Fatal("撤退串入其他遭遇战")
	}
	if err := s.maintainCurrentFiendWarSession(peers[1]); err != nil {
		t.Fatal(err)
	}
	if !runs[1].townReturnPending || !runs[1].townReturnOp24Sent || !runs[1].fiendWarRetreatKeepsParty || int(runs[1].townReturnTransition.AreaID) != b.FiendWar.Config.WaitingArea {
		t.Fatal("队员没有被送回本场等待室")
	}
	if runs[2].townReturnPending || runs[3].townReturnPending || b.FiendWarEncounters[areas[1]].RunID != second {
		t.Fatal("误退另一场队伍")
	}
	if err := s.commitPendingDungeonReturnForSceneRequest(peers[1], "测试撤退确认"); err != nil {
		t.Fatal(err)
	}
	party := s.runtimePartyStateForSession(peers[1])
	if party.PartyID != 0 {
		t.Fatal("超时空临时队伍在确认回城后未解除")
	}
	if other := s.runtimePartyStateForSession(peers[2]); len(runtimePartyMembers(other)) != 2 {
		t.Fatal("临时队伍清理误伤另一区域")
	}
}
