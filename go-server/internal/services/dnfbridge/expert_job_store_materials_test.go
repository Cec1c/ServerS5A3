package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Catalog and item IDs come from the actual archive; all player assets are
// isolated in memory, never read from or written to a live player database.
func TestRealPVFStoreEnchantMaterialsAtomic(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := dnfexpertjob.Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	config, _ := jobs.Enchanter()
	const targetID int64 = 100300212
	cards := make([]int64, 0, len(config.Cards))
	for id := range config.Cards {
		cards = append(cards, id)
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i] < cards[j] })
	var cardID int64
	var recipe dnfexpertjob.EnchanterCardRecipe
	for _, id := range cards {
		r, e := resolveCurrentEnchantCardMetadata(items, archive, id, targetID)
		if e != nil || r.CardItemID != id || !currentExpertJobStoreStringContainsFold(r.AllowedEquipmentTypes, r.TargetEquipmentType) || (len(r.UpgradeCounts) > 0 && !currentExpertJobStoreIntContains(r.UpgradeCounts, 0)) {
			continue
		}
		for _, candidate := range config.CardRecipes {
			if candidate.Qualification == config.Cards[id].Qualification {
				recipe = candidate
				cardID = id
				break
			}
		}
		if cardID != 0 {
			break
		}
	}
	if cardID == 0 || len(recipe.Materials) == 0 {
		t.Fatal("no actual compatible card recipe")
	}
	definition, err := items.ResolveItem(uint32(recipe.RecipeItemID))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseDungeonCardPVFDocument(archive, definition.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real recipe=%d path=%s input item=%v card=%d target=%d", recipe.RecipeItemID, definition.PVFPath, doc.Ints("input item"), cardID, targetID)
	for _, material := range recipe.Materials {
		d, e := items.ResolveItem(uint32(material.ItemID))
		if e != nil {
			t.Fatal(e)
		}
		t.Logf("material=%d count=%d path=%s", material.ItemID, material.Count, d.PVFPath)
	}
	for _, self := range []bool{false, true} {
		for _, mode := range []string{"enough", "short", "locked"} {
			t.Run(fmt.Sprintf("self=%t/%s", self, mode), func(t *testing.T) {
				ctx := context.Background()
				repos := dnfrepomemory.NewMemoryGroup()
				for _, id := range []string{"19", "20"} {
					c := dnfrepo.CharacterRecord{CharacterID: id, AccountID: "account-" + id, Stats: map[string]int64{"gold": 100000, "expert_job_type": 1, "expert_job_exp": 1195, currentExpertJobMachineEnduranceStat: 300}}
					inv := dnfrepo.InventoryRecord{CharacterID: id, Slots: map[string]dnfrepo.ItemStack{"0:10": {ItemID: targetID, Count: 1}, "0:75": {ItemID: cardID, Count: 2}}}
					acc := dnfrepo.AccountInventoryRecord{AccountID: c.AccountID, Slots: map[string]dnfrepo.ItemStack{}}
					for i, m := range recipe.Materials {
						stack := dnfrepo.ItemStack{ItemID: m.ItemID, Count: m.Count * 3}
						if id == "19" && i == 0 {
							if mode == "short" {
								stack.Count = m.Count - 1
							}
							if mode == "locked" {
								stack.Extra = map[string]string{"equipment_lock_state": "1"}
							}
						}
						if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(m.ItemID); shared {
							acc.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)] = stack
						} else {
							inv.Slots[fmt.Sprintf("0:%d", 130+i)] = stack
						}
					}
					if e := repos.Character.Save(ctx, c); e != nil {
						t.Fatal(e)
					}
					if e := repos.Inventory.Save(ctx, inv); e != nil {
						t.Fatal(e)
					}
					if e := repos.AccountInventory.Save(ctx, acc); e != nil {
						t.Fatal(e)
					}
				}
				snapshot := func() []any {
					var out []any
					for _, id := range []string{"19", "20"} {
						c, _, _ := repos.Character.Load(ctx, id)
						i, _, _ := repos.Inventory.Load(ctx, id)
						a, _, _ := repos.AccountInventory.Load(ctx, "account-"+id)
						out = append(out, c, i, a)
					}
					return out
				}
				before := snapshot()
				owner := uint16(20)
				if self {
					owner = 19
				}
				s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }, expertJobCatalog: jobs, pvfItemCatalog: items, initialEquipmentArchive: archive}
				result, e := s.commitCurrentExpertJobStoreEnchant(ctx, &gameSession{selectedCharacterID: 19, accountID: "account-19"}, &currentExpertJobStore{OwnerCharacterID: owner, Endurance: 300, Cost: 100}, currentExpertJobStoreEnchantRequest{OwnerID: owner, RecipeID: recipe.RecipeItemID, TargetSlot: 10, CardSlot: 75})
				after := snapshot()
				if mode != "enough" {
					if !errors.Is(e, errCurrentExpertJobInsufficientMaterials) {
						t.Errorf("want insufficient materials, got %v", e)
					}
					if !reflect.DeepEqual(before, after) {
						t.Error("rejected order changed card, materials, gold, target, experience or endurance")
					}
					return
				}
				if e != nil {
					t.Fatal(e)
				}
				inv := after[1].(dnfrepo.InventoryRecord)
				acc := after[2].(dnfrepo.AccountInventoryRecord)
				if inv.Slots["0:75"].Count != 1 {
					t.Error("card not consumed once")
				}
				shared := false
				for _, m := range recipe.Materials {
					got := equipmentRecipeCompoundTestTotal(inv, m.ItemID)
					if fixed, ok := dnfrepo.AccountWarehouseSlotForItem(m.ItemID); ok {
						shared = true
						got = acc.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)].Count
					}
					if got != m.Count*2 {
						t.Errorf("material %d remaining=%d want=%d", m.ItemID, got, m.Count*2)
					}
				}
				if !shared && !reflect.DeepEqual(before[2], after[2]) {
					t.Error("ordinary materials rewrote account inventory")
				}
				if !reflect.DeepEqual(before[5], after[5]) {
					t.Error("owner account inventory changed")
				}
				if !self {
					for _, m := range recipe.Materials {
						if equipmentRecipeCompoundTestTotal(before[4].(dnfrepo.InventoryRecord), m.ItemID) != equipmentRecipeCompoundTestTotal(after[4].(dnfrepo.InventoryRecord), m.ItemID) {
							t.Error("owner materials changed")
						}
					}
				}
				wantGold := int64(99900)
				if self {
					wantGold = 100000
				}
				if after[0].(dnfrepo.CharacterRecord).Stats["gold"] != wantGold {
					t.Error("fee incorrect")
				}
				if len(buildCurrentExpertJobStoreEnchantSuccessBody(result)) != 11 {
					t.Error("ACK shape changed")
				}
			})
		}
	}
}

// Exercises the shared transaction with memory repositories independently of PVF.
func TestStoreRecipeMaterialTransaction(t *testing.T) {
	for _, self := range []bool{false, true} {
		for _, mode := range []string{"ordinary", "shared", "short", "locked", "late-failure"} {
			t.Run(fmt.Sprintf("self=%t/%s", self, mode), func(t *testing.T) {
				ctx := context.Background()
				repos := dnfrepomemory.NewMemoryGroup()
				for _, id := range []string{"19", "20"} {
					if e := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: "a" + id, Stats: map[string]int64{"gold": 1000, "expert_job_exp": 10, currentExpertJobMachineEnduranceStat: 300}}); e != nil {
						t.Fatal(e)
					}
					if e := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: id, Slots: map[string]dnfrepo.ItemStack{"0:130": {ItemID: 3227, Count: 10}, "0:75": {ItemID: 3619, Count: 2}}}); e != nil {
						t.Fatal(e)
					}
					stack := dnfrepo.ItemStack{ItemID: 3037, Count: 10}
					if id == "19" {
						if mode == "short" {
							stack.Count = 1
						}
						if mode == "locked" {
							stack.Extra = map[string]string{"equipment_lock_state": "1"}
						}
					}
					fixed, ok := dnfrepo.AccountWarehouseSlotForItem(3037)
					if !ok {
						t.Fatal("shared fixture invalid")
					}
					if e := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "a" + id, Slots: map[string]dnfrepo.ItemStack{dnfrepo.AccountSharedInventorySlotKey(fixed): stack}}); e != nil {
						t.Fatal(e)
					}
				}
				snapshot := func() []any {
					var rows []any
					for _, id := range []string{"19", "20"} {
						c, _, _ := repos.Character.Load(ctx, id)
						i, _, _ := repos.Inventory.Load(ctx, id)
						a, _, _ := repos.AccountInventory.Load(ctx, "a"+id)
						rows = append(rows, c, i, a)
					}
					return rows
				}
				before := snapshot()
				owner := uint16(20)
				if self {
					owner = 19
				}
				s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
				e := s.mutateCurrentExpertJobStoreAssets(ctx, &gameSession{selectedCharacterID: 19, accountID: "a19"}, &currentExpertJobStore{OwnerCharacterID: owner}, true, func(rc *dnfrepo.CharacterRecord, ri *dnfrepo.InventoryRecord, oc *dnfrepo.CharacterRecord, oi *dnfrepo.InventoryRecord, a *dnfrepo.AccountInventoryRecord) error {
					// Deliberately stage other writes first: any material error must undo them.
					delete(ri.Slots, "0:75")
					oc.Stats["expert_job_exp"]++
					oc.Stats[currentExpertJobMachineEnduranceStat]--
					if _, _, err := s.currentExpertJobSettleStoreFee(rc, ri, oc, oi, 100); err != nil {
						return err
					}
					material := int64(3037)
					if mode == "ordinary" {
						material = 3227
					}
					if _, err := s.consumeCurrentRecipeMaterials(&dnfexpertjob.Assets{Character: rc, Inventory: ri, AccountInventory: a}, []dnfexpertjob.RecipeEntry{{ItemID: material, Count: 2}}, 0); err != nil {
						return err
					}
					if mode == "late-failure" {
						return errors.New("injected after material consumption")
					}
					return nil
				})
				after := snapshot()
				if mode == "short" || mode == "locked" || mode == "late-failure" {
					if e == nil {
						t.Error("invalid transaction succeeded")
					}
					if !reflect.DeepEqual(before, after) {
						t.Error("failed transaction leaked writes")
					}
					return
				}
				if e != nil {
					t.Fatal(e)
				}
				if mode == "ordinary" {
					if !reflect.DeepEqual(before[2], after[2]) {
						t.Error("ordinary material rewrote unrelated warehouse")
					}
					if after[1].(dnfrepo.InventoryRecord).Slots["0:130"].Count != 8 {
						t.Error("ordinary material balance")
					}
				} else {
					fixed, _ := dnfrepo.AccountWarehouseSlotForItem(3037)
					if after[2].(dnfrepo.AccountInventoryRecord).Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)].Count != 8 {
						t.Error("shared material balance")
					}
				}
				if !reflect.DeepEqual(before[5], after[5]) {
					t.Error("owner warehouse changed")
				}
			})
		}
	}
}
