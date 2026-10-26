package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFLegendaryEnchanterBeadBatch(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the pinned runtime PVF")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348" {
		t.Fatal("wrong PVF")
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
	cfg, ok := jobs.Enchanter()
	if !ok {
		t.Fatal("no enchanter")
	}
	recipe := cfg.CardRecipes[10159564]
	if recipe.RecipeItemID != 10159564 || len(recipe.Materials) == 0 || cfg.BeadByCard[10159184] != 10159230 {
		t.Fatal("real recipe changed")
	}
	for _, level := range []int{9, 10, 11} {
		t.Run(fmt.Sprintf("level%d_probability", level), func(t *testing.T) {
			exp := cfg.Recipes.ExperienceThresholds[level-2]
			if cfg.Recipes.Level(exp) != level {
				t.Fatal("level fixture")
			}
			for roll := 0; roll < 100; roll++ {
				rng := func(n int) int {
					if n == 100 {
						return roll
					}
					return 0
				}
				bead, beadErr := jobs.PlanEnchanterBead(exp, recipe.RecipeItemID, 10159184, rng)
				store, storeErr := jobs.PlanEnchanterStore(exp, recipe.RecipeItemID, 10159184, cfg.InitialEndurance, rng)
				if level == 9 {
					if !errors.Is(beadErr, dnfexpertjob.ErrRecipeUnavailable) || !errors.Is(storeErr, dnfexpertjob.ErrRecipeUnavailable) {
						t.Fatal("underlevel accepted", beadErr, storeErr)
					}
					continue
				}
				want := level == 11 || roll < 90
				if beadErr != nil || storeErr != nil || bead.Success != want || store.Success != want {
					t.Fatalf("level=%d roll=%d want=%v bead=%+v/%v store=%+v/%v", level, roll, want, bead, beadErr, store, storeErr)
				}
				if !want && (bead.ExperienceGain != 0 || store.ExperienceGain != 0) {
					t.Fatal("failed attempt gained EXP")
				}
			}
		})
	}
	definition, err := items.ResolveItem(10159230)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual recipe=%+v card10159184->bead10159230 stack_limit=%d type=%s", recipe, definition.StackLimit, definition.StackableType)
	for _, mode := range []string{"batch3", "batch1", "upgrade0", "upgrade1", "underlevel", "short-card", "locked-card", "short-material", "foreign-owner", "late-commit", "zero-direct", "too-large-direct", "full-bag", "conflicting-upgrade"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			// Reuse the shared-material fixture's ordinary-card exemption; the
			// actual learned recipe and level are set below from the real PVF.
			repos := expertSharedRecipeFixture(t, dnfexpertjob.Recipe{RecipeItemID: 10015129, Materials: recipe.Materials})
			character, _, _ := repos.Character.Load(ctx, "19")
			character.Name = "r134"
			character.Stats["expert_job_exp"] = cfg.Recipes.ExperienceThresholds[9]
			if mode == "underlevel" {
				character.Stats["expert_job_exp"] = cfg.Recipes.ExperienceThresholds[7]
			}
			character.Stats[currentExpertJobRecipeStatKey(dnfexpertjob.EnchanterType, recipe.RecipeItemID)] = 1
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			inv, _, _ := repos.Inventory.Load(ctx, "19")
			inv.Slots["0:75"] = dnfrepo.ItemStack{ItemID: 10159184, Count: 3, Extra: map[string]string{"enchant_upgrade_count": "2"}}
			count := 3
			upgrade := 2
			if mode == "upgrade0" {
				upgrade = 0
			}
			if mode == "upgrade1" {
				upgrade = 1
			}
			inv.Slots["0:75"].Extra["enchant_upgrade_count"] = fmt.Sprint(upgrade)
			switch mode {
			case "batch1", "upgrade0", "upgrade1":
				count = 1
			case "short-card":
				x := inv.Slots["0:75"]
				x.Count = 2
				inv.Slots["0:75"] = x
			case "locked-card":
				x := inv.Slots["0:75"]
				x.Extra["equipment_lock_state"] = "1"
				inv.Slots["0:75"] = x
			case "short-material":
				m := recipe.Materials[0]
				if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(m.ItemID); shared {
					a, _, _ := repos.AccountInventory.Load(ctx, "account-1")
					key := dnfrepo.AccountSharedInventorySlotKey(fixed)
					x := a.Slots[key]
					x.Count = m.Count*3 - 1
					a.Slots[key] = x
					if err := repos.AccountInventory.Save(ctx, a); err != nil {
						t.Fatal(err)
					}
				} else {
					for key, x := range inv.Slots {
						if x.ItemID == m.ItemID {
							x.Count = m.Count*3 - 1
							inv.Slots[key] = x
							break
						}
					}
				}
			case "zero-direct":
				count = 0
			case "too-large-direct":
				count = 65536
			case "full-bag":
				for slot := int16(3); slot <= 120; slot++ {
					if slot > 8 && slot < 65 {
						continue
					}
					key := currentCeraShopInventorySlotKey(0, slot)
					if _, exists := inv.Slots[key]; !exists {
						inv.Slots[key] = dnfrepo.ItemStack{ItemID: 3037, Count: 1}
					}
				}
				x := inv.Slots["0:75"]
				x.Count = 4
				inv.Slots["0:75"] = x
			case "conflicting-upgrade":
				x := inv.Slots["0:75"]
				x.Extra["byte_12"] = "1"
				inv.Slots["0:75"] = x
			}
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			beforeChar, _, _ := repos.Character.Load(ctx, "19")
			beforeInv, _, _ := repos.Inventory.Load(ctx, "19")
			beforeAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			injected := errors.New("injected after all bead saves")
			if mode == "late-commit" {
				repos.CharacterSettlement = rejectingCurrentNPCShopCommit{inner: repos.CharacterSettlement, err: injected}
			}
			s := currentDisjointTestService(repos, items)
			s.expertJobCatalog = jobs
			session := &gameSession{selectedCharacterID: 19, itemListMigrationsDone: true, conn: &bufferConn{}}
			if mode == "foreign-owner" {
				s.options.accountID = "foreign"
			}
			if mode == "batch3" || mode == "batch1" || mode == "upgrade0" || mode == "upgrade1" {
				body := make([]byte, 8)
				binary.LittleEndian.PutUint32(body, 10159564)
				binary.LittleEndian.PutUint16(body[4:], uint16(count))
				binary.LittleEndian.PutUint16(body[6:], 75)
				if err := s.handleCurrentExpertJobCompound(session, body); err != nil {
					t.Fatal(err)
				}
				ack, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
				if ack.Header.MsgID != 238 || len(ack.Body) != 19 || ack.Body[0] != 1 || ack.Body[1] != 1 || binary.LittleEndian.Uint32(ack.Body[2:]) != 10159230 || binary.LittleEndian.Uint32(ack.Body[6:]) != uint32(count) || binary.LittleEndian.Uint32(ack.Body[10:]) != uint32(count) || binary.LittleEndian.Uint32(ack.Body[14:]) != 0 {
					t.Fatalf("batch%d ACK rejected/wrong: %x", count, ack.Body)
				}
				if len(rest) == 0 {
					t.Fatal("no authoritative inventory refresh")
				}
				after, _, _ := repos.Inventory.Load(ctx, "19")
				if after.Slots["0:75"].Count != int64(3-count) || equipmentRecipeCompoundTestTotal(after, 10159230) != int64(count) {
					t.Fatalf("card or output count: %+v", after.Slots)
				}
				for _, stack := range after.Slots {
					if stack.ItemID == 10159230 && int(currentExpertJobEnchantUpgradeCount(stack)) != upgrade {
						t.Fatal("bead lost card upgrade")
					}
				}
				account, _, _ := repos.AccountInventory.Load(ctx, "account-1")
				for _, m := range recipe.Materials {
					var got int64
					if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(m.ItemID); shared {
						got = account.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)].Count
					} else {
						got = equipmentRecipeCompoundTestTotal(after, m.ItemID)
					}
					if got != m.Count*int64(3-count) {
						t.Fatalf("material%d=%d", m.ItemID, got)
					}
				}
				char, _, _ := repos.Character.Load(ctx, "19")
				if char.Stats["gold"] != beforeChar.Stats["gold"] {
					t.Fatal("bead recipe charged gold")
				}
				return
			}
			_, err := s.commitCurrentExpertJobCompound(ctx, session, currentExpertJobCompoundRequest{RecipeItemID: 10159564, Count: count, CardSlot: 75})
			if err == nil {
				t.Fatal("invalid/faulted batch accepted")
			}
			if mode == "late-commit" && !errors.Is(err, injected) {
				t.Fatalf("did not reach final commit: %v", err)
			}
			afterChar, _, _ := repos.Character.Load(ctx, "19")
			afterInv, _, _ := repos.Inventory.Load(ctx, "19")
			afterAccount, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			if !reflect.DeepEqual(beforeChar, afterChar) || !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeAccount, afterAccount) {
				t.Fatal("rejected batch changed assets")
			}
		})
	}
}
