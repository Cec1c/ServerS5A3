package dungeon

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func addDimensionExtraMaterials(t *testing.T, g dnfrepo.Group, slots map[string]dnfrepo.ItemStack) {
	t.Helper()
	ctx := context.Background()
	inventory, found, err := g.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	inventory = dnfrepo.CloneInventory(inventory)
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	for key, stack := range slots {
		inventory.Slots[key] = stack
	}
	if err := g.Inventory.Save(ctx, inventory); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
}

func TestDimensionGateExtraMaterialRequirementTakesFirstPositivePair(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		values     []int64
		wantItemID int64
		wantCount  int64
	}{
		{"missing", nil, 0, 0},
		{"single", []int64{3228, 60}, 3228, 60},
		{"skip_zero_item", []int64{0, 60, 3228, 60}, 3228, 60},
		{"skip_zero_count", []int64{3228, 0, 3332, 5}, 3332, 5},
		{"odd_tail", []int64{3228}, 0, 0},
		{"all_invalid", []int64{-1, 60, 3228, -1}, 0, 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			itemID, count := DimensionGateExtraMaterialRequirement(testCase.values)
			if itemID != testCase.wantItemID || count != testCase.wantCount {
				t.Fatalf("got %d x %d, want %d x %d", itemID, count, testCase.wantItemID, testCase.wantCount)
			}
		})
	}
}

func TestDimensionGateExtraTicketWithMaterialConsumesBothOnce(t *testing.T) {
	g, owner, command := dimensionExtraFixture(t)
	addDimensionExtraMaterials(t, g, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: 3228, Count: 60},
	})
	command.MaterialItemID = 3228
	command.MaterialCount = 60

	result, err := owner.GrantDimensionGateExtraEntry(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	character, inventory := dimensionExtraSnapshot(t, g)
	if inventory.Slots["0:20"].Count != 19 {
		t.Fatalf("ticket count=%d, want 19", inventory.Slots["0:20"].Count)
	}
	if _, found := inventory.Slots["0:30"]; found {
		t.Fatal("material stack should be removed after exactly 60 were consumed")
	}
	if character.Stats[dimensionGateExtraKey] != 1 {
		t.Fatalf("extra grant=%d, want 1", character.Stats[dimensionGateExtraKey])
	}
	if character.Stats[dimensionGateExtraLastUseKey] != command.UpdatedAt.UnixMilli() {
		t.Fatalf("last use=%d, want %d", character.Stats[dimensionGateExtraLastUseKey], command.UpdatedAt.UnixMilli())
	}
	if result.Granted != 1 || result.Total != 6 || result.Slot.Slot != 20 || result.Slot.CountAfter != 19 {
		t.Fatalf("result=%+v", result)
	}
	if len(result.MaterialSlots) != 1 || result.MaterialSlots[0].Slot != 30 || !result.MaterialSlots[0].Removed {
		t.Fatalf("material slots=%+v", result.MaterialSlots)
	}
}

func TestDimensionGateExtraMaterialCanSpanStacks(t *testing.T) {
	g, owner, command := dimensionExtraFixture(t)
	addDimensionExtraMaterials(t, g, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: 3228, Count: 40},
		"0:31": {ItemID: 3228, Count: 20},
	})
	command.MaterialItemID = 3228
	command.MaterialCount = 60

	result, err := owner.GrantDimensionGateExtraEntry(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	_, inventory := dimensionExtraSnapshot(t, g)
	if _, found := inventory.Slots["0:30"]; found {
		t.Fatal("first material stack should be removed")
	}
	if _, found := inventory.Slots["0:31"]; found {
		t.Fatal("second material stack should be removed")
	}
	if len(result.MaterialSlots) != 2 || result.MaterialSlots[0].Slot != 30 || result.MaterialSlots[1].Slot != 31 {
		t.Fatalf("material slots=%+v, want slots 30 then 31", result.MaterialSlots)
	}
}

func TestDimensionGateExtraMaterialShortageLeavesEverythingUntouched(t *testing.T) {
	g, owner, command := dimensionExtraFixture(t)
	addDimensionExtraMaterials(t, g, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: 3228, Count: 59},
	})
	command.MaterialItemID = 3228
	command.MaterialCount = 60
	beforeCharacter, beforeInventory := dimensionExtraSnapshot(t, g)

	result, err := owner.GrantDimensionGateExtraEntry(context.Background(), command)
	if !errors.Is(err, ErrEntryItemConsumeInsufficient) {
		t.Fatalf("err=%v, want insufficient material", err)
	}
	if !reflect.DeepEqual(result, DimensionGateExtraEntryResult{}) {
		t.Fatalf("result=%+v, want zero result", result)
	}
	afterCharacter, afterInventory := dimensionExtraSnapshot(t, g)
	if !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeInventory, afterInventory) {
		t.Fatal("shortage changed the ticket, material or daily grant")
	}
}

func TestDimensionGateExtraMaterialRejectsPartialOrNegativePair(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		itemID     int64
		materialID int64
		count      int64
	}{
		{"item_only", 3228, 3228, 0},
		{"count_only", 3228, 0, 60},
		{"negative_item", 3228, -1, 60},
		{"negative_count", 3228, 3228, -1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			g, owner, command := dimensionExtraFixture(t)
			addDimensionExtraMaterials(t, g, map[string]dnfrepo.ItemStack{
				"0:30": {ItemID: testCase.itemID, Count: 60},
			})
			command.MaterialItemID = testCase.materialID
			command.MaterialCount = testCase.count
			beforeCharacter, beforeInventory := dimensionExtraSnapshot(t, g)
			result, err := owner.GrantDimensionGateExtraEntry(context.Background(), command)
			if !errors.Is(err, ErrEntryItemConsumeInvalid) {
				t.Fatalf("err=%v, want invalid command", err)
			}
			if !reflect.DeepEqual(result, DimensionGateExtraEntryResult{}) {
				t.Fatalf("result=%+v, want zero result", result)
			}
			afterCharacter, afterInventory := dimensionExtraSnapshot(t, g)
			if !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeInventory, afterInventory) {
				t.Fatal("invalid material declaration changed assets")
			}
		})
	}
}

func TestDimensionGateExtraMaterialReplayDoesNotDoubleConsume(t *testing.T) {
	g, owner, command := dimensionExtraFixture(t)
	addDimensionExtraMaterials(t, g, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: 3228, Count: 120},
	})
	command.MaterialItemID = 3228
	command.MaterialCount = 60

	first, err := owner.GrantDimensionGateExtraEntry(context.Background(), command)
	if err != nil || first.Granted != 1 {
		t.Fatalf("first grant=%+v err=%v", first, err)
	}
	firstCharacter, firstInventory := dimensionExtraSnapshot(t, g)
	if firstInventory.Slots["0:20"].Count != 19 || firstInventory.Slots["0:30"].Count != 60 {
		t.Fatalf("after first use inventory=%+v", firstInventory.Slots)
	}

	replay, err := owner.GrantDimensionGateExtraEntry(context.Background(), command)
	if !errors.Is(err, ErrEntryItemConsumeInvalid) {
		t.Fatalf("replay err=%v, want cooldown rejection", err)
	}
	if !reflect.DeepEqual(replay, DimensionGateExtraEntryResult{}) {
		t.Fatalf("replay result=%+v, want zero result", replay)
	}
	afterCharacter, afterInventory := dimensionExtraSnapshot(t, g)
	if !reflect.DeepEqual(firstCharacter, afterCharacter) || !reflect.DeepEqual(firstInventory, afterInventory) {
		t.Fatal("replayed use consumed the ticket or material again")
	}
	if afterCharacter.Stats[dimensionGateExtraKey] != 1 {
		t.Fatalf("replayed use granted %d extra entries, want 1 total", afterCharacter.Stats[dimensionGateExtraKey])
	}
}

func TestDimensionGateExtraMaterialRollbackAfterBothWritesAndRetry(t *testing.T) {
	g, owner, command := dimensionExtraFixture(t)
	addDimensionExtraMaterials(t, g, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: 3228, Count: 120},
	})
	command.MaterialItemID = 3228
	command.MaterialCount = 60
	beforeCharacter, beforeInventory := dimensionExtraSnapshot(t, g)

	underlying := g.CharacterAssets
	fault := errors.New("before commit")
	g.CharacterAssets = epicRollbackAssets{underlying, fault}
	owner, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.GrantDimensionGateExtraEntry(context.Background(), command)
	if !errors.Is(err, fault) || !reflect.DeepEqual(result, DimensionGateExtraEntryResult{}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	afterCharacter, afterInventory := dimensionExtraSnapshot(t, g)
	if !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeInventory, afterInventory) {
		t.Fatal("failed commit changed the ticket, material or daily grant")
	}

	g.CharacterAssets = underlying
	owner, err = NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	result, err = owner.GrantDimensionGateExtraEntry(context.Background(), command)
	if err != nil || result.Granted != 1 || result.Slot.CountAfter != 19 || len(result.MaterialSlots) != 1 || result.MaterialSlots[0].CountAfter != 60 {
		t.Fatalf("retry result=%+v err=%v", result, err)
	}
}

func TestDimensionGateExtraNoMaterialTicketKeepsUnrelatedMaterial(t *testing.T) {
	g, owner, command := dimensionExtraFixture(t)
	addDimensionExtraMaterials(t, g, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: 3228, Count: 120},
	})

	result, err := owner.GrantDimensionGateExtraEntry(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	_, inventory := dimensionExtraSnapshot(t, g)
	if inventory.Slots["0:20"].Count != 19 {
		t.Fatalf("ticket count=%d, want 19", inventory.Slots["0:20"].Count)
	}
	if inventory.Slots["0:30"].Count != 120 {
		t.Fatalf("no-material ticket consumed unrelated material: count=%d", inventory.Slots["0:30"].Count)
	}
	if len(result.MaterialSlots) != 0 {
		t.Fatalf("material slots=%+v, want none", result.MaterialSlots)
	}
}

func cloneDimensionExtraMaterialSlots(slots map[string]dnfrepo.ItemStack) map[string]dnfrepo.ItemStack {
	return dnfrepo.CloneInventory(dnfrepo.InventoryRecord{Slots: slots}).Slots
}

func seedDimensionExtraMaterialComparison(t *testing.T, slots map[string]dnfrepo.ItemStack) (dnfrepo.Group, *Owner) {
	t.Helper()
	g, owner := seedEntryItemInventory(t, cloneDimensionExtraMaterialSlots(slots))
	if err := g.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "own",
		Level:       90,
		Stats:       map[string]int64{"fatigue": 100},
	}); err != nil {
		t.Fatal(err)
	}
	return g, owner
}

// The material path must keep the exact selection semantics of the existing
// entry consumer. This includes the existing treatment of expiration and lock
// metadata (the helper does not special-case either), and it must not widen the
// search to warehouse/account containers. If the helper's policy changes, both
// paths must change together; this test is deliberately a parity assertion, not
// a new lock or expiration policy.
func TestDimensionGateExtraMaterialMatchesExistingEntryConsumer(t *testing.T) {
	expired := time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name  string
		slots map[string]dnfrepo.ItemStack
	}{
		{
			name: "locked_expired_main",
			slots: map[string]dnfrepo.ItemStack{
				"0:20": {ItemID: 10158125, Count: 1},
				"0:30": {
					ItemID:   3228,
					Count:    60,
					ExpireAt: expired,
					Extra: map[string]string{
						"sort_lock":            "1",
						"equipment_lock_state": "locked",
					},
				},
			},
		},
		{
			name: "non_main_container",
			slots: map[string]dnfrepo.ItemStack{
				"0:20": {ItemID: 10158125, Count: 1},
				"2:30": {ItemID: 3228, Count: 60},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			entryGroup, entryOwner := seedDimensionExtraMaterialComparison(t, testCase.slots)
			extraGroup, extraOwner := seedDimensionExtraMaterialComparison(t, testCase.slots)
			entryResult, entryErr := entryOwner.ConsumeEntryItems(context.Background(), EntryItemConsumeCommand{
				CharacterID: "19",
				Requirements: []EntryItemRequirement{{
					ItemID: 3228, Count: 60, ConsumeOnEntry: true,
				}},
				UpdatedAt: now,
			})
			extraResult, extraErr := extraOwner.GrantDimensionGateExtraEntry(context.Background(), DimensionGateExtraEntryCommand{
				CharacterID: "19", AccountID: "own", Slot: 20, ItemID: 10158125, Count: 1,
				MaterialItemID: 3228, MaterialCount: 60,
				BaseLimit: 5, ExtraLimit: 5, MinimumLevel: 70,
				Cooldown: 10 * time.Second, UpdatedAt: now,
			})
			if (entryErr == nil) != (extraErr == nil) {
				t.Fatalf("existing entry consumer err=%v, material ticket err=%v", entryErr, extraErr)
			}
			_, entryInventory := dimensionExtraSnapshot(t, entryGroup)
			_, extraInventory := dimensionExtraSnapshot(t, extraGroup)
			if entryErr != nil {
				if !errors.Is(entryErr, ErrEntryItemConsumeInsufficient) || !errors.Is(extraErr, ErrEntryItemConsumeInsufficient) {
					t.Fatalf("unexpected errors: entry=%v material=%v", entryErr, extraErr)
				}
				if !reflect.DeepEqual(entryResult, EntryItemConsumeResult{}) || !reflect.DeepEqual(extraResult, DimensionGateExtraEntryResult{}) {
					t.Fatalf("rejected consumers returned results: entry=%+v material=%+v", entryResult, extraResult)
				}
				if extraInventory.Slots["0:20"].Count != 1 {
					t.Fatal("material rejection consumed the ticket")
				}
				if entryInventory.Slots["2:30"].Count != 60 || extraInventory.Slots["2:30"].Count != 60 {
					t.Fatalf("non-main material changed: entry=%+v material=%+v", entryInventory.Slots["2:30"], extraInventory.Slots["2:30"])
				}
				return
			}
			if _, found := entryInventory.Slots["0:30"]; found {
				t.Fatal("existing entry consumer left the main material stack")
			}
			if _, found := extraInventory.Slots["0:30"]; found {
				t.Fatal("material ticket path left the main material stack")
			}
			if !entryResult.Changed || len(entryResult.Consumed) != 1 {
				t.Fatalf("existing entry result=%+v", entryResult)
			}
			if len(extraResult.MaterialSlots) != 1 || !extraResult.MaterialSlots[0].Removed {
				t.Fatalf("material result=%+v", extraResult)
			}
			t.Log("both consumers accepted the stack; existing entry and material paths remain in parity")
		})
	}
}
