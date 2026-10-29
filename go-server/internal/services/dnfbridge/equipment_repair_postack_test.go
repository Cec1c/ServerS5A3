package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

// The repair ACK alone does not replace the worn item row in a dungeon panel.
// Project the committed durability after the ACK, and never refresh a row that
// the transaction did not change.
func TestRepairAllProjectsOnlyCommittedWornDurabilityAfterAck(t *testing.T) {
	s, session, repositories := newDurabilityBridgeFixture(t, 2)
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst": "", "stackable/stackable.lst": "",
		"equipment/equipment.lst": "101040493 `weapon.equ`\n",
		"equipment/weapon.equ":    "[durability] 48\n[equipment type] `[weapon]` 0\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = catalog
	ctx := context.Background()
	character, found, err := repositories.Character.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Stats["gold"] = 100000
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	request := alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketRepairEquipment),
		Body:                []byte{3, 0xff, 0xff, 0xff, 0xff, 0, 0, 1},
		AccountID:           "account-1",
		SelectedCharacterID: 99,
		Repositories:        repositories,
		RepairCostResolver: func(itemID int64) (alignedcmd.RepairCostEvidence, error) {
			return alignedcmd.RepairCostEvidence{
				EquipmentType: "[weapon]", MaxDurability: 48, RepairPrice: 6400,
				Grade: 20, RepairCostRate: 0.08, QuickRepairRate: 1.5,
				UpgradeRates: []float64{1, 1, 1},
			}, nil
		},
	}
	result, err := dnfinventory.NewHandler().Handle(ctx, request)
	if err != nil || !result.Handled || !result.ResponseAllowed {
		t.Fatalf("repair result=%+v err=%v", result, err)
	}
	if len(result.PostAckWornEquipmentSlotRefreshes) != 1 || result.PostAckWornEquipmentSlotRefreshes[0] != 12 {
		t.Fatalf("post-ACK worn slots=%v", result.PostAckWornEquipmentSlotRefreshes)
	}
	equipment, _, _ := repositories.Equipment.Load(ctx, "99")
	entry := equipment.Entries["12"]
	if got := binary.LittleEndian.Uint16(entry.RawEntry[11:13]); got != 48 {
		t.Fatalf("committed durability=%d want 48", got)
	}
	if err := s.sendAlignedUpperResponses(session, result); err != nil {
		t.Fatal(err)
	}
	first, rest := splitLongHengGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	second, tail := splitLongHengGameServerUpperPacket(t, rest)
	if first.Header.MsgID != uint16(dnfenum.CmdPacketRepairEquipment) || first.Header.Classification != 1 ||
		second.Header.MsgID != 14 || second.Header.Classification != 0 || len(tail) != 0 {
		t.Fatalf("repair frames first=%+v second=%+v trailing=%x", first.Header, second.Header, tail)
	}
	projected, ok := currentItemListEntryFromEquipment(entry)
	if !ok || !bytes.Equal(second.Body, buildCurrentItemUpdateBody(3, []currentItemListEntry{projected})) {
		t.Fatalf("post-ACK row did not match committed equipment: %x", second.Body)
	}
	paid, _, _ := repositories.Character.Load(ctx, "99")
	if paid.Stats["gold"] >= 100000 {
		t.Fatalf("quick repair did not charge once: gold=%d", paid.Stats["gold"])
	}
	session.conn.(*bufferConn).write.Reset()
	result, err = dnfinventory.NewHandler().Handle(ctx, request)
	if err != nil || len(result.PostAckWornEquipmentSlotRefreshes) != 0 {
		t.Fatalf("full durability should not schedule row refresh: %+v err=%v", result, err)
	}
	if err := s.sendAlignedUpperResponses(session, result); err != nil {
		t.Fatal(err)
	}
	only, tail := splitLongHengGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	if only.Header.MsgID != uint16(dnfenum.CmdPacketRepairEquipment) || len(tail) != 0 {
		t.Fatalf("no-op repair frames=%+v tail=%x", only.Header, tail)
	}
	again, _, _ := repositories.Character.Load(ctx, "99")
	if again.Stats["gold"] != paid.Stats["gold"] {
		t.Fatalf("no-op repair charged twice: before=%d after=%d", paid.Stats["gold"], again.Stats["gold"])
	}
}
