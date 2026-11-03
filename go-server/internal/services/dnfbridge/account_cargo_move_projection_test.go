package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	inv "longheng.io/server/internal/modules/dnf/inventory"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestAccountCargoBulkMoveProjectsOnlyCommittedRowsWithoutReload(t *testing.T) {
	ctx := context.Background()
	f := newRefreshCostFixture(t)
	account, _, _ := f.group.Account.Load(ctx, "dnf:1")
	if account.Metadata == nil {
		account.Metadata = make(map[string]string)
	}
	account.Metadata["account_cargo_level"] = "240"
	if err := f.group.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	extra := map[string]string{"item_kind": "stackable", "stackable_type": "[material]", "pvf_path": "stackable/material/solvent_magicpower.stk", "stack_limit": "999"}
	bag := r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{}}
	for i := 0; i < 28; i++ {
		bag.Slots[fmt.Sprintf("0:%d", 121+i)] = r.ItemStack{ItemID: 3227, Count: 2, Extra: extra}
	}
	cargo := r.AccountInventoryRecord{AccountID: "dnf:1", Slots: map[string]r.ItemStack{"12:3": {ItemID: 3227, Count: 7, Extra: extra}, "0:354": {ItemID: 3033, Count: 13}}}
	// A populated cargo must not increase the number or size of post-move rows.
	for i := 20; i < 220; i++ {
		cargo.Slots[fmt.Sprintf("12:%d", i)] = r.ItemStack{ItemID: int64(50000 + i), Count: 1}
	}
	if err := f.group.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	if err := f.group.AccountInventory.Save(ctx, cargo); err != nil {
		t.Fatal(err)
	}
	originalCharacter, _, _ := f.group.Character.Load(ctx, "19")
	sent := 0
	for i := 0; i < 28; i++ {
		body := make([]byte, 28)
		binary.LittleEndian.PutUint16(body[1:3], uint16(121+i))
		binary.LittleEndian.PutUint32(body[7:11], 2)
		body[11] = 12
		binary.LittleEndian.PutUint16(body[12:14], 4)
		result, err := inv.NewHandler().Handle(ctx, alignedcmd.Request{Opcode: 19, Body: body, AccountID: "dnf:1", SelectedCharacterID: 19, Repositories: f.group})
		if err != nil || !result.ResponseAllowed {
			t.Fatalf("move %d: %+v %v", i, result, err)
		}
		f.counter.reset()
		f.buffer.write.Reset()
		if err := f.service.sendAlignedUpperResponses(f.session, result); err != nil {
			t.Fatal(err)
		}
		if calls := f.counter.total(""); calls != 0 {
			t.Fatalf("projection reloaded repositories: %s", f.counter.summary())
		}
		frames := splitSortLockFrames(t, f.buffer.write.Bytes())
		if len(frames) != 3 || frames[0].Header.MsgID != 19 || frames[0].Header.Classification != 1 {
			t.Fatalf("ACK order: %+v", frames)
		}
		for j, list := range []byte{0, 12} {
			frame := frames[j+1]
			if frame.Header.MsgID != 14 || frame.Header.Classification != 0 || len(frame.Body) != 122 || frame.Body[0] != list || binary.LittleEndian.Uint16(frame.Body[1:3]) != 1 {
				t.Fatalf("unexpected projection: %+v", frame)
			}
			row := frame.Body[3:]
			slot := int16(binary.LittleEndian.Uint16(row[:2]))
			id := binary.LittleEndian.Uint32(row[2:6])
			count := binary.LittleEndian.Uint32(row[6:10])
			if list == 0 {
				if slot != int16(121+i) || id != math.MaxUint32 || count != 0 {
					t.Fatalf("source not cleared: %x", row[:10])
				}
			} else {
				if slot != 3 || id != 3227 || count != uint32(9+i*2) {
					t.Fatalf("merge target wrong: %x", row[:10])
				}
			}
			sent += len(frame.Body)
		}
	}
	finalBag, _, _ := f.group.Inventory.Load(ctx, "19")
	finalCargo, _, _ := f.group.AccountInventory.Load(ctx, "dnf:1")
	finalCharacter, _, _ := f.group.Character.Load(ctx, "19")
	if len(finalBag.Slots) != 0 || finalCargo.Slots["12:3"].Count != 63 || finalCargo.Slots["0:354"].Count != 13 || len(finalCargo.Slots) != 202 {
		t.Fatalf("assets after bulk move: bag=%v cargo=%v", finalBag, finalCargo)
	}
	if !reflect.DeepEqual(originalCharacter, finalCharacter) {
		t.Fatal("cargo move changed character/wallet")
	}
	t.Logf("28 transfers: incremental body bytes=%d, repository reads after commit=0; cargo=201 occupied rows", sent)
}

func TestAccountCargoNativeMoveIngressUsesEndpointRefresh(t *testing.T) {
	ctx := context.Background()
	f := newRefreshCostFixture(t)
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst": "", "equipment/equipment.lst": "", "stackable/stackable.lst": "3227 `material/test.stk`\n",
		"stackable/material/test.stk": "[stackable type] `[material]`\n[attach type] `[free]`\n[stack limit] 999\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.service.pvfItemCatalog = catalog
	account, _, _ := f.group.Account.Load(ctx, "dnf:1")
	if account.Metadata == nil {
		account.Metadata = make(map[string]string)
	}
	account.Metadata["account_cargo_level"] = "8"
	if err := f.group.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	extra := map[string]string{"item_kind": "stackable", "pvf_path": "stackable/material/test.stk", "stack_limit": "999"}
	if err := f.group.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "19", Slots: map[string]r.ItemStack{"0:121": {ItemID: 3227, Count: 2, Extra: extra}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.group.AccountInventory.Save(ctx, r.AccountInventoryRecord{AccountID: "dnf:1", Slots: map[string]r.ItemStack{"12:3": {ItemID: 3227, Count: 7, Extra: extra}}}); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 28)
	binary.LittleEndian.PutUint16(body[1:3], 121)
	binary.LittleEndian.PutUint32(body[7:11], 2)
	body[11] = 12
	binary.LittleEndian.PutUint16(body[12:14], 4)
	packet := buildLegacyGamePacketForBridgeTest(1, 19, 901, body)
	if err := f.service.handleGameStreamPacket(f.session, proto.LatestGameStreamPacket{Kind: proto.LatestGameStreamLegacy, Data: packet}); err != nil {
		t.Fatal(err)
	}
	frames := splitSortLockFrames(t, f.buffer.write.Bytes())
	if len(frames) != 3 || frames[0].Header.MsgID != 19 || frames[1].Header.MsgID != 14 || frames[2].Header.MsgID != 14 {
		t.Fatalf("native move response=%+v", frames)
	}
	if frames[1].Body[0] != 0 || frames[2].Body[0] != 12 || binary.LittleEndian.Uint32(frames[1].Body[5:9]) != math.MaxUint32 || binary.LittleEndian.Uint32(frames[2].Body[9:13]) != 9 {
		t.Fatalf("endpoint body mismatch: %x %x", frames[1].Body, frames[2].Body)
	}
}

func TestAccountCargoProjectionRejectsWrongOwnerAndKeepsEquipmentQuality(t *testing.T) {
	f := newRefreshCostFixture(t)
	stack := r.ItemStack{ItemID: 9001, Count: 1, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "572024372", "upgrade_level": "7"}}
	p := &alignedcmd.AccountCargoMoveProjection{AccountID: "other", CharacterID: "19", Rows: []alignedcmd.CommittedItemRow{{ListType: 12, SlotIndex: 1, Stack: &stack}, {ListType: 0, SlotIndex: 9}}}
	if err := f.service.sendCommittedAccountCargoMove(f.session, p); err == nil || f.buffer.write.Len() != 0 {
		t.Fatal("foreign snapshot sent")
	}
	p.AccountID = "dnf:1"
	if err := f.service.sendCommittedAccountCargoMove(f.session, p); err != nil {
		t.Fatal(err)
	}
	frames := splitSortLockFrames(t, f.buffer.write.Bytes())
	row := frames[0].Body[3:]
	if binary.LittleEndian.Uint32(row[6:10]) != 572024372 {
		t.Fatalf("equipment quality changed: %x", row[:14])
	}
}
