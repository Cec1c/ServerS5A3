package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const recipeUpgradeDrawing = "[stackable type]\n`[recipe]` 1\n[minimum level]\n90\n[usable job]\n`[all]`\n[/usable job]\n[input item]\n5000 1 2000 2 3037 1 0 50\n[/input item]\n[output item]\n6000 1\n[/output item]\n[maintain ability]\n`normal`\n[minimum itemmaking level]\n`larmor` 8\n[maintain socket stat]\n"

func recipeUpgradeFixture(t *testing.T, doc string) (dnfrepo.Group, *Service, *gameSession) {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	source := equipmentRecipeCompoundTestSource(doc)
	source["equipment/equipment.lst"] = "5000 `armor/input.equ`\n6000 `armor/output.equ`\n"
	source["equipment/armor/input.equ"] = "[equipment type]\n`[coat]`\n[grade]\n90\n[attach type]\n`[trade]`\n[durability]\n40\n"
	source["equipment/armor/output.equ"] = source["equipment/armor/input.equ"]
	source["stackable/stackable.lst"] += "3037 `material/shared.stk`\n"
	source["stackable/material/shared.stk"] = "[stackable type]\n`[material]`\n[stack limit]\n999\n"
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], 9)
	binary.LittleEndian.PutUint32(raw[2:6], 5000)
	binary.LittleEndian.PutUint32(raw[6:10], 12345)
	raw[0x0A] = 12
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], 40)
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], 987654)
	raw[currentEquipmentVectorOffset] = 2
	binary.LittleEndian.PutUint32(raw[currentEquipmentVectorOffset+1:currentEquipmentVectorOffset+5], 123)
	binary.LittleEndian.PutUint32(raw[currentEquipmentVectorOffset+5:currentEquipmentVectorOffset+9], 456)
	inv := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:69": {ItemID: 1000, Count: 2}, "0:9": {ItemID: 5000, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "pvf_path": "equipment/armor/input.equ", "instance_note": "keep"}}, "0:121": {ItemID: 2000, Count: 10}}}
	for _, err := range []error{repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90, Stats: map[string]int64{"gold": 100}}), repos.Inventory.Save(ctx, inv), repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{"0:358": {ItemID: 3037, Count: 3}}})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	s := equipmentRecipeCompoundTestService(t, source, repos)
	return repos, s, &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, itemListMigrationsDone: true}
}

func TestCurrentEquipmentRecipeUpgradePreservesInstanceAndCommitsAllAssets(t *testing.T) {
	for _, keep := range []bool{true, false} {
		t.Run(map[bool]string{true: "keep_sockets", false: "reset_sockets"}[keep], func(t *testing.T) {
			doc := recipeUpgradeDrawing
			if !keep {
				doc = strings.TrimSuffix(doc, "[maintain socket stat]\n")
			}
			repos, s, session := recipeUpgradeFixture(t, doc)
			ctx := context.Background()
			before, _, _ := repos.Inventory.Load(ctx, "19")
			// Fill every other ordinary equipment slot: an in-place upgrade needs no empty slot.
			for slot := int16(10); slot <= 64; slot++ {
				before.Slots[currentCeraShopInventorySlotKey(0, slot)] = dnfrepo.ItemStack{ItemID: 4000, Count: 1}
			}
			if err := repos.Inventory.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			if err := s.handleGameCommand(session, 1, 25, append(equipmentRecipeCompoundTestSlotRequest(69, 1), 0, 1)); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
			if !bytes.Equal(ack.Body, []byte{1, 0, 0}) || len(rest) == 0 {
				t.Fatalf("ack=%x trailing=%d", ack.Body, len(rest))
			}
			after, _, _ := repos.Inventory.Load(ctx, "19")
			character, _, _ := repos.Character.Load(ctx, "19")
			account, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			if after.Slots["0:9"].ItemID != 6000 || after.Slots["0:69"].Count != 1 || after.Slots["0:121"].Count != 8 || character.Stats["gold"] != 50 || account.Slots["0:358"].Count != 2 {
				t.Fatalf("partial/wrong upgrade inv=%+v char=%+v account=%+v", after, character, account)
			}
			want := append([]byte(nil), before.Slots["0:9"].RawEntry...)
			binary.LittleEndian.PutUint32(want[2:6], 6000)
			if !keep {
				for i := currentEquipmentVectorOffset; i < currentEquipmentVectorOffset+9; i++ {
					want[i] = 0
				}
			}
			if !bytes.Equal(after.Slots["0:9"].RawEntry, want) || after.Slots["0:9"].Extra["instance_note"] != "keep" {
				t.Fatalf("instance attributes changed: got=%x want=%x", after.Slots["0:9"].RawEntry, want)
			}
			projected := currentItemListEntryFromStack(0, 9, after.Slots["0:9"])
			if !bytes.Equal(projected.data[:], want) {
				t.Fatalf("refresh lost instance attributes: got=%x want=%x", projected.data, want)
			}
			// A duplicate request cannot consume another drawing after the input instance is gone.
			if _, err := s.commitCurrentEquipmentRecipeCompound(ctx, session, currentEquipmentRecipeCompoundRequest{SourceSlot: 69, Count: 1}); err == nil {
				t.Fatal("duplicate source transform accepted")
			}
			unchanged, _, _ := repos.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(after, unchanged) {
				t.Fatal("duplicate changed assets")
			}
		})
	}
}

type recipeUpgradeFailInventory struct{ dnfrepo.InventoryRepository }

func (r recipeUpgradeFailInventory) Save(context.Context, dnfrepo.InventoryRecord) error {
	return errors.New("inventory commit fault")
}

type recipeUpgradeFailUOW struct {
	dnfrepo.CharacterSettlementUnitOfWork
}

func (u recipeUpgradeFailUOW) WithinCharacterSettlement(ctx context.Context, id string, fn func(dnfrepo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error { tx.Inventory = recipeUpgradeFailInventory{tx.Inventory}; return fn(tx) })
}

func TestCurrentEquipmentRecipeUpgradeRejectsWithoutPartialConsumption(t *testing.T) {
	for _, name := range []string{"low_level", "missing_material", "shared_insufficient", "locked_equipment", "multiple_transforms", "unknown_mode", "socket_payload", "rate", "corrupt_raw", "save_fault"} {
		t.Run(name, func(t *testing.T) {
			doc := recipeUpgradeDrawing
			switch name {
			case "unknown_mode":
				doc = strings.Replace(doc, "`normal`", "`all`", 1)
			case "socket_payload":
				doc += "1\n"
			case "rate":
				doc += "[success rate]\n100\n"
			}
			repos, s, session := recipeUpgradeFixture(t, doc)
			ctx := context.Background()
			inv, _, _ := repos.Inventory.Load(ctx, "19")
			char, _, _ := repos.Character.Load(ctx, "19")
			acc, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			count := int64(1)
			switch name {
			case "low_level":
				char.Level = 89
			case "missing_material":
				delete(inv.Slots, "0:121")
			case "shared_insufficient":
				acc.Slots["0:358"] = dnfrepo.ItemStack{ItemID: 3037, Count: 0}
			case "locked_equipment":
				stack := inv.Slots["0:9"]
				stack.Extra["equipment_lock_state"] = "locked"
				inv.Slots["0:9"] = stack
			case "multiple_transforms":
				count = 2
			case "corrupt_raw":
				stack := inv.Slots["0:9"]
				stack.RawEntry[2] = 1
				inv.Slots["0:9"] = stack
			}
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			if err := repos.Character.Save(ctx, char); err != nil {
				t.Fatal(err)
			}
			if err := repos.AccountInventory.Save(ctx, acc); err != nil {
				t.Fatal(err)
			}
			if name == "save_fault" {
				repos.CharacterSettlement = recipeUpgradeFailUOW{repos.CharacterSettlement}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			}
			if _, err := s.commitCurrentEquipmentRecipeCompound(ctx, session, currentEquipmentRecipeCompoundRequest{SourceSlot: 69, Count: count}); err == nil {
				t.Fatal("invalid transform committed")
			}
			gotInv, _, _ := repos.Inventory.Load(ctx, "19")
			gotChar, _, _ := repos.Character.Load(ctx, "19")
			gotAcc, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			if !reflect.DeepEqual(inv, gotInv) || !reflect.DeepEqual(char, gotChar) || !reflect.DeepEqual(acc, gotAcc) {
				t.Fatal("rejected/failed transform changed assets")
			}
		})
	}
}
