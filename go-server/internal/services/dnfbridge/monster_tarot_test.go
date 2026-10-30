package dnfbridge

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	tarotTestSourceItemID = 900
	tarotTestRewardItemA  = 600
	tarotTestRewardItemB  = 601
	tarotTestSourceSlot   = 75
	tarotTestOpeningCost  = 100000
)

func monsterTarotTestCatalog(t *testing.T, rewards string) (*pvfDungeonDropCatalog, dungeonDropCatalogTestSource) {
	t.Helper()
	_, source := newMagicBoxTestCatalog(t)
	source["stackable/stackable.lst"] += "900 `cash/monster_tarot.stk`\n600 `material/tarot_reward_a.stk`\n601 `material/tarot_reward_b.stk`\n"
	source["stackable/material/tarot_reward_a.stk"] = "[name] `Tarot Reward A`\n[stackable type] `[material]`\n[stack limit] 1000\n"
	source["stackable/material/tarot_reward_b.stk"] = "[name] `Tarot Reward B`\n[stackable type] `[material]`\n[stack limit] 1000\n"
	source["stackable/cash/monster_tarot.stk"] = "[name] `Monster Tarot`\n[stackable type] `[monster tarot card]` 25\n[stack limit] 100\n[lottery use cost]\n100000\n" + rewards
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog, source
}

func monsterTarotTestAssets(t *testing.T, gold int64, sourceCount int64) dnfrepo.Group {
	t.Helper()
	ctx := context.Background()
	repositories := memory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Level:       90,
		Stats:       map[string]int64{"gold": gold, "fatigue": 120},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:75": {ItemID: tarotTestSourceItemID, Count: sourceCount},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return repositories
}

func monsterTarotInventoryCount(inventory dnfrepo.InventoryRecord, itemID int64) int64 {
	var total int64
	for _, stack := range inventory.Slots {
		if stack.ItemID == itemID {
			total += stack.Count
		}
	}
	return total
}

func TestMonsterTarotPVFWeightedPoolAndCountsAreDataDriven(t *testing.T) {
	catalog, source := monsterTarotTestCatalog(t, "[int data]\n600 3 2 601 1 5\n[/int data]\n")
	if !currentLotterySupportedType("[monster tarot card]") {
		t.Fatal("monster tarot is not admitted by the lottery family")
	}

	resolution, err := resolveCurrentMagicBox(catalog, source, tarotTestSourceItemID)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Kind != "random" || len(resolution.Groups) != 1 {
		t.Fatalf("resolution=%+v", resolution)
	}
	group := resolution.Groups[0]
	if group.DrawCount != 1 || len(group.Entries) != 2 {
		t.Fatalf("group=%+v", group)
	}
	if group.Entries[0].ItemID != tarotTestRewardItemA || group.Entries[0].Weight != 3 || group.Entries[0].Count != 2 {
		t.Fatalf("entry A=%+v", group.Entries[0])
	}
	if group.Entries[1].ItemID != tarotTestRewardItemB || group.Entries[1].Weight != 1 || group.Entries[1].Count != 5 {
		t.Fatalf("entry B=%+v", group.Entries[1])
	}
	if resolution.MaterialItemID != 0 || resolution.MaterialCountPerUse != 0 {
		t.Fatalf("unexpected extra material=%d x%d", resolution.MaterialItemID, resolution.MaterialCountPerUse)
	}

	definition, err := resolveCurrentBoosterDefinition(catalog, tarotTestSourceItemID, currentBoosterRequestRandom)
	if err != nil {
		t.Fatal(err)
	}
	if definition.StackableType != "[monster tarot card]" || definition.LotteryGoldCost != tarotTestOpeningCost {
		t.Fatalf("definition=%+v", definition)
	}

	for _, testCase := range []struct {
		roll      int64
		wantItem  uint32
		wantCount uint32
	}{
		{roll: 0, wantItem: tarotTestRewardItemA, wantCount: 2},
		{roll: 2, wantItem: tarotTestRewardItemA, wantCount: 2},
		{roll: 3, wantItem: tarotTestRewardItemB, wantCount: 5},
	} {
		t.Run(fmt.Sprintf("roll_%d", testCase.roll), func(t *testing.T) {
			amounts, _, err := resolveCurrentBoosterRewardAmounts(
				definition,
				currentBoosterOpenRequest{Kind: currentBoosterRequestRandom},
				func(int64) (int64, error) { return testCase.roll, nil },
			)
			if err != nil {
				t.Fatal(err)
			}
			want := map[uint32]uint32{testCase.wantItem: testCase.wantCount}
			if !reflect.DeepEqual(amounts, want) {
				t.Fatalf("amounts=%v want=%v", amounts, want)
			}
		})
	}
}

func TestMonsterTarotSingleDrawConsumesOneAndExactlyOpeningFee(t *testing.T) {
	catalog, _ := monsterTarotTestCatalog(t, "[int data]\n600 100 6\n[/int data]\n")
	repositories := monsterTarotTestAssets(t, 234567, 1)
	ctx := context.Background()
	definition, err := resolveCurrentBoosterDefinition(catalog, tarotTestSourceItemID, currentBoosterRequestRandom)
	if err != nil {
		t.Fatal(err)
	}
	service := currentBoosterTestService(catalog, repositories)
	request := currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: tarotTestSourceSlot}
	session := &gameSession{selectedCharacterID: 19}
	if _, err := service.prepareCurrentBooster(ctx, session, catalog, request); err != nil {
		t.Fatal(err)
	}
	result, err := service.commitCurrentBooster(ctx, session, catalog, definition, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceItemID != tarotTestSourceItemID || result.SourceRemaining != 0 || len(result.Rewards) != 1 {
		t.Fatalf("result=%+v", result)
	}
	if result.Rewards[0].ItemID != tarotTestRewardItemA || result.Rewards[0].Count != 6 || result.GoldAmount != 0 {
		t.Fatalf("rewards=%+v goldReward=%d", result.Rewards, result.GoldAmount)
	}
	character, found, err := repositories.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("character found=%t err=%v", found, err)
	}
	if character.Stats["gold"] != 134567 || character.Stats["fatigue"] != 120 {
		t.Fatalf("character stats=%v", character.Stats)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("inventory found=%t err=%v", found, err)
	}
	if _, exists := inventory.Slots["0:75"]; exists {
		t.Fatalf("consumed source remained: %+v", inventory.Slots["0:75"])
	}
	if got := monsterTarotInventoryCount(inventory, tarotTestRewardItemA); got != 6 {
		t.Fatalf("reward count=%d, want 6", got)
	}
}

func TestMonsterTarotFailurePathsPreserveAllAssets(t *testing.T) {
	for _, mode := range []string{"insufficient-gold", "full-bag", "late-failure"} {
		t.Run(mode, func(t *testing.T) {
			catalog, _ := monsterTarotTestCatalog(t, "[int data]\n600 100 1\n[/int data]\n")
			gold := int64(234567)
			if mode == "insufficient-gold" {
				gold = tarotTestOpeningCost - 1
			}
			repositories := monsterTarotTestAssets(t, gold, 1)
			ctx := context.Background()
			if mode == "full-bag" {
				inventory, _, _ := repositories.Inventory.Load(ctx, "19")
				for slot := 3; slot <= 248; slot++ {
					if slot != tarotTestSourceSlot {
						inventory.Slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: 2002, Count: 1}
					}
				}
				if err := repositories.Inventory.Save(ctx, inventory); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "late-failure" {
				repositories.AccountAssets = lotteryFailLateAssets{repositories.AccountAssets}
			}
			beforeCharacter, _, _ := repositories.Character.Load(ctx, "19")
			beforeInventory, _, _ := repositories.Inventory.Load(ctx, "19")
			beforeAccount, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
			definition, err := resolveCurrentBoosterDefinition(catalog, tarotTestSourceItemID, currentBoosterRequestRandom)
			if err != nil {
				t.Fatal(err)
			}
			service := currentBoosterTestService(catalog, repositories)
			_, err = service.commitCurrentBooster(
				ctx,
				&gameSession{selectedCharacterID: 19},
				catalog,
				definition,
				currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: tarotTestSourceSlot},
			)
			if err == nil {
				t.Fatal("invalid or faulted tarot draw succeeded")
			}
			afterCharacter, _, _ := repositories.Character.Load(ctx, "19")
			afterInventory, _, _ := repositories.Inventory.Load(ctx, "19")
			afterAccount, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
			if !reflect.DeepEqual(beforeCharacter, afterCharacter) ||
				!reflect.DeepEqual(beforeInventory, afterInventory) ||
				!reflect.DeepEqual(beforeAccount, afterAccount) {
				t.Fatal("failed tarot draw changed character, inventory, or account assets")
			}
		})
	}
}

func TestMonsterTarotMalformedPoolFailsClosed(t *testing.T) {
	tests := map[string]string{
		"string-token":       "[int data]\n600 100 `not-an-int`\n[/int data]\n",
		"float-token":        "[int data]\n600 1.25 1\n[/int data]\n",
		"duplicate-int-data": "[int data]\n600 100 1\n[/int data]\n[int data]\n601 100 1\n[/int data]\n",
		"zero-item-id":       "[int data]\n0 100 1\n[/int data]\n",
		"negative-item-id":   "[int data]\n-1 100 1\n[/int data]\n",
		"oversize-item-id":   "[int data]\n4294967296 100 1\n[/int data]\n",
		"negative-weight":    "[int data]\n600 -1 1\n[/int data]\n",
		"zero-count":         "[int data]\n600 100 0\n[/int data]\n",
		"truncated-triple":   "[int data]\n600 100\n[/int data]\n",
		"empty-int-data":     "[int data]\n[/int data]\n",
		"missing-int-data":   "",
	}
	for name, rewards := range tests {
		t.Run(name, func(t *testing.T) {
			catalog, source := monsterTarotTestCatalog(t, rewards)
			if _, err := resolveCurrentMagicBox(catalog, source, tarotTestSourceItemID); err == nil {
				t.Fatal("malformed monster tarot pool was accepted by the magic-box parser")
			}
			if _, err := resolveCurrentBoosterDefinition(catalog, tarotTestSourceItemID, currentBoosterRequestRandom); err == nil {
				t.Fatal("malformed monster tarot pool was accepted by the booster route")
			}
		})
	}
}

func TestMonsterTarotIgnoresDoubleJarPrivilege(t *testing.T) {
	catalog, _ := monsterTarotTestCatalog(t, "[int data]\n600 100 3\n[/int data]\n")
	repositories := monsterTarotTestAssets(t, 200000, 1)
	ctx := context.Background()
	account := dnfrepo.AccountRecord{AccountID: "account-1"}
	premium.Upsert(&account, premium.DevilSlotType(premium.DevilSlotDoubleJar), 7*24*60*60, 1, time.Now().UTC())
	if err := repositories.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	service := currentBoosterTestService(catalog, repositories)
	service.options.currentClientVersion = "official-24.9"
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	// mode=1 asks for the double-jar privilege. Packet response shape is
	// intentionally not asserted here because opcode ownership is still being
	// established by the client-side investigation.
	if err := service.handleCurrentLotteryItem(session, []byte{1, 0, tarotTestSourceSlot, 0}); err != nil {
		t.Fatal(err)
	}
	character, found, err := repositories.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("character found=%t err=%v", found, err)
	}
	if character.Stats["gold"] != 100000 {
		t.Fatalf("gold=%d, want one 100000 fee", character.Stats["gold"])
	}
	if got := premium.DailyUsage(character, premium.DevilSlotDoubleJar, time.Now().UTC()); got != 0 {
		t.Fatalf("double-jar daily usage=%d, want 0 for monster tarot", got)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("inventory found=%t err=%v", found, err)
	}
	if got := monsterTarotInventoryCount(inventory, tarotTestRewardItemA); got != 3 {
		t.Fatalf("reward count=%d, want the single PVF row count 3", got)
	}
}

func TestMonsterTarotCannotBeOpenedFreeThroughOp44(t *testing.T) {
	catalog, _ := monsterTarotTestCatalog(t, "[int data]\n600 100 1\n[/int data]\n")
	repositories := monsterTarotTestAssets(t, 234567, 2)
	ctx := context.Background()
	beforeCharacter, _, _ := repositories.Character.Load(ctx, "19")
	beforeInventory, _, _ := repositories.Inventory.Load(ctx, "19")
	beforeAccount, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
	service := currentBoosterTestService(catalog, repositories)
	session := &gameSession{conn: &bufferConn{}, connID: "monster-tarot-op44-test", selectedCharacterID: 19}
	_ = service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketUseStackable),
		buildRejectedUseStackableBody(tarotTestSourceSlot, 0, 0x11223344, tarotTestSourceItemID),
	)
	afterCharacter, _, _ := repositories.Character.Load(ctx, "19")
	afterInventory, _, _ := repositories.Inventory.Load(ctx, "19")
	afterAccount, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
	if !reflect.DeepEqual(beforeCharacter, afterCharacter) ||
		!reflect.DeepEqual(beforeInventory, afterInventory) ||
		!reflect.DeepEqual(beforeAccount, afterAccount) {
		t.Fatal("op44 changed assets for a paid monster tarot card")
	}
	if source := afterInventory.Slots["0:75"]; source.ItemID != tarotTestSourceItemID || source.Count != 2 {
		t.Fatalf("op44 source=%+v", source)
	}
	if got := monsterTarotInventoryCount(afterInventory, tarotTestRewardItemA); got != 0 {
		t.Fatalf("op44 granted %d tarot rewards without the lottery transaction", got)
	}
}
