package fatigue

import (
	"context"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestPotionDailyAllowanceSurvivesReloadAndResetsAtSix(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	before := time.Date(2026, 10, 1, 21, 59, 59, 0, time.UTC)
	account := dnfrepo.AccountRecord{AccountID: "a"}
	character := dnfrepo.CharacterRecord{CharacterID: "6", AccountID: "a", Stats: map[string]int64{"fatigue": 0, "fatigue_limit": 156}}
	Refresh(&character, account, before)
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "6", Slots: map[string]dnfrepo.ItemStack{"0:1": {ItemID: 10015080, Count: 3}}}); err != nil {
		t.Fatal(err)
	}
	definition := PotionDefinition{ItemID: 10015080, Points: 20, DailyLimit: 1, PVFPath: "stackable/10015080.stk"}
	result, err := RestorePotion(ctx, repos, account, "6", 1, definition, before)
	if err != nil || result.Remaining != 2 || result.Character.Stats["fatigue"] != 20 {
		t.Fatalf("first use=%+v %v", result, err)
	}
	if _, err := RestorePotion(ctx, repos, account, "6", 1, definition, before); err == nil {
		t.Fatal("daily limit bypass")
	}
	bag, _, _ := repos.Inventory.Load(ctx, "6")
	if bag.Slots["0:1"].Count != 2 {
		t.Fatal("rejected use consumed potion")
	}
	next := before.Add(time.Second)
	character, _, _ = repos.Character.Load(ctx, "6")
	Refresh(&character, account, next)
	character.Stats["fatigue"] = 0
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	result, err = RestorePotion(ctx, repos, account, "6", 1, definition, next)
	if err != nil || result.Remaining != 1 || result.Character.Stats["fatigue"] != 20 {
		t.Fatalf("next service day=%+v %v", result, err)
	}
}
