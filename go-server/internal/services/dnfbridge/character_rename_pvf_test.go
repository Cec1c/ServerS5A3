package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/characterdata"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件是 currentRenameCardRulesFromPVF / commitCurrentRenameCard 的真档固定验证：
// 只在 DNFBRIDGE_REAL_PVF_SMOKE 指向线上同源的 Script.pvf 时运行，catalog 的打开
// 方式与仓库既有 real-PVF 用例（newPVFCeraShopCatalog）逐字一致。
//
// 真档口径（全部逐条断言，取值来自 Script.pvf 本身，不来自产品代码的常量）：
//
//	2660698   实物卡 [waste] + [character name change]：最低 1 级、[all] 职业、
//	          冷却 0、原名保留 0
//	2682290   商城卡 stackable/cash/chn_character_rename.stk（[etc]）：最低 30 级、
//	          冷却 90 天、原名保留 180 天
//	490006669 [waste] + [character name change]：最低 1 级、只认 [demonic lancer]
//	25 / 490001375 / 490006664 都不是角色更名卡：必须被拒，不许被当成更名卡
//
// 使用口径：实物卡一次只扣 1 张；背包里的 15 个 10015553（更名卡的 NPC 兑换配方材料）
// 与金币都不能被这张卡再收一次费。locked / 最低等级 / 职业 / 到期实例这几条边界全部走
// 产品自己的 helper（itemlock / itemplacement / 到期投影），测试不另造一套判据。
const (
	realPVFRenameCardWasteItemID     = uint32(2660698)
	realPVFRenameCardShopItemID      = uint32(2682290)
	realPVFRenameCardJobLockedItemID = uint32(490006669)
	realPVFRenameCardShopPVFPath     = "stackable/cash/chn_character_rename.stk"
	realPVFRenameCardPetCreatureID   = uint32(25)
	realPVFRenameCardGuildItemID     = uint32(490001375)
	realPVFRenameCardGiftBoxItemID   = uint32(490006664)
	realPVFRenameCardRecipeItemID    = int64(10015553)
	realPVFRenameCardRecipeCount     = int64(15)
	realPVFRenameCardAccountID       = "account-1"
	realPVFRenameCardCharacterID     = "19"
	realPVFRenameCardGoldBaseline    = int64(1234567)
	realPVFRenameCardFullLevel       = 90
	realPVFRenameCardShopMinLevel    = 30
)

// realPVFRenameCardRejectedItemIDs 是真档里"看着像更名卡、实际不是"的三件：
// 25 = 宠物更名卡（[creature] 家族）、490001375 = 公会相关、490006664 = 礼盒。
var realPVFRenameCardRejectedItemIDs = []uint32{
	realPVFRenameCardPetCreatureID,
	realPVFRenameCardGuildItemID,
	realPVFRenameCardGiftBoxItemID,
}

var (
	realRenameCardCatalogOnce sync.Once
	realRenameCardCatalogPVF  *pvfCeraShopCatalog
	realRenameCardCatalogErr  error
	realRenameCardCatalogPath string
)

// realRenameCardCatalog 打开真实运行时 Script.pvf 并构造生产 handler 用的同一份
// cera shop catalog；同一个测试进程里只读一次（真档一百多 MB，重复打开没有收益）。
func realRenameCardCatalog(t *testing.T) *pvfCeraShopCatalog {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	realRenameCardCatalogOnce.Do(func() {
		realRenameCardCatalogPath = path
		archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
		if err != nil {
			realRenameCardCatalogErr = fmt.Errorf("load real Script.pvf: %w", err)
			return
		}
		realRenameCardCatalogPVF, realRenameCardCatalogErr = newPVFCeraShopCatalog(archive)
	})
	if realRenameCardCatalogErr != nil {
		t.Fatalf("%v (path=%s)", realRenameCardCatalogErr, realRenameCardCatalogPath)
	}
	return realRenameCardCatalogPVF
}

// realRenameCardLogDefinition 把真档里这件道具的形状如实打出来（名称/家族/动作/
// 段位/堆叠上限），供报告逐条对照；道具不在道具表里时返回 false 并同样留痕。
func realRenameCardLogDefinition(t *testing.T, catalog *pvfCeraShopCatalog, itemID uint32) (dungeonDropItemDefinition, bool) {
	t.Helper()
	definition, err := catalog.items.ResolveItem(itemID)
	if err != nil {
		t.Logf("item=%d 不在当前 Script.pvf 的道具表里: %v", itemID, err)
		return dungeonDropItemDefinition{}, false
	}
	name := ""
	if document, readErr := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath); readErr == nil {
		name, _ = document.Text("name")
	}
	t.Logf("item=%d kind=%s path=%s name=%q stackable=%q action=%q slot=%d..%d stack_limit=%d",
		itemID, definition.Kind, definition.PVFPath, strings.TrimSpace(name), definition.StackableType,
		definition.ActionType, definition.SlotStart, definition.SlotEnd, definition.StackLimit)
	return definition, true
}

// realRenameCardOpenSlot 用产品自己的容量规则（itemplacement.SlotOpen）在真档给这件
// 道具算出的分段里挑一个当前开放的格；测试不自己发明槽位。
func realRenameCardOpenSlot(t *testing.T, definition dungeonDropItemDefinition, taken map[int16]bool) int16 {
	t.Helper()
	for slot := definition.SlotStart; slot <= definition.SlotEnd; slot++ {
		if taken[slot] {
			continue
		}
		if itemplacement.SlotOpen(dnfrepo.MainInventoryListType, slot, nil) {
			return slot
		}
	}
	t.Fatalf("item=%d slot=%d..%d 里没有当前开放的格", definition.ItemID, definition.SlotStart, definition.SlotEnd)
	return -1
}

// realRenameCardSeedRepositories 造一个只含一个角色与给定背包槽位的内存仓储：
// UseRenameCard 事务要的账号 / 角色 / 背包 / 设置四项都在 memory group 里。
func realRenameCardSeedRepositories(
	t *testing.T,
	job string,
	level int,
	slots map[string]dnfrepo.ItemStack,
) (context.Context, dnfrepo.Group) {
	t.Helper()
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: realPVFRenameCardAccountID}); err != nil {
		t.Fatalf("save account: %v", err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: realPVFRenameCardCharacterID,
		AccountID:   realPVFRenameCardAccountID,
		Job:         job,
		Level:       level,
		Stats:       map[string]int64{"delete_flag": 0, "gold": realPVFRenameCardGoldBaseline},
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: realPVFRenameCardCharacterID,
		Slots:       slots,
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	return ctx, repositories
}

func realRenameCardSlotKey(slot int16) string {
	return fmt.Sprintf("%d:%d", dnfrepo.MainInventoryListType, slot)
}

// realRenameCardBagStack 读回背包里某一格，用于"拒绝时一个字节都不许变"的断言。
func realRenameCardBagStack(
	t *testing.T,
	ctx context.Context,
	repositories dnfrepo.Group,
	slot int16,
) (dnfrepo.ItemStack, bool) {
	t.Helper()
	bag, found, err := repositories.Inventory.Load(ctx, realPVFRenameCardCharacterID)
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if !found {
		t.Fatalf("inventory row %s is missing", realPVFRenameCardCharacterID)
	}
	stack, exists := bag.Slots[realRenameCardSlotKey(slot)]
	return stack, exists
}

func realRenameCardCharacter(t *testing.T, ctx context.Context, repositories dnfrepo.Group) dnfrepo.CharacterRecord {
	t.Helper()
	character, found, err := repositories.Character.Load(ctx, realPVFRenameCardCharacterID)
	if err != nil {
		t.Fatalf("load character: %v", err)
	}
	if !found || character.AccountID != realPVFRenameCardAccountID {
		t.Fatalf("character row = %+v found=%t", character, found)
	}
	return character
}

// realRenameCardStack 按真档定义造一格实物（Extra 用真档自己的 path / 家族）。
func realRenameCardStack(definition dungeonDropItemDefinition, count int64) dnfrepo.ItemStack {
	return dnfrepo.ItemStack{
		ItemID: int64(definition.ItemID),
		Count:  count,
		Extra: map[string]string{
			"item_kind":      string(definition.Kind),
			"pvf_path":       definition.PVFPath,
			"stackable_type": definition.StackableType,
		},
	}
}

// TestRealPVFRenameCardRules 钉死真档四件道具的规则：三件更名卡的 path / 家族 /
// 动作 / 最低等级 / 职业 / 冷却 / 保留期逐项断言，三件非更名卡必须拒绝。
func TestRealPVFRenameCardRules(t *testing.T) {
	catalog := realRenameCardCatalog(t)
	shopCooldown := int64(characterdata.RenameCooldown / time.Second)
	shopReservation := int64(characterdata.PreviousNameReservation / time.Second)
	if shopCooldown != 90*24*60*60 || shopReservation != 180*24*60*60 {
		t.Fatalf("90 天/180 天基线 = %d/%d 秒", shopCooldown, shopReservation)
	}
	cases := []struct {
		name            string
		itemID          uint32
		wantPath        string
		wantStackable   string
		wantAction      string
		wantMinimum     int64
		wantJobs        []string
		wantCooldown    int64
		wantReservation int64
	}{
		{
			name:          "实物卡 2660698",
			itemID:        realPVFRenameCardWasteItemID,
			wantStackable: "[waste]",
			wantAction:    "[character name change]",
			wantMinimum:   1,
			wantJobs:      []string{"[all]"},
		},
		{
			name:            "商城卡 2682290",
			itemID:          realPVFRenameCardShopItemID,
			wantPath:        realPVFRenameCardShopPVFPath,
			wantStackable:   "[etc]",
			wantMinimum:     realPVFRenameCardShopMinLevel,
			wantJobs:        []string{"[all]"},
			wantCooldown:    shopCooldown,
			wantReservation: shopReservation,
		},
		{
			name:          "限定职业卡 490006669",
			itemID:        realPVFRenameCardJobLockedItemID,
			wantStackable: "[waste]",
			wantAction:    "[character name change]",
			wantMinimum:   1,
			wantJobs:      []string{"[demonic lancer]"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			definition, resolved := realRenameCardLogDefinition(t, catalog, testCase.itemID)
			if !resolved {
				t.Fatalf("item=%d 解析失败", testCase.itemID)
			}
			rules, err := currentRenameCardRulesFromPVF(catalog, testCase.itemID)
			if err != nil {
				t.Fatalf("currentRenameCardRulesFromPVF(%d): %v", testCase.itemID, err)
			}
			if rules.definition.Kind != dungeonDropItemStackable {
				t.Fatalf("item=%d kind=%s want stackable", testCase.itemID, rules.definition.Kind)
			}
			if testCase.wantPath != "" && !strings.EqualFold(rules.definition.PVFPath, testCase.wantPath) {
				t.Fatalf("item=%d path=%q want %q", testCase.itemID, rules.definition.PVFPath, testCase.wantPath)
			}
			if rules.definition.StackableType != testCase.wantStackable {
				t.Fatalf("item=%d stackable=%q want %q", testCase.itemID, rules.definition.StackableType, testCase.wantStackable)
			}
			if testCase.wantAction != "" && rules.definition.ActionType != testCase.wantAction {
				t.Fatalf("item=%d action=%q want %q", testCase.itemID, rules.definition.ActionType, testCase.wantAction)
			}
			if rules.minimumLevel != testCase.wantMinimum {
				t.Fatalf("item=%d minimum_level=%d want %d", testCase.itemID, rules.minimumLevel, testCase.wantMinimum)
			}
			if testCase.wantJobs != nil {
				if len(rules.jobs) != len(testCase.wantJobs) {
					t.Fatalf("item=%d usable job=%v want %v", testCase.itemID, rules.jobs, testCase.wantJobs)
				}
				for index, want := range testCase.wantJobs {
					if rules.jobs[index] != want {
						t.Fatalf("item=%d usable job[%d]=%q want %q (all=%v)", testCase.itemID, index, rules.jobs[index], want, rules.jobs)
					}
				}
			}
			if rules.cooldownSeconds != testCase.wantCooldown || rules.reservationSeconds != testCase.wantReservation {
				t.Fatalf("item=%d cooldown=%ds reservation=%ds want %ds/%ds",
					testCase.itemID, rules.cooldownSeconds, rules.reservationSeconds, testCase.wantCooldown, testCase.wantReservation)
			}
			// 段位（选格范围）必须来自真档 stackable 家族：零值会让 policy.Slots 变成空段。
			if rules.definition.SlotStart <= 0 || rules.definition.SlotEnd < rules.definition.SlotStart {
				t.Fatalf("item=%d slot=%d..%d", testCase.itemID, rules.definition.SlotStart, rules.definition.SlotEnd)
			}
			t.Logf("item=%d path=%s minimum=%d jobs=%v cooldown=%ds reservation=%ds slot=%d..%d",
				testCase.itemID, definition.PVFPath, rules.minimumLevel, rules.jobs,
				rules.cooldownSeconds, rules.reservationSeconds, rules.definition.SlotStart, rules.definition.SlotEnd)
		})
	}
	t.Run("非更名卡必须拒绝", func(t *testing.T) {
		for _, itemID := range realPVFRenameCardRejectedItemIDs {
			realRenameCardLogDefinition(t, catalog, itemID)
			rules, err := currentRenameCardRulesFromPVF(catalog, itemID)
			if !errors.Is(err, characterdata.ErrRenameUnavailable) {
				t.Fatalf("item=%d err=%v want %v (rules=%+v)", itemID, err, characterdata.ErrRenameUnavailable, rules)
			}
			if rules.minimumLevel != 0 || len(rules.jobs) != 0 {
				t.Fatalf("item=%d 被拒绝却带出规则: %+v", itemID, rules)
			}
			t.Logf("item=%d 拒绝理由=%v", itemID, err)
		}
	})
}

// TestRealPVFRenameCardUseChargesOnlyOneCard 用真档实物卡走一次完整结算：只扣 1 张卡，
// 15 个 10015553 材料与金币一个字节都不动，资格与 0 冷却/0 保留期落库。
func TestRealPVFRenameCardUseChargesOnlyOneCard(t *testing.T) {
	catalog := realRenameCardCatalog(t)
	rules, err := currentRenameCardRulesFromPVF(catalog, realPVFRenameCardWasteItemID)
	if err != nil {
		t.Fatalf("resolve real card %d: %v", realPVFRenameCardWasteItemID, err)
	}
	if rules.minimumLevel > realPVFRenameCardFullLevel {
		t.Fatalf("card minimum level %d exceeds fixture level %d", rules.minimumLevel, realPVFRenameCardFullLevel)
	}
	// 真档的堆叠上限决定这一格能放几张（只取 1..3 张，够判"只扣 1"即可）。
	cardCount := rules.definition.StackLimit
	if cardCount < 1 {
		cardCount = 1
	}
	if cardCount > 3 {
		cardCount = 3
	}
	cardSlot := realRenameCardOpenSlot(t, rules.definition, nil)
	materialSlot := int16(121)
	taken := map[int16]bool{cardSlot: true}
	if definition, resolveErr := catalog.items.ResolveItem(uint32(realPVFRenameCardRecipeItemID)); resolveErr == nil {
		materialSlot = realRenameCardOpenSlot(t, definition, taken)
		t.Logf("材料 %d 真档 path=%s slot=%d..%d 放在 %d", realPVFRenameCardRecipeItemID,
			definition.PVFPath, definition.SlotStart, definition.SlotEnd, materialSlot)
	} else {
		t.Logf("材料 %d 不在当前 Script.pvf 的道具表里(%v)，用材料页首格 %d 做保留性断言",
			realPVFRenameCardRecipeItemID, resolveErr, materialSlot)
	}
	slots := map[string]dnfrepo.ItemStack{
		realRenameCardSlotKey(cardSlot): realRenameCardStack(rules.definition, cardCount),
		realRenameCardSlotKey(materialSlot): {
			ItemID: realPVFRenameCardRecipeItemID,
			Count:  realPVFRenameCardRecipeCount,
			Extra:  map[string]string{"item_kind": "stackable"},
		},
	}
	ctx, repositories := realRenameCardSeedRepositories(t, "0", realPVFRenameCardFullLevel, slots)
	now := time.Now().UTC()
	updates, err := commitCurrentRenameCard(ctx, repositories, catalog, realPVFRenameCardAccountID, realPVFRenameCardCharacterID, cardSlot, now)
	if err != nil {
		t.Fatalf("commitCurrentRenameCard(card=%d slot=%d): %v", realPVFRenameCardWasteItemID, cardSlot, err)
	}
	if len(updates) != 1 {
		t.Fatalf("updates=%d want 1", len(updates))
	}
	if got := int16(binary.LittleEndian.Uint16(updates[0].data[0x00:0x02])); got != cardSlot {
		t.Fatalf("update slot=%d want %d", got, cardSlot)
	}

	card, cardExists := realRenameCardBagStack(t, ctx, repositories, cardSlot)
	if cardCount == 1 {
		if cardExists {
			t.Fatalf("count=1 的卡用掉后必须整格消失: %+v", card)
		}
	} else if !cardExists || card.ItemID != int64(realPVFRenameCardWasteItemID) || card.Count != cardCount-1 {
		t.Fatalf("用一张卡后该格=%+v exists=%t want count=%d", card, cardExists, cardCount-1)
	}
	if cardExists {
		if got := binary.LittleEndian.Uint32(updates[0].data[0x02:0x06]); got != realPVFRenameCardWasteItemID {
			t.Fatalf("update item=%d want %d", got, realPVFRenameCardWasteItemID)
		}
		if got := binary.LittleEndian.Uint32(updates[0].data[0x06:0x0A]); got != uint32(cardCount-1) {
			t.Fatalf("update amount=%d want %d", got, cardCount-1)
		}
	}

	material, materialExists := realRenameCardBagStack(t, ctx, repositories, materialSlot)
	if !materialExists || material.ItemID != realPVFRenameCardRecipeItemID || material.Count != realPVFRenameCardRecipeCount {
		t.Fatalf("更名卡没有吃掉配方材料 %d: %+v exists=%t want count=%d",
			realPVFRenameCardRecipeItemID, material, materialExists, realPVFRenameCardRecipeCount)
	}
	character := realRenameCardCharacter(t, ctx, repositories)
	if character.Stats["gold"] != realPVFRenameCardGoldBaseline {
		t.Fatalf("更名卡额外收费了: gold=%d want %d", character.Stats["gold"], realPVFRenameCardGoldBaseline)
	}
	if character.Stats[characterdata.RenameEntitlementStat] != 1 ||
		character.Stats[characterdata.RenameCardSlotStat] != int64(cardSlot) ||
		character.Stats[characterdata.RenameMinimumLevelStat] != rules.minimumLevel ||
		character.Stats[characterdata.RenameCooldownSecondsStat] != 0 ||
		character.Stats[characterdata.RenameReservationSecondsStat] != 0 {
		t.Fatalf("实物卡的资格/策略落库 = %+v (rules=%+v slot=%d)", character.Stats, rules, cardSlot)
	}
	// 真档的 NPC 兑换配方（[need material]）不是使用费：这张卡的配方就是背包里那 15 个
	// 10015553，而上面已经证明用完一张卡之后它一个都没少。
	document, docErr := parseDungeonCardPVFDocument(catalog.source, rules.definition.PVFPath)
	if docErr != nil {
		t.Fatalf("read real card %d document %s: %v", realPVFRenameCardWasteItemID, rules.definition.PVFPath, docErr)
	}
	recipe := document.Ints("need material")
	if len(recipe) != 2 || recipe[0] != realPVFRenameCardRecipeItemID || recipe[1] != realPVFRenameCardRecipeCount {
		t.Fatalf("真档 [need material]=%v want [%d %d]", recipe, realPVFRenameCardRecipeItemID, realPVFRenameCardRecipeCount)
	}
	t.Logf("item=%d path=%s [need material]=%v [usable job]=%v [action usable place]=%v",
		realPVFRenameCardWasteItemID, rules.definition.PVFPath, recipe,
		document.Texts("usable job"), document.Texts("action usable place"))
}

// TestRealPVFRenameCardUseWithTwoCardsKeepsOne 覆盖叠放那一条：真档这张卡没有
// [stack limit]，把 2 张放在同一格再使用一次，只许扣 1 张（剩 1 张），配方材料与金币
// 同样不动。
func TestRealPVFRenameCardUseWithTwoCardsKeepsOne(t *testing.T) {
	catalog := realRenameCardCatalog(t)
	rules, err := currentRenameCardRulesFromPVF(catalog, realPVFRenameCardWasteItemID)
	if err != nil {
		t.Fatalf("resolve real card %d: %v", realPVFRenameCardWasteItemID, err)
	}
	cardSlot := realRenameCardOpenSlot(t, rules.definition, nil)
	materialSlot := int16(121)
	slots := map[string]dnfrepo.ItemStack{
		realRenameCardSlotKey(cardSlot): realRenameCardStack(rules.definition, 2),
		realRenameCardSlotKey(materialSlot): {
			ItemID: realPVFRenameCardRecipeItemID,
			Count:  realPVFRenameCardRecipeCount,
			Extra:  map[string]string{"item_kind": "stackable"},
		},
	}
	ctx, repositories := realRenameCardSeedRepositories(t, "0", realPVFRenameCardFullLevel, slots)
	updates, err := commitCurrentRenameCard(ctx, repositories, catalog,
		realPVFRenameCardAccountID, realPVFRenameCardCharacterID, cardSlot, time.Now().UTC())
	if err != nil {
		t.Fatalf("commitCurrentRenameCard(2 张卡 slot=%d): %v", cardSlot, err)
	}
	if len(updates) != 1 {
		t.Fatalf("updates=%d want 1", len(updates))
	}
	if got := binary.LittleEndian.Uint32(updates[0].data[0x02:0x06]); got != realPVFRenameCardWasteItemID {
		t.Fatalf("update item=%d want %d", got, realPVFRenameCardWasteItemID)
	}
	if got := binary.LittleEndian.Uint32(updates[0].data[0x06:0x0A]); got != 1 {
		t.Fatalf("update amount=%d want 1（只扣 1 张）", got)
	}
	card, cardExists := realRenameCardBagStack(t, ctx, repositories, cardSlot)
	if !cardExists || card.ItemID != int64(realPVFRenameCardWasteItemID) || card.Count != 1 {
		t.Fatalf("2 张卡用掉一张后该格=%+v exists=%t want count=1", card, cardExists)
	}
	material, materialExists := realRenameCardBagStack(t, ctx, repositories, materialSlot)
	if !materialExists || material.ItemID != realPVFRenameCardRecipeItemID || material.Count != realPVFRenameCardRecipeCount {
		t.Fatalf("更名卡没有吃掉配方材料 %d: %+v exists=%t want count=%d",
			realPVFRenameCardRecipeItemID, material, materialExists, realPVFRenameCardRecipeCount)
	}
	character := realRenameCardCharacter(t, ctx, repositories)
	if character.Stats["gold"] != realPVFRenameCardGoldBaseline {
		t.Fatalf("更名卡额外收费了: gold=%d want %d", character.Stats["gold"], realPVFRenameCardGoldBaseline)
	}
	if character.Stats[characterdata.RenameCooldownSecondsStat] != rules.cooldownSeconds ||
		character.Stats[characterdata.RenameReservationSecondsStat] != rules.reservationSeconds {
		t.Fatalf("实物卡的冷却/保留期落库 = %+v (rules=%+v)", character.Stats, rules)
	}
}

// TestRealPVFRenameCardBoundariesFollowRealHelpers 是真档边界：locked、最低等级、
// 职业、到期实例四条都用产品自己的 helper 造实例与判据，正面/反面成对断言。
func TestRealPVFRenameCardBoundariesFollowRealHelpers(t *testing.T) {
	catalog := realRenameCardCatalog(t)
	wasteRules, err := currentRenameCardRulesFromPVF(catalog, realPVFRenameCardWasteItemID)
	if err != nil {
		t.Fatalf("resolve real card %d: %v", realPVFRenameCardWasteItemID, err)
	}
	shopRules, err := currentRenameCardRulesFromPVF(catalog, realPVFRenameCardShopItemID)
	if err != nil {
		t.Fatalf("resolve real card %d: %v", realPVFRenameCardShopItemID, err)
	}
	jobRules, err := currentRenameCardRulesFromPVF(catalog, realPVFRenameCardJobLockedItemID)
	if err != nil {
		t.Fatalf("resolve real card %d: %v", realPVFRenameCardJobLockedItemID, err)
	}
	wasteSlot := realRenameCardOpenSlot(t, wasteRules.definition, nil)
	shopSlot := realRenameCardOpenSlot(t, shopRules.definition, nil)
	jobSlot := realRenameCardOpenSlot(t, jobRules.definition, nil)

	t.Run("locked 实物卡拒绝且零消耗", func(t *testing.T) {
		stack := realRenameCardStack(wasteRules.definition, 3)
		stack.Extra["equipment_lock_state"] = "1"
		if !currentDungeonDiscardStackLocked(stack) {
			t.Fatalf("产品自己的锁判据没有认出这格: %+v", stack.Extra)
		}
		ctx, repositories := realRenameCardSeedRepositories(t, "0", realPVFRenameCardFullLevel,
			map[string]dnfrepo.ItemStack{realRenameCardSlotKey(wasteSlot): stack})
		_, err := commitCurrentRenameCard(ctx, repositories, catalog,
			realPVFRenameCardAccountID, realPVFRenameCardCharacterID, wasteSlot, time.Now().UTC())
		if !errors.Is(err, characterdata.ErrRenameUnavailable) {
			t.Fatalf("locked 卡 err=%v want %v", err, characterdata.ErrRenameUnavailable)
		}
		card, exists := realRenameCardBagStack(t, ctx, repositories, wasteSlot)
		if !exists || card.Count != 3 {
			t.Fatalf("locked 卡被消耗了: %+v exists=%t", card, exists)
		}
		character := realRenameCardCharacter(t, ctx, repositories)
		if character.Stats[characterdata.RenameEntitlementStat] != 0 {
			t.Fatalf("locked 卡给出了资格: %+v", character.Stats)
		}
	})

	t.Run("最低等级边界", func(t *testing.T) {
		if shopRules.minimumLevel != realPVFRenameCardShopMinLevel {
			t.Fatalf("商城卡 minimum=%d want %d", shopRules.minimumLevel, realPVFRenameCardShopMinLevel)
		}
		belowCtx, belowRepositories := realRenameCardSeedRepositories(t, "0", realPVFRenameCardShopMinLevel-1,
			map[string]dnfrepo.ItemStack{realRenameCardSlotKey(shopSlot): realRenameCardStack(shopRules.definition, 1)})
		if _, err := commitCurrentRenameCard(belowCtx, belowRepositories, catalog,
			realPVFRenameCardAccountID, realPVFRenameCardCharacterID, shopSlot, time.Now().UTC()); !errors.Is(err, characterdata.ErrRenameUnavailable) {
			t.Fatalf("等级 %d < 最低 %d 竟然通过: %v", realPVFRenameCardShopMinLevel-1, shopRules.minimumLevel, err)
		}
		if card, exists := realRenameCardBagStack(t, belowCtx, belowRepositories, shopSlot); !exists || card.Count != 1 {
			t.Fatalf("低等级被拒却动了卡: %+v exists=%t", card, exists)
		}
		// 正面：刚好到最低等级必须通过，证明拒绝的原因是等级而不是别的。
		ctx, repositories := realRenameCardSeedRepositories(t, "0", realPVFRenameCardShopMinLevel,
			map[string]dnfrepo.ItemStack{realRenameCardSlotKey(shopSlot): realRenameCardStack(shopRules.definition, 1)})
		if _, err := commitCurrentRenameCard(ctx, repositories, catalog,
			realPVFRenameCardAccountID, realPVFRenameCardCharacterID, shopSlot, time.Now().UTC()); err != nil {
			t.Fatalf("等级 %d = 最低 %d 被拒: %v", realPVFRenameCardShopMinLevel, shopRules.minimumLevel, err)
		}
		character := realRenameCardCharacter(t, ctx, repositories)
		if character.Stats[characterdata.RenameCooldownSecondsStat] != int64(characterdata.RenameCooldown/time.Second) ||
			character.Stats[characterdata.RenameReservationSecondsStat] != int64(characterdata.PreviousNameReservation/time.Second) {
			t.Fatalf("商城卡的 90 天/180 天没有落库: %+v", character.Stats)
		}
	})

	t.Run("职业边界", func(t *testing.T) {
		// 真档这张卡只认 [demonic lancer]（job 13）。
		otherCtx, otherRepositories := realRenameCardSeedRepositories(t, "0", realPVFRenameCardFullLevel,
			map[string]dnfrepo.ItemStack{realRenameCardSlotKey(jobSlot): realRenameCardStack(jobRules.definition, 1)})
		if _, err := commitCurrentRenameCard(otherCtx, otherRepositories, catalog,
			realPVFRenameCardAccountID, realPVFRenameCardCharacterID, jobSlot, time.Now().UTC()); !errors.Is(err, characterdata.ErrRenameUnavailable) {
			t.Fatalf("job=0 用了只认 %v 的卡: err=%v", jobRules.jobs, err)
		}
		if card, exists := realRenameCardBagStack(t, otherCtx, otherRepositories, jobSlot); !exists || card.Count != 1 {
			t.Fatalf("错误职业被拒却动了卡: %+v exists=%t", card, exists)
		}
		ctx, repositories := realRenameCardSeedRepositories(t, "13", realPVFRenameCardFullLevel,
			map[string]dnfrepo.ItemStack{realRenameCardSlotKey(jobSlot): realRenameCardStack(jobRules.definition, 1)})
		if _, err := commitCurrentRenameCard(ctx, repositories, catalog,
			realPVFRenameCardAccountID, realPVFRenameCardCharacterID, jobSlot, time.Now().UTC()); err != nil {
			t.Fatalf("job=13 [demonic lancer] 被拒: %v", err)
		}
	})

	t.Run("到期实例边界", func(t *testing.T) {
		now := time.Now().UTC()
		// 真档这张实物卡没有绝对 [expiration date]（零值），所以到期只由实例自己的
		// expire 决定，走的是 currentDungeonDiscardStackExpired 而不是退役续期那条。
		if !wasteRules.definition.ExpirationDate.IsZero() {
			t.Fatalf("item=%d 真档出现了绝对 [expiration date]=%s，边界口径需要重新取证",
				realPVFRenameCardWasteItemID, wasteRules.definition.ExpirationDate.Format(time.RFC3339))
		}
		past := now.Add(-24 * time.Hour)
		expired := realRenameCardStack(wasteRules.definition, 1)
		expired.ExpireAt = past
		expired.Extra["expire_time"] = fmt.Sprintf("%d", past.Unix())
		projected, _ := applyCurrentPVFItemExpirationAt(expired, wasteRules.definition, now)
		t.Logf("item=%d pvf_expiration=%s 过期实例(expire=%d) 投影后 expire=%d helper 判过期=%t",
			realPVFRenameCardWasteItemID, wasteRules.definition.ExpirationDate.Format(time.RFC3339),
			past.Unix(), currentItemListStackExpire(projected), currentDungeonDiscardStackExpired(projected, now))
		ctx, repositories := realRenameCardSeedRepositories(t, "0", realPVFRenameCardFullLevel,
			map[string]dnfrepo.ItemStack{realRenameCardSlotKey(wasteSlot): expired})
		if _, err := commitCurrentRenameCard(ctx, repositories, catalog,
			realPVFRenameCardAccountID, realPVFRenameCardCharacterID, wasteSlot, now); !errors.Is(err, characterdata.ErrRenameUnavailable) {
			t.Fatalf("已过期实例 err=%v want %v", err, characterdata.ErrRenameUnavailable)
		}
		if card, exists := realRenameCardBagStack(t, ctx, repositories, wasteSlot); !exists || card.Count != 1 {
			t.Fatalf("过期实例被消耗了: %+v exists=%t", card, exists)
		}

		future := now.Add(30 * 24 * time.Hour)
		live := realRenameCardStack(wasteRules.definition, 1)
		live.ExpireAt = future
		live.Extra["expire_time"] = fmt.Sprintf("%d", future.Unix())
		liveProjected, _ := applyCurrentPVFItemExpirationAt(live, wasteRules.definition, now)
		if currentDungeonDiscardStackExpired(liveProjected, now) {
			t.Fatalf("未到期实例被 helper 判成过期: expire=%d", currentItemListStackExpire(liveProjected))
		}
		liveCtx, liveRepositories := realRenameCardSeedRepositories(t, "0", realPVFRenameCardFullLevel,
			map[string]dnfrepo.ItemStack{realRenameCardSlotKey(wasteSlot): live})
		if _, err := commitCurrentRenameCard(liveCtx, liveRepositories, catalog,
			realPVFRenameCardAccountID, realPVFRenameCardCharacterID, wasteSlot, now); err != nil {
			t.Fatalf("未到期实例被拒: %v", err)
		}
		character := realRenameCardCharacter(t, liveCtx, liveRepositories)
		if character.Stats[characterdata.RenameEntitlementStat] != 1 {
			t.Fatalf("未到期实例没有给出资格: %+v", character.Stats)
		}
	})
}
