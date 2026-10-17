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

func TestEnchanterBeadBatchRealPVF(t *testing.T) {
	path := os.Getenv("QA_BEAD_BATCH_PVF")
	if path == "" {
		t.Skip("set QA_BEAD_BATCH_PVF to the pinned runtime PVF")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "a56cacf96087ba34ca5299e04ab65f8268f694b214b97dec9879fdebf46f5290" {
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
	recipe := cfg.CardRecipes[10015129]
	if recipe.RecipeItemID != 10015129 || len(recipe.Materials) == 0 || cfg.BeadByCard[3619] != 2600313 {
		t.Fatal("real recipe changed")
	}
	definition, err := items.ResolveItem(2600313)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual recipe=%+v card3619->bead2600313 stack_limit=%d type=%s", recipe, definition.StackLimit, definition.StackableType)
	for _, mode := range []string{"batch3", "batch1", "short-card", "locked-card", "short-material", "foreign-owner", "late-commit", "zero-direct", "too-large-direct", "full-bag", "conflicting-upgrade"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			repos := expertSharedRecipeFixture(t, dnfexpertjob.Recipe{RecipeItemID: recipe.RecipeItemID, Materials: recipe.Materials})
			inv, _, _ := repos.Inventory.Load(ctx, "19")
			inv.Slots["0:75"] = dnfrepo.ItemStack{ItemID: 3619, Count: 3, Extra: map[string]string{"enchant_upgrade_count": "2"}}
			count := 3
			switch mode {
			case "batch1":
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
			if mode == "batch3" || mode == "batch1" {
				body := make([]byte, 8)
				binary.LittleEndian.PutUint32(body, 10015129)
				binary.LittleEndian.PutUint16(body[4:], uint16(count))
				binary.LittleEndian.PutUint16(body[6:], 75)
				if err := s.handleCurrentExpertJobCompound(session, body); err != nil {
					t.Fatal(err)
				}
				ack, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
				if ack.Header.MsgID != 238 || len(ack.Body) != 19 || ack.Body[0] != 1 || ack.Body[1] != 1 || binary.LittleEndian.Uint32(ack.Body[2:]) != 2600313 || binary.LittleEndian.Uint32(ack.Body[6:]) != uint32(count) || binary.LittleEndian.Uint32(ack.Body[10:]) != uint32(count) || binary.LittleEndian.Uint32(ack.Body[14:]) != 0 {
					t.Fatalf("batch%d ACK rejected/wrong: %x", count, ack.Body)
				}
				if len(rest) == 0 {
					t.Fatal("no authoritative inventory refresh")
				}
				after, _, _ := repos.Inventory.Load(ctx, "19")
				if after.Slots["0:75"].Count != int64(3-count) || equipmentRecipeCompoundTestTotal(after, 2600313) != int64(count) {
					t.Fatalf("card or output count: %+v", after.Slots)
				}
				for _, stack := range after.Slots {
					if stack.ItemID == 2600313 && currentExpertJobEnchantUpgradeCount(stack) != 2 {
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
			_, err := s.commitCurrentExpertJobCompound(ctx, session, currentExpertJobCompoundRequest{RecipeItemID: 10015129, Count: count, CardSlot: 75})
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
