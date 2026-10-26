package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// The native ability writer sends inventory slot/category/option; a completed
// weapon remains editable when the character has no active production target.
func TestRealPVFEpicAbilityUsesOwnedWeaponInsteadOfActiveProductionRound7(t *testing.T) {
	for _, scenario := range []string{"no_active_target", "different_target", "same_target", "foreign_owner", "ordinary_weapon", "locked", "stacked_weapon", "missing_material", "late_abort"} {
		t.Run(scenario, func(t *testing.T) {
			s, session, conn := newCurrentEpicProductionRealPVFService(t, epicProductionSessionAccount, epicProductionAccountScopeSession, epicProductionInfoPushAlways)
			s.options.currentClientVersion = "official-24.9"
			g, _ := s.repositoryGroup()
			ctx := context.Background()
			character, _, _ := g.Character.Load(ctx, "3")
			character.Stats = nil
			if scenario == "different_target" {
				character.Stats = map[string]int64{currentEpicProductionTargetStat: 101000747, currentEpicProductionChargeStat: 12345, "unrelated": 8}
			}
			if scenario == "same_target" {
				character.Stats = map[string]int64{currentEpicProductionTargetStat: 108000520}
			}
			if scenario == "foreign_owner" {
				session.accountID = "foreign-account"
			}
			target := repo.ItemStack{ItemID: 108000520, Count: 1, Extra: map[string]string{"tail_data_72": "0000000001", "instance_id": "72"}}
			if scenario == "ordinary_weapon" {
				target.ItemID = 450004 // Real epic-book output, not an epic-production weapon.
				character.Stats = map[string]int64{currentEpicProductionTargetStat: target.ItemID}
			}
			if scenario == "locked" {
				target.Extra["equipment_lock_state"] = "locked"
			}
			if scenario == "stacked_weapon" {
				target.Count = 2
				character.Stats = map[string]int64{currentEpicProductionTargetStat: target.ItemID}
			}
			inventory := repo.InventoryRecord{CharacterID: "3", Slots: map[string]repo.ItemStack{"0:26": target}}
			account := repo.AccountInventoryRecord{AccountID: character.AccountID, Slots: map[string]repo.ItemStack{}}
			catalog, err := parseCurrentEpicProductionAbilityCatalog(s.initialEquipmentArchive)
			if err != nil {
				t.Fatal(err)
			}
			request := currentEpicProductionAbilityRequest{Slot: 26, Category: 0, Option: 4}
			recipe, err := currentEpicProductionAbilityRecipeFor(catalog, request)
			if err != nil {
				t.Fatal(err)
			}
			for i, material := range recipe.Materials {
				stack := repo.ItemStack{ItemID: int64(material.ItemID), Count: int64(material.Count) + 5}
				if fixed, shared := currentDisjointWarehouseFixedSlots[material.ItemID]; shared {
					account.Slots[repo.AccountSharedInventorySlotKey(fixed)] = stack
				} else {
					inventory.Slots[currentCeraShopInventorySlotKey(repo.MainInventoryListType, int16(30+i))] = stack
				}
			}
			if scenario == "missing_material" {
				inventory.Slots = map[string]repo.ItemStack{"0:26": target}
				account.Slots = map[string]repo.ItemStack{}
			}
			for _, err := range []error{g.Character.Save(ctx, character), g.Inventory.Save(ctx, inventory), g.AccountInventory.Save(ctx, account)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "late_abort" {
				modified := g
				modified.AccountAssets = epicBatchLateFailure{g.AccountAssets}
				s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
			}
			beforeInv, _, _ := g.Inventory.Load(ctx, "3")
			beforeAccount, _, _ := g.AccountInventory.Load(ctx, character.AccountID)
			beforeChar, _, _ := g.Character.Load(ctx, "3")
			body := []byte{26, 0, 0, 4}
			if err := s.handleCurrentEpicProductionAbility(session, body); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
			if packet.Header.MsgID != 1419 {
				t.Fatalf("ability reply=%d", packet.Header.MsgID)
			}
			success := scenario == "no_active_target" || scenario == "different_target" || scenario == "same_target"
			afterInv, _, _ := g.Inventory.Load(ctx, "3")
			afterAccount, _, _ := g.AccountInventory.Load(ctx, character.AccountID)
			afterChar, _, _ := g.Character.Load(ctx, "3")
			if !reflect.DeepEqual(beforeChar, afterChar) {
				t.Fatal("ability edit changed active production state")
			}
			if !success {
				want := []byte{0, 3}
				if scenario == "missing_material" {
					want[1] = 22
				}
				if !bytes.Equal(packet.Body, want) || len(rest) != 0 || !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeAccount, afterAccount) {
					t.Fatalf("refusal changed assets/reply: body=%x trailing=%d", packet.Body, len(rest))
				}
				return
			}
			if !bytes.Equal(packet.Body, []byte{1}) {
				t.Fatalf("owned valid production weapon rejected: %x", packet.Body)
			}
			update, trailing := splitGameServerUpperPacket(t, rest)
			if update.Header.MsgID != 14 || len(trailing) != 0 {
				t.Fatalf("updates=%d trailing=%d", update.Header.MsgID, len(trailing))
			}
			actual := afterInv.Slots["0:26"]
			if actual.Extra["tail_data_72"] != "0400000001" || actual.Extra["instance_id"] != "72" || actual.RawEntry[currentEpicProductionAbilityTailOffset] != 4 {
				t.Fatalf("ability tail/identity=%+v", actual.Extra)
			}
			for _, material := range recipe.Materials {
				var left int64
				if fixed, shared := currentDisjointWarehouseFixedSlots[material.ItemID]; shared {
					left = afterAccount.Slots[repo.AccountSharedInventorySlotKey(fixed)].Count
				} else {
					for _, stack := range afterInv.Slots {
						if stack.ItemID == int64(material.ItemID) {
							left += stack.Count
						}
					}
				}
				if left != 5 {
					t.Fatalf("material %d remaining=%d", material.ItemID, left)
				}
			}
			conn.write.Reset()
			if err := s.handleCurrentEpicProductionAbility(session, body); err != nil {
				t.Fatal(err)
			}
			replayed, _, _ := g.Inventory.Load(ctx, "3")
			replayedAccount, _, _ := g.AccountInventory.Load(ctx, character.AccountID)
			if !reflect.DeepEqual(afterInv, replayed) || !reflect.DeepEqual(afterAccount, replayedAccount) {
				t.Fatal("same-option replay charged again")
			}
			packet, rest = splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
			if !bytes.Equal(packet.Body, []byte{1}) || len(rest) != 0 {
				t.Fatal("same-option replay emitted updates")
			}
		})
	}
}
