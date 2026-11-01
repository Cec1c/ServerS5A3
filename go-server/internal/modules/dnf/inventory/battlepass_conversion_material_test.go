package inventory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	battlePassConversionItemID int64  = 10158964
	battlePassConversionCount  int64  = 3
	battlePassConversionSlot   int16  = 135
	battlePassConversionNPC    uint16 = 476
)

func battlePassConversionKey(kind uint32, param int64) string {
	return fmt.Sprintf("%s%s:%d:%d", dnfrepo.BattlePassCounterPrefix, dnfrepo.ActivityDay(time.Now()), kind, param)
}

func seedBattlePassConversionFixture(t *testing.T, ctx context.Context, repos dnfrepo.Group, withMaterial bool) {
	t.Helper()
	seedUpgradeLateFailureWalletFixture(t, ctx, repos)
	if !withMaterial {
		return
	}
	bag := loadTestInventory(t, ctx, repos, upgradeLateFailureCharacterID)
	bag.Slots[slotKey(listTypeMain, battlePassConversionSlot)] = dnfrepo.ItemStack{
		ItemID: battlePassConversionItemID,
		Count:  battlePassConversionCount,
	}
	saveTestInventory(t, ctx, repos, bag)
}

func battlePassConversionCommand(quote alignedcmd.UpgradeCostResolution) Command {
	return Command{
		Operation:              "upgrade_item",
		Mode:                   "reinforce",
		AccountID:              upgradeLateFailureAccountID,
		SelectedCharacterID:    77,
		TargetSlotIndex:        upgradeLateFailureTargetSlot,
		TargetItemTemplateID:   upgradeLateFailureTargetItem,
		MaterialSlotIndex:      upgradeLateFailureMaterialSlot,
		OptionalTicketSlot:     -1,
		UpgradeSuccessWeight:   currentUpgradeSuccessBase,
		UpgradeMaterialItemID:  upgradeLateFailureMainMaterial,
		UpgradeMaterialCount:   upgradeLateFailureMaterialNeed,
		UpgradeNPCPresent:      true,
		UpgradeNPCID:           battlePassConversionNPC,
		UpgradeNPCMaterialSlot: battlePassConversionSlot,
		UpgradeCostResolver: func(string, int64, int, bool, uint16) (alignedcmd.UpgradeCostResolution, error) {
			return quote, nil
		},
	}
}

func loadBattlePassConversionCharacter(t *testing.T, ctx context.Context, repos dnfrepo.Group) dnfrepo.CharacterRecord {
	t.Helper()
	character, ok, err := repos.Character.Load(ctx, upgradeLateFailureCharacterID)
	if err != nil || !ok {
		t.Fatalf("character load ok=%t err=%v", ok, err)
	}
	return character
}

func TestUpgradeJonathanConversionCountsBattlePassWeeklyMission(t *testing.T) {
	conversionQuote := alignedcmd.UpgradeCostResolution{
		Resolved:         true,
		ConversionItemID: battlePassConversionItemID,
		ConversionCount:  battlePassConversionCount,
	}

	t.Run("success counts the real material identity and deducted amount", func(t *testing.T) {
		ctx := context.Background()
		repos := memory.NewMemoryGroup()
		seedBattlePassConversionFixture(t, ctx, repos, true)
		owner, err := NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}

		got, err := owner.Upgrade(ctx, battlePassConversionCommand(conversionQuote))
		if err != nil || !got.Success || !got.UpgradeSucceeded {
			t.Fatalf("upgrade=%+v err=%v", got, err)
		}
		character := loadBattlePassConversionCharacter(t, ctx, repos)
		if amount := character.Stats[battlePassConversionKey(28, battlePassConversionItemID)]; amount != battlePassConversionCount {
			t.Fatalf("kind 28 amount=%d, want %d; stats=%+v", amount, battlePassConversionCount, character.Stats)
		}
		if amount := character.Stats[battlePassConversionKey(16, -1)]; amount != 1 {
			t.Fatalf("ordinary reinforce count=%d, want 1; stats=%+v", amount, character.Stats)
		}
		bag := loadTestInventory(t, ctx, repos, upgradeLateFailureCharacterID)
		if _, found := bag.Slots[slotKey(listTypeMain, battlePassConversionSlot)]; found {
			t.Fatal("conversion material was not consumed")
		}
	})

	t.Run("failed reinforcement still counts the consumed material", func(t *testing.T) {
		ctx := context.Background()
		repos := memory.NewMemoryGroup()
		seedBattlePassConversionFixture(t, ctx, repos, true)
		owner, err := NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		cmd := battlePassConversionCommand(conversionQuote)
		cmd.UpgradeSuccessWeight = 0
		cmd.UpgradePolicyRequired = true
		cmd.UpgradePolicyCurrentLevel = 0

		got, err := owner.Upgrade(ctx, cmd)
		if err != nil || !got.Success || got.UpgradeSucceeded {
			t.Fatalf("upgrade=%+v err=%v", got, err)
		}
		character := loadBattlePassConversionCharacter(t, ctx, repos)
		if amount := character.Stats[battlePassConversionKey(28, battlePassConversionItemID)]; amount != battlePassConversionCount {
			t.Fatalf("kind 28 amount=%d, want %d; stats=%+v", amount, battlePassConversionCount, character.Stats)
		}
		bag := loadTestInventory(t, ctx, repos, upgradeLateFailureCharacterID)
		if _, found := bag.Slots[slotKey(listTypeMain, battlePassConversionSlot)]; found {
			t.Fatal("failed reinforcement did not consume conversion material")
		}
		if level := upgradeLevelOf(bag.Slots[slotKey(listTypeMain, upgradeLateFailureTargetSlot)]); level != 0 {
			t.Fatalf("failed reinforcement changed target level to %d", level)
		}
	})

	t.Run("missing conversion material rejects without progress", func(t *testing.T) {
		ctx := context.Background()
		repos := memory.NewMemoryGroup()
		seedBattlePassConversionFixture(t, ctx, repos, false)
		owner, err := NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}

		got, err := owner.Upgrade(ctx, battlePassConversionCommand(conversionQuote))
		if err != nil || got.Success || got.ErrorCode != upgradeErrorInvalidMaterial {
			t.Fatalf("upgrade=%+v err=%v", got, err)
		}
		character := loadBattlePassConversionCharacter(t, ctx, repos)
		if amount := character.Stats[battlePassConversionKey(28, battlePassConversionItemID)]; amount != 0 {
			t.Fatalf("rejected request wrote kind 28 amount %d; stats=%+v", amount, character.Stats)
		}
		if amount := character.Stats[battlePassConversionKey(16, -1)]; amount != 0 {
			t.Fatalf("rejected request wrote upgrade count %d; stats=%+v", amount, character.Stats)
		}
	})

	t.Run("gold-only reinforcement does not write the conversion mission", func(t *testing.T) {
		ctx := context.Background()
		repos := memory.NewMemoryGroup()
		seedBattlePassConversionFixture(t, ctx, repos, false)
		owner, err := NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		cmd := battlePassConversionCommand(alignedcmd.UpgradeCostResolution{Resolved: true, GoldCost: 800})
		cmd.UpgradeNPCPresent = false
		cmd.UpgradeNPCID = 0

		got, err := owner.Upgrade(ctx, cmd)
		if err != nil || !got.Success {
			t.Fatalf("upgrade=%+v err=%v", got, err)
		}
		character := loadBattlePassConversionCharacter(t, ctx, repos)
		if amount := character.Stats[battlePassConversionKey(16, -1)]; amount != 1 {
			t.Fatalf("ordinary reinforce count=%d, want 1; stats=%+v", amount, character.Stats)
		}
		if amount := character.Stats[battlePassConversionKey(28, battlePassConversionItemID)]; amount != 0 {
			t.Fatalf("gold-only reinforcement wrote kind 28 amount %d; stats=%+v", amount, character.Stats)
		}
	})

	t.Run("amplify keeps its own counter and skips the reinforce mission", func(t *testing.T) {
		ctx := context.Background()
		repos := memory.NewMemoryGroup()
		seedBattlePassConversionFixture(t, ctx, repos, true)
		bag := loadTestInventory(t, ctx, repos, upgradeLateFailureCharacterID)
		target := bag.Slots[slotKey(listTypeMain, upgradeLateFailureTargetSlot)]
		target.Extra["amplify_type"] = "3"
		target.Extra["amplify_value"] = "5"
		bag.Slots[slotKey(listTypeMain, upgradeLateFailureTargetSlot)] = target
		saveTestInventory(t, ctx, repos, bag)
		owner, err := NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		cmd := battlePassConversionCommand(conversionQuote)
		cmd.Mode = "amplify"

		got, err := owner.Upgrade(ctx, cmd)
		if err != nil || !got.Success {
			t.Fatalf("upgrade=%+v err=%v", got, err)
		}
		character := loadBattlePassConversionCharacter(t, ctx, repos)
		if amount := character.Stats[battlePassConversionKey(38, -1)]; amount != 1 {
			t.Fatalf("amplify count=%d, want 1; stats=%+v", amount, character.Stats)
		}
		if amount := character.Stats[battlePassConversionKey(28, battlePassConversionItemID)]; amount != 0 {
			t.Fatalf("amplify wrote reinforce kind 28 amount %d; stats=%+v", amount, character.Stats)
		}
	})

	t.Run("late inventory save failure rolls back the mission counter", func(t *testing.T) {
		ctx := context.Background()
		repos := memory.NewMemoryGroup()
		probe := &upgradeLateFailureProbe{saveErr: errUpgradeLateBagSave}
		repos.CharacterAssets = upgradeLateFailureAssets{base: repos.CharacterAssets, probe: probe}
		seedBattlePassConversionFixture(t, ctx, repos, true)
		owner, err := NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}

		got, err := owner.Upgrade(ctx, battlePassConversionCommand(conversionQuote))
		if err == nil || got.Success {
			t.Fatalf("late save failure was not returned: upgrade=%+v err=%v", got, err)
		}
		if !probe.goldSeen || probe.goldAfterApply != upgradeLateFailureStartGold {
			t.Fatalf("character record was not saved before the late inventory failure: %+v", probe)
		}
		character := loadBattlePassConversionCharacter(t, ctx, repos)
		if amount := character.Stats[battlePassConversionKey(28, battlePassConversionItemID)]; amount != 0 {
			t.Fatalf("rolled-back transaction retained kind 28 amount %d; stats=%+v", amount, character.Stats)
		}
		bag := loadTestInventory(t, ctx, repos, upgradeLateFailureCharacterID)
		if count := bag.Slots[slotKey(listTypeMain, battlePassConversionSlot)].Count; count != battlePassConversionCount {
			t.Fatalf("rolled-back conversion material count=%d, want %d", count, battlePassConversionCount)
		}
		if level := upgradeLevelOf(bag.Slots[slotKey(listTypeMain, upgradeLateFailureTargetSlot)]); level != 0 {
			t.Fatalf("rolled-back target level=%d, want 0", level)
		}
	})
}
