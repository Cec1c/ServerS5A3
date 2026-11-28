package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfitemgrade "longheng.io/server/internal/modules/dnf/itemgrade"
	"longheng.io/server/internal/modules/dnf/itemquality"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestCurrentResetItemAttrMasterUsesTopSeedAndRemovedMaterialSlot(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:42": {ItemID: 1001, Count: 1},
			"0:75": {ItemID: 10150863, Count: 1, Extra: map[string]string{"item_kind": "stackable"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	connection := &bufferConn{}
	catalog := &pvfDungeonDropCatalog{
		source: finishBridgePVFSource{},
		itemCache: map[uint32]dungeonDropItemDefinition{
			10150863: {
				ItemID:        10150863,
				Kind:          dungeonDropItemStackable,
				StackableType: "[etc]",
				PVFPath:       "stackable/10150001/10150863.stk",
			},
		},
	}
	service := &Service{
		options:            options{accountID: "account-1"},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: connection, connID: "master-grade-adjust-test", selectedCharacterID: 19}
	body := make([]byte, 8)
	binary.LittleEndian.PutUint16(body[0:2], 42)
	binary.LittleEndian.PutUint32(body[2:6], 1001)
	binary.LittleEndian.PutUint16(body[6:8], 75)
	if err := service.handleCurrentResetItemAttr(session, body); err != nil {
		t.Fatal(err)
	}

	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(ack.Body) != 9 || ack.Body[0] != 1 ||
		binary.LittleEndian.Uint16(ack.Body[1:3]) != 75 ||
		binary.LittleEndian.Uint32(ack.Body[3:7]) != 0 ||
		binary.LittleEndian.Uint16(ack.Body[7:9]) != currentResetItemAttrGradeResult {
		t.Fatalf("master ack=%x", ack.Body)
	}
	refresh, trailing := splitGameServerUpperPacket(t, rest)
	if len(trailing) != 0 ||
		refresh.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) ||
		len(refresh.Body) != 3+currentItemListEntryWireSize {
		t.Fatalf("master refresh header=%+v body_len=%d trailing=%d", refresh.Header, len(refresh.Body), len(trailing))
	}
	if seed := binary.LittleEndian.Uint32(refresh.Body[3+6 : 3+10]); seed != currentEquipmentTopQualitySeed {
		t.Fatalf("master refreshed quality seed=%d, want top seed=%d", seed, currentEquipmentTopQualitySeed)
	}
	saved, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load: %v %v", found, err)
	}
	if currentEquipmentQualitySeedFromStack(saved.Slots["0:42"]) != currentEquipmentTopQualitySeed {
		t.Fatal("persisted seed differs from reply")
	}
	if _, exists := saved.Slots["0:75"]; exists {
		t.Fatal("one master box must be removed")
	}
	if saved.Slots["0:42"].Extra["grade_adjust_type"] != "master_kaleido" {
		t.Fatal("master provenance missing")
	}

}

func TestMasterKaleidoIdentityPolicy(t *testing.T) {
	valid := dungeonDropItemDefinition{ItemID: 10150863, Kind: dungeonDropItemStackable, PVFPath: "stackable/10150001/10150863.stk", StackableType: "[etc]"}
	if !currentMasterKaleidoMaximumQuality(valid) {
		t.Fatal("master policy not recognized")
	}
	cases := []dungeonDropItemDefinition{valid, valid, valid, valid}
	cases[0].ItemID = 15
	cases[1].PVFPath = "stackable/cash/box_kaleido.stk"
	cases[2].Kind = dungeonDropItemEquipment
	cases[3].StackableType = "[waste]"
	for _, invalid := range cases {
		if currentMasterKaleidoMaximumQuality(invalid) {
			t.Fatalf("unrelated definition got maximum policy: %+v", invalid)
		}
	}
}
func TestRealPVFMasterKaleidoUsesMaximumAttributes(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF fixture not supplied")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	adapter := currentItemGradeCatalog{catalog: catalog}
	master, err := adapter.ResolveItem(10150863)
	if err != nil {
		t.Fatal(err)
	}
	if !master.MaximumQuality || master.StackableType != "[etc]" {
		t.Fatalf("actual master definition=%+v", master)
	}
	ordinary, err := adapter.ResolveItem(15)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.MaximumQuality {
		t.Fatal("ordinary box got maximum quality policy")
	}
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:42": {ItemID: 101010856, Count: 1}, "0:75": {ItemID: 10150863, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfitemgrade.NewOwner(repos, adapter, func() (uint32, error) { t.Fatal("master box reached random branch"); return 0, nil })
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Adjust(ctx, dnfitemgrade.Command{SelectedCharacterID: 19, TargetSlot: 42, TargetItemID: 101010856, MaterialSlot: 75, TargetEquipmentType: "[weapon]"})
	if err != nil {
		t.Fatal(err)
	}
	if result.NewSeed != itemquality.TopSeed || !result.MaterialRemoved {
		t.Fatalf("real PVF result=%+v", result)
	}
}
