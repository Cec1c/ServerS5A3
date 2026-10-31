package quest

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	finishGoldCostMaterialItemID int64 = 9001
	finishGoldCostMaterialKey          = "0:11"
)

type finishGoldCostFixture struct {
	ctx        context.Context
	repos      dnfrepo.Group
	owner      *Owner
	definition Definition
	catalog    *Catalog
	input      FinishCommitInput
}

type finishGoldCostSnapshot struct {
	character dnfrepo.CharacterRecord
	inventory dnfrepo.InventoryRecord
	quest     dnfrepo.QuestRecord
}

func TestOwnerApplyFinishSettlementConsumesSeekingGoldMaterialAndSlotReward(t *testing.T) {
	tests := []struct {
		name  string
		cost  int64
		index uint32
		bit   byte
	}{
		{name: "support", cost: 300000, index: 0, bit: ExEquipSlotSupport},
		{name: "magic_stone", cost: 800000, index: 1, bit: ExEquipSlotMagicStone},
		{name: "earring", cost: 1000000, index: 2, bit: ExEquipSlotEarring},
	}
	for testIndex, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const materialBefore int64 = 5
			const goldRemainder int64 = 77
			fixture := newFinishGoldCostFixture(
				t,
				int64(6100+testIndex),
				test.cost,
				test.index,
				test.cost+goldRemainder,
				materialBefore,
			)

			first, err := fixture.owner.ApplyFinishSettlement(fixture.ctx, fixture.catalog, fixture.input)
			if err != nil {
				t.Fatal(err)
			}
			if !first.AtomicCommitted || first.Replayed {
				t.Fatalf("first settlement committed=%t replayed=%t", first.AtomicCommitted, first.Replayed)
			}
			if len(first.Currency) != 1 || first.Currency[0].Name != "gold" ||
				first.Currency[0].Delta != -test.cost || first.Currency[0].PostValue != goldRemainder {
				t.Fatalf("gold receipt=%+v, want delta=%d post=%d", first.Currency, -test.cost, goldRemainder)
			}
			if len(first.ConsumedItems) != 1 {
				t.Fatalf("consumed materials=%+v", first.ConsumedItems)
			}
			consumed := first.ConsumedItems[0]
			if consumed.ItemID != finishGoldCostMaterialItemID || consumed.Delta != 2 || consumed.PostCount != materialBefore-2 {
				t.Fatalf("consumed material=%+v, want item=%d delta=2 post=%d", consumed, finishGoldCostMaterialItemID, materialBefore-2)
			}
			if !first.HasSlotExpansion || first.SlotExpansionIndex != test.index || first.SlotExpansionBit != test.bit {
				t.Fatalf("slot expansion=%t index=%d bit=%#x, want index=%d bit=%#x",
					first.HasSlotExpansion, first.SlotExpansionIndex, first.SlotExpansionBit, test.index, test.bit)
			}

			afterFirst := fixture.snapshot(t)
			if got := afterFirst.character.Stats["gold"]; got != goldRemainder {
				t.Fatalf("persisted gold=%d, want %d", got, goldRemainder)
			}
			if got := afterFirst.inventory.Slots[finishGoldCostMaterialKey].Count; got != materialBefore-2 {
				t.Fatalf("persisted material count=%d, want %d", got, materialBefore-2)
			}
			if got := afterFirst.character.Stats["ex_equip_slot_stat"]; got != int64(test.bit) {
				t.Fatalf("persisted ex_equip_slot_stat=%#x, want %#x", got, test.bit)
			}
			state := afterFirst.quest.States[fixture.definition.ID]
			if state.Status != "completed" || state.Extra["reward_state"] != finishRewardGranted {
				t.Fatalf("persisted quest=%+v", state)
			}

			second, err := fixture.owner.ApplyFinishSettlement(fixture.ctx, fixture.catalog, fixture.input)
			if err != nil {
				t.Fatal(err)
			}
			if !second.AtomicCommitted || !second.Replayed {
				t.Fatalf("replay committed=%t replayed=%t", second.AtomicCommitted, second.Replayed)
			}
			if len(second.Currency) != 1 || second.Currency[0].Delta != -test.cost || second.Currency[0].PostValue != goldRemainder {
				t.Fatalf("replay gold receipt=%+v", second.Currency)
			}
			afterSecond := fixture.snapshot(t)
			if !reflect.DeepEqual(afterSecond.character, afterFirst.character) {
				t.Fatalf("replay changed character\nfirst=%+v\nsecond=%+v", afterFirst.character, afterSecond.character)
			}
			if !reflect.DeepEqual(afterSecond.inventory, afterFirst.inventory) {
				t.Fatalf("replay changed inventory\nfirst=%+v\nsecond=%+v", afterFirst.inventory, afterSecond.inventory)
			}
			if !reflect.DeepEqual(afterSecond.quest, afterFirst.quest) {
				t.Fatalf("replay changed quest\nfirst=%+v\nsecond=%+v", afterFirst.quest, afterSecond.quest)
			}
		})
	}
}

func TestOwnerApplyFinishSettlementGoldMaterialFailuresLeaveRecordsUnchanged(t *testing.T) {
	tests := []struct {
		name         string
		gold         int64
		material     int64
		expectedErr  error
		fixtureIndex int64
	}{
		{name: "one_gold_short", gold: 299999, material: 5, expectedErr: ErrFinishRequiredGoldMissing, fixtureIndex: 0},
		{name: "one_material_short", gold: 300000, material: 1, expectedErr: ErrFinishRequiredItemsMissing, fixtureIndex: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFinishGoldCostFixture(t, 6200+test.fixtureIndex, 300000, 0, test.gold, test.material)
			before := fixture.snapshot(t)
			result, err := fixture.owner.ApplyFinishSettlement(fixture.ctx, fixture.catalog, fixture.input)
			if !errors.Is(err, test.expectedErr) {
				t.Fatalf("settlement error=%v, want %v", err, test.expectedErr)
			}
			if !reflect.DeepEqual(result, FinishCommitResult{}) {
				t.Fatalf("failed settlement returned result=%+v", result)
			}
			after := fixture.snapshot(t)
			if !reflect.DeepEqual(after.character, before.character) {
				t.Fatalf("failed settlement changed character\nbefore=%+v\nafter=%+v", before.character, after.character)
			}
			if !reflect.DeepEqual(after.inventory, before.inventory) {
				t.Fatalf("failed settlement changed inventory\nbefore=%+v\nafter=%+v", before.inventory, after.inventory)
			}
			if !reflect.DeepEqual(after.quest, before.quest) {
				t.Fatalf("failed settlement changed quest\nbefore=%+v\nafter=%+v", before.quest, after.quest)
			}
		})
	}
}

func TestCoalesceFinishCurrencyNetsSeekingGoldFeeAgainstReward(t *testing.T) {
	const fee int64 = 300000
	character := dnfrepo.CharacterRecord{
		CharacterID: "19",
		Level:       1,
		Stats:       map[string]int64{"gold": fee + 123},
	}
	definition := Definition{Type: "[seeking]", IntData: []int64{0, fee, finishGoldCostMaterialItemID, 2}}
	paid, err := consumeFinishRequiredGold(&character, definition)
	if err != nil {
		t.Fatal(err)
	}
	reward, err := applyFinishRewardCurrency(&character, finishOwnerProgression(t), FinishRewardPlan{
		HasGoldReward: true,
		QuestLevel:    1,
		Difficulty:    "N",
		GoldMultiple:  100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reward) != 1 || reward[0].Delta != 10 {
		t.Fatalf("fixture reward currency=%+v, want one +10 gold entry", reward)
	}
	merged, err := coalesceFinishCurrency(append(paid, reward...))
	if err != nil {
		t.Fatal(err)
	}
	wantDelta := -fee + reward[0].Delta
	if len(merged) != 1 || merged[0].Name != "gold" || merged[0].Delta != wantDelta || merged[0].PostValue != character.Stats["gold"] {
		t.Fatalf("merged currency=%+v, want name=gold delta=%d post=%d", merged, wantDelta, character.Stats["gold"])
	}
}

func TestConsumeFinishRequiredGoldRejectsNegativeAndOverflowCosts(t *testing.T) {
	tests := []struct {
		name string
		data []int64
	}{
		{name: "negative", data: []int64{0, -1, finishGoldCostMaterialItemID, 2}},
		{name: "overflow", data: []int64{0, math.MaxInt32, 0, 1, finishGoldCostMaterialItemID, 2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			character := dnfrepo.CharacterRecord{Stats: map[string]int64{"gold": math.MaxInt64}}
			before := dnfrepo.CloneCharacter(character)
			paid, err := consumeFinishRequiredGold(&character, Definition{Type: "[seeking]", IntData: test.data})
			if !errors.Is(err, ErrQuestRewardMalformed) {
				t.Fatalf("error=%v, want %v", err, ErrQuestRewardMalformed)
			}
			if paid != nil {
				t.Fatalf("invalid cost returned currency=%+v", paid)
			}
			if !reflect.DeepEqual(character, before) {
				t.Fatalf("invalid cost mutated character: before=%+v after=%+v", before, character)
			}
		})
	}
}

func newFinishGoldCostFixture(
	t *testing.T,
	questID int64,
	cost int64,
	slotExpansionIndex uint32,
	gold int64,
	material int64,
) *finishGoldCostFixture {
	t.Helper()
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)

	character, found, err := repos.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load seeded character found=%t err=%v", found, err)
	}
	character.Stats["gold"] = gold
	character.Stats["ex_equip_slot_stat"] = 0
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}

	acceptedAt := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	completionKey := "finish-gold-cost/19/" + strconv.FormatInt(questID, 10)
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		questID: {
			Status: "active", ProgressValue: 0, UpdatedAt: acceptedAt,
			Extra: map[string]string{
				"completion_key":  completionKey,
				"completion_kind": "clear_map",
				"reward_state":    "pending",
			},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
		finishGoldCostMaterialKey: {ItemID: finishGoldCostMaterialItemID, Count: material},
	}}); err != nil {
		t.Fatal(err)
	}

	definition := Definition{
		ID: questID, Path: "n_quest/finish_gold_cost.qst", Type: "[seeking]",
		LevelMin: 1, LevelMax: 99, Difficulty: "N",
		IntData:       []int64{0, cost, finishGoldCostMaterialItemID, 2},
		RewardType:    "[slot expansion]",
		RewardIntData: []int64{int64(slotExpansionIndex)},
		NoExperience:  true,
	}
	catalog := &Catalog{ordered: []Definition{definition}, byID: map[int64]Definition{definition.ID: definition}}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	return &finishGoldCostFixture{
		ctx:        ctx,
		repos:      repos,
		owner:      owner,
		definition: definition,
		catalog:    catalog,
		input: FinishCommitInput{
			AccountID: "acc", CharacterID: "19", QuestID: questID, Multiplier: 1,
			ExpectedCompletionKey: completionKey,
			CommittedAt:           acceptedAt.Add(time.Minute),
			Progression:           finishOwnerProgression(t),
		},
	}
}

func (f *finishGoldCostFixture) snapshot(t *testing.T) finishGoldCostSnapshot {
	t.Helper()
	character, found, err := f.repos.Character.Load(f.ctx, "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	inventory, found, err := f.repos.Inventory.Load(f.ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	quest, found, err := f.repos.Quest.Load(f.ctx, "19")
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	return finishGoldCostSnapshot{character: character, inventory: inventory, quest: quest}
}
