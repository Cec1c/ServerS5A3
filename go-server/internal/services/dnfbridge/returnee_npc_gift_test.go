package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfonlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestPortedNPCGiftRealPVFSeriaAndWire(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, session, repos := newTownMoveTest(t)
	logPath := filepath.Join(t.TempDir(), "gift.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	s.packetLog = logger
	t.Cleanup(func() {
		_ = logger.Close()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			t.Logf("gift log:\n%s", data)
		}
	})
	s.initialEquipmentArchive = archive
	s.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	s.townCatalog, err = dnftown.Load(ctx, archive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.returneeCatalog = &dnfonlineevent.ReturneeCatalog{}
	s.luckyCharacterCatalog = &currentLuckyCatalog{}
	ch, _, _ := repos.Character.Load(ctx, "29")
	// newTownMoveTest swaps the character store for scene tests; use one
	// consistent memory group for the account/character asset transaction.
	repos = memory.NewMemoryGroup()
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	session.accountID = ch.AccountID
	ch.Level = 37
	if err = repos.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if err = repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: ch.AccountID}); err != nil {
		t.Fatal(err)
	}
	session.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 29, TownID: 38, AreaID: 1, PositionX: 450, PositionY: 310, PositionValid: true}
	if !s.currentTownHasGiftNPC(session.townPositionSnapshot, 2) {
		area, ok := s.townArea(38, 1)
		t.Fatalf("Seria absent from actual current area: found=%t area=%+v", ok, area)
	}

	if s.currentTownHasGiftNPC(session.townPositionSnapshot, 999999) {
		t.Fatal("accepted NPC outside area")
	}
	if err = repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: ch.AccountID, Slots: map[string]dnfrepo.ItemStack{"0:357": {ItemID: 3035, Count: 600}, "0:358": {ItemID: 3036, Count: 600}}}); err != nil {
		t.Fatal(err)
	}
	conn := session.conn.(*bufferConn)
	for _, slot := range []uint16{357, 358} {
		conn.write.Reset()
		body := []byte{0, 2, 0, 0, 0, 36, 0, 0}
		binary.LittleEndian.PutUint16(body[6:], slot)
		dispatchReturneeNPCGift(t, s, session, body)
		update, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if update.Header.MsgID != 14 {
			t.Fatalf("expected item update, got %+v body=%x", update.Header, update.Body)
		}
		ack, _ := splitGameServerUpperPacket(t, rest)
		if ack.Header.MsgID != 812 || ack.Header.Classification != 1 || len(ack.Body) != 13 || ack.Body[0] != 1 {
			t.Fatalf("gift ACK=%+v body=%x", ack.Header, ack.Body)
		}
		gain := binary.LittleEndian.Uint16(ack.Body[6:])
		if gain < 200 || gain > 600 {
			t.Fatalf("affection gain=%d (must not echo 100 consumed cubes)", gain)
		}
	}
	stored, _, _ := repos.AccountInventory.Load(ctx, ch.AccountID)
	if stored.Slots["0:357"].Count != 500 || stored.Slots["0:358"].Count != 500 {
		t.Fatalf("cube balances=%v", stored.Slots)
	}
	ch, _, _ = repos.Character.Load(ctx, "29")
	if ch.Stats["npc_favor_2_gifts"] != 2 {
		t.Fatal("daily usage not committed")
	}
	for _, tc := range []struct {
		container byte
		slot      uint16
		item      uint32
		wantItem  uint32
	}{
		{36, 357, 3035, 3035},     // the crystal warehouse retains its fixed slot
		{0, 61, 3033, 0xFFFFFFFF}, // an exhausted backpack slot is removed
	} {
		stack := dnfrepo.ItemStack{ItemID: int64(tc.item), Count: 100, Extra: map[string]string{"amount": "100"}}
		if tc.container == 36 {
			stored.Slots["0:357"] = stack
			err = repos.AccountInventory.Save(ctx, stored)
		} else {
			err = repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: ch.CharacterID, Slots: map[string]dnfrepo.ItemStack{"0:61": stack}})
		}
		if err != nil {
			t.Fatal(err)
		}
		conn.write.Reset()
		body := []byte{0, 2, 0, 0, 0, tc.container, 0, 0}
		binary.LittleEndian.PutUint16(body[6:], tc.slot)
		dispatchReturneeNPCGift(t, s, session, body)
		update, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if update.Header.MsgID != 14 || len(update.Body) < 13 || binary.LittleEndian.Uint32(update.Body[5:]) != tc.wantItem || binary.LittleEndian.Uint32(update.Body[9:]) != 0 {
			t.Fatalf("exhausted slot update=%x", update.Body)
		}
		ack, _ := splitGameServerUpperPacket(t, rest)
		if ack.Header.MsgID != 812 || ack.Body[0] != 1 {
			t.Fatalf("gift ACK=%x", ack.Body)
		}
	}
	ch, _, _ = repos.Character.Load(ctx, "29")
	for _, tc := range []struct {
		name  string
		npc   uint32
		level int
		used  int64
		count int64
		code  byte
	}{
		{"unknown NPC", 999999, 37, 4, 500, 100},
		{"level", 2, 19, 4, 500, 14},
		{"count", 2, 37, 4, 99, 22},
		{"daily limit", 2, 37, 5, 500, 102},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch.Level = tc.level
			ch.Stats["npc_favor_2_gifts"] = tc.used
			if err = repos.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
			stored.Slots["0:358"] = dnfrepo.ItemStack{ItemID: 3036, Count: tc.count}
			if err = repos.AccountInventory.Save(ctx, stored); err != nil {
				t.Fatal(err)
			}
			conn.write.Reset()
			body := []byte{0, 0, 0, 0, 0, 36, 102, 1}
			binary.LittleEndian.PutUint32(body[1:], tc.npc)
			dispatchReturneeNPCGift(t, s, session, body)
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 812 || len(ack.Body) != 2 || ack.Body[0] != 0 || ack.Body[1] != tc.code || len(rest) != 0 {
				t.Fatalf("failure ACK=%x extra=%x", ack.Body, rest)
			}
			balance, _, _ := repos.AccountInventory.Load(ctx, ch.AccountID)
			if balance.Slots["0:358"].Count != tc.count {
				t.Fatal("failed gift consumed cubes")
			}
		})
	}
}

func TestReturneeNPCGiftDispatchSelectedAndUnselected(t *testing.T) {
	const messageID uint16 = 812
	body := []byte{0, 2, 0, 0, 0, 36, 0, 0}
	for _, tc := range []struct {
		name     string
		selected uint16
		wantCode byte
	}{
		{name: "selected reaches gift handler", selected: 29, wantCode: 100},
		{name: "unselected rejected before assets", selected: 0, wantCode: 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, session, repositories := newTownMoveTest(t)
			seedReturneeNPCGiftDispatchAssets(t, repositories)
			session.selectedCharacterID = tc.selected
			session.townSceneReadyCharacterID = tc.selected
			if tc.selected != 0 {
				session.townPositionSnapshot = currentTownPositionSnapshot{
					CharacterID: tc.selected, TownID: 38, AreaID: 1,
					PositionX: 450, PositionY: 234, PositionValid: true,
				}
			}

			conn := session.conn.(*bufferConn)
			conn.write.Reset()
			handled, err := service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), messageID, body)
			if err != nil || !handled {
				t.Fatalf("handleAlignedGameCommand handled=%t err=%v", handled, err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != messageID || ack.Header.Classification != 1 || len(ack.Body) != 2 || ack.Body[0] != 0 || ack.Body[1] != tc.wantCode || len(rest) != 0 {
				t.Fatalf("failure ACK=%+v body=%x rest=%x", ack.Header, ack.Body, rest)
			}
			assertReturneeNPCGiftDispatchAssetsUnchanged(t, repositories)
		})
	}
}

func TestReturneeNPCGiftDispatchRequiresCommand1AndType812(t *testing.T) {
	const messageID uint16 = 812
	body := []byte{0, 2, 0, 0, 0, 36, 0, 0}
	for _, tc := range []struct {
		name string
		cmd  byte
		typ  uint16
	}{
		{name: "notice command same type", cmd: byte(dnfenum.GameCmdNotice), typ: messageID},
		{name: "other command class same type", cmd: 2, typ: messageID},
		{name: "command other type", cmd: byte(dnfenum.GameCmdCommand), typ: 9999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, session, repositories := newTownMoveTest(t)
			seedReturneeNPCGiftDispatchAssets(t, repositories)
			session.townPositionSnapshot = currentTownPositionSnapshot{
				CharacterID: 29, TownID: 38, AreaID: 1,
				PositionX: 450, PositionY: 234, PositionValid: true,
			}

			conn := session.conn.(*bufferConn)
			conn.write.Reset()
			handled, err := service.handleAlignedGameCommand(session, tc.cmd, tc.typ, body)
			if err != nil || handled {
				t.Fatalf("handleAlignedGameCommand handled=%t err=%v", handled, err)
			}
			if conn.write.Len() != 0 {
				t.Fatalf("nonmatching command wrote %d bytes", conn.write.Len())
			}
			assertReturneeNPCGiftDispatchAssetsUnchanged(t, repositories)
		})
	}
}

func dispatchReturneeNPCGift(t *testing.T, service *Service, session *gameSession, body []byte) {
	t.Helper()
	handled, err := service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), 812, body)
	if err != nil || !handled {
		t.Fatalf("returnee NPC gift handled=%t err=%v", handled, err)
	}
}

func seedReturneeNPCGiftDispatchAssets(t *testing.T, repositories dnfrepo.Group) {
	t.Helper()
	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Stats["npc_favor_2_gifts"] = 0
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.AccountInventory.Save(context.Background(), dnfrepo.AccountInventoryRecord{
		AccountID: character.AccountID,
		Slots: map[string]dnfrepo.ItemStack{
			"0:0": {ItemID: 3036, Count: 600},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func assertReturneeNPCGiftDispatchAssetsUnchanged(t *testing.T, repositories dnfrepo.Group) {
	t.Helper()
	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found || character.Stats["npc_favor_2_gifts"] != 0 {
		t.Fatalf("character mutated found=%t err=%v stats=%+v", found, err, character.Stats)
	}
	inventory, found, err := repositories.AccountInventory.Load(context.Background(), character.AccountID)
	if err != nil || !found || inventory.Slots["0:0"].Count != 600 {
		t.Fatalf("inventory mutated found=%t err=%v slots=%+v", found, err, inventory.Slots)
	}
}
