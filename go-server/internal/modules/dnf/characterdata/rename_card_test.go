package characterdata

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	renameCardAccountID          = "renter"
	renameCardCharacterID        = "77"
	renameCardOwnedSlot    int16 = 65
	renameCardMaterialSlot int16 = 121
	renameCardSharedSlot   int16 = 354
	renameCardExpandedSlot int16 = 97
	renameCardItemID       int64 = 2660698
	renameCardMaterialID   int64 = 1001
	renameCardOwnedGold    int64 = 9876
)

// errRenameCardResolverFailure is returned by an injected resolver that cannot
// confirm the held card. UseRenameCard must surface it without consuming the card.
var errRenameCardResolverFailure = errors.New("rename card resolver failed")

// renameCardPolicy mirrors the PVF-backed policy of one rename card: the card
// lives in the main bag page 65..120 and quick slots stay eligible.
func renameCardPolicy() RenameCardPolicy {
	return RenameCardPolicy{
		MinimumLevel:       1,
		CooldownSeconds:    int64(RenameCooldown / time.Second),
		ReservationSeconds: int64(PreviousNameReservation / time.Second),
		Slots: itemplacement.SlotPolicy{
			ListType:        repo.MainInventoryListType,
			Start:           renameCardOwnedSlot,
			End:             120,
			AllowQuickSlots: true,
		},
	}
}

func renameCardSlotKey(slot int16) string {
	return fmt.Sprintf("%d:%d", repo.MainInventoryListType, slot)
}

func renameCardStack(itemID, count int64) repo.ItemStack {
	return repo.ItemStack{ItemID: itemID, Count: count, Extra: map[string]string{"source": "cash_shop"}}
}

type recordingRenameCardResolver struct {
	policy  RenameCardPolicy
	rewrite func(repo.ItemStack) (RenameCardPolicy, repo.ItemStack, error)
	calls   int
	sawChar repo.CharacterRecord
	sawHeld repo.ItemStack
}

func (r *recordingRenameCardResolver) resolve(character repo.CharacterRecord, held repo.ItemStack) (RenameCardPolicy, repo.ItemStack, error) {
	r.calls++
	r.sawChar = character
	r.sawHeld = held
	if r.rewrite != nil {
		return r.rewrite(held)
	}
	return r.policy, held, nil
}

func renameCardOKResolver() *recordingRenameCardResolver {
	return &recordingRenameCardResolver{policy: renameCardPolicy()}
}

func saveRenameCardAccount(t *testing.T, ctx context.Context, g repo.Group, accountID string) {
	t.Helper()
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: accountID, State: "active"}); err != nil {
		t.Fatalf("seed account %s: %v", accountID, err)
	}
}

func saveRenameCardInventory(t *testing.T, ctx context.Context, g repo.Group, slots map[string]repo.ItemStack) {
	t.Helper()
	if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: renameCardCharacterID, Slots: slots}); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
}

func saveRenameCardCharacter(t *testing.T, ctx context.Context, g repo.Group, record repo.CharacterRecord) {
	t.Helper()
	if err := g.Character.Save(ctx, record); err != nil {
		t.Fatalf("save character: %v", err)
	}
}

func saveRenameCardContainerState(t *testing.T, ctx context.Context, g repo.Group, mainSlots string) {
	t.Helper()
	if err := g.Settings.Save(ctx, repo.SettingsRecord{
		Scope:  repo.CharacterContainerStateScope(renameCardCharacterID),
		Values: map[string]string{"main_list_param16": mainSlots, "avatar_list_param16": "0", "personal_cargo_list_param16": "8"},
	}); err != nil {
		t.Fatalf("seed container state: %v", err)
	}
}

func renameCardFixtureSlots() map[string]repo.ItemStack {
	return map[string]repo.ItemStack{
		renameCardSlotKey(renameCardOwnedSlot):    renameCardStack(renameCardItemID, 2),
		renameCardSlotKey(renameCardMaterialSlot): {ItemID: renameCardMaterialID, Count: 5},
	}
}

// newRenameCardFixture seeds one account with an owned level 1 character and a
// bag that holds two rename cards plus an untouched material stack.
func newRenameCardFixture(t *testing.T) (repo.Group, time.Time) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	now := time.Date(2026, 10, 17, 3, 4, 5, 0, time.UTC)
	saveRenameCardAccount(t, ctx, g, renameCardAccountID)
	saveRenameCardCharacter(t, ctx, g, repo.CharacterRecord{
		CharacterID: renameCardCharacterID,
		AccountID:   renameCardAccountID,
		Slot:        1,
		Name:        "旧名字",
		Level:       1,
		Stats:       map[string]int64{"gold": renameCardOwnedGold},
	})
	saveRenameCardInventory(t, ctx, g, renameCardFixtureSlots())
	return g, now
}

func requireRenameCardCharacter(t *testing.T, ctx context.Context, g repo.Group) repo.CharacterRecord {
	t.Helper()
	character, found, err := g.Character.Load(ctx, renameCardCharacterID)
	if err != nil || !found {
		t.Fatalf("load character %s: found=%v err=%v", renameCardCharacterID, found, err)
	}
	return character
}

func requireRenameCardInventory(t *testing.T, ctx context.Context, g repo.Group) repo.InventoryRecord {
	t.Helper()
	inventory, found, err := g.Inventory.Load(ctx, renameCardCharacterID)
	if err != nil || !found {
		t.Fatalf("load inventory %s: found=%v err=%v", renameCardCharacterID, found, err)
	}
	return inventory
}

func snapshotRenameCardState(t *testing.T, ctx context.Context, g repo.Group) (repo.CharacterRecord, repo.InventoryRecord) {
	t.Helper()
	return repo.CloneCharacter(requireRenameCardCharacter(t, ctx, g)), repo.CloneInventory(requireRenameCardInventory(t, ctx, g))
}

func assertRenameCardStateUnchanged(t *testing.T, ctx context.Context, g repo.Group, character repo.CharacterRecord, inventory repo.InventoryRecord) {
	t.Helper()
	if after := requireRenameCardCharacter(t, ctx, g); !reflect.DeepEqual(after, character) {
		t.Fatalf("rejected use changed the character:\n got %+v\nwant %+v", after, character)
	}
	if after := requireRenameCardInventory(t, ctx, g); !reflect.DeepEqual(after, inventory) {
		t.Fatalf("rejected use changed the inventory:\n got %+v\nwant %+v", after, inventory)
	}
}

func TestUseRenameCardConsumesOneCardAndKeepsEverythingElse(t *testing.T) {
	ctx := context.Background()
	g, now := newRenameCardFixture(t)
	resolver := renameCardOKResolver()

	result, err := UseRenameCard(ctx, g, renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, now, resolver.resolve)
	if err != nil {
		t.Fatalf("UseRenameCard error = %v", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.calls)
	}
	if resolver.sawChar.CharacterID != renameCardCharacterID || resolver.sawHeld.ItemID != renameCardItemID || resolver.sawHeld.Count != 2 {
		t.Fatalf("resolver saw character=%s stack=%+v", resolver.sawChar.CharacterID, resolver.sawHeld)
	}
	if !result.Changed || result.Removed || result.Stack.ItemID != renameCardItemID || result.Stack.Count != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Stack.Extra["source"] != "cash_shop" {
		t.Fatalf("card metadata lost: %+v", result.Stack)
	}

	character := requireRenameCardCharacter(t, ctx, g)
	if character.Stats[RenameEntitlementStat] != 1 || character.Stats[RenameCardSlotStat] != int64(renameCardOwnedSlot) {
		t.Fatalf("entitlement not granted for the card slot: %+v", character.Stats)
	}
	if character.Stats[RenameMinimumLevelStat] != 1 ||
		character.Stats[RenameCooldownSecondsStat] != int64(RenameCooldown/time.Second) ||
		character.Stats[RenameReservationSecondsStat] != int64(PreviousNameReservation/time.Second) {
		t.Fatalf("policy not carried into the entitlement: %+v", character.Stats)
	}
	if character.Stats["gold"] != renameCardOwnedGold || character.Level != 1 {
		t.Fatalf("successful use rewrote unrelated character state: %+v", character)
	}
	if !character.UpdatedAt.Equal(now) {
		t.Fatalf("character UpdatedAt = %s, want %s", character.UpdatedAt, now)
	}

	inventory := requireRenameCardInventory(t, ctx, g)
	card := inventory.Slots[renameCardSlotKey(renameCardOwnedSlot)]
	if card.ItemID != renameCardItemID || card.Count != 1 || card.Extra["source"] != "cash_shop" {
		t.Fatalf("card stack after use = %+v", card)
	}
	if material := inventory.Slots[renameCardSlotKey(renameCardMaterialSlot)]; material.ItemID != renameCardMaterialID || material.Count != 5 {
		t.Fatalf("unrelated material stack changed: %+v", material)
	}
	if !inventory.UpdatedAt.Equal(now) {
		t.Fatalf("inventory UpdatedAt = %s, want %s", inventory.UpdatedAt, now)
	}
}

func TestUseRenameCardRetryOnTheGrantedSlotDoesNotConsumeAgain(t *testing.T) {
	ctx := context.Background()
	g, now := newRenameCardFixture(t)
	resolver := renameCardOKResolver()
	if _, err := UseRenameCard(ctx, g, renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, now, resolver.resolve); err != nil {
		t.Fatalf("first use error = %v", err)
	}
	beforeCharacter, beforeInventory := snapshotRenameCardState(t, ctx, g)

	retry, err := UseRenameCard(ctx, g, renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, now.Add(time.Hour), resolver.resolve)
	if err != nil {
		t.Fatalf("retry error = %v", err)
	}
	if retry.Changed || retry.Removed || retry.Stack.ItemID != 0 || retry.Stack.Count != 0 {
		t.Fatalf("retry did work: %+v", retry)
	}
	if resolver.calls != 1 {
		t.Fatalf("retry resolved a second card: calls = %d", resolver.calls)
	}
	assertRenameCardStateUnchanged(t, ctx, g, beforeCharacter, beforeInventory)

	other, err := UseRenameCard(ctx, g, renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot+1, now, resolver.resolve)
	if !errors.Is(err, ErrRenameUnavailable) {
		t.Fatalf("granted rename moved to another slot: err = %v, result = %+v", err, other)
	}
	if other.Changed || resolver.calls != 1 {
		t.Fatalf("second slot consumed a card: calls = %d result = %+v", resolver.calls, other)
	}
	assertRenameCardStateUnchanged(t, ctx, g, beforeCharacter, beforeInventory)
}

func TestUseRenameCardRemovesTheRowWhenTheLastCardIsConsumed(t *testing.T) {
	ctx := context.Background()
	g, now := newRenameCardFixture(t)
	slots := renameCardFixtureSlots()
	slots[renameCardSlotKey(renameCardOwnedSlot)] = renameCardStack(renameCardItemID, 1)
	saveRenameCardInventory(t, ctx, g, slots)

	result, err := UseRenameCard(ctx, g, renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, now, renameCardOKResolver().resolve)
	if err != nil {
		t.Fatalf("UseRenameCard error = %v", err)
	}
	if !result.Changed || !result.Removed || result.Stack.Count != 0 {
		t.Fatalf("result = %+v", result)
	}
	inventory := requireRenameCardInventory(t, ctx, g)
	if _, found := inventory.Slots[renameCardSlotKey(renameCardOwnedSlot)]; found {
		t.Fatalf("exhausted card row survived: %+v", inventory.Slots)
	}
	if material := inventory.Slots[renameCardSlotKey(renameCardMaterialSlot)]; material.Count != 5 {
		t.Fatalf("unrelated material stack changed: %+v", material)
	}
	if character := requireRenameCardCharacter(t, ctx, g); character.Stats[RenameEntitlementStat] != 1 {
		t.Fatalf("entitlement not granted: %+v", character.Stats)
	}
}

func TestUseRenameCardTakesSlotCapacityFromThePersistedExpansion(t *testing.T) {
	ctx := context.Background()
	g, now := newRenameCardFixture(t)
	slots := renameCardFixtureSlots()
	delete(slots, renameCardSlotKey(renameCardOwnedSlot))
	slots[renameCardSlotKey(renameCardExpandedSlot)] = renameCardStack(renameCardItemID, 2)
	saveRenameCardInventory(t, ctx, g, slots)
	resolver := renameCardOKResolver()

	if _, err := UseRenameCard(ctx, g, renameCardAccountID, renameCardCharacterID, renameCardExpandedSlot, now, resolver.resolve); !errors.Is(err, ErrRenameUnavailable) {
		t.Fatalf("unexpanded bag accepted slot %d: err = %v", renameCardExpandedSlot, err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want the policy to be consulted once", resolver.calls)
	}

	saveRenameCardContainerState(t, ctx, g, "8")
	result, err := UseRenameCard(ctx, g, renameCardAccountID, renameCardCharacterID, renameCardExpandedSlot, now, resolver.resolve)
	if err != nil {
		t.Fatalf("expanded bag rejected slot %d: %v", renameCardExpandedSlot, err)
	}
	if !result.Changed || result.Removed || result.Stack.Count != 1 {
		t.Fatalf("result = %+v", result)
	}
	character := requireRenameCardCharacter(t, ctx, g)
	if character.Stats[RenameCardSlotStat] != int64(renameCardExpandedSlot) || character.Stats[RenameEntitlementStat] != 1 {
		t.Fatalf("expansion slot not recorded: %+v", character.Stats)
	}
	inventory := requireRenameCardInventory(t, ctx, g)
	if card := inventory.Slots[renameCardSlotKey(renameCardExpandedSlot)]; card.Count != 1 {
		t.Fatalf("expanded slot card stack = %+v", card)
	}
}

func TestUseRenameCardRejectsUnavailableUsesWithoutConsuming(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name          string
		resolverCalls int
		wantErr       error
		arrange       func(t *testing.T, g repo.Group, now time.Time) (string, string, int16, *recordingRenameCardResolver)
	}{
		{
			name:          "account_owned_by_another_owner",
			resolverCalls: 0,
			wantErr:       ErrRenameUnavailable,
			arrange: func(t *testing.T, g repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				saveRenameCardAccount(t, ctx, g, "intruder")
				return "intruder", renameCardCharacterID, renameCardOwnedSlot, renameCardOKResolver()
			},
		},
		{
			name:          "account_not_opened",
			resolverCalls: 0,
			wantErr:       ErrRenameUnavailable,
			arrange: func(_ *testing.T, _ repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				return "ghost", renameCardCharacterID, renameCardOwnedSlot, renameCardOKResolver()
			},
		},
		{
			name:          "character_not_created",
			resolverCalls: 0,
			wantErr:       ErrRenameUnavailable,
			arrange: func(_ *testing.T, _ repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				return renameCardAccountID, "404", renameCardOwnedSlot, renameCardOKResolver()
			},
		},
		{
			name:          "character_marked_deleted",
			resolverCalls: 0,
			wantErr:       ErrRenameUnavailable,
			arrange: func(t *testing.T, g repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				character := requireRenameCardCharacter(t, ctx, g)
				character.Stats["delete_flag"] = 1
				saveRenameCardCharacter(t, ctx, g, character)
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, renameCardOKResolver()
			},
		},
		{
			name:          "character_inside_dungeon",
			resolverCalls: 0,
			wantErr:       ErrRenameUnavailable,
			arrange: func(t *testing.T, g repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				character := requireRenameCardCharacter(t, ctx, g)
				character.Location.DungeonID = 7
				saveRenameCardCharacter(t, ctx, g, character)
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, renameCardOKResolver()
			},
		},
		{
			name:          "no_card_in_slot",
			resolverCalls: 0,
			wantErr:       ErrRenameUnavailable,
			arrange: func(t *testing.T, g repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				slots := renameCardFixtureSlots()
				delete(slots, renameCardSlotKey(renameCardOwnedSlot))
				saveRenameCardInventory(t, ctx, g, slots)
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, renameCardOKResolver()
			},
		},
		{
			name:          "card_count_zero",
			resolverCalls: 0,
			wantErr:       ErrRenameUnavailable,
			arrange: func(t *testing.T, g repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				slots := renameCardFixtureSlots()
				slots[renameCardSlotKey(renameCardOwnedSlot)] = renameCardStack(renameCardItemID, 0)
				saveRenameCardInventory(t, ctx, g, slots)
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, renameCardOKResolver()
			},
		},
		{
			name:          "card_count_negative",
			resolverCalls: 0,
			wantErr:       ErrRenameUnavailable,
			arrange: func(t *testing.T, g repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				slots := renameCardFixtureSlots()
				slots[renameCardSlotKey(renameCardOwnedSlot)] = renameCardStack(renameCardItemID, -1)
				saveRenameCardInventory(t, ctx, g, slots)
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, renameCardOKResolver()
			},
		},
		{
			name:          "card_count_overflow",
			resolverCalls: 0,
			wantErr:       ErrRenameUnavailable,
			arrange: func(t *testing.T, g repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				slots := renameCardFixtureSlots()
				slots[renameCardSlotKey(renameCardOwnedSlot)] = renameCardStack(renameCardItemID, int64(math.MaxUint32)+1)
				saveRenameCardInventory(t, ctx, g, slots)
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, renameCardOKResolver()
			},
		},
		{
			name:          "card_item_id_overflow",
			resolverCalls: 0,
			wantErr:       ErrRenameUnavailable,
			arrange: func(t *testing.T, g repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				slots := renameCardFixtureSlots()
				slots[renameCardSlotKey(renameCardOwnedSlot)] = renameCardStack(int64(math.MaxUint32)+1, 2)
				saveRenameCardInventory(t, ctx, g, slots)
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, renameCardOKResolver()
			},
		},
		{
			name:          "card_item_id_unset",
			resolverCalls: 0,
			wantErr:       ErrRenameUnavailable,
			arrange: func(t *testing.T, g repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				slots := renameCardFixtureSlots()
				slots[renameCardSlotKey(renameCardOwnedSlot)] = renameCardStack(0, 2)
				saveRenameCardInventory(t, ctx, g, slots)
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, renameCardOKResolver()
			},
		},
		{
			name:          "account_shared_warehouse_slot",
			resolverCalls: 0,
			wantErr:       ErrRenameUnavailable,
			arrange: func(t *testing.T, g repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				slots := renameCardFixtureSlots()
				slots[renameCardSlotKey(renameCardSharedSlot)] = renameCardStack(renameCardItemID, 2)
				saveRenameCardInventory(t, ctx, g, slots)
				return renameCardAccountID, renameCardCharacterID, renameCardSharedSlot, renameCardOKResolver()
			},
		},
		{
			name:          "slot_outside_persisted_capacity",
			resolverCalls: 1,
			wantErr:       ErrRenameUnavailable,
			arrange: func(t *testing.T, g repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				slots := renameCardFixtureSlots()
				delete(slots, renameCardSlotKey(renameCardOwnedSlot))
				slots[renameCardSlotKey(renameCardExpandedSlot)] = renameCardStack(renameCardItemID, 2)
				saveRenameCardInventory(t, ctx, g, slots)
				return renameCardAccountID, renameCardCharacterID, renameCardExpandedSlot, renameCardOKResolver()
			},
		},
		{
			name:          "minimum_level_not_met",
			resolverCalls: 1,
			wantErr:       ErrRenameUnavailable,
			arrange: func(_ *testing.T, _ repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				resolver := renameCardOKResolver()
				policy := renameCardPolicy()
				policy.MinimumLevel = 2
				resolver.policy = policy
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, resolver
			},
		},
		{
			name:          "minimum_level_unset",
			resolverCalls: 1,
			wantErr:       ErrRenameUnavailable,
			arrange: func(_ *testing.T, _ repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				resolver := renameCardOKResolver()
				policy := renameCardPolicy()
				policy.MinimumLevel = 0
				resolver.policy = policy
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, resolver
			},
		},
		{
			name:          "cooldown_seconds_drift",
			resolverCalls: 1,
			wantErr:       ErrRenameUnavailable,
			arrange: func(_ *testing.T, _ repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				resolver := renameCardOKResolver()
				policy := renameCardPolicy()
				policy.CooldownSeconds = 60
				resolver.policy = policy
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, resolver
			},
		},
		{
			name:          "reservation_seconds_drift",
			resolverCalls: 1,
			wantErr:       ErrRenameUnavailable,
			arrange: func(_ *testing.T, _ repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				resolver := renameCardOKResolver()
				policy := renameCardPolicy()
				policy.ReservationSeconds = 60
				resolver.policy = policy
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, resolver
			},
		},
		{
			name:          "next_rename_still_on_cooldown",
			resolverCalls: 0,
			wantErr:       ErrRenameUnavailable,
			arrange: func(t *testing.T, g repo.Group, now time.Time) (string, string, int16, *recordingRenameCardResolver) {
				character := requireRenameCardCharacter(t, ctx, g)
				character.Stats[RenameNextAllowedUnixStat] = now.Add(time.Hour).Unix()
				saveRenameCardCharacter(t, ctx, g, character)
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, renameCardOKResolver()
			},
		},
		{
			name:          "resolver_error",
			resolverCalls: 1,
			wantErr:       errRenameCardResolverFailure,
			arrange: func(_ *testing.T, _ repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				resolver := renameCardOKResolver()
				resolver.rewrite = func(repo.ItemStack) (RenameCardPolicy, repo.ItemStack, error) {
					return RenameCardPolicy{}, repo.ItemStack{}, errRenameCardResolverFailure
				}
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, resolver
			},
		},
		{
			name:          "resolver_rewrote_item_id",
			resolverCalls: 1,
			wantErr:       ErrRenameUnavailable,
			arrange: func(_ *testing.T, _ repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				resolver := renameCardOKResolver()
				resolver.rewrite = func(held repo.ItemStack) (RenameCardPolicy, repo.ItemStack, error) {
					projected := held
					projected.ItemID++
					return renameCardPolicy(), projected, nil
				}
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, resolver
			},
		},
		{
			name:          "resolver_rewrote_count",
			resolverCalls: 1,
			wantErr:       ErrRenameUnavailable,
			arrange: func(_ *testing.T, _ repo.Group, _ time.Time) (string, string, int16, *recordingRenameCardResolver) {
				resolver := renameCardOKResolver()
				resolver.rewrite = func(held repo.ItemStack) (RenameCardPolicy, repo.ItemStack, error) {
					projected := held
					projected.Count++
					return renameCardPolicy(), projected, nil
				}
				return renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, resolver
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, now := newRenameCardFixture(t)
			accountID, characterID, slot, resolver := tc.arrange(t, g, now)
			beforeCharacter, beforeInventory := snapshotRenameCardState(t, ctx, g)

			result, err := UseRenameCard(ctx, g, accountID, characterID, slot, now, resolver.resolve)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
			} else if err == nil {
				t.Fatal("accepted an unavailable rename card use")
			}
			if result.Changed || result.Removed || result.Stack.ItemID != 0 || result.Stack.Count != 0 {
				t.Fatalf("rejected use returned %+v", result)
			}
			if resolver.calls != tc.resolverCalls {
				t.Fatalf("resolver calls = %d, want %d", resolver.calls, tc.resolverCalls)
			}
			assertRenameCardStateUnchanged(t, ctx, g, beforeCharacter, beforeInventory)
		})
	}
}

func TestUseRenameCardRejectsInvalidArguments(t *testing.T) {
	ctx := context.Background()
	g, now := newRenameCardFixture(t)
	resolver := renameCardOKResolver()
	withoutTransaction := g
	withoutTransaction.CeraShopAssets = nil

	cases := []struct {
		name        string
		group       repo.Group
		accountID   string
		characterID string
		slot        int16
		now         time.Time
		resolve     RenameCardResolver
	}{
		{"missing_cera_shop_transaction", withoutTransaction, renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, now, resolver.resolve},
		{"missing_resolver", g, renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, now, nil},
		{"missing_account_id", g, "", renameCardCharacterID, renameCardOwnedSlot, now, resolver.resolve},
		{"missing_character_id", g, renameCardAccountID, "", renameCardOwnedSlot, now, resolver.resolve},
		{"missing_clock", g, renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, time.Time{}, resolver.resolve},
		{"negative_slot", g, renameCardAccountID, renameCardCharacterID, -1, now, resolver.resolve},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := UseRenameCard(ctx, tc.group, tc.accountID, tc.characterID, tc.slot, tc.now, tc.resolve); !errors.Is(err, ErrRenameUnavailable) {
				t.Fatalf("error = %v, want %v", err, ErrRenameUnavailable)
			}
		})
	}
	if resolver.calls != 0 {
		t.Fatalf("invalid arguments consulted the resolver %d times", resolver.calls)
	}
}

// The memory group captures its stores inside the CeraShopAssets unit of work at
// construction time, so a failing replacement for repo.Group.Inventory never
// reaches the transaction. This decorator keeps the real memory transaction and
// substitutes only the inventory repository the callback writes through, which is
// the same store call UseRenameCard makes: SaveInventoryFields has no field-store
// implementation behind either repository and degrades to Save. The memory unit of
// work writes only after the callback returns nil, so the failure aborts the use
// before any row is committed.
type failingInventoryCeraShop struct {
	repo.CeraShopAssetUnitOfWork
	fail   error
	staged repo.InventoryRecord
}

func (w *failingInventoryCeraShop) WithinCeraShopAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	settingsScope string,
	apply func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository) error,
) error {
	return w.CeraShopAssetUnitOfWork.WithinCeraShopAssets(ctx, accountID, characterID, settingsScope,
		func(accounts repo.AccountRepository, characters repo.CharacterRepository, inventories repo.InventoryRepository, equipment repo.EquipmentRepository, settings repo.SettingsRepository) error {
			return apply(accounts, characters, &failingInventoryStore{InventoryRepository: inventories, owner: w}, equipment, settings)
		})
}

type failingInventoryStore struct {
	repo.InventoryRepository
	owner *failingInventoryCeraShop
}

func (s *failingInventoryStore) Save(_ context.Context, record repo.InventoryRecord) error {
	s.owner.staged = repo.CloneInventory(record)
	return s.owner.fail
}

func TestUseRenameCardInventorySaveFailureCommitsNothing(t *testing.T) {
	ctx := context.Background()
	g, now := newRenameCardFixture(t)
	failing := &failingInventoryCeraShop{CeraShopAssetUnitOfWork: g.CeraShopAssets, fail: errors.New("inventory row save failed")}
	transactional := g
	transactional.CeraShopAssets = failing
	resolver := renameCardOKResolver()

	result, err := UseRenameCard(ctx, transactional, renameCardAccountID, renameCardCharacterID, renameCardOwnedSlot, now, resolver.resolve)
	if !errors.Is(err, failing.fail) {
		t.Fatalf("error = %v, want the inventory save failure", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.calls)
	}
	if result.Changed || result.Removed || result.Stack.Count != 0 {
		t.Fatalf("failed use reported success: %+v", result)
	}
	if staged := failing.staged.Slots[renameCardSlotKey(renameCardOwnedSlot)]; staged.Count != 1 {
		t.Fatalf("failure was not raised on the decremented stack: %+v", failing.staged.Slots)
	}

	character := requireRenameCardCharacter(t, ctx, g)
	if character.Stats[RenameEntitlementStat] != 0 || character.Stats[RenameCardSlotStat] != 0 || !character.UpdatedAt.IsZero() {
		t.Fatalf("failed use leaked an entitlement: %+v", character)
	}
	inventory := requireRenameCardInventory(t, ctx, g)
	if card := inventory.Slots[renameCardSlotKey(renameCardOwnedSlot)]; card.Count != 2 || card.ItemID != renameCardItemID {
		t.Fatalf("failed use leaked the card write: %+v", card)
	}
	if !inventory.UpdatedAt.IsZero() {
		t.Fatalf("failed use leaked an inventory timestamp: %+v", inventory)
	}
}
