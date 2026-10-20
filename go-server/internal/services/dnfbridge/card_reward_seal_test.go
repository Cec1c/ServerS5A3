package dnfbridge

import (
	"context"
	"fmt"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 固定翻牌候选核对真实装备定义和实际发放状态，不声明当前副本的抽取概率。
func TestRealPVFCardEquipmentSealSurvivesEveryGrantAndOverflow(t *testing.T) {
	catalog := realPackageSealCatalog(t)
	testRealPVFCardEquipmentSealSurvivesEveryGrantAndOverflow(t, catalog, 10016, 1)
}

func testRealPVFCardEquipmentSealSurvivesEveryGrantAndOverflow(t *testing.T, catalog *pvfDungeonDropCatalog, itemID uint32, wantSeal byte) {
	t.Helper()
	definition, err := catalog.ResolveItem(itemID)
	if err != nil || currentNewPVFEquipmentSealFlag(definition) != wantSeal {
		t.Fatalf("真实封装装备定义不可用：definition=%+v err=%v", definition, err)
	}
	catalog.clearCardEquipmentLoaded = true
	catalog.clearCardEquipment = make(map[dungeonDropGradeRarityKey][]dungeonDropWeightedItem)
	for rarity := int64(0); rarity <= 3; rarity++ {
		catalog.clearCardEquipment[dungeonDropGradeRarityKey{Grade: 1, Rarity: rarity}] = []dungeonDropWeightedItem{{ItemID: itemID, Weight: 1}}
	}
	for _, route := range []string{"免费", "付费", "付费双倍", "黑钻额外", "锦标赛"} {
		for mode := 0; mode < 3; mode++ {
			t.Run(fmt.Sprintf("%s/%d", route, mode), func(t *testing.T) {
				ctx := context.Background()
				repositories := dnfrepomemory.NewMemoryGroup()
				if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90, Stats: map[string]int64{"gold": 100}}); err != nil {
					t.Fatal(err)
				}
				slots := make(map[string]dnfrepo.ItemStack)
				if mode > 0 {
					for slot := int16(9); slot <= 64; slot++ {
						slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
					}
				}
				if mode > 1 {
					for slot := int16(3); slot <= 8; slot++ {
						slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
					}
				}
				if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: slots}); err != nil {
					t.Fatal(err)
				}
				service := currentBoosterTestService(catalog, repositories)
				session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
				service.bindGameSessionCharacter(session, 19)
				item, found, err := currentDungeonRandomCardItem(newCurrentDungeonDropLCG(1), catalog, 1, dungeonCardPVFItemDropReference{ValueA: 0, ValueB: 1}, time.Now().UTC())
				if err != nil || !found || item.ItemID != int64(itemID) {
					t.Fatalf("翻牌新装备创建失败：found=%t item=%+v err=%v", found, item, err)
				}
				bundle := dungeonCardRewardBundle{Items: []dungeonCardItemReward{item}}
				if route == "锦标赛" {
					bundle, err = currentTournamentRewardBundle(tournamentCardReward{ItemID: int64(itemID), StackCount: 1}, &definition)
					if err != nil {
						t.Fatal(err)
					}
				}
				side := dungeonCardSideFree
				free, paid := bundle, dungeonCardRewardBundle{}
				wantCount := 1
				if route == "付费" || route == "付费双倍" {
					side = dungeonCardSidePaid
					free, paid = dungeonCardRewardBundle{}, bundle
					if route == "付费双倍" {
						if err := applyCurrentDungeonGoldCardItemMultiplier(&paid); err != nil {
							t.Fatal(err)
						}
						wantCount = 2
					}
				}
				plan, err := newDungeonCardRewardPlan(dungeonCardPlanIdentity{CharacterID: "19", DungeonID: 11, RunSeed: 1}, "封装翻牌隔离验收", free, paid)
				if err != nil {
					t.Fatal(err)
				}
				if route == "黑钻额外" {
					plan.BlackDiamondBonus = cloneDungeonCardRewardBundle(bundle)
					wantCount = 2
				}
				state, err := newDungeonCardState(plan)
				if err != nil {
					t.Fatal(err)
				}
				reservation, _, err := state.reserveSelection(side, 0)
				if err != nil {
					t.Fatal(err)
				}
				result, err := deliverDungeonCardReservation(ctx, state, reservation, repositories, 0, time.Now().UTC())
				if err != nil || len(result.ItemSlots) != wantCount {
					t.Fatalf("翻牌发放失败：result=%+v err=%v", result, err)
				}
				if mode == 2 {
					claimPackageSealOverflow(t, service, repositories, session, result.OverflowMailID)
				}
				assertPackageSealInventoryAndWire(t, service, repositories, session, itemID, wantSeal, mode%2)
				inventory, _, _ := repositories.Inventory.Load(ctx, "19")
				count := 0
				for _, stack := range inventory.Slots {
					if stack.ItemID == int64(itemID) {
						if stack.Count != 1 {
							t.Fatal("翻牌装备没有按独立实例发放")
						}
						count++
					}
				}
				if count != wantCount {
					t.Fatalf("翻牌装备实例数错误：got=%d want=%d", count, wantCount)
				}
				replay, _, err := state.reserveSelection(side, 0)
				if err != nil || replay.grant {
					t.Fatalf("重复翻牌产生了二次发放：grant=%t err=%v", replay.grant, err)
				}
			})
		}
	}
}
