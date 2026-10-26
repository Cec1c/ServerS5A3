package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 使用完整真档生成奖励，但复用已有已通关运行态和结算事务夹具，
// 核对生产者的价格冻结及op35投影，不手工创建带价格的奖励计划。
func TestRealScriptPVFDungeonPaidCardFreezeFeedsSettlementPrice(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("设置DNF_WORLDMAP_REAL_PVF_SMOKE以核对付费翻牌真实冻结与op35")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	const fixtureLevel int64 = 1
	listPrice, err := currentDungeonPaidCardCost(archive, fixtureLevel)
	if err != nil || listPrice <= 0 {
		t.Fatalf("真档普通翻牌价格不可用：level=%d cost=%d err=%v", fixtureLevel, listPrice, err)
	}
	for _, scenario := range []struct {
		name     string
		contract bool
		used     int64
		free     bool
		expired  bool
		oldDay   bool
	}{
		{name: "ordinary_without_contract"},
		{name: "active_contract_has_one_use_left", contract: true, used: premium.DailyLimit(premium.DevilSlotGoldCard) - 1, free: true},
		{name: "active_contract_daily_limit_used", contract: true, used: premium.DailyLimit(premium.DevilSlotGoldCard)},
		{name: "expired_contract", contract: true, expired: true},
		{name: "old_service_day_usage", contract: true, used: premium.DailyLimit(premium.DevilSlotGoldCard), oldDay: true, free: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			service, runtime, session, connection, _ := prepareCompletedSettlementRuntime(t)
			repos, ok := service.repositoryGroup()
			if !ok {
				t.Fatal("结算夹具仓储不可用")
			}
			catalog, err := newPVFDungeonMonsterCatalog(archive)
			if err != nil {
				t.Fatal(err)
			}
			service.dungeonMonsterTable = catalog
			now := service.gameplayNow().UTC()
			account := dnfrepo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{}}
			if scenario.contract {
				expiry := now.Add(24 * time.Hour)
				if scenario.expired {
					expiry = now.Add(-time.Hour)
				}
				account.Metadata[premium.MetadataKey(premium.DevilSlotType(premium.DevilSlotGoldCard))] =
					strconv.FormatInt(expiry.Unix(), 10)
			}
			if err := repos.Account.Save(ctx, account); err != nil {
				t.Fatal(err)
			}
			character, found, err := repos.Character.Load(ctx, "99")
			if err != nil || !found {
				t.Fatalf("读取夹具角色：found=%t err=%v", found, err)
			}
			character.AccountID = "account-1"
			character.Level = 1
			character.Stats["exp"] = 90
			character.Stats["gold"] = listPrice + 100
			usageDay := now
			if scenario.oldDay {
				usageDay = now.Add(-48 * time.Hour)
			}
			for used := int64(0); used < scenario.used; used++ {
				if !premium.TryConsumeDaily(&character, premium.DevilSlotGoldCard, usageDay) {
					t.Fatal("无法建立当日黄金翻牌使用次数夹具")
				}
			}
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{
				CharacterID: "99", Points: dnfrepo.SkillPointState{TotalSP: 10, RemainingSP: 10, SyncedLevel: 1},
			}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			runtime.Character = character
			runtime.Dungeon.Metadata.BasisLevel.Set = true
			runtime.Dungeon.Metadata.BasisLevel.Value = fixtureLevel
			runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Set = true
			runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Value = 1
			runtime.Request.Difficulty = 1
			runtime.DropOwner = newRuntimeDungeonDropOwner()
			runtime.settlementPlayResultReceived = true
			runtime.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
			runtime.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset] = 73
			runtime.startedAt = now.Add(-13 * time.Second)
			runtime.clearMapCompletionAt = now
			if runtime.settlementCardRewardState != nil || runtime.settlementResultPlan != nil {
				t.Fatal("夹具已预先冻结价格，无法验证真实生产者")
			}
			resources := currentDungeonSettlementTestResources(t)
			if err := service.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, runtime, resources); err != nil {
				t.Fatal(err)
			}
			state := runtime.settlementCardRewardState
			if state == nil || runtime.settlementResultPlan == nil {
				// 生产者会记录卡片资源错误；在测试内重读相同入口，给出具体依赖失败。
				t.Fatalf("生产者未冻结真实翻牌计划：freeze_error=%v", service.freezeCurrentDungeonCardRewardStateLocked(session, runtime))
			}
			state.mu.Lock()
			frozenPlan := cloneDungeonCardRewardPlan(state.plan)
			state.mu.Unlock()
			paid := frozenPlan.Sides[dungeonCardSidePaid]
			want := listPrice
			if scenario.free {
				want = 0
			}
			if paid.Cost != want || paid.ConsumePremiumDaily != scenario.free {
				t.Fatalf("生产者冻结费用错误：cost=%d want=%d daily=%t free=%t", paid.Cost, want, paid.ConsumePremiumDaily, scenario.free)
			}
			if len(paid.Items) != 1 || paid.Items[0].Count != 1 {
				t.Fatalf("普通和契约翻牌均应保持真实生成器的单件奖励：%+v", paid.Items)
			}
			tuples := currentDungeonOp71RewardTuplesFromCardBundle(paid)
			if len(tuples) != 2 || tuples[1] != (currentDungeonOp71RewardTuple{ValueA: uint32(paid.Items[0].ItemID), ValueB: 1}) {
				t.Fatalf("翻牌显示未保持一件奖励：%+v", tuples)
			}
			body := append([]byte(nil), runtime.settlementResultPlan.ClearRewardBody...)
			premiumBody := append([]byte(nil), runtime.settlementResultPlan.PremiumServiceBody...)
			if !bytes.Equal(premiumBody, buildCurrentPremiumServiceDataBody(account, character, now)) {
				t.Fatal("翻牌牌面明细与费用、奖励资格未使用同一份快照")
			}
			if got := paidCardPriceFromProducedOp35(t, body); int64(got) != want {
				t.Fatalf("op35价格与实际冻结费用不一致：wire=%d frozen=%d", got, want)
			}
			stored, _, err := repos.Character.Load(ctx, "99")
			wantUsed := scenario.used
			if scenario.oldDay {
				wantUsed = 0
			}
			if err != nil || stored.Stats["gold"] != character.Stats["gold"] ||
				premium.DailyUsage(stored, premium.DevilSlotGoldCard, now) != wantUsed {
				t.Fatalf("显示价格提前扣金币或次数：character=%+v err=%v", stored, err)
			}

			// 冻结后即使PVF来源已不可读、钱包和副本等级改变，同一state仍保持原价和奖励。
			catalog.source = bridgePVFSource{}
			stored.Stats["gold"] = 0
			stored.Level = 84
			if err := repos.Character.Save(ctx, stored); err != nil {
				t.Fatal(err)
			}
			runtime.Character = stored
			runtime.Dungeon.Metadata.BasisLevel.Value = 84
			if err := service.freezeCurrentDungeonCardRewardStateLocked(session, runtime); err != nil {
				t.Fatalf("重复冻结重新读取来源或重算价格：%v", err)
			}
			state.mu.Lock()
			unchanged := reflect.DeepEqual(frozenPlan, state.plan)
			state.mu.Unlock()
			if runtime.settlementCardRewardState != state || !unchanged {
				t.Fatal("重复冻结改写了本局费用或奖励计划")
			}
			if err := service.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, runtime, resources); err != nil ||
				!bytes.Equal(runtime.settlementResultPlan.ClearRewardBody, body) || !bytes.Equal(runtime.settlementResultPlan.PremiumServiceBody, premiumBody) {
				t.Fatalf("生产者重试改写已冻结op35：%v", err)
			}

			// 用生产者的同一份计划实际发奖，核对单件入包、免费/收费及次数；重复点击不再发放。
			// 冻结不回读来源已在上方验证；物品通知仍应使用可读的真实资源。
			catalog.source = archive
			stored.Stats["gold"] = listPrice + 100
			if err := repos.Character.Save(ctx, stored); err != nil {
				t.Fatal(err)
			}
			runtime.settlementCardLayoutSent = true
			runtime.advanceSettlementPhase(currentDungeonSettlementPhaseCardsRevealed)
			connection.write.Reset()
			if err := service.selectCurrentDungeonCardLocked(session, runtime, dungeonCardSidePaid, 0, "真实PVF翻牌与次数刷新"); err != nil {
				t.Fatal(err)
			}
			grant := runtime.settlementPaidCardGrant
			if grant == nil || grant.Cost != want || grant.GoldAfter != listPrice+100-want+paid.Gold ||
				len(grant.ItemSlots) != 1 || grant.ItemSlots[0] < 0 {
				t.Fatalf("真实单件奖励发放错误：grant=%+v", grant)
			}
			grantedCharacter, _, err := repos.Character.Load(ctx, "99")
			if scenario.free {
				wantUsed++
			}
			if err != nil || grantedCharacter.Stats["gold"] != grant.GoldAfter ||
				premium.DailyUsage(grantedCharacter, premium.DevilSlotGoldCard, now) != wantUsed {
				t.Fatalf("发奖后的金币或契约次数错误：character=%+v err=%v", grantedCharacter, err)
			}
			if scenario.free {
				assertPremiumPaidCardUsagePacket(t, connection.write.Bytes(), repos, wantUsed)
			} else {
				for _, packet := range splitAllCurrentUpperPackets(t, connection.write.Bytes()) {
					if packet.Header.MsgID == 903 {
						t.Fatal("普通收费翻牌不应发送次数消耗通知")
					}
				}
			}
			bag, _, err := repos.Inventory.Load(ctx, "99")
			if err != nil || len(bag.Slots) != 1 {
				t.Fatalf("翻牌应只入包一件：slots=%+v err=%v", bag.Slots, err)
			}
			for _, stack := range bag.Slots {
				if stack.ItemID != paid.Items[0].ItemID || stack.Count != 1 {
					t.Fatalf("实际入包数量与单件显示不一致：%+v", stack)
				}
			}
			replay, selection, err := state.reserveSelection(dungeonCardSidePaid, 0)
			if err != nil || selection != dungeonCardSelectionReplay || replay.grant {
				t.Fatalf("重复点击重新授权奖励：selection=%d grant=%t err=%v", selection, replay.grant, err)
			}
			if _, err := deliverDungeonCardReservation(ctx, state, replay, repos, 0, now); err != nil {
				t.Fatal(err)
			}
			replayedCharacter, _, _ := repos.Character.Load(ctx, "99")
			replayedBag, _, _ := repos.Inventory.Load(ctx, "99")
			if !reflect.DeepEqual(grantedCharacter, replayedCharacter) || !reflect.DeepEqual(bag, replayedBag) {
				t.Fatal("重复点击改变金币、契约次数或库存")
			}
			t.Logf("真实冻结与op35一致：场景=%s level=%d 资源原价=%d 冻结/显示价=%d 已用次数=%d，来源=%s [gold card cost table]",
				scenario.name, fixtureLevel, listPrice, want, scenario.used, currentDungeonClearRewardRulePath)
		})
	}
}

func paidCardPriceFromProducedOp35(t *testing.T, body []byte) uint32 {
	t.Helper()
	reader := settlementProtocolReader{body: body}
	for index := 0; index < 4; index++ {
		reader.u32()
	}
	reader.u8()
	for index := 0; index < currentDungeonClearRewardBonusFieldCount; index++ {
		reader.u32()
	}
	skipPairs := func(count uint32) {
		for index := uint32(0); index < count; index++ {
			reader.u32()
			reader.u32()
		}
	}
	skipPairs(uint32(reader.u8()))
	skipPairs(uint32(reader.u8()))
	for index := 0; index < currentDungeonClearRewardPostBaseFieldCount+currentDungeonClearRewardScoreFieldCount; index++ {
		reader.u32()
	}
	skipPairs(reader.u32())
	for count := reader.u8(); count > 0; count-- {
		reader.u16()
		reader.u32()
		reader.u32()
		reader.u16()
		reader.u8()
		reader.u16()
	}
	for index := 0; index < currentDungeonClearRewardGroupCount; index++ {
		skipPairs(uint32(reader.u8()))
	}
	return reader.u32()
}
