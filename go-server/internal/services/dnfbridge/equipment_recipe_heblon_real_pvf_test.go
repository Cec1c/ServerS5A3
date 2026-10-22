package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func heblonResources(t *testing.T) (*platformpvf.Archive, *pvfEquipmentRecipeCatalog, *pvfDungeonDropCatalog) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current real PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.Snapshot().Checksum != "25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348" {
		t.Fatal("real PVF mismatch")
	}
	recipes, err := newPVFEquipmentRecipeCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	return a, recipes, items
}

func heblonFixture(t *testing.T, a *platformpvf.Archive, items *pvfDungeonDropCatalog, entry equipmentRecipeEntry, priceBalance int64) (dnfrepo.Group, *Service, *gameSession) {
	t.Helper()
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw, 9)
	binary.LittleEndian.PutUint32(raw[2:], uint32(entry.Materials[0].ItemID))
	binary.LittleEndian.PutUint32(raw[6:], 203004656)
	raw[0x0a] = 12
	binary.LittleEndian.PutUint32(raw[0x0e:], 987654)
	raw[currentEquipmentVectorOffset] = 2
	binary.LittleEndian.PutUint32(raw[currentEquipmentVectorOffset+1:], 123)
	binary.LittleEndian.PutUint32(raw[currentEquipmentVectorOffset+5:], 456)
	slots := map[string]dnfrepo.ItemStack{
		"0:9":  {ItemID: entry.Materials[0].ItemID, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "instance_note": "preserve"}},
		"0:69": {ItemID: entry.ItemID, Count: 2},
	}
	if priceBalance > 0 {
		slots["0:70"] = dnfrepo.ItemStack{ItemID: 10159142, Count: priceBalance}
	}
	shared := dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{}}
	for i, material := range entry.Materials[1:] {
		if slot, account := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); account {
			shared.Slots[dnfrepo.AccountSharedInventorySlotKey(slot)] = dnfrepo.ItemStack{ItemID: material.ItemID, Count: material.Count * 2}
		} else {
			slots[fmt.Sprintf("0:%d", 121+i)] = dnfrepo.ItemStack{ItemID: material.ItemID, Count: material.Count * 2}
		}
	}
	repos := mustCurrentNPCShopRepositories(t, 10000000, slots)
	ctx := context.Background()
	c, _, _ := repos.Character.Load(ctx, "19")
	c.Level = 90
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, shared); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{accountID: "account-1", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}, initialEquipmentArchive: a, pvfItemCatalog: items, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	return repos, s, &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, accountID: "account-1", itemListMigrationsDone: true}
}

func TestRealPVFHeblonOwnedUpgradePreservesInstanceAndPurchaseBalance(t *testing.T) {
	a, recipes, items := heblonResources(t)
	ctx := context.Background()
	for _, id := range []int64{10304471, 10304472, 10304473} {
		entry, found, err := recipes.Recipe(id)
		if err != nil || !found {
			t.Fatal(err)
		}
		for _, balance := range []int64{0, 949, 950, 951} {
			t.Run(fmt.Sprintf("%d_price_balance%d", id, balance), func(t *testing.T) {
				repos, s, p := heblonFixture(t, a, items, entry, balance)
				before, _, _ := repos.Inventory.Load(ctx, "19")
				character, _, _ := repos.Character.Load(ctx, "19")
				if err := s.handleGameCommand(p, 1, 25, append(equipmentRecipeCompoundTestSlotRequest(69, 1), 0, 1)); err != nil {
					t.Fatal(err)
				}
				ack, rest := splitGameServerUpperPacket(t, p.conn.(*bufferConn).write.Bytes())
				if !bytes.Equal(ack.Body, []byte{1, 0, 0}) || len(rest) == 0 {
					t.Fatalf("owned real drawing failed: %x", ack.Body)
				}
				after, _, _ := repos.Inventory.Load(ctx, "19")
				account, _, _ := repos.AccountInventory.Load(ctx, "account-1")
				finalCharacter, _, _ := repos.Character.Load(ctx, "19")
				if after.Slots["0:9"].ItemID != entry.Outputs[0].ItemID || after.Slots["0:69"].Count != 1 || after.Slots["0:70"].Count != balance || finalCharacter.Stats["gold"] != character.Stats["gold"] {
					t.Fatal("wrong equipment/drawing/price/gold settlement")
				}
				for _, material := range entry.Materials[1:] {
					amount := equipmentRecipeCompoundTestTotal(after, material.ItemID)
					if slot, shared := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); shared {
						amount = account.Slots[dnfrepo.AccountSharedInventorySlotKey(slot)].Count
					}
					if amount != material.Count {
						t.Fatalf("material%d=%d want%d", material.ItemID, amount, material.Count)
					}
				}
				want := append([]byte(nil), before.Slots["0:9"].RawEntry...)
				binary.LittleEndian.PutUint32(want[2:], uint32(entry.Outputs[0].ItemID))
				output, err := items.ResolveItem(uint32(entry.Outputs[0].ItemID))
				if err != nil {
					t.Fatal(err)
				}
				if normalizeDungeonDropStackableType(output.AttachType) == "sealing" {
					want[0x0d] = 1
				} else {
					want[0x0d] = 0
				}
				if !bytes.Equal(after.Slots["0:9"].RawEntry, want) || after.Slots["0:9"].Extra["instance_note"] != "preserve" {
					t.Fatal("upgrade changed retained instance attributes or sockets")
				}
				if _, err := s.commitCurrentEquipmentRecipeCompound(ctx, p, currentEquipmentRecipeCompoundRequest{SourceSlot: 69, Count: 1}); err == nil {
					t.Fatal("duplicate transform accepted without original equipment")
				}
				again, _, _ := repos.Inventory.Load(ctx, "19")
				if !reflect.DeepEqual(after, again) {
					t.Fatal("duplicate changed assets")
				}
				// The price remains in the read-only catalog and generic crafting
				// still rejects it; only this owned transform has an exception.
				check, _, _ := recipes.Recipe(id)
				plain := check
				plain.MaintainAbility, plain.MaintainSocketStat = "", false
				if len(check.ExtraMaterials) != 1 || currentEquipmentRecipeCompoundRejectReason(plain, nil) != "need_material_semantics_unproven" {
					t.Fatal("catalog price or generic crafting guard was erased")
				}
			})
		}
	}
}

type heblonFailSave struct{ dnfrepo.InventoryRepository }

func (heblonFailSave) Save(context.Context, dnfrepo.InventoryRecord) error {
	return errors.New("injected inventory failure")
}

type heblonFailUOW struct {
	dnfrepo.CharacterSettlementUnitOfWork
	late bool
}

func (u heblonFailUOW) WithinCharacterSettlement(ctx context.Context, id string, apply func(dnfrepo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error {
		if !u.late {
			tx.Inventory = heblonFailSave{tx.Inventory}
		}
		if err := apply(tx); err != nil {
			return err
		}
		return errors.New("injected final commit failure")
	})
}

func TestRealPVFHeblonOwnedUpgradeRejectsAtomically(t *testing.T) {
	a, recipes, items := heblonResources(t)
	entry, _, err := recipes.Recipe(10304471)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"no_drawing", "locked_drawing", "locked_input", "low_level", "multiple", "inventory_save", "final_commit"}
	for i := range entry.Materials {
		names = append(names, fmt.Sprintf("missing_input_%d", i))
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			repos, s, p := heblonFixture(t, a, items, entry, 951)
			ctx := context.Background()
			inv, _, _ := repos.Inventory.Load(ctx, "19")
			account, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			character, _, _ := repos.Character.Load(ctx, "19")
			count := uint16(1)
			switch name {
			case "no_drawing":
				delete(inv.Slots, "0:69")
			case "locked_drawing", "locked_input":
				key := "0:69"
				if name == "locked_input" {
					key = "0:9"
				}
				stack := inv.Slots[key]
				if stack.Extra == nil {
					stack.Extra = map[string]string{}
				}
				stack.Extra["equipment_lock_state"] = "locked"
				inv.Slots[key] = stack
			case "low_level":
				character.Level = 89
			case "multiple":
				count = 2
			}
			for i, material := range entry.Materials {
				if name != fmt.Sprintf("missing_input_%d", i) {
					continue
				}
				for key, stack := range inv.Slots {
					if stack.ItemID == material.ItemID {
						delete(inv.Slots, key)
					}
				}
				for key, stack := range account.Slots {
					if stack.ItemID == material.ItemID {
						delete(account.Slots, key)
					}
				}
			}
			for _, err := range []error{repos.Inventory.Save(ctx, inv), repos.AccountInventory.Save(ctx, account), repos.Character.Save(ctx, character)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			// Compare against the actual persisted pre-request snapshot. The
			// memory repository canonicalizes an empty item map to nil on Save.
			inv, _, _ = repos.Inventory.Load(ctx, "19")
			account, _, _ = repos.AccountInventory.Load(ctx, "account-1")
			character, _, _ = repos.Character.Load(ctx, "19")
			if name == "inventory_save" || name == "final_commit" {
				repos.CharacterSettlement = heblonFailUOW{repos.CharacterSettlement, name == "final_commit"}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			}
			if err := s.handleGameCommand(p, 1, 25, append(equipmentRecipeCompoundTestSlotRequest(69, count), 0, 1)); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, p.conn.(*bufferConn).write.Bytes())
			if len(ack.Body) == 0 || ack.Body[0] != 0 {
				t.Fatal("invalid transform succeeded")
			}
			gotInv, _, _ := repos.Inventory.Load(ctx, "19")
			gotAcc, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			gotChar, _, _ := repos.Character.Load(ctx, "19")
			if !reflect.DeepEqual(inv, gotInv) || !reflect.DeepEqual(account, gotAcc) || !reflect.DeepEqual(character, gotChar) {
				t.Fatal("rejection or rollback changed assets")
			}
		})
	}
}

func TestRealPVFHeblonPriceExceptionRejectsChangedContracts(t *testing.T) {
	a, recipes, _ := heblonResources(t)
	original, _, err := recipes.Recipe(10304471)
	if err != nil {
		t.Fatal(err)
	}
	text, err := a.ReadText(original.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(string) string{
		"different_price":              func(s string) string { return strings.Replace(s, "10159142 950", "10159142 951", 1) },
		"different_currency":           func(s string) string { return strings.Replace(s, "10159142 950", "10159143 950", 1) },
		"extra_price":                  func(s string) string { return strings.Replace(s, "10159142 950", "10159142 950 10159143 1", 1) },
		"duplicate_price":              func(s string) string { return s + "\n[need material]\n10159142 950\n[/need material]\n" },
		"missing_price_close":          func(s string) string { return strings.Replace(s, "[/need material]", "", 1) },
		"missing_input_close":          func(s string) string { return strings.Replace(s, "[/input item]", "", 1) },
		"different_input":              func(s string) string { return strings.Replace(s, "10159623 80", "10159623 79", 1) },
		"different_output":             func(s string) string { return strings.Replace(s, "100344527 1", "100344527 2", 1) },
		"missing_socket_flag":          func(s string) string { return strings.Replace(s, "[maintain socket stat]", "", 1) },
		"rate":                         func(s string) string { return s + "\n[success rate]\n100\n" },
		"int_data":                     func(s string) string { return s + "\n[int data]\n1\n[/int data]\n" },
		"need_skill":                   func(s string) string { return s + "\n[need skill]\n9 10\n[/need skill]\n" },
		"usable_datetime":              func(s string) string { return s + "\n[usable datetime]\n`2030-01-01 00:00:00`\n" },
		"orphan_usable_datetime_close": func(s string) string { return s + "\n[/usable datetime]\n" },
	} {
		t.Run(name, func(t *testing.T) {
			entry := original
			changed := edit(text)
			if changed == text {
				t.Fatal("mutation did not apply")
			}
			// Deliberately keep the old catalog view: raw validation must reject
			// malformed or newly priced documents instead of trusting that view.
			if _, err := currentEquipmentRecipeUpgradeDocument(entry, dungeonDropCatalogTestSource{entry.PVFPath: changed}); err == nil {
				t.Fatal("changed raw contract admitted")
			}
		})
	}
	other := original
	other.ItemID = 999999
	if _, err := currentEquipmentRecipeUpgradeDocument(other, a); err == nil {
		t.Fatal("unknown drawing inherited price exemption")
	}
}
