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
	queenRecoveryTestAccount   = "dnf:1"
	queenRecoveryTestCharacter = "19"
)

var errQueenRecoveryTestBagFull = errors.New("test queen recovery bag full")

func TestQueenLetterRecoveryBothNoRewardMarkers(t *testing.T) {
	for _, reason := range []string{
		"main_story_level_clear_no_reward",
		"epic_below_character_level_no_reward",
	} {
		t.Run(reason, func(t *testing.T) {
			repositories, owner, catalog := queenRecoveryTestFixture(t, reason)
			result, err := owner.RecoverQueenLetter(context.Background(), catalog, queenRecoveryTestInput(nil))
			if err != nil {
				t.Fatal(err)
			}
			if !result.Recovered || result.Item.ItemID != QueenLetterItemID || result.Item.Delta != 1 {
				t.Fatalf("recovery result=%+v", result)
			}
			assertQueenRecoveryLetterCount(t, repositories, 1)
			record := loadQueenRecoveryQuest(t, repositories)
			raw := record.States[QueenLetterSourceQuestID].Extra[QueenLetterRecoveryReceiptKey]
			if raw == "" {
				t.Fatal("source quest has no recovery receipt")
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
			if receipt.Version != 1 || receipt.CharacterID != queenRecoveryTestCharacter ||
				receipt.SourceQuestID != QueenLetterSourceQuestID ||
				receipt.ParentQuestID != QueenLetterParentQuestID ||
				receipt.DeliveryQuestID != QueenLetterDeliveryQuestID ||
				receipt.ItemID != QueenLetterItemID || receipt.Count != 1 ||
				receipt.SlotKey != result.Item.SlotKey {
				t.Fatalf("receipt=%+v item=%+v", receipt, result.Item)
			}
			if record.States[4226].Extra["keep"] != "yes" || record.States[3249].Status != "active" ||
				record.States[3610].Status != "active" {
				t.Fatalf("unrelated quest state changed: %+v", record.States)
			}
		})
	}
}

func TestQueenLetterRecoveryDoesNotGrantForOrdinaryOrCompletedStates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*dnfrepo.QuestRecord, *dnfrepo.InventoryRecord)
	}{
		{
			name: "normal-source",
			mutate: func(record *dnfrepo.QuestRecord, _ *dnfrepo.InventoryRecord) {
				state := record.States[QueenLetterSourceQuestID]
				delete(state.Extra, "auto_complete_reason")
				state.Extra[finishSettlementReceiptKey] = `{"version":1}`
				record.States[QueenLetterSourceQuestID] = state
			},
		},
		{
			name: "already-has-letter",
			mutate: func(_ *dnfrepo.QuestRecord, inventory *dnfrepo.InventoryRecord) {
				inventory.Slots["0:80"] = queenRecoveryTestStack(80, 1)
			},
		},
		{
			name: "target-already-completed",
			mutate: func(record *dnfrepo.QuestRecord, _ *dnfrepo.InventoryRecord) {
				state := record.States[QueenLetterDeliveryQuestID]
				state.Status = "completed"
				record.States[QueenLetterDeliveryQuestID] = state
			},
		},
		{
			name: "existing-receipt",
			mutate: func(record *dnfrepo.QuestRecord, _ *dnfrepo.InventoryRecord) {
				state := record.States[QueenLetterSourceQuestID]
				state.Extra[QueenLetterRecoveryReceiptKey] = `{"version":1}`
				record.States[QueenLetterSourceQuestID] = state
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repositories := queenRecoveryTestRepositories(t)
			ctx := context.Background()
			record, found, err := repositories.Quest.Load(ctx, queenRecoveryTestCharacter)
			if err != nil || !found {
				t.Fatalf("load quest found=%t err=%v", found, err)
			}
			inventory, found, err := repositories.Inventory.Load(ctx, queenRecoveryTestCharacter)
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
			beforeQuests := loadQueenRecoveryQuest(t, repositories)
			beforeInventory := loadQueenRecoveryInventory(t, repositories)
			owner, err := NewOwner(repositories)
			if err != nil {
				t.Fatal(err)
			}
			catalog := queenRecoveryTestCatalog(t, nil)
			result, err := owner.RecoverQueenLetter(ctx, catalog, queenRecoveryTestInput(nil))
			if err != nil {
				t.Fatal(err)
			}
			if result.Recovered {
				t.Fatalf("unexpected recovery: %+v", result)
			}
			if got := loadQueenRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
				t.Fatalf("quest changed: got=%+v want=%+v", got, beforeQuests)
			}
			if got := loadQueenRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
				t.Fatalf("inventory changed: got=%+v want=%+v", got, beforeInventory)
			}
			assertQueenRecoveryLetterCount(t, repositories, queenRecoveryInventoryLetterCount(beforeInventory))
		})
	}
}

func TestQueenLetterRecoveryDefinitionMismatchFailsClosed(t *testing.T) {
	repositories, owner, _ := queenRecoveryTestFixture(t, "epic_below_character_level_no_reward")
	catalog := queenRecoveryTestCatalog(t, func(source catalogTestSource) {
		path := "n_quest/queen_delivery.qst"
		source[path] = strings.Replace(source[path], "10164786 1 303", "10164786 2 303", 1)
	})
	beforeQuests := loadQueenRecoveryQuest(t, repositories)
	beforeInventory := loadQueenRecoveryInventory(t, repositories)
	_, err := owner.RecoverQueenLetter(context.Background(), catalog, queenRecoveryTestInput(nil))
	if !errors.Is(err, ErrQueenLetterRecoveryDefinitionMismatch) {
		t.Fatalf("definition mismatch error=%v", err)
	}
	if got := loadQueenRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
		t.Fatalf("definition mismatch changed quest: %+v", got)
	}
	if got := loadQueenRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
		t.Fatalf("definition mismatch changed inventory: %+v", got)
	}
}

func TestQueenLetterRecoveryRepeatedAndConcurrentGrantOnce(t *testing.T) {
	repositories, owner, catalog := queenRecoveryTestFixture(t, "main_story_level_clear_no_reward")
	first, err := owner.RecoverQueenLetter(context.Background(), catalog, queenRecoveryTestInput(nil))
	if err != nil || !first.Recovered {
		t.Fatalf("first recovery result=%+v err=%v", first, err)
	}
	second, err := owner.RecoverQueenLetter(context.Background(), catalog, queenRecoveryTestInput(nil))
	if err != nil || second.Recovered {
		t.Fatalf("repeated recovery result=%+v err=%v", second, err)
	}
	assertQueenRecoveryLetterCount(t, repositories, 1)

	repositories, owner, catalog = queenRecoveryTestFixture(t, "main_story_level_clear_no_reward")
	const workers = 12
	var recovered atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := owner.RecoverQueenLetter(context.Background(), catalog, queenRecoveryTestInput(nil))
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
	assertQueenRecoveryLetterCount(t, repositories, 1)
}

func TestQueenLetterRecoveryRollbackAndRetry(t *testing.T) {
	for _, failure := range []string{"inventory", "quest"} {
		t.Run(failure+"-save", func(t *testing.T) {
			repositories, owner, catalog := queenRecoveryTestFixture(t, "epic_below_character_level_no_reward")
			beforeQuests := loadQueenRecoveryQuest(t, repositories)
			beforeInventory := loadQueenRecoveryInventory(t, repositories)
			original := repositories.CharacterSettlement
			repositories.CharacterSettlement = queenRecoveryFailingSettlement{
				CharacterSettlementUnitOfWork: original,
				failure:                       failure,
			}
			owner, _ = NewOwner(repositories)
			_, err := owner.RecoverQueenLetter(context.Background(), catalog, queenRecoveryTestInput(nil))
			if err == nil {
				t.Fatalf("%s save failure was accepted", failure)
			}
			if got := loadQueenRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
				t.Fatalf("%s save failure changed quest: %+v", failure, got)
			}
			if got := loadQueenRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
				t.Fatalf("%s save failure changed inventory: %+v", failure, got)
			}

			repositories.CharacterSettlement = original
			owner, _ = NewOwner(repositories)
			retry, err := owner.RecoverQueenLetter(context.Background(), catalog, queenRecoveryTestInput(nil))
			if err != nil || !retry.Recovered {
				t.Fatalf("retry after %s save failure result=%+v err=%v", failure, retry, err)
			}
			assertQueenRecoveryLetterCount(t, repositories, 1)
		})
	}
}

func TestQueenLetterRecoveryBagFullAndAccountMismatchChangeNothing(t *testing.T) {
	t.Run("bag-full", func(t *testing.T) {
		repositories, owner, catalog := queenRecoveryTestFixture(t, "epic_below_character_level_no_reward")
		beforeQuests := loadQueenRecoveryQuest(t, repositories)
		beforeInventory := loadQueenRecoveryInventory(t, repositories)
		_, err := owner.RecoverQueenLetter(context.Background(), catalog,
			queenRecoveryTestInput(func(*dnfrepo.InventoryRecord, FinishItemGrantRequest) ([]FinishCommittedItem, error) {
				return nil, errQueenRecoveryTestBagFull
			}))
		if !errors.Is(err, errQueenRecoveryTestBagFull) {
			t.Fatalf("bag full error=%v", err)
		}
		if got := loadQueenRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
			t.Fatalf("bag full changed quest: %+v", got)
		}
		if got := loadQueenRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
			t.Fatalf("bag full changed inventory: %+v", got)
		}
	})

	t.Run("account-mismatch", func(t *testing.T) {
		repositories, owner, catalog := queenRecoveryTestFixture(t, "epic_below_character_level_no_reward")
		beforeQuests := loadQueenRecoveryQuest(t, repositories)
		beforeInventory := loadQueenRecoveryInventory(t, repositories)
		input := queenRecoveryTestInput(nil)
		input.AccountID = "dnf:other"
		_, err := owner.RecoverQueenLetter(context.Background(), catalog, input)
		if !errors.Is(err, ErrCharacterNotFound) {
			t.Fatalf("account mismatch error=%v", err)
		}
		if got := loadQueenRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
			t.Fatalf("account mismatch changed quest: %+v", got)
		}
		if got := loadQueenRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
			t.Fatalf("account mismatch changed inventory: %+v", got)
		}
	})
}

func TestQueenLetterRecoveryCandidateRequiresTheExactActiveRelation(t *testing.T) {
	repositories, _, _ := queenRecoveryTestFixture(t, "main_story_level_clear_no_reward")
	record := loadQueenRecoveryQuest(t, repositories)
	if !IsQueenLetterRecoveryCandidate(record) {
		t.Fatal("fixture is not a recovery candidate")
	}
	record.States[QueenLetterParentQuestID] = dnfrepo.QuestState{Status: "completed"}
	if IsQueenLetterRecoveryCandidate(record) {
		t.Fatal("completed parent was still accepted as a live recovery candidate")
	}
}

func queenRecoveryTestFixture(
	t *testing.T,
	reason string,
) (dnfrepo.Group, *Owner, *Catalog) {
	t.Helper()
	repositories := queenRecoveryTestRepositories(t)
	record := loadQueenRecoveryQuest(t, repositories)
	source := record.States[QueenLetterSourceQuestID]
	source.Extra["auto_complete_reason"] = reason
	record.States[QueenLetterSourceQuestID] = source
	if err := repositories.Quest.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	return repositories, owner, queenRecoveryTestCatalog(t, nil)
}

func queenRecoveryTestRepositories(t *testing.T) dnfrepo.Group {
	t.Helper()
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: queenRecoveryTestCharacter,
		AccountID:   queenRecoveryTestAccount,
		Level:       90,
		Job:         "2",
		Stats:       map[string]int64{"grow_type": 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: queenRecoveryTestCharacter,
		States: map[int64]dnfrepo.QuestState{
			QueenLetterSourceQuestID: {
				Status: "completed",
				Extra: map[string]string{
					"reward_state":         "granted",
					"auto_completed":       "true",
					"auto_complete_reason": "",
				},
			},
			QueenLetterParentQuestID:   {Status: "active", ProgressValue: 2},
			QueenLetterMeetQuestID:     {Status: "active", ProgressValue: 1},
			QueenLetterDeliveryQuestID: {Status: "active", ProgressValue: 513},
			4226:                       {Status: "completed", Extra: map[string]string{"keep": "yes"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: queenRecoveryTestCharacter,
		Slots: map[string]dnfrepo.ItemStack{
			"0:79": {ItemID: 3033, Count: 2, Extra: map[string]string{"keep": "yes"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return repositories
}

func queenRecoveryTestCatalog(t *testing.T, mutate func(catalogTestSource)) *Catalog {
	t.Helper()
	source := catalogTestSource{
		DefaultList: "3226 `queen_source.qst`\n3249 `queen_parent.qst`\n3609 `queen_meet.qst`\n3610 `queen_delivery.qst`\n",
		"n_quest/queen_source.qst": questCatalogTestDefinition("[epic]", 80, 99, "[all]",
			"[type]\n`[meet npc]`\n[int data]\n303\n[reward type]\n`[item]`\n[reward int data]\n10164786 1\n"),
		"n_quest/queen_parent.qst": questCatalogTestDefinition("[epic]", 80, 99, "[all]",
			"[pre required quest]\n3225\n[type]\n`[quest clear]`\n[int data]\n3609 3610\n[reward type]\n`[item]`\n[reward int data]\n0 0\n"),
		"n_quest/queen_meet.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[meet npc]`\n[main quest]\n3249\n[int data]\n303\n[reward type]\n`[item]`\n[reward int data]\n0 0\n"),
		"n_quest/queen_delivery.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]",
			"[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[seek n meet npc]`\n[main quest]\n3249\n[int data]\n10164786 1 303\n[reward type]\n`[item]`\n[reward int data]\n0 0\n"),
	}
	if mutate != nil {
		mutate(source)
	}
	return loadQuestCatalogForTest(t, source)
}

func queenRecoveryTestInput(allocator FinishItemAllocator) QueenLetterRecoveryInput {
	if allocator == nil {
		allocator = queenRecoveryTestAllocator()
	}
	return QueenLetterRecoveryInput{
		AccountID:    queenRecoveryTestAccount,
		CharacterID:  queenRecoveryTestCharacter,
		RecoveredAt:  time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC),
		AllocateItem: allocator,
	}
}

func queenRecoveryTestAllocator() FinishItemAllocator {
	return func(record *dnfrepo.InventoryRecord, request FinishItemGrantRequest) ([]FinishCommittedItem, error) {
		if record == nil || request.ItemID != QueenLetterItemID || request.Count != 1 {
			return nil, errQueenRecoveryTestBagFull
		}
		const slot uint16 = 50
		key := "0:50"
		if _, occupied := record.Slots[key]; occupied {
			return nil, errQueenRecoveryTestBagFull
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

func queenRecoveryTestStack(slot uint16, count int64) dnfrepo.ItemStack {
	raw := make([]byte, currentQuestFinishRawEntrySize)
	binary.LittleEndian.PutUint16(raw[0:2], slot)
	binary.LittleEndian.PutUint32(raw[2:6], uint32(QueenLetterItemID))
	binary.LittleEndian.PutUint32(raw[6:10], uint32(count))
	return dnfrepo.ItemStack{ItemID: QueenLetterItemID, Count: count, RawEntry: raw}
}

func loadQueenRecoveryQuest(t *testing.T, repositories dnfrepo.Group) dnfrepo.QuestRecord {
	t.Helper()
	record, found, err := repositories.Quest.Load(context.Background(), queenRecoveryTestCharacter)
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	return record
}

func loadQueenRecoveryInventory(t *testing.T, repositories dnfrepo.Group) dnfrepo.InventoryRecord {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(context.Background(), queenRecoveryTestCharacter)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	return inventory
}

func queenRecoveryInventoryLetterCount(inventory dnfrepo.InventoryRecord) int64 {
	var count int64
	for _, stack := range inventory.Slots {
		if stack.ItemID == QueenLetterItemID {
			count += stack.Count
		}
	}
	return count
}

func assertQueenRecoveryLetterCount(t *testing.T, repositories dnfrepo.Group, want int64) {
	t.Helper()
	if got := queenRecoveryInventoryLetterCount(loadQueenRecoveryInventory(t, repositories)); got != want {
		t.Fatalf("queen letter count=%d, want %d", got, want)
	}
}

type queenRecoveryFailingSettlement struct {
	dnfrepo.CharacterSettlementUnitOfWork
	failure string
}

func (u queenRecoveryFailingSettlement) WithinCharacterSettlement(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.Group) error,
) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		switch u.failure {
		case "inventory":
			tx.Inventory = queenRecoveryFailInventory{InventoryRepository: tx.Inventory}
		case "quest":
			tx.Quest = queenRecoveryFailQuest{QuestRepository: tx.Quest}
		}
		return apply(tx)
	})
}

type queenRecoveryFailInventory struct {
	dnfrepo.InventoryRepository
}

func (queenRecoveryFailInventory) Save(context.Context, dnfrepo.InventoryRecord) error {
	return errors.New("test inventory save failure")
}

type queenRecoveryFailQuest struct {
	dnfrepo.QuestRepository
}

func (queenRecoveryFailQuest) Save(context.Context, dnfrepo.QuestRecord) error {
	return errors.New("test quest save failure")
}
