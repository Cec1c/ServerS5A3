package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFExpertCompoundUsesAccountWarehouse(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("requires matching real PVF")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := dnfexpertjob.Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	config, _ := jobs.Config(dnfexpertjob.EnchanterType)
	ids := make([]int64, 0, len(config.Recipes))
	for id := range config.Recipes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) == 0 {
		t.Fatal("no actual enchanter recipes")
	}
	for _, id := range ids {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			recipe := config.Recipes[id]
			repos := expertSharedRecipeFixture(t, recipe)
			s := currentDisjointTestService(repos, items)
			s.expertJobCatalog = jobs
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 19, itemListMigrationsDone: true}
			body := make([]byte, 8)
			binary.LittleEndian.PutUint32(body, uint32(id))
			binary.LittleEndian.PutUint16(body[4:], 2)
			binary.LittleEndian.PutUint16(body[6:], 0xffff)
			if err := s.handleCurrentExpertJobCompound(session, body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 238 || len(ack.Body) < 2 || ack.Body[0] != 1 {
				t.Fatalf("valid recipe rejected: %x", ack.Body)
			}
			ctx := context.Background()
			gotAccount, _, err := repos.AccountInventory.Load(ctx, "account-1")
			if err != nil {
				t.Fatal(err)
			}
			gotInv, _, err := repos.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			wantRefresh := map[uint32]uint64{uint32(recipe.Output.ItemID): uint64(recipe.Output.Count * 2)}
			for _, material := range recipe.Materials {
				wantRefresh[uint32(material.ItemID)] = uint64(material.Count)
				if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); shared {
					key := dnfrepo.AccountSharedInventorySlotKey(fixed)
					if got := gotAccount.Slots[key].Count; got != material.Count {
						t.Fatalf("shared material%d balance%d want%d", material.ItemID, got, material.Count)
					}
					if gotInv.Slots[key].Count != 99999 {
						t.Fatal("stale character copy was consumed")
					}
				} else if got := equipmentRecipeCompoundTestTotal(gotInv, material.ItemID); got != material.Count {
					t.Fatalf("ordinary material%d balance%d want%d", material.ItemID, got, material.Count)
				}
			}
			if got := equipmentRecipeCompoundTestTotal(gotInv, recipe.Output.ItemID); got != recipe.Output.Count*2 {
				t.Fatalf("output count%d", got)
			}
			gotCharacter, _, _ := repos.Character.Load(ctx, "19")
			if gotCharacter.Stats["gold"] != 100000-recipe.GoldCost*2 || gotCharacter.Stats["expert_job_exp"] < 1195 {
				t.Fatal("wrong crafting fee or experience")
			}
			equipmentRecipeCompoundTestRefresh(t, rest, wantRefresh)
			t.Logf("recipe%d batch2 output%d committed with shared warehouse and refresh", id, recipe.Output.ItemID)
		})
	}
	t.Run("bead retains card upgrade and ordinary materials", func(t *testing.T) {
		ctx := context.Background()
		enchanter, _ := jobs.Enchanter()
		recipe := enchanter.CardRecipes[10015129]
		repos := expertSharedRecipeFixture(t, dnfexpertjob.Recipe{RecipeItemID: recipe.RecipeItemID, Materials: recipe.Materials})
		inv, _, _ := repos.Inventory.Load(ctx, "19")
		inv.Slots["0:75"] = dnfrepo.ItemStack{ItemID: 3619, Count: 2, Extra: map[string]string{"enchant_upgrade_count": "2"}}
		if err := repos.Inventory.Save(ctx, inv); err != nil {
			t.Fatal(err)
		}
		beforeAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
		s := currentDisjointTestService(repos, items)
		s.expertJobCatalog = jobs
		result, err := s.commitCurrentExpertJobCompound(ctx, &gameSession{selectedCharacterID: 19}, currentExpertJobCompoundRequest{RecipeItemID: recipe.RecipeItemID, Count: 1, CardSlot: 75})
		if err != nil || result.Plan.SuccessCount != 1 {
			t.Fatalf("bead failed: %+v %v", result, err)
		}
		after, _, _ := repos.Inventory.Load(ctx, "19")
		if after.Slots["0:75"].Count != 1 || equipmentRecipeCompoundTestTotal(after, 2600313) != 1 {
			t.Fatal("card or bead count wrong")
		}
		for _, stack := range after.Slots {
			if stack.ItemID == 2600313 && currentExpertJobEnchantUpgradeCount(stack) != 2 {
				t.Fatal("bead lost card upgrade")
			}
		}
		afterAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
		shared := false
		for _, material := range recipe.Materials {
			if fixed, ok := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); ok {
				shared = true
				if afterAccount.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)].Count != material.Count*2 {
					t.Fatal("bead shared material not persisted")
				}
			} else if equipmentRecipeCompoundTestTotal(after, material.ItemID) != material.Count*2 {
				t.Fatal("bead ordinary material not consumed")
			}
		}
		if !shared && !reflect.DeepEqual(beforeAccount, afterAccount) {
			t.Fatal("ordinary recipe rewrote unrelated account inventory")
		}
	})
	for _, mode := range []string{"short", "locked", "wrong-item", "late-failure", "foreign-owner"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			recipe := config.Recipes[2600141]
			repos := expertSharedRecipeFixture(t, recipe)
			account, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			stack := account.Slots["0:354"]
			switch mode {
			case "short":
				stack.Count = 9
			case "locked":
				stack.Extra = map[string]string{"equipment_lock_state": "1"}
			case "wrong-item":
				stack.ItemID = 3037
			}
			account.Slots["0:354"] = stack
			if err := repos.AccountInventory.Save(ctx, account); err != nil {
				t.Fatal(err)
			}
			beforeCharacter, _, _ := repos.Character.Load(ctx, "19")
			beforeInv, _, _ := repos.Inventory.Load(ctx, "19")
			if mode == "late-failure" {
				repos.CharacterSettlement = rejectingCurrentNPCShopCommit{inner: repos.CharacterSettlement, err: errors.New("injected after all saves")}
			}
			s := currentDisjointTestService(repos, items)
			s.expertJobCatalog = jobs
			if mode == "foreign-owner" {
				s.options.accountID = "other-account"
			}
			_, err := s.commitCurrentExpertJobCompound(ctx, &gameSession{selectedCharacterID: 19}, currentExpertJobCompoundRequest{RecipeItemID: 2600141, Count: 1, CardSlot: -1})
			if err == nil {
				t.Fatal("invalid/faulted crafting accepted")
			}
			afterAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			afterCharacter, _, _ := repos.Character.Load(ctx, "19")
			afterInv, _, _ := repos.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(account, afterAccount) || !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeInv, afterInv) {
				t.Fatal("failed transaction changed warehouse, materials, output, gold or experience")
			}
		})
	}
}

func expertSharedRecipeFixture(t *testing.T, recipe dnfexpertjob.Recipe) dnfrepo.Group {
	t.Helper()
	ctx := context.Background()
	repos := mustCurrentNPCShopRepositories(t, 100000, nil)
	character, _, _ := repos.Character.Load(ctx, "19")
	character.Stats["expert_job_type"] = int64(dnfexpertjob.EnchanterType)
	character.Stats["expert_job_exp"] = 1195
	character.Stats[currentExpertJobRecipeStatKey(dnfexpertjob.EnchanterType, recipe.RecipeItemID)] = 1
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "19")
	inv.Slots = map[string]dnfrepo.ItemStack{}
	account := dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{}}
	sharedCount := 0
	for i, material := range recipe.Materials {
		stack := dnfrepo.ItemStack{ItemID: material.ItemID, Count: material.Count * 3}
		if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); shared {
			account.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)] = stack
			inv.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)] = dnfrepo.ItemStack{ItemID: material.ItemID, Count: 99999}
			sharedCount++
		} else {
			inv.Slots[currentCeraShopInventorySlotKey(0, int16(130+i))] = stack
		}
	}
	if sharedCount == 0 && recipe.RecipeItemID != 10015129 {
		t.Fatal("fixture must require actual shared material")
	}
	if err := repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	return repos
}
