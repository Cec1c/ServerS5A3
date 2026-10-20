package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func newDimensionExtraNativeFixture(t *testing.T, path string) (*Service, dnfrepo.Group, *gameSession, *bufferConn) {
	t.Helper()
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	towns, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "native-extra-account",
		Level:       90,
		Stats:       map[string]int64{"fatigue": 100},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:20": {ItemID: 10158125, Count: 2},
			"0:21": {ItemID: 10158692, Count: 1},
			"0:22": {ItemID: 10158126, Count: 1},
			"0:30": {ItemID: 3228, Count: 60},
		},
	}); err != nil {
		t.Fatal(err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	conn := &bufferConn{}
	service := &Service{
		options: options{
			currentClientVersion: "official-24.9",
			gameUpperHeader:      gameUpperHeaderServer16,
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
		},
		initialEquipmentArchive: archive,
		pvfItemCatalog:          catalog,
		townCatalog:             towns,
		gameplayTimers:          queue,
		repositoryProvider: func() (dnfrepo.Group, bool) {
			return repos, true
		},
	}
	session := &gameSession{
		selectedCharacterID: 19,
		accountID:           "native-extra-account",
		conn:                conn,
		connID:              "dimension-extra-native",
	}
	bindDungeonSelectorOriginForTestAt(t, service, session, 107, 0, 250, 224)
	return service, repos, session, conn
}

func dimensionExtraNativeRequest(slot int16) []byte {
	body := make([]byte, currentDimensionGateExtraTicketBodySize)
	binary.LittleEndian.PutUint16(body[0:2], uint16(slot))
	binary.LittleEndian.PutUint32(body[7:11], currentDimensionGateExtraTicketAction)
	return body
}

func TestRealPVFDimensionGateExtraTicketNativeAction156(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the a56 Script.pvf")
	}
	for _, testCase := range []struct {
		name        string
		slot        int16
		itemID      int64
		wantGranted int64
		material    bool
	}{
		{"one_ticket", 20, 10158125, 1, false},
		{"five_ticket", 21, 10158692, 5, false},
		{"material_ticket", 22, 10158126, 1, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service, repos, session, conn := newDimensionExtraNativeFixture(t, path)
			err := service.handleGameCommandRequest(session, gameplayRequest{Opcode: 515, Classification: 1, Body: dimensionExtraNativeRequest(testCase.slot)})
			if err != nil {
				t.Fatal(err)
			}
			character, found, err := repos.Character.Load(context.Background(), "19")
			if err != nil || !found {
				t.Fatal(found, err)
			}
			inventory, found, err := repos.Inventory.Load(context.Background(), "19")
			if err != nil || !found {
				t.Fatal(found, err)
			}
			used, granted, total, err := dnfdungeon.DimensionGateDailyAllowance(character, service.gameplayNow(), 5, 5)
			if err != nil || used != 0 || granted != testCase.wantGranted || total != 5+testCase.wantGranted {
				t.Fatalf("used=%d granted=%d total=%d err=%v", used, granted, total, err)
			}
			slotKey := "0:" + strconv.Itoa(int(testCase.slot))
			remaining := inventory.Slots[slotKey]
			if testCase.slot == 20 && remaining.Count != 1 {
				t.Fatalf("slot 20 count=%d, want 1 after consuming one", remaining.Count)
			}
			if testCase.slot != 20 {
				if _, found := inventory.Slots[slotKey]; found {
					t.Fatalf("slot %d was not consumed", testCase.slot)
				}
			}
			if testCase.material {
				if _, found := inventory.Slots["0:30"]; found {
					t.Fatal("material fee 3228 x 60 was not consumed")
				}
			} else if inventory.Slots["0:30"].Count != 60 {
				t.Fatal("unrelated material was consumed")
			}
			ackPacket, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
			if ackPacket.Header.MsgID != 515 || ackPacket.Header.Classification != 1 {
				t.Fatalf("first packet=%+v, want class1/op515 ACK", ackPacket)
			}
			wantAck := dnfinventory.BuildStackableActionAck(testCase.slot, dnfrepo.MainInventoryListType, currentDimensionGateExtraTicketAction)
			if got := ackPacket.Body; len(got) != len(wantAck) || string(got) != string(wantAck) {
				t.Fatalf("ack=%x want=%x", got, wantAck)
			}
			refresh, trailing := splitLongHengGameServerUpperPacket(t, rest)
			if refresh.Header.MsgID != 14 || refresh.Header.Classification != 0 || len(trailing) != 0 || len(refresh.Body) < 3 {
				t.Fatalf("second packet msg=%d, want op14 slot refresh", refresh.Header.MsgID)
			}
			rows := decodeCurrentItemListTestRows(t, refresh.Body[3:])
			wantRows := 1
			if testCase.material {
				wantRows = 2
			}
			if len(rows) != wantRows || int(binary.LittleEndian.Uint16(refresh.Body[1:3])) != wantRows {
				t.Fatalf("changed-slot rows=%d, want=%d (ticket and every charged material)", len(rows), wantRows)
			}
			if testCase.slot == 20 {
				assertCurrentItemListTestRow(t, rows[0], 20, uint32(testCase.itemID), 1)
			} else {
				assertCurrentItemListTestRow(t, rows[0], uint16(testCase.slot), ^uint32(0), 0)
			}
			if testCase.material {
				assertCurrentItemListTestRow(t, rows[1], 30, ^uint32(0), 0)
			}
		})
	}
}

func TestRealPVFDimensionGateExtraTicketNativeRejectedWithoutAssets(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the a56 Script.pvf")
	}
	service, repos, session, conn := newDimensionExtraNativeFixture(t, path)
	ctx := context.Background()
	baseCharacter, _, _ := repos.Character.Load(ctx, "19")
	baseInventory, _, _ := repos.Inventory.Load(ctx, "19")
	for _, name := range []string{"short", "trailing", "wrong_list", "negative_slot", "reserved0", "reserved1", "reserved2", "foreign_account", "under_level", "expired_stack", "wrong_item", "material_short", "not_ready"} {
		t.Run(name, func(t *testing.T) {
			character := dnfrepo.CloneCharacter(baseCharacter)
			inventory := dnfrepo.CloneInventory(baseInventory)
			body := dimensionExtraNativeRequest(22)
			session.accountID = "native-extra-account"
			bindDungeonSelectorOriginForTestAt(t, service, session, 107, 0, 250, 224)
			switch name {
			case "short":
				body = body[:11]
			case "trailing":
				body = append(body, 0)
			case "wrong_list":
				body[2] = 1
			case "negative_slot":
				binary.LittleEndian.PutUint16(body[:2], 0xffff)
			case "reserved0":
				body[3] = 1
			case "reserved1":
				body[11] = 1
			case "reserved2":
				body[15] = 1
			case "foreign_account":
				session.accountID = "another-account"
			case "under_level":
				character.Level = 69
			case "expired_stack":
				stack := inventory.Slots["0:22"]
				stack.ExpireAt = service.gameplayNow().Add(-time.Second)
				inventory.Slots["0:22"] = stack
			case "wrong_item":
				inventory.Slots["0:22"] = dnfrepo.ItemStack{ItemID: 3228, Count: 60}
			case "material_short":
				stack := inventory.Slots["0:30"]
				stack.Count = 59
				inventory.Slots["0:30"] = stack
			case "not_ready":
				session.townSceneReadyCharacterID = 0
			}
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, inventory); err != nil {
				t.Fatal(err)
			}
			conn.write.Reset()
			if handled, err := service.handleCurrentDimensionGateExtraTicketUse(session, body); err != nil || !handled {
				t.Fatalf("handled=%t err=%v", handled, err)
			}
			afterCharacter, _, _ := repos.Character.Load(ctx, "19")
			afterInventory, _, _ := repos.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(character, afterCharacter) || !reflect.DeepEqual(inventory, afterInventory) {
				t.Fatal("rejected native request changed character or inventory")
			}
			packet, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
			if packet.Header.MsgID != 515 || packet.Header.Classification != 1 || len(rest) != 0 || len(packet.Body) != 6 || packet.Body[0] != 0 || packet.Body[5] != 156 {
				t.Fatalf("failure packet=%+v rest=%x", packet, rest)
			}
		})
	}
}

func TestRealPVFDimensionGateExtraTicketReplayAndForeignAction(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the a56 Script.pvf")
	}
	service, repos, session, _ := newDimensionExtraNativeFixture(t, path)
	body := dimensionExtraNativeRequest(20)
	if handled, err := service.handleCurrentDimensionGateExtraTicketUse(session, body); err != nil || !handled {
		t.Fatal(handled, err)
	}
	before, _, err := repos.Character.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	if handled, err := service.handleCurrentDimensionGateExtraTicketUse(session, body); err != nil || !handled {
		t.Fatalf("replay handled=%t err=%v", handled, err)
	}
	after, _, err := repos.Character.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	_, beforeGranted, _, err := dnfdungeon.DimensionGateDailyAllowance(before, service.gameplayNow(), 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	_, afterGranted, _, err := dnfdungeon.DimensionGateDailyAllowance(after, service.gameplayNow(), 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if afterGranted != beforeGranted {
		t.Fatal("cooldown replay changed the persisted allowance")
	}
	foreign := dimensionExtraNativeRequest(20)
	binary.LittleEndian.PutUint32(foreign[7:11], currentDimensionGateExtraTicketAction+1)
	if handled, err := service.handleCurrentDimensionGateExtraTicketUse(session, foreign); handled || err != nil {
		t.Fatalf("foreign action handled=%t err=%v", handled, err)
	}
}
