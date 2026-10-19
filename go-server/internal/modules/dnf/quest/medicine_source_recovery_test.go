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
	medicineRecoveryTestAccount   = "dnf:1"
	medicineRecoveryTestCharacter = "19"
)

var errMedicineRecoveryTestBagFull = errors.New("test medicine recovery bag full")

func TestMedicineSourceRecoveryBothNoRewardMarkers(t *testing.T) {
	for _, reason := range []string{
		"main_story_level_clear_no_reward",
		"epic_below_character_level_no_reward",
	} {
		t.Run(reason, func(t *testing.T) {
			repositories, owner, catalog := medicineRecoveryTestFixture(t, reason)
			result, err := owner.RecoverMedicineSource(context.Background(), catalog, medicineRecoveryTestInput(nil))
			if err != nil {
				t.Fatal(err)
			}
			if !result.Recovered || result.Item.ItemID != MedicineSourceItemID || result.Item.Delta != 1 {
				t.Fatalf("recovery result=%+v", result)
			}
			assertMedicineRecoveryCount(t, repositories, 1)
			record := loadMedicineRecoveryQuest(t, repositories)
			raw := record.States[MedicineSourceQuestID].Extra[MedicineSourceRecoveryReceiptKey]
			if raw == "" {
				t.Fatal("source quest has no medicine recovery receipt")
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
			if receipt.Version != 1 || receipt.CharacterID != medicineRecoveryTestCharacter ||
				receipt.SourceQuestID != MedicineSourceQuestID ||
				receipt.ParentQuestID != MedicineSourceParentQuestID ||
				receipt.DeliveryQuestID != MedicineSourceDeliveryQuestID ||
				receipt.ItemID != MedicineSourceItemID || receipt.Count != 1 ||
				receipt.SlotKey != result.Item.SlotKey {
				t.Fatalf("receipt=%+v item=%+v", receipt, result.Item)
			}
			if record.States[4226].Extra["keep"] != "yes" ||
				record.States[MedicineSourceParentQuestID].Status != "active" ||
				record.States[MedicineSourceDeliveryQuestID].Status != "active" {
				t.Fatalf("unrelated quest state changed: %+v", record.States)
			}
		})
	}
}

func TestMedicineSourceRecoveryDoesNotGrantForOrdinaryOrCompletedStates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*dnfrepo.QuestRecord, *dnfrepo.InventoryRecord)
	}{
		{
			name: "normal-source",
			mutate: func(record *dnfrepo.QuestRecord, _ *dnfrepo.InventoryRecord) {
				state := record.States[MedicineSourceQuestID]
				delete(state.Extra, "auto_complete_reason")
				state.Extra[finishSettlementReceiptKey] = `{"version":1}`
				record.States[MedicineSourceQuestID] = state
			},
		},
		{
			name: "source-not-completed",
			mutate: func(record *dnfrepo.QuestRecord, _ *dnfrepo.InventoryRecord) {
				state := record.States[MedicineSourceQuestID]
				state.Status = "active"
				record.States[MedicineSourceQuestID] = state
			},
		},
		{
			name: "already-has-medicine",
			mutate: func(_ *dnfrepo.QuestRecord, inventory *dnfrepo.InventoryRecord) {
				inventory.Slots["0:80"] = medicineRecoveryTestStack(80, 1)
			},
		},
		{
			name: "target-already-completed",
			mutate: func(record *dnfrepo.QuestRecord, _ *dnfrepo.InventoryRecord) {
				state := record.States[MedicineSourceDeliveryQuestID]
				state.Status = "completed"
				record.States[MedicineSourceDeliveryQuestID] = state
			},
		},
		{
			name: "parent-already-completed",
			mutate: func(record *dnfrepo.QuestRecord, _ *dnfrepo.InventoryRecord) {
				state := record.States[MedicineSourceParentQuestID]
				state.Status = "completed"
				record.States[MedicineSourceParentQuestID] = state
			},
		},
		{
			name: "existing-receipt",
			mutate: func(record *dnfrepo.QuestRecord, _ *dnfrepo.InventoryRecord) {
				state := record.States[MedicineSourceQuestID]
				state.Extra[MedicineSourceRecoveryReceiptKey] = `{"version":1}`
				record.States[MedicineSourceQuestID] = state
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repositories := medicineRecoveryTestRepositories(t)
			ctx := context.Background()
			record, found, err := repositories.Quest.Load(ctx, medicineRecoveryTestCharacter)
			if err != nil || !found {
				t.Fatalf("load quest found=%t err=%v", found, err)
			}
			inventory, found, err := repositories.Inventory.Load(ctx, medicineRecoveryTestCharacter)
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
			beforeQuests := loadMedicineRecoveryQuest(t, repositories)
			beforeInventory := loadMedicineRecoveryInventory(t, repositories)
			owner, err := NewOwner(repositories)
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.RecoverMedicineSource(ctx, medicineRecoveryTestCatalog(t, nil), medicineRecoveryTestInput(nil))
			if err != nil {
				t.Fatal(err)
			}
			if result.Recovered {
				t.Fatalf("unexpected recovery: %+v", result)
			}
			if got := loadMedicineRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
				t.Fatalf("quest changed: got=%+v want=%+v", got, beforeQuests)
			}
			if got := loadMedicineRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
				t.Fatalf("inventory changed: got=%+v want=%+v", got, beforeInventory)
			}
		})
	}
}

func TestMedicineSourceRecoveryAllowsCompletedHuntObjective(t *testing.T) {
	repositories, owner, catalog := medicineRecoveryTestFixture(t, "main_story_level_clear_no_reward")
	record := loadMedicineRecoveryQuest(t, repositories)
	record.States[MedicineSourceMeetQuestID] = dnfrepo.QuestState{Status: "completed"}
	if err := repositories.Quest.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	result, err := owner.RecoverMedicineSource(context.Background(), catalog, medicineRecoveryTestInput(nil))
	if err != nil || !result.Recovered {
		t.Fatalf("completed hunt recovery result=%+v err=%v", result, err)
	}
	assertMedicineRecoveryCount(t, repositories, 1)
}

func TestMedicineSourceRecoveryDefinitionMismatchFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(catalogTestSource)
	}{
		{
			name: "hunt-type",
			mutate: func(source catalogTestSource) {
				source["n_quest/medicine_meet.qst"] = strings.Replace(
					source["n_quest/medicine_meet.qst"], "`[hunt enemy]`", "`[clear map]`", 1,
				)
			},
		},
		{
			name: "delivery-type",
			mutate: func(source catalogTestSource) {
				source["n_quest/medicine_delivery.qst"] = strings.Replace(
					source["n_quest/medicine_delivery.qst"], "`[seeking]`", "`[seek n meet npc]`", 1,
				)
			},
		},
		{
			name: "item-count",
			mutate: func(source catalogTestSource) {
				source["n_quest/medicine_delivery.qst"] = strings.Replace(
					source["n_quest/medicine_delivery.qst"], "10164764 1", "10164764 2", 1,
				)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repositories, owner, _ := medicineRecoveryTestFixture(t, "epic_below_character_level_no_reward")
			beforeQuests := loadMedicineRecoveryQuest(t, repositories)
			beforeInventory := loadMedicineRecoveryInventory(t, repositories)
			_, err := owner.RecoverMedicineSource(
				context.Background(),
				medicineRecoveryTestCatalog(t, tc.mutate),
				medicineRecoveryTestInput(nil),
			)
			if !errors.Is(err, ErrMedicineSourceRecoveryDefinitionMismatch) {
				t.Fatalf("definition mismatch error=%v", err)
			}
			if got := loadMedicineRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
				t.Fatalf("definition mismatch changed quest: %+v", got)
			}
			if got := loadMedicineRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
				t.Fatalf("definition mismatch changed inventory: %+v", got)
			}
		})
	}
}

func TestMedicineSourceRecoveryRepeatedAndConcurrentGrantOnce(t *testing.T) {
	repositories, owner, catalog := medicineRecoveryTestFixture(t, "main_story_level_clear_no_reward")
	first, err := owner.RecoverMedicineSource(context.Background(), catalog, medicineRecoveryTestInput(nil))
	if err != nil || !first.Recovered {
		t.Fatalf("first recovery result=%+v err=%v", first, err)
	}
	second, err := owner.RecoverMedicineSource(context.Background(), catalog, medicineRecoveryTestInput(nil))
	if err != nil || second.Recovered {
		t.Fatalf("repeated recovery result=%+v err=%v", second, err)
	}
	assertMedicineRecoveryCount(t, repositories, 1)

	repositories, owner, catalog = medicineRecoveryTestFixture(t, "main_story_level_clear_no_reward")
	const workers = 12
	var recovered atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := owner.RecoverMedicineSource(context.Background(), catalog, medicineRecoveryTestInput(nil))
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
	assertMedicineRecoveryCount(t, repositories, 1)
}

func TestMedicineSourceRecoveryRollbackAndRetry(t *testing.T) {
	for _, failure := range []string{"inventory", "quest"} {
		t.Run(failure+"-save", func(t *testing.T) {
			repositories, owner, catalog := medicineRecoveryTestFixture(t, "epic_below_character_level_no_reward")
			beforeQuests := loadMedicineRecoveryQuest(t, repositories)
			beforeInventory := loadMedicineRecoveryInventory(t, repositories)
			original := repositories.CharacterSettlement
			repositories.CharacterSettlement = medicineRecoveryFailingSettlement{
				CharacterSettlementUnitOfWork: original,
				failure:                       failure,
			}
			owner, _ = NewOwner(repositories)
			_, err := owner.RecoverMedicineSource(context.Background(), catalog, medicineRecoveryTestInput(nil))
			if err == nil {
				t.Fatalf("%s save failure was accepted", failure)
			}
			if got := loadMedicineRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
				t.Fatalf("%s save failure changed quest: %+v", failure, got)
			}
			if got := loadMedicineRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
				t.Fatalf("%s save failure changed inventory: %+v", failure, got)
			}

			repositories.CharacterSettlement = original
			owner, _ = NewOwner(repositories)
			retry, err := owner.RecoverMedicineSource(context.Background(), catalog, medicineRecoveryTestInput(nil))
			if err != nil || !retry.Recovered {
				t.Fatalf("retry after %s save failure result=%+v err=%v", failure, retry, err)
			}
			assertMedicineRecoveryCount(t, repositories, 1)
		})
	}
}

func TestMedicineSourceRecoveryBagFullAndAccountMismatchChangeNothing(t *testing.T) {
	t.Run("bag-full", func(t *testing.T) {
		repositories, owner, catalog := medicineRecoveryTestFixture(t, "epic_below_character_level_no_reward")
		beforeQuests := loadMedicineRecoveryQuest(t, repositories)
		beforeInventory := loadMedicineRecoveryInventory(t, repositories)
		_, err := owner.RecoverMedicineSource(context.Background(), catalog,
			medicineRecoveryTestInput(func(*dnfrepo.InventoryRecord, FinishItemGrantRequest) ([]FinishCommittedItem, error) {
				return nil, errMedicineRecoveryTestBagFull
			}))
		if !errors.Is(err, errMedicineRecoveryTestBagFull) {
			t.Fatalf("bag full error=%v", err)
		}
		if got := loadMedicineRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
			t.Fatalf("bag full changed quest: %+v", got)
		}
		if got := loadMedicineRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
			t.Fatalf("bag full changed inventory: %+v", got)
		}
	})

	t.Run("account-mismatch", func(t *testing.T) {
		repositories, owner, catalog := medicineRecoveryTestFixture(t, "epic_below_character_level_no_reward")
		beforeQuests := loadMedicineRecoveryQuest(t, repositories)
		beforeInventory := loadMedicineRecoveryInventory(t, repositories)
		input := medicineRecoveryTestInput(nil)
		input.AccountID = "dnf:other"
		_, err := owner.RecoverMedicineSource(context.Background(), catalog, input)
		if !errors.Is(err, ErrCharacterNotFound) {
			t.Fatalf("account mismatch error=%v", err)
		}
		if got := loadMedicineRecoveryQuest(t, repositories); !reflect.DeepEqual(got, beforeQuests) {
			t.Fatalf("account mismatch changed quest: %+v", got)
		}
		if got := loadMedicineRecoveryInventory(t, repositories); !reflect.DeepEqual(got, beforeInventory) {
			t.Fatalf("account mismatch changed inventory: %+v", got)
		}
	})
}

func TestMedicineSourceRecoveryCandidateRequiresTheExactActiveRelation(t *testing.T) {
	repositories, _, _ := medicineRecoveryTestFixture(t, "main_story_level_clear_no_reward")
	record := loadMedicineRecoveryQuest(t, repositories)
	if !IsMedicineSourceRecoveryCandidate(record) {
		t.Fatal("fixture is not a medicine recovery candidate")
	}
	record.States[MedicineSourceParentQuestID] = dnfrepo.QuestState{Status: "completed"}
	if IsMedicineSourceRecoveryCandidate(record) {
		t.Fatal("completed parent was still accepted as a live recovery candidate")
	}
}

func medicineRecoveryTestFixture(
	t *testing.T,
	reason string,
) (dnfrepo.Group, *Owner, *Catalog) {
	t.Helper()
	repositories := medicineRecoveryTestRepositories(t)
	record := loadMedicineRecoveryQuest(t, repositories)
	source := record.States[MedicineSourceQuestID]
	source.Extra["auto_complete_reason"] = reason
	record.States[MedicineSourceQuestID] = source
	if err := repositories.Quest.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	return repositories, owner, medicineRecoveryTestCatalog(t, nil)
}

func medicineRecoveryTestRepositories(t *testing.T) dnfrepo.Group {
	t.Helper()
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: medicineRecoveryTestCharacter,
		AccountID:   medicineRecoveryTestAccount,
		Level:       90,
		Job:         "2",
		Stats:       map[string]int64{"grow_type": 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: medicineRecoveryTestCharacter,
		States: map[int64]dnfrepo.QuestState{
			MedicineSourceQuestID: {
				Status: "completed",
				Extra: map[string]string{
					"reward_state":         "granted",
					"auto_completed":       "true",
					"auto_complete_reason": "",
				},
			},
			MedicineSourceParentQuestID:   {Status: "active", ProgressValue: 2},
			MedicineSourceMeetQuestID:     {Status: "active", ProgressValue: 1},
			MedicineSourceDeliveryQuestID: {Status: "active", ProgressValue: 513},
			4226:                          {Status: "completed", Extra: map[string]string{"keep": "yes"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: medicineRecoveryTestCharacter,
		Slots: map[string]dnfrepo.ItemStack{
			"0:79": {ItemID: 3033, Count: 2, Extra: map[string]string{"keep": "yes"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return repositories
}

func medicineRecoveryTestCatalog(t *testing.T, mutate func(catalogTestSource)) *Catalog {
	t.Helper()
	source := catalogTestSource{
		DefaultList: "3346 `medicine_source.qst`\n3347 `medicine_parent.qst`\n3425 `medicine_meet.qst`\n3426 `medicine_delivery.qst`\n",
		"n_quest/medicine_source.qst": questCatalogTestDefinition("[epic]", 48, 99, "[all]",
			"[type]\n`[seeking]`\n[int data]\n3033 30 3166 10\n[reward type]\n`[item]`\n[reward int data]\n10164764 1\n"),
		"n_quest/medicine_parent.qst": questCatalogTestDefinition("[epic]", 48, 99, "[all]",
			"[pre required quest]\n3346\n[type]\n`[quest clear]`\n[int data]\n3425 3426\n[reward type]\n`[item]`\n[reward int data]\n0 0\n"),
		"n_quest/medicine_meet.qst": questCatalogTestDefinition("[sub]", 48, 99, "[all]",
			"[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[hunt enemy]`\n[main quest]\n3347\n[int data]\n61 -1 109001851 3 1\n[reward type]\n`[item]`\n[reward int data]\n0 0\n"),
		"n_quest/medicine_delivery.qst": questCatalogTestDefinition("[sub]", 48, 99, "[all]",
			"[attribute]\n`not give exp quest`\n[/attribute]\n[type]\n`[seeking]`\n[main quest]\n3347\n[int data]\n10164764 1\n[reward type]\n`[item]`\n[reward int data]\n0 0\n"),
	}
	if mutate != nil {
		mutate(source)
	}
	return loadQuestCatalogForTest(t, source)
}

func medicineRecoveryTestInput(allocator FinishItemAllocator) MedicineSourceRecoveryInput {
	if allocator == nil {
		allocator = medicineRecoveryTestAllocator()
	}
	return MedicineSourceRecoveryInput{
		AccountID:    medicineRecoveryTestAccount,
		CharacterID:  medicineRecoveryTestCharacter,
		RecoveredAt:  time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
		AllocateItem: allocator,
	}
}

func medicineRecoveryTestAllocator() FinishItemAllocator {
	return func(record *dnfrepo.InventoryRecord, request FinishItemGrantRequest) ([]FinishCommittedItem, error) {
		if record == nil || request.ItemID != MedicineSourceItemID || request.Count != 1 {
			return nil, errMedicineRecoveryTestBagFull
		}
		const slot uint16 = 50
		key := "0:50"
		if _, occupied := record.Slots[key]; occupied {
			return nil, errMedicineRecoveryTestBagFull
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

func medicineRecoveryTestStack(slot uint16, count int64) dnfrepo.ItemStack {
	raw := make([]byte, currentQuestFinishRawEntrySize)
	binary.LittleEndian.PutUint16(raw[0:2], slot)
	binary.LittleEndian.PutUint32(raw[2:6], uint32(MedicineSourceItemID))
	binary.LittleEndian.PutUint32(raw[6:10], uint32(count))
	return dnfrepo.ItemStack{ItemID: MedicineSourceItemID, Count: count, RawEntry: raw}
}

func loadMedicineRecoveryQuest(t *testing.T, repositories dnfrepo.Group) dnfrepo.QuestRecord {
	t.Helper()
	record, found, err := repositories.Quest.Load(context.Background(), medicineRecoveryTestCharacter)
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	return record
}

func loadMedicineRecoveryInventory(t *testing.T, repositories dnfrepo.Group) dnfrepo.InventoryRecord {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(context.Background(), medicineRecoveryTestCharacter)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	return inventory
}

func medicineRecoveryInventoryCount(inventory dnfrepo.InventoryRecord) int64 {
	var count int64
	for _, stack := range inventory.Slots {
		if stack.ItemID == MedicineSourceItemID {
			count += stack.Count
		}
	}
	return count
}

func assertMedicineRecoveryCount(t *testing.T, repositories dnfrepo.Group, want int64) {
	t.Helper()
	if got := medicineRecoveryInventoryCount(loadMedicineRecoveryInventory(t, repositories)); got != want {
		t.Fatalf("medicine count=%d, want %d", got, want)
	}
}

type medicineRecoveryFailingSettlement struct {
	dnfrepo.CharacterSettlementUnitOfWork
	failure string
}

func (u medicineRecoveryFailingSettlement) WithinCharacterSettlement(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.Group) error,
) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		switch u.failure {
		case "inventory":
			tx.Inventory = medicineRecoveryFailInventory{InventoryRepository: tx.Inventory}
		case "quest":
			tx.Quest = medicineRecoveryFailQuest{QuestRepository: tx.Quest}
		}
		return apply(tx)
	})
}

type medicineRecoveryFailInventory struct {
	dnfrepo.InventoryRepository
}

func (medicineRecoveryFailInventory) Save(context.Context, dnfrepo.InventoryRecord) error {
	return errors.New("test medicine inventory save failure")
}

type medicineRecoveryFailQuest struct {
	dnfrepo.QuestRepository
}

func (medicineRecoveryFailQuest) Save(context.Context, dnfrepo.QuestRecord) error {
	return errors.New("test medicine quest save failure")
}
