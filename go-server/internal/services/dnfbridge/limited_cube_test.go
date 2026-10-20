package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnflimitedcube "longheng.io/server/internal/modules/dnf/limitedcube"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestResolveCurrentLimitedCubePolicyUsesPVFConditionsAndWeights(t *testing.T) {
	catalog := newLimitedCubeTestCatalog(t)
	policy, recognized, err := resolveCurrentLimitedCubePolicy(catalog, 9000)
	if err != nil || !recognized {
		t.Fatalf("resolve recognized=%t err=%v", recognized, err)
	}
	if policy.TicketItemID != 9000 ||
		!reflect.DeepEqual(policy.Conditions, []dnflimitedcube.Requirement{{ItemID: 9001, Count: 1}, {ItemID: 9002, Count: 1}}) ||
		!reflect.DeepEqual(policy.Materials, []dnflimitedcube.Requirement{{ItemID: 3037, Count: 10}}) ||
		len(policy.Results) != 2 || policy.Results[0].Weight != 1000 || policy.Results[1].Weight != 1000 {
		t.Fatalf("policy = %+v", policy)
	}
	if first := policy.Results[0].Stack; first.ItemID != 9001 || first.Count != 1 || first.Extra["item_kind"] != "stackable" ||
		first.Extra["pvf_path"] != "stackable/bead/power.stk" || first.Extra["stackable_type"] != "[enchant waste]" {
		t.Fatalf("first result stack = %+v", first)
	}

	for _, itemID := range []int64{9003, 3037, 9999} {
		if _, recognized, err := resolveCurrentLimitedCubePolicy(catalog, itemID); err != nil || recognized {
			t.Fatalf("item %d recognized=%t err=%v, want non-limited cube", itemID, recognized, err)
		}
	}
}

func TestRealPVFPetBeadChangeTicketsResolveExactPVFPool(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to validate pet-bead change ticket PVF rules")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, ticketItemID := range []int64{490007246, 490701734} {
		policy, recognized, err := resolveCurrentLimitedCubePolicy(catalog, ticketItemID)
		if err != nil || !recognized {
			t.Fatalf("ticket %d recognized=%t err=%v", ticketItemID, recognized, err)
		}
		if policy.TicketItemID != ticketItemID || len(policy.Conditions) != 91 ||
			!reflect.DeepEqual(policy.Materials, []dnflimitedcube.Requirement{{ItemID: 3037, Count: 10}}) || len(policy.Results) != 91 {
			t.Fatalf("ticket %d policy shape = %+v", ticketItemID, policy)
		}
		for offset := int64(0); offset < 91; offset++ {
			wantItemID := int64(490007152) + offset
			if condition := policy.Conditions[offset]; condition.ItemID != wantItemID || condition.Count != 1 {
				t.Fatalf("ticket %d condition %d = %+v, want item=%d count=1", ticketItemID, offset, condition, wantItemID)
			}
			if result := policy.Results[offset]; result.Stack.ItemID != wantItemID || result.Stack.Count != 1 || result.Weight != 1000 {
				t.Fatalf("ticket %d result %d = %+v, want item=%d count=1 weight=1000", ticketItemID, offset, result, wantItemID)
			}
		}
	}
}

func TestRealPVFPetBeadChangeTicketConsumesCrystalWarehouseMaterial(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to validate account crystal material consumption")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	service, session, repositories, connection := newPremiumServiceTestRuntime(t)
	service.pvfItemCatalog = catalog
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:95": {ItemID: 490007240, Count: 1},
			"0:97": {ItemID: 490701734, Count: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.AccountInventory.Save(context.Background(), dnfrepo.AccountInventoryRecord{
		AccountID: "account-1",
		Slots: map[string]dnfrepo.ItemStack{
			dnfrepo.AccountSharedInventorySlotKey(358): {ItemID: 3037, Count: 100},
		},
	}); err != nil {
		t.Fatal(err)
	}
	request := make([]byte, currentCrystalContractConsumeBodySize)
	binary.LittleEndian.PutUint16(request[0:2], 95)
	binary.LittleEndian.PutUint32(request[2:6], 490007240)
	binary.LittleEndian.PutUint16(request[6:8], 97)
	if err := service.handleCurrentLimitedCubeOrCrystalContractCubeUse(session, request); err != nil {
		t.Fatal(err)
	}
	cleared, remaining := splitGameServerUpperPacket(t, connection.write.Bytes())
	refresh, remaining := splitGameServerUpperPacket(t, remaining)
	ack, trailing := splitGameServerUpperPacket(t, remaining)
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketUseLimitCube) || len(ack.Body) != 12 || ack.Body[0] != 1 {
		t.Fatalf("op338 acknowledgement=%+v body=%x", ack.Header, ack.Body)
	}
	if refresh.Header.MsgID != 14 || len(trailing) != 0 {
		t.Fatal("native result refresh must precede op338 ACK")
	}
	if cleared.Header.Classification != 0 || cleared.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) ||
		len(cleared.Body) != 3+currentItemListEntryWireSize || cleared.Body[0] != dnfrepo.MainInventoryListType ||
		binary.LittleEndian.Uint16(cleared.Body[1:3]) != 1 {
		t.Fatalf("target replacement clear=%+v body=%x", cleared.Header, cleared.Body)
	}
	if got := limitedCubeRefreshRows(t, cleared.Body)[95]; got.itemID != ^uint32(0) || got.count != 0 {
		t.Fatalf("target replacement clear row=%+v", got)
	}
	account, found, err := repositories.AccountInventory.Load(context.Background(), "account-1")
	if err != nil || !found {
		t.Fatalf("load account inventory found=%t err=%v", found, err)
	}
	if got := account.Slots["0:358"].Count; got != 90 {
		t.Fatalf("account clear cube count=%d want=90", got)
	}
	inventory, found, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if bead := inventory.Slots["0:95"]; bead.ItemID == 490007240 || bead.Count != 1 || bead.Extra["source"] != "limited_cube_result" {
		t.Fatalf("changed real-PVF bead = %+v, want a different one-count result", bead)
	} else if promptItemID := int64(binary.LittleEndian.Uint32(ack.Body[1:5])); promptItemID != bead.ItemID {
		t.Fatalf("op338 prompt item=%d want transformed bead=%d", promptItemID, bead.ItemID)
	}
}

func TestCurrentLimitedCubeOp338ChangesPetBeadAndRefreshesAllChangedSlots(t *testing.T) {
	service, session, repositories, connection := newPremiumServiceTestRuntime(t)
	service.pvfItemCatalog = newLimitedCubeTestCatalog(t)
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:2": {ItemID: 9000, Count: 1},
			"0:7": {ItemID: 9001, Count: 1, Bind: true},
			"0:9": {ItemID: 3037, Count: 10},
		},
	}); err != nil {
		t.Fatal(err)
	}
	request := make([]byte, currentCrystalContractConsumeBodySize)
	binary.LittleEndian.PutUint16(request[0:2], 7)
	binary.LittleEndian.PutUint32(request[2:6], 9001)
	binary.LittleEndian.PutUint16(request[6:8], 2)
	if err := service.handleCurrentLimitedCubeOrCrystalContractCubeUse(session, request); err != nil {
		t.Fatal(err)
	}

	cleared, remaining := splitGameServerUpperPacket(t, connection.write.Bytes())
	refresh, remaining := splitGameServerUpperPacket(t, remaining)
	ack, trailing := splitGameServerUpperPacket(t, remaining)
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketUseLimitCube) ||
		len(ack.Body) != 12 || ack.Body[0] != 1 ||
		binary.LittleEndian.Uint32(ack.Body[1:5]) != 9002 ||
		binary.LittleEndian.Uint16(ack.Body[5:7]) != 1 ||
		binary.LittleEndian.Uint32(ack.Body[7:11]) != 7 || ack.Body[11] != 1 {
		t.Fatalf("op338 acknowledgement = %+v body=%x", ack.Header, ack.Body)
	}
	if cleared.Header.Classification != 0 || cleared.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) ||
		len(cleared.Body) != 3+currentItemListEntryWireSize || cleared.Body[0] != dnfrepo.MainInventoryListType ||
		binary.LittleEndian.Uint16(cleared.Body[1:3]) != 1 {
		t.Fatalf("target replacement clear header=%+v body=%x", cleared.Header, cleared.Body)
	}
	if got := limitedCubeRefreshRows(t, cleared.Body)[7]; got.itemID != ^uint32(0) || got.count != 0 {
		t.Fatalf("target replacement clear row=%+v", got)
	}
	if len(trailing) != 0 || refresh.Header.Classification != 0 || refresh.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) ||
		len(refresh.Body) != 3+3*currentItemListEntryWireSize || refresh.Body[0] != dnfrepo.MainInventoryListType || binary.LittleEndian.Uint16(refresh.Body[1:3]) != 3 {
		t.Fatalf("op14 refresh header=%+v body=%x trailing=%x", refresh.Header, refresh.Body, trailing)
	}
	updates := limitedCubeRefreshRows(t, refresh.Body)
	if got := updates[2]; got.itemID != ^uint32(0) || got.count != 0 {
		t.Fatalf("ticket update = %+v", got)
	}
	if got := updates[7]; got.itemID != 9002 || got.count != 1 {
		t.Fatalf("bead result update = %+v, want item=9002 count=1", got)
	}
	if got := updates[9]; got.itemID != ^uint32(0) || got.count != 0 {
		t.Fatalf("material update = %+v", got)
	}

	inventory, found, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if _, found := inventory.Slots["0:2"]; found {
		t.Fatalf("ticket remains in inventory: %+v", inventory.Slots)
	}
	bead := inventory.Slots["0:7"]
	if bead.ItemID != 9002 || bead.Count != 1 || !bead.Bind || bead.Extra["source"] != "limited_cube_result" {
		t.Fatalf("changed bead = %+v", bead)
	}
	if _, found := inventory.Slots["0:9"]; found {
		t.Fatalf("material remains in inventory: %+v", inventory.Slots)
	}
}

func TestCurrentLimitedCubeOp338ConsumesCrystalWarehouseMaterial(t *testing.T) {
	service, session, repositories, connection := newPremiumServiceTestRuntime(t)
	service.pvfItemCatalog = newLimitedCubeTestCatalog(t)
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:2": {ItemID: 9000, Count: 1},
			"0:7": {ItemID: 9001, Count: 1, Bind: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.AccountInventory.Save(context.Background(), dnfrepo.AccountInventoryRecord{
		AccountID: "account-1",
		Slots: map[string]dnfrepo.ItemStack{
			dnfrepo.AccountSharedInventorySlotKey(358): {ItemID: 3037, Count: 999},
		},
	}); err != nil {
		t.Fatal(err)
	}
	request := make([]byte, currentCrystalContractConsumeBodySize)
	binary.LittleEndian.PutUint16(request[0:2], 7)
	binary.LittleEndian.PutUint32(request[2:6], 9001)
	binary.LittleEndian.PutUint16(request[6:8], 2)
	if err := service.handleCurrentLimitedCubeOrCrystalContractCubeUse(session, request); err != nil {
		t.Fatal(err)
	}

	cleared, remaining := splitGameServerUpperPacket(t, connection.write.Bytes())
	refresh, remaining := splitGameServerUpperPacket(t, remaining)
	ack, trailing := splitGameServerUpperPacket(t, remaining)
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketUseLimitCube) || len(ack.Body) != 12 || ack.Body[0] != 1 {
		t.Fatalf("op338 acknowledgement=%+v body=%x", ack.Header, ack.Body)
	}
	if cleared.Header.Classification != 0 || cleared.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) ||
		len(cleared.Body) != 3+currentItemListEntryWireSize || cleared.Body[0] != dnfrepo.MainInventoryListType ||
		binary.LittleEndian.Uint16(cleared.Body[1:3]) != 1 {
		t.Fatalf("target replacement clear header=%+v body=%x", cleared.Header, cleared.Body)
	}
	if got := limitedCubeRefreshRows(t, cleared.Body)[7]; got.itemID != ^uint32(0) || got.count != 0 {
		t.Fatalf("target replacement clear row=%+v", got)
	}
	if len(trailing) != 0 || refresh.Header.Classification != 0 || refresh.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) ||
		len(refresh.Body) != 3+3*currentItemListEntryWireSize || refresh.Body[0] != dnfrepo.MainInventoryListType || binary.LittleEndian.Uint16(refresh.Body[1:3]) != 3 {
		t.Fatalf("op14 refresh header=%+v body=%x trailing=%x", refresh.Header, refresh.Body, trailing)
	}
	updates := limitedCubeRefreshRows(t, refresh.Body)
	if got := updates[358]; got.itemID != 3037 || got.count != 989 {
		t.Fatalf("account crystal update=%+v want item=3037 count=989", got)
	}
	account, found, err := repositories.AccountInventory.Load(context.Background(), "account-1")
	if err != nil || !found {
		t.Fatalf("load account inventory found=%t err=%v", found, err)
	}
	if got := account.Slots["0:358"].Count; got != 989 {
		t.Fatalf("persisted account crystal count=%d want=989", got)
	}
}

func TestCurrentLimitedCubeDispatcherLeavesCrystalContractCubesOnExistingPath(t *testing.T) {
	service, session, _, connection := newCrystalContractTestRuntime(t, map[string]dnfrepo.ItemStack{
		"0:354": {ItemID: 3033, Count: 1},
	})
	service.pvfItemCatalog = newLimitedCubeTestCatalog(t)
	if err := service.handleCurrentCrystalContractUpdate(session, []byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	connection.write.Reset()
	session.dungeon.runtime = &runtimeDungeonState{Character: dnfrepo.CharacterRecord{CharacterID: "19"}}
	request := make([]byte, currentCrystalContractConsumeBodySize)
	binary.LittleEndian.PutUint16(request[0:2], 354)
	binary.LittleEndian.PutUint32(request[2:6], 3033)
	binary.LittleEndian.PutUint16(request[6:8], 1)
	if err := service.handleCurrentLimitedCubeOrCrystalContractCubeUse(session, request); err != nil {
		t.Fatal(err)
	}
	packet, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) == 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketUseLimitCube) || !bytes.Equal(packet.Body[:1], []byte{1}) {
		t.Fatalf("crystal op338 packet=%+v body=%x trailing=%x", packet.Header, packet.Body, trailing)
	}
}

func TestCurrentLimitedCubeRejectsDungeonUseWithoutMutation(t *testing.T) {
	service, session, repositories, connection := newPremiumServiceTestRuntime(t)
	service.pvfItemCatalog = newLimitedCubeTestCatalog(t)
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:2": {ItemID: 9000, Count: 1},
			"0:7": {ItemID: 9001, Count: 1},
			"0:9": {ItemID: 3037, Count: 10},
		},
	}); err != nil {
		t.Fatal(err)
	}
	session.dungeon.runtime = &runtimeDungeonState{Character: dnfrepo.CharacterRecord{CharacterID: "19"}}
	request := make([]byte, currentCrystalContractConsumeBodySize)
	binary.LittleEndian.PutUint16(request[0:2], 7)
	binary.LittleEndian.PutUint32(request[2:6], 9001)
	binary.LittleEndian.PutUint16(request[6:8], 2)
	if err := service.handleCurrentLimitedCubeOrCrystalContractCubeUse(session, request); err != nil {
		t.Fatal(err)
	}
	packet, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketUseLimitCube) || !bytes.Equal(packet.Body, []byte{0, 1}) {
		t.Fatalf("dungeon use packet=%+v body=%x trailing=%x", packet.Header, packet.Body, trailing)
	}
	inventory, found, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if inventory.Slots["0:2"].Count != 1 || inventory.Slots["0:7"].ItemID != 9001 || inventory.Slots["0:9"].Count != 10 {
		t.Fatalf("dungeon use mutated inventory = %+v", inventory.Slots)
	}
}

type limitedCubeRefreshRow struct {
	itemID uint32
	count  uint32
}

func limitedCubeRefreshRows(t *testing.T, body []byte) map[uint16]limitedCubeRefreshRow {
	t.Helper()
	rows := make(map[uint16]limitedCubeRefreshRow)
	for offset := 3; offset+currentItemListEntryWireSize <= len(body); offset += currentItemListEntryWireSize {
		row := body[offset : offset+currentItemListEntryWireSize]
		rows[binary.LittleEndian.Uint16(row[0:2])] = limitedCubeRefreshRow{
			itemID: binary.LittleEndian.Uint32(row[2:6]),
			count:  binary.LittleEndian.Uint32(row[6:10]),
		}
	}
	return rows
}

func newLimitedCubeTestCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":                  "",
		"equipment/equipment.lst":              "",
		"stackable/stackable.lst":              "9000 `cash/pet_bead_change.stk`\n9001 `bead/power.stk`\n9002 `bead/element.stk`\n9003 `material/plain.stk`\n3037 `material/clear_crystal.stk`\n",
		"stackable/cash/pet_bead_change.stk":   "[stackable type] `[upgrade limit cube]`\n[action type] `[limited cube]`\n[action usable place] `[village]`\n[A condition item]\n9001 1\n9002 1\n[B condition item]\n3037 10\n[result item]\n9001 1 1000\n9002 1 1000\n",
		"stackable/bead/power.stk":             "[stackable type] `[enchant waste]`\n[stack limit] 1\n",
		"stackable/bead/element.stk":           "[stackable type] `[enchant waste]`\n[stack limit] 1\n",
		"stackable/material/plain.stk":         "[stackable type] `[material]`\n",
		"stackable/material/clear_crystal.stk": "[stackable type] `[material]`\n[stack limit] 99999\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestCurrentLimitedCubeConsecutiveUsesKeepNativeResultSelection(t *testing.T) {
	assertLimitedCubeConsecutiveNativeSelection(t, newLimitedCubeTestCatalog(t), 9000, 9001)
}

func TestRealPVFLimitedCubeConsecutiveOrcTicketUses(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	assertLimitedCubeConsecutiveNativeSelection(t, catalog, 490701734, 490007240)
}

func assertLimitedCubeConsecutiveNativeSelection(t *testing.T, catalog *pvfDungeonDropCatalog, ticketItem, firstBead int64) {
	t.Helper()
	service, session, repositories, connection := newPremiumServiceTestRuntime(t)
	service.pvfItemCatalog = catalog
	ctx := context.Background()
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
		"0:95": {ItemID: firstBead, Count: 1, Bind: true}, "0:97": {ItemID: ticketItem, Count: 3},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{"0:358": {ItemID: 3037, Count: 100}}}); err != nil {
		t.Fatal(err)
	}
	// Maintain the client's inventory view in actual packet order. The native
	// 3485D50 comparison resolves the ACK's third field as a slot and compares
	// its current template before allowing window 512 to retain selection.
	model := map[uint16]limitedCubeRefreshRow{95: {uint32(firstBead), 1}, 97: {uint32(ticketItem), 3}, 358: {3037, 100}}
	selected := uint32(firstBead)
	for use := 0; use < 3; use++ {
		connection.write.Reset()
		request := make([]byte, currentCrystalContractConsumeBodySize)
		binary.LittleEndian.PutUint16(request[0:2], 95)
		binary.LittleEndian.PutUint32(request[2:6], selected)
		binary.LittleEndian.PutUint16(request[6:8], 97)
		if err := service.handleCurrentLimitedCubeOrCrystalContractCubeUse(session, request); err != nil {
			t.Fatal(err)
		}
		remaining := connection.write.Bytes()
		updates := 0
		acknowledged := false
		for len(remaining) > 0 {
			packet, rest := splitGameServerUpperPacket(t, remaining)
			remaining = rest
			if packet.Header.MsgID == 14 && packet.Header.Classification == 0 {
				if acknowledged {
					t.Fatal("inventory refresh arrived after native selection validation")
				}
				updates++
				for slot, row := range limitedCubeRefreshRows(t, packet.Body) {
					if row.itemID == ^uint32(0) {
						delete(model, slot)
					} else {
						model[slot] = row
					}
				}
			} else if packet.Header.MsgID == 338 && packet.Header.Classification == 1 {
				if updates != 2 || len(packet.Body) != 12 || packet.Body[0] != 1 {
					t.Fatalf("use=%d ACK before replacement or failed body=%x", use, packet.Body)
				}
				slot := binary.LittleEndian.Uint32(packet.Body[7:11])
				item := binary.LittleEndian.Uint32(packet.Body[1:5])
				if slot != 95 || model[uint16(slot)].itemID != item || item == selected {
					t.Fatalf("use=%d native selection mismatch slot=%d item=%d model=%v", use, slot, item, model)
				}
				selected = item
				// Export the committed production packet and client inventory view
				// for opt-in offline execution of the current EXE's result handler.
				// These fixtures come from the isolated repository above, never
				// from a player account or a reconstructed packet constant.
				if dir := os.Getenv("DNFBRIDGE_LIMITED_CUBE_NATIVE_EXPORT"); dir != "" {
					if err := os.MkdirAll(dir, 0755); err != nil {
						t.Fatal(err)
					}
					name := fmt.Sprintf("%d-use-%d", ticketItem, use+1)
					if err := os.WriteFile(filepath.Join(dir, name+".bin"), packet.Body, 0644); err != nil {
						t.Fatal(err)
					}
					viewRows := make(map[uint16][2]uint32, len(model))
					for viewSlot, row := range model {
						viewRows[viewSlot] = [2]uint32{row.itemID, row.count}
					}
					view, err := json.Marshal(viewRows)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, name+".json"), view, 0644); err != nil {
						t.Fatal(err)
					}
				}
				acknowledged = true
			} else {
				t.Fatalf("unexpected limited cube packet=%+v", packet.Header)
			}
		}
		if !acknowledged || model[97].count != uint32(2-use) || model[358].count != uint32(90-use*10) || model[95].count != 1 {
			t.Fatalf("use=%d model=%v", use, model)
		}
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "19")
	account, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
	if inventory.Slots["0:97"].Count != 0 || inventory.Slots["0:95"].ItemID != int64(selected) || !inventory.Slots["0:95"].Bind || account.Slots["0:358"].Count != 70 {
		t.Fatal("consecutive atomic settlements mismatch")
	}
}
