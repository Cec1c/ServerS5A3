package quest

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	boxRecoveryTestAccount   = "dnf:1"
	boxRecoveryTestCharacter = "19"
)

var errBoxRecoveryTestBagFull = errors.New("test box recovery bag full")

func TestBoxSourceRecoveryBothNoRewardMarkers(t *testing.T) {
	for _, reason := range []string{
		"main_story_level_clear_no_reward",
		"epic_below_character_level_no_reward",
	} {
		t.Run(reason, func(t *testing.T) {
			repositories, owner, catalog := boxRecoveryTestFixture(t, reason)
			result, err := owner.RecoverBoxSource(context.Background(), catalog, boxRecoveryTestInput(nil))
			if err != nil {
				t.Fatal(err)
			}
			if !result.Recovered || result.Item.ItemID != BoxSourceItemID || result.Item.Delta != 1 {
				t.Fatalf("recovery result=%+v", result)
			}
			assertBoxRecoveryCount(t, repositories, 1)
			record := loadBoxRecoveryQuest(t, repositories)
			raw := record.States[BoxSourceQuestID].Extra[BoxSourceRecoveryReceiptKey]
			if raw == "" {
				t.Fatal("source quest has no box recovery receipt")
			}
			var receipt struct {
				Version         int    `json:"version"`
				CharacterID     string `json:"character_id"`
				SourceQuestID   int64  `json:"source_quest_id"`
				ParentQuestID   int64  `json:"parent_quest_id"`
				DeliveryQuestID int64  `json:"delivery_quest_id"`
				ItemID          int64  `json:"item_id"`
				Count           int64  `json:"count"`
				SlotKey         string `json:"slot_key"`
			}
			if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
				t.Fatalf("decode receipt: %v", err)
			}
			if receipt.Version != 1 || receipt.CharacterID != boxRecoveryTestCharacter ||
				receipt.SourceQuestID != BoxSourceQuestID ||
				receipt.ParentQuestID != BoxSourceParentQuestID ||
				receipt.DeliveryQuestID != BoxSourceDeliveryQuestID ||
				receipt.ItemID != BoxSourceItemID || receipt.Count != 1 ||
				receipt.SlotKey != result.Item.SlotKey {
				t.Fatalf("receipt=%+v item=%+v", receipt, result.Item)
			}
			if record.States[4226].Extra["keep"] != "yes" ||
				record.States[BoxSourceParentQuestID].Status != "active" ||
				record.States[BoxSourceDeliveryQuestID].Status != "active" {
				t.Fatalf("unrelated quest state changed: %+v", record.States)
			}
		})
	}
}

func TestBoxSourceRecoveryDoesNotGrantForOrdinaryOrCompletedStates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*dnfrepo.QuestRecord, *dnfrepo.InventoryRecord)
	}{
		{
			name: "normal-source",
			mutate: func(record *dnfrepo.QuestRecord, _ *dnfrepo.InventoryRecord) {
				state := record.States[BoxSourceQuestID]
				delete(state.Extra, "auto_complete_reason")
				state.Extra[finishSettlementReceiptKey] = `{"version":1}`
				record.States[BoxSourceQuestID] = state
			},
		},
		{
			name: "source-not-completed",
			mutate: func(record *dnfrepo.QuestRecord, _ *dnfrepo.InventoryRecord) {
				state := record.States[BoxSourceQuestID]
				state.Status = "active"
				record.States[BoxSourceQuestID] = state
			},
		},
		{
			name: "already-has-box",
			mutate: func(_ *dnfrepo.QuestRecord, inventory *dnfrepo.InventoryRecord) {
				inventory.Slots["0:80"] = boxRecoveryTestStack(80, 1)
			},
		},
		{
			name: "target-already-completed",
			mutate: func(record *dnfrepo.QuestRecord, _ *dnfrepo.InventoryRecord) {
				state := record.States[BoxSourceDeliveryQuestID]
				state.Status = "completed"
				record.States[BoxSourceDeliveryQuestID] = state
			},
		},
		{
			name: "parent-already-completed",
			mutate: func(record *dnfrepo.QuestRecord, _ *dnfrepo.InventoryRecord) {
				state := record.States[BoxSourceParentQuestID]
				state.Status = "completed"
				record.States[BoxSourceParentQuestID] = state
			},
		},
		{
			name: "existing-receipt",
			mutate: func(record *dnfrepo.QuestRecord, _ *dnfrepo.InventoryRecord) {
				state := record.States[BoxSourceQuestID]
				state.Extra[BoxSourceRecoveryReceiptKey] = `{"version":1}`
				record.States[BoxSourceQuestID] = state
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repositories := boxRecoveryTestRepositories(t)
			ctx := context.Background()
			record, found, err := repositories.Quest.Load(ctx, boxRecoveryTestCharacter)
			if err != nil || !found {
				t.Fatalf("load quest found=%t err=%v", found, err)
			}
			inventory, found, err := repositories.Inventory.Load(ctx, boxRecoveryTestCharacter)
			if err != nil || !found {
				t.Fatalf("load inventory found=%t err=%v", found, err)
			}
			tc.mutate(&record, &inventory)
			if err := repositories.Quest.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory.Save(ctx, inventory); err != nil {
				t.Fatal(err)
			}
			beforeQuests := loadBoxRecoveryQuest(t, repositories)
			beforeInventory := loadBoxRecoveryInventory(t, repositories)
			owner, err := NewOwner(repositories)
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.RecoverBoxSource(ctx, boxRecoveryTestCatalog(t, nil), boxRecoveryTestInput(nil))
			if err != nil {
				t.Fatal(err)
			}
			if result.Recovered {
				t.Fatalf("unexpected recovery: %+v", result)
			}
			if got := loadBoxRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
				t.Fatalf("quest changed: got=%+v want=%+v", got, beforeQuests)
			}
			if got := loadBoxRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
				t.Fatalf("inventory changed: got=%+v want=%+v", got, beforeInventory)
			}
		})
	}
}

func TestBoxSourceRecoveryDefinitionMismatchFailsClosed(t *testing.T) {
	repositories, owner, _ := boxRecoveryTestFixture(t, "epic_below_character_level_no_reward")
	catalog := boxRecoveryTestCatalog(t, func(source catalogTestSource) {
		const path = "n_quest/box_delivery.qst"
		source[path] = strings.Replace(source[path], "`[seeking]`", "`[seek n meet npc]`", 1)
	})
	beforeQuests := loadBoxRecoveryQuest(t, repositories)
	beforeInventory := loadBoxRecoveryInventory(t, repositories)
	_, err := owner.RecoverBoxSource(context.Background(), catalog, boxRecoveryTestInput(nil))
	if !errors.Is(err, ErrBoxSourceRecoveryDefinitionMismatch) {
		t.Fatalf("definition mismatch error=%v", err)
	}
	if got := loadBoxRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
		t.Fatalf("definition mismatch changed quest: %+v", got)
	}
	if got := loadBoxRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
		t.Fatalf("definition mismatch changed inventory: %+v", got)
	}
}

func TestBoxSourceRecoveryRepeatedAndConcurrentGrantOnce(t *testing.T) {
	repositories, owner, catalog := boxRecoveryTestFixture(t, "main_story_level_clear_no_reward")
	first, err := owner.RecoverBoxSource(context.Background(), catalog, boxRecoveryTestInput(nil))
	if err != nil || !first.Recovered {
		t.Fatalf("first recovery result=%+v err=%v", first, err)
	}
	second, err := owner.RecoverBoxSource(context.Background(), catalog, boxRecoveryTestInput(nil))
	if err != nil || second.Recovered {
		t.Fatalf("repeated recovery result=%+v err=%v", second, err)
	}
	assertBoxRecoveryCount(t, repositories, 1)

	repositories, owner, catalog = boxRecoveryTestFixture(t, "main_story_level_clear_no_reward")
	const workers = 12
	var recovered atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := owner.RecoverBoxSource(context.Background(), catalog, boxRecoveryTestInput(nil))
			if err != nil {
				errs <- err
				return
			}
			if result.Recovered {
				recovered.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := recovered.Load(); got != 1 {
		t.Fatalf("concurrent recovered calls=%d, want 1", got)
	}
	assertBoxRecoveryCount(t, repositories, 1)
}

func TestBoxSourceRecoveryRollbackAndRetry(t *testing.T) {
	for _, failure := range []string{"inventory", "quest"} {
		t.Run(failure+"-save", func(t *testing.T) {
			repositories, owner, catalog := boxRecoveryTestFixture(t, "epic_below_character_level_no_reward")
			beforeQuests := loadBoxRecoveryQuest(t, repositories)
			beforeInventory := loadBoxRecoveryInventory(t, repositories)
			original := repositories.CharacterSettlement
			repositories.CharacterSettlement = boxRecoveryFailingSettlement{
				CharacterSettlementUnitOfWork: original,
				failure:                       failure,
			}
			owner, _ = NewOwner(repositories)
			_, err := owner.RecoverBoxSource(context.Background(), catalog, boxRecoveryTestInput(nil))
			if err == nil {
				t.Fatalf("%s save failure was accepted", failure)
			}
			if got := loadBoxRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
				t.Fatalf("%s save failure changed quest: %+v", failure, got)
			}
			if got := loadBoxRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
				t.Fatalf("%s save failure changed inventory: %+v", failure, got)
			}

			repositories.CharacterSettlement = original
			owner, _ = NewOwner(repositories)
			retry, err := owner.RecoverBoxSource(context.Background(), catalog, boxRecoveryTestInput(nil))
			if err != nil || !retry.Recovered {
				t.Fatalf("retry after %s save failure result=%+v err=%v", failure, retry, err)
			}
			assertBoxRecoveryCount(t, repositories, 1)
		})
	}
}

func TestBoxSourceRecoveryBagFullAndAccountMismatchChangeNothing(t *testing.T) {
	t.Run("bag-full", func(t *testing.T) {
		repositories, owner, catalog := boxRecoveryTestFixture(t, "epic_below_character_level_no_reward")
		beforeQuests := loadBoxRecoveryQuest(t, repositories)
		beforeInventory := loadBoxRecoveryInventory(t, repositories)
		_, err := owner.RecoverBoxSource(context.Background(), catalog,
			boxRecoveryTestInput(func(*dnfrepo.InventoryRecord, FinishItemGrantRequest) ([]FinishCommittedItem, error) {
				return nil, errBoxRecoveryTestBagFull
			}))
		if !errors.Is(err, errBoxRecoveryTestBagFull) {
			t.Fatalf("bag full error=%v", err)
		}
		if got := loadBoxRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
			t.Fatalf("bag full changed quest: %+v", got)
		}
		if got := loadBoxRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
			t.Fatalf("bag full changed inventory: %+v", got)
		}
	})

	t.Run("account-mismatch", func(t *testing.T) {
		repositories, owner, catalog := boxRecoveryTestFixture(t, "epic_below_character_level_no_reward")
		beforeQuests := loadBoxRecoveryQuest(t, repositories)
		beforeInventory := loadBoxRecoveryInventory(t, repositories)
		input := boxRecoveryTestInput(nil)
		input.AccountID = "dnf:other"
		_, err := owner.RecoverBoxSource(context.Background(), catalog, input)
		if !errors.Is(err, ErrCharacterNotFound) {
			t.Fatalf("account mismatch error=%v", err)
		}
		if got := loadBoxRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
			t.Fatalf("account mismatch changed quest: %+v", got)
		}
		if got := loadBoxRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
			t.Fatalf("account mismatch changed inventory: %+v", got)
		}
	})
}

func TestBoxSourceRecoveryCandidateRequiresTheExactActiveRelation(t *testing.T) {
	repositories, _, _ := boxRecoveryTestFixture(t, "main_story_level_clear_no_reward")
	record := loadBoxRecoveryQuest(t, repositories)
	if !IsBoxSourceRecoveryCandidate(record) {
		t.Fatal("fixture is not a box recovery candidate")
	}
	record.States[BoxSourceParentQuestID] = dnfrepo.QuestState{Status: "completed"}
	if IsBoxSourceRecoveryCandidate(record) {
		t.Fatal("completed parent was still accepted as a live recovery candidate")
	}
}

func boxRecoveryTestFixture(
	t *testing.T,
	reason string,
) (dnfrepo.Group, *Owner, *Catalog) {
	t.Helper()
	repositories := boxRecoveryTestRepositories(t)
	record := loadBoxRecoveryQuest(t, repositories)
	source := record.States[BoxSourceQuestID]
	source.Extra["auto_complete_reason"] = reason
	record.States[BoxSourceQuestID] = source
	if err := repositories.Quest.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	return repositories, owner, boxRecoveryTestCatalog(t, nil)
}

func boxRecoveryTestRepositories(t *testing.T) dnfrepo.Group {
	t.Helper()
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: boxRecoveryTestCharacter,
		AccountID:   boxRecoveryTestAccount,
		Level:       90,
		Job:         "2",
		Stats:       map[string]int64{"grow_type": 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: boxRecoveryTestCharacter,
		States: map[int64]dnfrepo.QuestState{
			BoxSourceQuestID: {
				Status: "completed",
				Extra: map[string]string{
					"reward_state":         "granted",
					"auto_completed":       "true",
					"auto_complete_reason": "",
				},
			},
			BoxSourceParentQuestID:   {Status: "active", ProgressValue: 2},
			BoxSourceMeetQuestID:     {Status: "active", ProgressValue: 1},
			BoxSourceDeliveryQuestID: {Status: "active", ProgressValue: 513},
			4226:                     {Status: "completed", Extra: map[string]string{"keep": "yes"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: boxRecoveryTestCharacter,
		Slots: map[string]dnfrepo.ItemStack{
			"0:79": {ItemID: 3033, Count: 2, Extra: map[string]string{"keep": "yes"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return repositories
}

func boxRecoveryTestCatalog(t *testing.T, mutate func(catalogTestSource)) *Catalog {
	t.Helper()
	source := catalogTestSource{
		DefaultList: "3649 `box_source.qst`\n3656 `box_parent.qst`\n3692 `box_clear.qst`\n3693 `box_delivery.qst`\n",
		"n_quest/box_source.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[type]\n`[clear map]`\n[int data]\n91935\n[reward type]\n`[item]`\n[reward int data]\n10164767 1\n"),
		"n_quest/box_parent.qst": questCatalogTestDefinition("[epic]", 1, 99, "[all]",
			"[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[quest clear]`\n[int data]\n3692 3693\n[reward type]\n`[item]`\n[reward int data]\n0 0\n"),
		"n_quest/box_clear.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[clear map]`\n[main quest]\n3656\n[int data]\n91986\n[reward type]\n`[item]`\n[reward int data]\n0 0\n"),
		"n_quest/box_delivery.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[seeking]`\n[main quest]\n3656\n[int data]\n10164767 1\n[reward type]\n`[item]`\n[reward int data]\n0 0\n"),
	}
	if mutate != nil {
		mutate(source)
	}
	return loadQuestCatalogForTest(t, source)
}

func boxRecoveryTestInput(allocator FinishItemAllocator) BoxSourceRecoveryInput {
	if allocator == nil {
		allocator = boxRecoveryTestAllocator()
	}
	return BoxSourceRecoveryInput{
		AccountID:    boxRecoveryTestAccount,
		CharacterID:  boxRecoveryTestCharacter,
		RecoveredAt:  time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
		AllocateItem: allocator,
	}
}

func boxRecoveryTestAllocator() FinishItemAllocator {
	return func(record *dnfrepo.InventoryRecord, request FinishItemGrantRequest) ([]FinishCommittedItem, error) {
		if record == nil || request.ItemID != BoxSourceItemID || request.Count != 1 {
			return nil, errBoxRecoveryTestBagFull
		}
		const slot uint16 = 50
		key := "0:50"
		if _, occupied := record.Slots[key]; occupied {
			return nil, errBoxRecoveryTestBagFull
		}
		raw := make([]byte, currentQuestFinishRawEntrySize)
		binary.LittleEndian.PutUint16(raw[0:2], slot)
		binary.LittleEndian.PutUint32(raw[2:6], uint32(request.ItemID))
		binary.LittleEndian.PutUint32(raw[6:10], 1)
		stack := dnfrepo.ItemStack{
			ItemID: request.ItemID, Count: 1, RawEntry: raw,
			Extra: map[string]string{"count": "1", "source": request.Source},
		}
		record.Slots[key] = stack
		return []FinishCommittedItem{{
			SlotKey: key, SlotIndex: slot, ItemID: request.ItemID,
			Delta: 1, PostCount: 1, CountOrSeed: 1, RawEntry: append([]byte(nil), raw...),
		}}, nil
	}
}

func boxRecoveryTestStack(slot uint16, count int64) dnfrepo.ItemStack {
	raw := make([]byte, currentQuestFinishRawEntrySize)
	binary.LittleEndian.PutUint16(raw[0:2], slot)
	binary.LittleEndian.PutUint32(raw[2:6], uint32(BoxSourceItemID))
	binary.LittleEndian.PutUint32(raw[6:10], uint32(count))
	return dnfrepo.ItemStack{ItemID: BoxSourceItemID, Count: count, RawEntry: raw}
}

func loadBoxRecoveryQuest(t *testing.T, repositories dnfrepo.Group) dnfrepo.QuestRecord {
	t.Helper()
	record, found, err := repositories.Quest.Load(context.Background(), boxRecoveryTestCharacter)
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	return record
}

func loadBoxRecoveryInventory(t *testing.T, repositories dnfrepo.Group) dnfrepo.InventoryRecord {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(context.Background(), boxRecoveryTestCharacter)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	return inventory
}

func boxRecoveryInventoryCount(inventory dnfrepo.InventoryRecord) int64 {
	var count int64
	for _, stack := range inventory.Slots {
		if stack.ItemID == BoxSourceItemID {
			count += stack.Count
		}
	}
	return count
}

func assertBoxRecoveryCount(t *testing.T, repositories dnfrepo.Group, want int64) {
	t.Helper()
	if got := boxRecoveryInventoryCount(loadBoxRecoveryInventory(t, repositories)); got != want {
		t.Fatalf("box count=%d, want %d", got, want)
	}
}

type boxRecoveryFailingSettlement struct {
	dnfrepo.CharacterSettlementUnitOfWork
	failure string
}

func (u boxRecoveryFailingSettlement) WithinCharacterSettlement(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.Group) error,
) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		switch u.failure {
		case "inventory":
			tx.Inventory = boxRecoveryFailInventory{InventoryRepository: tx.Inventory}
		case "quest":
			tx.Quest = boxRecoveryFailQuest{QuestRepository: tx.Quest}
		}
		return apply(tx)
	})
}

type boxRecoveryFailInventory struct {
	dnfrepo.InventoryRepository
}

func (boxRecoveryFailInventory) Save(context.Context, dnfrepo.InventoryRecord) error {
	return errors.New("test box inventory save failure")
}

type boxRecoveryFailQuest struct {
	dnfrepo.QuestRepository
}

func (boxRecoveryFailQuest) Save(context.Context, dnfrepo.QuestRecord) error {
	return errors.New("test box quest save failure")
}
