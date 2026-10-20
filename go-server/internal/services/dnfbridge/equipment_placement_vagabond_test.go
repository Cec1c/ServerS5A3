package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Reference: A21 InventoryMoveService.IsValidEquipmentSlotForItem allows
// [weapon] -> SupportWeapon(24) only for job 11, (growType & 0x0f) == 4.
func TestCurrentEquipmentPlacementVagabondSupportWeapon(t *testing.T) {
	cases := []struct {
		name          string
		job           string
		growth        int64
		missingGrowth bool
		item          int64
		sourceList    byte
		sourceSlot    int16
		target        int16
		minimumLevel  int
		want          error
	}{
		{name: "vagabond", job: "11", growth: 4, item: 9001, target: 24},
		{name: "first awakening", job: "11", growth: 0x14, item: 9001, target: 24},
		{name: "second awakening", job: "11", growth: 0x24, item: 9001, target: 24},
		{name: "from cargo", job: "11", growth: 4, item: 9001, sourceList: 2, target: 24},
		{name: "main to support", job: "11", growth: 4, item: 9001, sourceList: 3, sourceSlot: 12, target: 24},
		{name: "support to main", job: "11", growth: 4, item: 9001, sourceList: 3, sourceSlot: 24, target: 12},
		{name: "unadvanced", job: "11", item: 9001, target: 24, want: errEquipmentPlacementSlotTypeMismatch},
		{name: "other female branch", job: "11", growth: 1, item: 9001, target: 24, want: errEquipmentPlacementSlotTypeMismatch},
		{name: "other job", job: "0", growth: 4, item: 9001, target: 24, want: errEquipmentPlacementSlotTypeMismatch},
		{name: "malformed job", job: "invalid", growth: 4, item: 9001, target: 24, want: errEquipmentPlacementSlotTypeMismatch},
		{name: "growth missing", job: "11", missingGrowth: true, item: 9001, target: 24, want: errEquipmentPlacementSlotTypeMismatch},
		{name: "growth negative", job: "11", growth: -12, item: 9001, target: 24, want: errEquipmentPlacementSlotTypeMismatch},
		{name: "growth out of domain", job: "11", growth: 0x104, item: 9001, target: 24, want: errEquipmentPlacementSlotTypeMismatch},
		{name: "title cannot become support weapon", job: "11", growth: 4, item: 9002, target: 24, want: errEquipmentPlacementSlotTypeMismatch},
		{name: "weapon avatar remains avatar", job: "11", growth: 4, item: 9110, sourceList: 1, target: 24, want: errEquipmentPlacementSlotClassMismatch},
		{name: "other target", job: "11", growth: 4, item: 9001, target: 25, want: errEquipmentPlacementSlotTypeMismatch},
		{name: "level still enforced", job: "11", growth: 4, item: 9001, target: 24, minimumLevel: 91, want: errEquipmentPlacementLevelInsufficient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newEquipmentPlacementValidatorFixture(t)
			ctx := context.Background()
			character, found, err := v.characters.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("load character: found=%t err=%v", found, err)
			}
			character.Job = tc.job
			if tc.missingGrowth {
				delete(character.Stats, dnfrepo.CharacterGrowTypeStatKey)
			} else {
				character.Stats[dnfrepo.CharacterGrowTypeStatKey] = tc.growth
			}
			if err := v.characters.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			if tc.minimumLevel > 0 {
				v.pvfSource.(equipmentPlacementPVFSource)["equipment/weapon/test.equ"] += "[minimum level]\n91\n"
			}
			err = v.ValidateEquipmentPlacement(ctx, dnfequip.Placement{CharacterID: "19", ItemID: tc.item, SourceListType: tc.sourceList, SourceSlotIndex: tc.sourceSlot, TargetSlotIndex: tc.target})
			if tc.want == nil && err != nil {
				t.Fatalf("valid placement rejected: %v", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestRealPVFCurrentVagabondSupportWeaponMoveRoundTrip(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("requires real PVF equipment metadata")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	f := newLoginWearBridgeFixture(t)
	f.service.initialEquipmentArchive = archive
	f.service.options.currentClientVersion = "official-24.9"
	f.session.op19QuietResolving = true
	ctx := context.Background()
	character, found, err := f.group.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load character: found=%t err=%v", found, err)
	}
	character.Job = "11"
	character.Stats = map[string]int64{dnfrepo.CharacterGrowTypeStatKey: 0x24}
	if err := f.group.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	worn, _, err := f.group.Equipment.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	entry := worn.Entries["12"]
	entry.RawEntry = loginWearAnchorRawEntry(12, entry.ItemID, 203004656, 48)
	worn.Entries["12"] = entry
	if err := f.group.Equipment.Save(ctx, worn); err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), entry.RawEntry...)
	move := func(src byte, srcSlot int16, dst byte, dstSlot int16) {
		t.Helper()
		f.buffer.write.Reset()
		body := loginWearBridgeBody(src, srcSlot, loginWearBridgeWeaponItemID, dst, dstSlot, 0)
		body[26] = 1
		handled, err := f.service.handleAlignedGameCommand(f.session, 1, 19, body)
		if err != nil || !handled {
			t.Fatalf("move %d:%d -> %d:%d handled=%t err=%v", src, srcSlot, dst, dstSlot, handled, err)
		}
		ack, _ := splitLongHengGameServerUpperPacket(t, f.buffer.write.Bytes())
		if ack.Header.MsgID != 19 || ack.Header.Classification != 1 || len(ack.Body) != 11 || ack.Body[0] != 1 {
			t.Fatalf("move ACK header=%+v body=%x", ack.Header, ack.Body)
		}
	}
	move(3, 12, 3, 24)
	worn, _, err = f.group.Equipment.Load(ctx, "19")
	if err != nil || worn.Entries["12"].ItemID != 0 || worn.Entries["24"].ItemID != entry.ItemID {
		t.Fatalf("main -> support equipment=%+v err=%v", worn, err)
	}
	move(3, 24, 0, 9)
	inv, _, err := f.group.Inventory.Load(ctx, "19")
	if err != nil || inv.Slots["0:9"].ItemID != entry.ItemID || inv.Slots["0:9"].Count != 1 {
		t.Fatalf("support -> bag inventory=%+v err=%v", inv, err)
	}
	move(0, 9, 3, 24)
	worn, _, err = f.group.Equipment.Load(ctx, "19")
	if err != nil || worn.Entries["24"].ItemID != entry.ItemID {
		t.Fatalf("bag -> support equipment=%+v err=%v", worn, err)
	}
	got := append([]byte(nil), worn.Entries["24"].RawEntry...)
	// Stored raw rows preserve their original slot header; the projection owns
	// rewriting it to EquipmentEntry.SlotIndex. Compare the identity and item
	// attributes after that header, and assert the authoritative slot separately.
	if worn.Entries["24"].SlotIndex != 24 || len(got) != len(original) || !bytes.Equal(got[2:], original[2:]) {
		t.Fatalf("move changed raw identity/attributes: got=%x want=%x", got, original)
	}
	inv, _, err = f.group.Inventory.Load(ctx, "19")
	if err != nil || inv.Slots["0:9"].ItemID != 0 {
		t.Fatalf("equipped weapon left in bag: inventory=%+v err=%v", inv, err)
	}
}
