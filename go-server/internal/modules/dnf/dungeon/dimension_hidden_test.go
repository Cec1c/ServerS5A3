package dungeon

import (
	"context"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestDimensionHiddenAdmissionsCommitWithDailyAllowanceAndRejectStalePlan(t *testing.T) {
	repos, owner, cmd := dimensionGateFixture(t)
	ctx := context.Background()
	for i, hidden := range []bool{false, true, true, false, false} {
		cmd.Hidden = &DimensionHiddenAdmission{ExpectedSequence: int64(i), Encountered: hidden, WeeklyRewardLimit: 2}
		result, err := owner.ConsumeDimensionGateEntry(ctx, cmd)
		if err != nil {
			t.Fatal(err)
		}
		if result.HiddenHighReward != (i == 1 || i == 2) {
			t.Fatalf("entry %d high reward=%t", i, result.HiddenHighReward)
		}
		state, err := DimensionHiddenSnapshot(result.Character, cmd.UpdatedAt)
		if err != nil || state.Sequence != int64(i+1) {
			t.Fatalf("state=%+v err=%v", state, err)
		}
		before, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
		if _, err := owner.ConsumeDimensionGateEntry(ctx, cmd); err == nil {
			t.Fatal("stale frozen entry accepted")
		}
		after, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("rejected stale plan changed state")
		}
	}
	stored, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
	state, err := DimensionHiddenSnapshot(stored, cmd.UpdatedAt)
	if err != nil || state.WeeklyAppearances != 2 || state.EntriesSinceEncounter != 2 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	// An exhausted daily budget cannot burn the highest reward or probability.
	cmd.Hidden = &DimensionHiddenAdmission{ExpectedSequence: 5, Encountered: true, WeeklyRewardLimit: 2}
	if _, err := owner.ConsumeDimensionGateEntry(ctx, cmd); err == nil {
		t.Fatal("sixth entry accepted")
	}
	after, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
	if !reflect.DeepEqual(stored, after) {
		t.Fatal("denied daily entry changed hidden state")
	}
}

func TestDimensionHiddenWeeklyCapRejectsThirdEncounterWithoutConsumingEntry(t *testing.T) {
	repos, owner, cmd := dimensionGateFixture(t)
	ctx := context.Background()
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: cmd.CharacterID, Slots: map[string]dnfrepo.ItemStack{"0:3": {ItemID: 3332, Count: 10}}}); err != nil {
		t.Fatal(err)
	}
	cmd.Requirements = []EntryItemRequirement{{ItemID: 3332, Count: 1, ConsumeOnEntry: true}}
	for sequence := int64(0); sequence < 2; sequence++ {
		cmd.Hidden = &DimensionHiddenAdmission{ExpectedSequence: sequence, Encountered: true, WeeklyRewardLimit: 2}
		if _, err := owner.ConsumeDimensionGateEntry(ctx, cmd); err != nil {
			t.Fatal(err)
		}
	}
	before, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
	bagBefore, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
	if bagBefore.Slots["0:3"].Count != 8 {
		t.Fatalf("two admitted entries did not consume exactly two tickets: %+v", bagBefore.Slots)
	}
	cmd.Hidden.ExpectedSequence = 2
	if _, err := owner.ConsumeDimensionGateEntry(ctx, cmd); err == nil {
		t.Fatal("third weekly encounter accepted")
	}
	after, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
	bagAfter, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(bagBefore, bagAfter) {
		t.Fatal("rejected encounter consumed an entry, ticket, or hidden count")
	}
	cmd.Hidden.Encountered = false
	if _, err := owner.ConsumeDimensionGateEntry(ctx, cmd); err != nil {
		t.Fatalf("ordinary entry after cap rejected: %v", err)
	}
	bagOrdinary, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
	if bagOrdinary.Slots["0:3"].Count != 7 {
		t.Fatalf("ordinary entry did not consume exactly one ticket: %+v", bagOrdinary.Slots)
	}
	cmd.UpdatedAt = cmd.UpdatedAt.Add(7 * 24 * time.Hour)
	cmd.Hidden = &DimensionHiddenAdmission{ExpectedSequence: 3, Encountered: true, WeeklyRewardLimit: 2}
	result, err := owner.ConsumeDimensionGateEntry(ctx, cmd)
	if err != nil || !result.HiddenHighReward {
		t.Fatalf("next week's encounter rejected: %v", err)
	}
	state, err := DimensionHiddenSnapshot(result.Character, cmd.UpdatedAt)
	if err != nil || state.WeeklyAppearances != 1 {
		t.Fatalf("next week state=%+v err=%v", state, err)
	}
}
