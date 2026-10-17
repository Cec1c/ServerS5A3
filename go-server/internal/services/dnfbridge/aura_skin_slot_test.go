package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestCurrentOpenAuraSkinSlotConsumesPVFTicketAndPersistsFlag(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Stats:       map[string]int64{"aura_flag": 0},
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:122": {ItemID: 490700411, Count: 1},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	catalog := mustTestAuraSkinSlotCatalog(t)
	service := &Service{
		options:            options{accountID: "account-1", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "aura-skin-slot-test", selectedCharacterID: 19}
	var request [4]byte
	binary.LittleEndian.PutUint32(request[:], 122)

	if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketOpenAuraSkinSlot), request[:]); err != nil {
		t.Fatalf("handle open aura skin slot: %v", err)
	}
	character, found, err := repositories.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	if got := character.Stats["aura_flag"]; got != 1 {
		t.Fatalf("aura_flag=%d want 1", got)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if _, ok := inventory.Slots["0:122"]; ok {
		t.Fatalf("source ticket slot still present after consume: %+v", inventory.Slots["0:122"])
	}

	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketOpenAuraSkinSlot) {
		t.Fatalf("ack header class=%d msg=%d", ack.Header.Classification, ack.Header.MsgID)
	}
	if len(ack.Body) != 1 || ack.Body[0] != 1 {
		t.Fatalf("ack body=%x", ack.Body)
	}
	update, rest := splitGameServerUpperPacket(t, rest)
	if update.Header.Classification != 0 || update.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("update header class=%d msg=%d", update.Header.Classification, update.Header.MsgID)
	}
	if len(update.Body) != 1+2+currentItemListEntryWireSize ||
		update.Body[0] != dnfrepo.MainInventoryListType ||
		binary.LittleEndian.Uint16(update.Body[1:3]) != 1 {
		headerLen := len(update.Body)
		if headerLen > 8 {
			headerLen = 8
		}
		t.Fatalf("update body header=%x len=%d", update.Body[:headerLen], len(update.Body))
	}
	row := update.Body[3:]
	if binary.LittleEndian.Uint16(row[0:2]) != 122 ||
		binary.LittleEndian.Uint32(row[2:6]) != math.MaxUint32 ||
		binary.LittleEndian.Uint32(row[6:10]) != 0 {
		t.Fatalf("remove row=%x", row[:10])
	}
	refresh, rest := splitGameServerUpperPacket(t, rest)
	if len(rest) != 0 || refresh.Header.Classification != 0 ||
		refresh.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) {
		t.Fatalf("native aura state header=%+v trailing=%d", refresh.Header, len(rest))
	}
	assertAuraSlotNativeState(t, refresh.Body, 0x08)
}

func TestCurrentOpenAuraSkinSlotAlreadyOpenDoesNotConsumeAgain(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Stats:       map[string]int64{"aura_flag": 1},
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:122": {ItemID: 490700411, Count: 1},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	service := &Service{
		options:            options{accountID: "account-1", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		pvfItemCatalog:     mustTestAuraSkinSlotCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "aura-skin-slot-test", selectedCharacterID: 19}
	var request [4]byte
	binary.LittleEndian.PutUint32(request[:], 122)

	if err := service.handleCurrentOpenAuraSkinSlot(session, request[:]); err != nil {
		t.Fatalf("handle already-open aura slot: %v", err)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	stack, ok := inventory.Slots["0:122"]
	if !ok || stack.Count != 1 {
		t.Fatalf("already-open ticket stack=%+v found=%t, want unchanged count 1", stack, ok)
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketOpenAuraSkinSlot) || len(ack.Body) != 1 || ack.Body[0] != 1 {
		t.Fatalf("already-open ack header=%+v body=%x", ack.Header, ack.Body)
	}
	refresh, rest := splitGameServerUpperPacket(t, rest)
	if len(rest) != 0 || refresh.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) || refresh.Header.Classification != 0 {
		t.Fatalf("already-open native aura state=%+v trailing=%d", refresh.Header, len(rest))
	}
	assertAuraSlotNativeState(t, refresh.Body, 0x08)
}

func TestCurrentAuraSkinSlotMissingMaterialKeepsDefaultLocked(t *testing.T) {
	for _, itemID := range []int64{0, 42} {
		t.Run(fmt.Sprintf("材料编号%d", itemID), func(t *testing.T) {
			ctx := context.Background()
			repositories := dnfrepomemory.NewMemoryGroup()
			if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
				t.Fatal(err)
			}
			inventory := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}
			if itemID != 0 {
				inventory.Slots["0:122"] = dnfrepo.ItemStack{ItemID: itemID, Count: 2}
			}
			if err := repositories.Inventory.Save(ctx, inventory); err != nil {
				t.Fatal(err)
			}
			service := &Service{
				options:            options{accountID: "account-1", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
				pvfItemCatalog:     mustTestAuraSkinSlotCatalog(t),
				repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
			}
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 19}
			var request [4]byte
			binary.LittleEndian.PutUint32(request[:], 122)
			if err := service.handleCurrentOpenAuraSkinSlot(session, request[:]); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if len(rest) != 0 || len(ack.Body) != 2 || ack.Body[0] != 0 {
				t.Fatalf("missing ticket ACK=%x trailing=%d", ack.Body, len(rest))
			}
			character, _, err := repositories.Character.Load(ctx, "19")
			if err != nil || character.Stats[currentOpenAuraSkinSlotFlagStat] != 0 {
				t.Fatalf("unpaid aura state=%v error=%v", character.Stats, err)
			}
			assertAuraSlotNativeState(t, service.buildCurrentSelectedUserInfoMode1Body(ctx, session, nil, character, true, 19), 0)
			after, _, err := repositories.Inventory.Load(ctx, "19")
			if err != nil || len(after.Slots) != len(inventory.Slots) || after.Slots["0:122"].Count != inventory.Slots["0:122"].Count {
				t.Fatalf("rejected ticket changed inventory=%v error=%v", after.Slots, err)
			}
		})
	}
}

func TestCurrentAuraSkinSlotTownUIReadyStateUsesNativeRestoreWithoutUnsolicitedAck(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Stats:       map[string]int64{"aura_flag": 1},
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	service := &Service{
		options:            options{accountID: "account-1", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "aura-skin-restore-test", selectedCharacterID: 19}

	if err := service.sendCurrentAuraSkinSlotTownUIReadyState(session); err != nil {
		t.Fatalf("send aura restore: %v", err)
	}
	if err := service.sendCurrentAuraSkinSlotTownUIReadyState(session); err != nil {
		t.Fatalf("send duplicate aura restore: %v", err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("cold login sent unsolicited aura ACK bytes=%d", conn.write.Len())
	}
	character, _, err := repositories.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	assertAuraSlotNativeState(t, service.buildCurrentSelectedUserInfoMode1Body(ctx, session, nil, character, true, 19), 0x08)
}

func TestCurrentAuraSkinSlotTownUIReadyStateKeepsDefaultLockedWithoutUnsolicitedAck(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Stats:       map[string]int64{"aura_flag": 0},
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	service := &Service{
		options:            options{accountID: "account-1", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "aura-skin-closed-test", selectedCharacterID: 19}

	if err := service.sendCurrentAuraSkinSlotTownUIReadyState(session); err != nil {
		t.Fatalf("check closed aura slot: %v", err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("locked character received unsolicited aura ACK bytes=%d", conn.write.Len())
	}
	character, _, err := repositories.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	assertAuraSlotNativeState(t, service.buildCurrentSelectedUserInfoMode1Body(ctx, session, nil, character, true, 19), 0)
}

func mustTestAuraSkinSlotCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst":              "",
		"stackable/stackable.lst":          "490700411 `cash/chn_490700411.stk`\n",
		"equipment/equipment.lst":          "",
		"stackable/cash/chn_490700411.stk": "[name] `光环幻化栏扩展券`\n[stackable type] `[material]`\n[stack limit] 1\n",
	})
	if err != nil {
		t.Fatalf("build aura-slot catalog: %v", err)
	}
	return catalog
}
