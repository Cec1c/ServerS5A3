package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestCurrentExpertJobRequestsMatchCurrentEXELayout(t *testing.T) {
	compoundBody := make([]byte, 8)
	binary.LittleEndian.PutUint32(compoundBody[0:4], 2600149)
	binary.LittleEndian.PutUint16(compoundBody[4:6], 3)
	binary.LittleEndian.PutUint16(compoundBody[6:8], uint16(0xffff))
	compound, err := parseCurrentExpertJobCompoundRequest(compoundBody)
	if err != nil || compound.RecipeItemID != 2600149 || compound.Count != 3 || compound.CardSlot != -1 {
		t.Fatalf("compound=%+v error=%v", compound, err)
	}
	binary.LittleEndian.PutUint16(compoundBody[4:6], 1)
	binary.LittleEndian.PutUint16(compoundBody[6:8], 12)
	cardCompound, err := parseCurrentExpertJobCompoundRequest(compoundBody)
	if err != nil || cardCompound.CardSlot != 12 || cardCompound.Count != 1 {
		t.Fatalf("card compound=%+v error=%v", cardCompound, err)
	}
	extractionBody := []byte{2, 9, 0, 0, 10, 0}
	extraction, err := parseCurrentExpertJobExtractionRequest(extractionBody)
	if err != nil || extraction.ExtractorType != 2 || extraction.ExtractorSlot != 9 || extraction.TargetList != 0 || extraction.TargetSlot != 10 {
		t.Fatalf("extraction=%+v error=%v", extraction, err)
	}
}

func TestCurrentExpertJobStoreLayoutsMatch86JPReaders(t *testing.T) {
	create := make([]byte, 18)
	create[0] = currentExpertJobEnchantStoreKind
	binary.LittleEndian.PutUint32(create[1:5], 3)
	copy(create[5:8], []byte("abc"))
	binary.LittleEndian.PutUint32(create[8:12], 500)
	binary.LittleEndian.PutUint16(create[12:14], uint16(120))
	binary.LittleEndian.PutUint16(create[14:16], uint16(220))
	binary.LittleEndian.PutUint16(create[16:18], uint16(5))
	request, err := parseCurrentExpertJobStoreCreateRequest(create)
	if err != nil || request.Kind != currentExpertJobEnchantStoreKind || string(request.Name) != "abc" || request.Cost != 500 || request.PositionX != 120 || request.PositionY != 220 || request.OpaqueObjectLink != 5 {
		t.Fatalf("create request=%+v error=%v", request, err)
	}
	store := &currentExpertJobStore{OwnerCharacterID: 19, Kind: currentExpertJobEnchantStoreKind, Name: []byte("abc"), Cost: 500, TownID: 38, AreaID: 2, PositionX: 120, PositionY: 220, Endurance: 297, Qualifications: []byte{0, 1}}
	wantCreate := []byte{3, 19, 0, 3, 0, 0, 0, 'a', 'b', 'c', 38, 2, 0, 0, 0, 120, 0, 220, 0, 0xf4, 1, 0, 0, 1, 2, 0, 1}
	if got := buildCurrentExpertJobStoreCreateNotification(store); !bytes.Equal(got, wantCreate) {
		t.Fatalf("create notification=%x want=%x", got, wantCreate)
	}
	wantUpdate := []byte{3, 19, 0, 3, 0, 0, 0, 'a', 'b', 'c', 2, 0, 1}
	if got := buildCurrentExpertJobStoreUpdateNotification(store); !bytes.Equal(got, wantUpdate) {
		t.Fatalf("update notification=%x want=%x", got, wantUpdate)
	}
	wantEnter := []byte{1, 3, 19, 0, 0x29, 1, 0, 0}
	if got := buildCurrentExpertJobStoreEnterSuccess(store); !bytes.Equal(got, wantEnter) {
		t.Fatalf("enter notification=%x want=%x", got, wantEnter)
	}
	disjoint, err := parseCurrentExpertJobStoreDisjointRequest([]byte{19, 0, 10, 0, 0})
	if err != nil || disjoint.OwnerID != 19 || disjoint.TargetSlot != 10 || disjoint.TargetList != 0 {
		t.Fatalf("disjoint request=%+v error=%v", disjoint, err)
	}
	enchantBody := make([]byte, 13)
	binary.LittleEndian.PutUint16(enchantBody[0:2], 19)
	binary.LittleEndian.PutUint32(enchantBody[2:6], 10015129)
	enchantBody[6], enchantBody[7] = 2, 0
	binary.LittleEndian.PutUint16(enchantBody[8:10], 10)
	enchantBody[10] = 0
	binary.LittleEndian.PutUint16(enchantBody[11:13], 11)
	enchant, err := parseCurrentExpertJobStoreEnchantRequest(enchantBody)
	if err != nil || enchant.OwnerID != 19 || enchant.RecipeID != 10015129 || enchant.TargetSlot != 10 || enchant.CardSlot != 11 {
		t.Fatalf("enchant request=%+v error=%v", enchant, err)
	}
	disjointResult := buildCurrentExpertJobStoreDisjointSuccessBody(
		currentExpertJobStoreDisjointRequest{OwnerID: 19, TargetSlot: 10, TargetList: 0},
		currentExpertJobStoreUseResult{RequesterGold: 900, Endurance: 299, Materials: []currentExpertJobExtractionMaterial{{Slot: 11, ItemID: 2610024, Count: 5}}},
	)
	wantDisjointResult := []byte{1, 10, 0, 0, 19, 0, 1, 11, 0, 0x68, 0xd3, 0x27, 0, 5, 0, 0, 0, 0x84, 3, 0, 0, 0x2b, 1, 0, 0}
	if !bytes.Equal(disjointResult, wantDisjointResult) {
		t.Fatalf("disjoint result=%x want=%x", disjointResult, wantDisjointResult)
	}
	enchantResult := buildCurrentExpertJobStoreEnchantSuccessBody(currentExpertJobStoreUseResult{Success: true, Experience: 42, Endurance: 297})
	wantEnchantResult := []byte{1, 1, 42, 0, 0, 0, 0, 0x29, 1, 0, 0}
	if !bytes.Equal(enchantResult, wantEnchantResult) {
		t.Fatalf("enchant result=%x want=%x", enchantResult, wantEnchantResult)
	}
}

func TestCurrentExpertJobGiveUpAndStoreNotificationContracts(t *testing.T) {
	if currentExpertJobStoreCreateNotification != 538 || currentExpertJobStoreCloseNotification != 539 ||
		currentExpertJobStoreUpdateNotification != 544 || currentExpertJobEnchantOwnerNotification != 533 {
		t.Fatalf("store notifications create=%d close=%d update=%d owner=%d",
			currentExpertJobStoreCreateNotification, currentExpertJobStoreCloseNotification,
			currentExpertJobStoreUpdateNotification, currentExpertJobEnchantOwnerNotification)
	}
	body := buildCurrentExpertJobGiveUpSuccessBody(currentExpertJobGiveUpResult{FinalGold: 59000, State: 1, Cost: 1000})
	want := []byte{1, 0x78, 0xe6, 0, 0, 1, 0}
	if !bytes.Equal(body, want) {
		t.Fatalf("give-up body=%x want=%x", body, want)
	}
	module := expertJobGameplayModule()
	opcode := uint16(239)
	if module.LegacyHandlers[opcode] == nil || module.UpperHandlers[opcode] == nil || module.LegacyNormalizers[opcode] == nil {
		t.Fatalf("op239 is not registered in every gameplay route")
	}
}

// 金币口径（2026-09-25）：默认档把副职业门店的金币读写落在
// `Character.Stats["gold"]`（= `dnf_characters.gold`，与客户端钱包投影同源），
// `inventory_wallet` 才是改动前的背包 `0:0` 伪槽。两个档位都跑一遍：
// 提交要真的落库、失败要整体回滚。
//
// 改动前这条用例把 `0:0` 当成唯一口径，等于把"金币存在背包伪槽里"这个错误
// 假设固化进了测试 —— 香港实服的 `dnf_inventory_items` 里 `0:0` 是 0 行，
// 所以那个假设在真库上根本不成立。见 expert_job_gold_source.go 文件头。
func TestCurrentExpertJobStorePairMutationCommitsAndRollsBackBothCharacters(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode expertJobGoldSourceMode
	}{
		{"character_stats", expertJobGoldCharacterStats},
		{"inventory_wallet", expertJobGoldInventoryWallet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repositories := dnfrepomemory.NewMemoryGroup()
			ctx := context.Background()
			for _, id := range []string{"19", "20"} {
				if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
					CharacterID: id,
					AccountID:   "account-" + id,
					Stats:       map[string]int64{"expert_job_exp": 0, "gold": 1000},
				}); err != nil {
					t.Fatal(err)
				}
				// 至少一件真实物品：仓储把空背包的 Slots 归一成 nil，
				// 而 mutateCurrentExpertJobStorePair 要求 Slots 非 nil。
				// 0:13 → 18400 就是香港库里角色 3 的真实一行。
				slots := map[string]dnfrepo.ItemStack{"0:13": {ItemID: 18400, Count: 1}}
				if tc.mode == expertJobGoldInventoryWallet {
					slots["0:0"] = dnfrepo.ItemStack{ItemID: 0, Count: 1000}
				}
				if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: id, Slots: slots}); err != nil {
					t.Fatal(err)
				}
			}
			service := &Service{
				repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
				options:            options{expertJobGoldSource: tc.mode},
			}
			readGold := func(id string) int64 {
				t.Helper()
				if tc.mode == expertJobGoldInventoryWallet {
					record, found, err := repositories.Inventory.Load(ctx, id)
					if err != nil || !found {
						t.Fatalf("inventory %s found=%t err=%v", id, found, err)
					}
					return record.Slots["0:0"].Count
				}
				record, found, err := repositories.Character.Load(ctx, id)
				if err != nil || !found {
					t.Fatalf("character %s found=%t err=%v", id, found, err)
				}
				return record.Stats["gold"]
			}

			session := &gameSession{selectedCharacterID: 19}
			store := &currentExpertJobStore{OwnerCharacterID: 20}
			if err := service.mutateCurrentExpertJobStorePair(ctx, session, store, func(requesterCharacter *dnfrepo.CharacterRecord, requesterInventory *dnfrepo.InventoryRecord, ownerCharacter *dnfrepo.CharacterRecord, ownerInventory *dnfrepo.InventoryRecord) error {
				requesterCharacter.Stats["expert_job_exp"] = 1
				ownerCharacter.Stats["expert_job_exp"] = 2
				service.currentExpertJobSetWalletGold(requesterCharacter, requesterInventory, 900)
				service.currentExpertJobSetWalletGold(ownerCharacter, ownerInventory, 1100)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if got := readGold("19"); got != 900 {
				t.Fatalf("committed requester gold=%d want 900", got)
			}
			if got := readGold("20"); got != 1100 {
				t.Fatalf("committed owner gold=%d want 1100", got)
			}
			// 落库的值必须能被同一档位的**读**函数读回来（读/写同源）。
			requesterCharacter, _, _ := repositories.Character.Load(ctx, "19")
			requesterInventory, _, _ := repositories.Inventory.Load(ctx, "19")
			if got, err := service.currentExpertJobWalletGold(&requesterCharacter, &requesterInventory); err != nil || got != 900 {
				t.Fatalf("wallet read-back gold=%d err=%v want 900/nil", got, err)
			}

			wantErr := errors.New("reject pair")
			if err := service.mutateCurrentExpertJobStorePair(ctx, session, store, func(requesterCharacter *dnfrepo.CharacterRecord, requesterInventory *dnfrepo.InventoryRecord, ownerCharacter *dnfrepo.CharacterRecord, ownerInventory *dnfrepo.InventoryRecord) error {
				service.currentExpertJobSetWalletGold(requesterCharacter, requesterInventory, 0)
				service.currentExpertJobSetWalletGold(ownerCharacter, ownerInventory, 0)
				return wantErr
			}); !errors.Is(err, wantErr) {
				t.Fatalf("rollback error=%v", err)
			}
			if got := readGold("19"); got != 900 {
				t.Fatalf("rolled-back requester gold=%d want 900", got)
			}
			if got := readGold("20"); got != 1100 {
				t.Fatalf("rolled-back owner gold=%d want 1100", got)
			}
		})
	}
}

func TestCurrentExpertJobSuccessBodiesMatchCurrentEXEReaders(t *testing.T) {
	compound := buildCurrentExpertJobCompoundSuccessBody(dnfexpertjob.CompoundPlan{AttemptedOutputs: []dnfexpertjob.RecipeEntry{{ItemID: 1110, Count: 3}}, SuccessCount: 2, FailureCount: 1})
	wantCompound := []byte{1, 1, 0x56, 0x04, 0, 0, 3, 0, 0, 0, 2, 0, 0, 0, 1, 0, 0, 0, 0}
	if !bytes.Equal(compound, wantCompound) {
		t.Fatalf("compound body=%x want=%x", compound, wantCompound)
	}
	extraction := buildCurrentExpertJobExtractionSuccessBody(currentExpertJobExtractionResult{TargetList: 0, TargetSlot: 10, Materials: []currentExpertJobExtractionMaterial{{Slot: 11, ItemID: 2610024, Count: 5}}})
	wantExtraction := []byte{1, 0, 10, 0, 1, 11, 0, 0x68, 0xd3, 0x27, 0, 5, 0, 0, 0}
	if !bytes.Equal(extraction, wantExtraction) {
		t.Fatalf("extraction body=%x want=%x", extraction, wantExtraction)
	}
}

func TestCurrentExpertJobLearnedRecipeStatsAreTypedAndSorted(t *testing.T) {
	stats := map[string]int64{"expert_job_recipe_2_2600150": 1, "expert_job_recipe_2_2600149": 1, "expert_job_recipe_4_2600083": 1, "expert_job_recipe_2_bad": 1, "expert_job_recipe_2_1": 0}
	if got := currentExpertJobLearnedRecipes(stats, 2); !bytes.Equal(int64SliceBytes(got), int64SliceBytes([]int64{2600149, 2600150})) {
		t.Fatalf("recipes=%v", got)
	}
}

func int64SliceBytes(values []int64) []byte {
	result := make([]byte, len(values)*8)
	for index, value := range values {
		binary.LittleEndian.PutUint64(result[index*8:], uint64(value))
	}
	return result
}
