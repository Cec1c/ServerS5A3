package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnffatigue "longheng.io/server/internal/modules/dnf/fatigue"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFFatiguePotionAckPreservesRequestAction(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires real PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	const itemID = 10015080
	item, err := catalog.ResolveItem(itemID)
	if err != nil {
		t.Fatal(err)
	}
	text, err := archive.ReadText(item.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	document, err := dnfpvf.Parse(item.PVFPath, text)
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := fatiguePotionDefinition(itemID, item.PVFPath, document)
	if !ok {
		t.Fatal("real item is not a fatigue potion")
	}
	for _, count := range []int64{1, 3} {
		t.Run(strconv.FormatInt(count, 10), func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			account := dnfrepo.AccountRecord{AccountID: "fatigue-ack"}
			queue := newFakeCurrentDungeonDeathTimerQueue()
			queue.now = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
			character := dnfrepo.CharacterRecord{CharacterID: "17", AccountID: account.AccountID, Level: 90, Stats: map[string]int64{"fatigue": 0, "fatigue_limit": 156}}
			dnffatigue.Refresh(&character, account, queue.now)
			if err := repos.Account.Save(ctx, account); err != nil {
				t.Fatal(err)
			}
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "17", Slots: map[string]dnfrepo.ItemStack{
				"0:87": {ItemID: itemID, Count: count, RawEntry: make([]byte, currentItemListEntryWireSize), Extra: map[string]string{"item_kind": "stackable", "pvf_path": item.PVFPath}},
			}}); err != nil {
				t.Fatal(err)
			}
			service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}, initialEquipmentArchive: archive, pvfItemCatalog: catalog, gameplayTimers: queue, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			session := &gameSession{conn: &bufferConn{}, connID: "fatigue-ack", selectedCharacterID: 17, accountID: account.AccountID}
			const action = currentFatiguePotionAction
			handled, err := service.handleCurrentFatiguePotionUse(session, testQuestClearCouponRequestBody(87, action))
			if err != nil || !handled {
				t.Fatalf("handled=%t err=%v", handled, err)
			}
			ack, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
			if ack.Header.MsgID != uint16(dnfenum.CmdPacketUseStackableAction) || ack.Header.Classification != 1 || len(ack.Body) != 8 || ack.Body[0] != 1 || binary.LittleEndian.Uint16(ack.Body[1:3]) != 87 || ack.Body[3] != 0 || binary.LittleEndian.Uint32(ack.Body[4:8]) != action {
				t.Fatalf("fatigue ACK header=%+v body=%x; expected native action %d", ack.Header, ack.Body, action)
			}
			refresh, rest := splitGameServerUpperPacket(t, rest)
			if refresh.Header.MsgID != 14 || refresh.Header.Classification != 0 {
				t.Fatalf("missing authoritative slot refresh: %+v", refresh.Header)
			}
			fatigue, rest := splitGameServerUpperPacket(t, rest)
			if fatigue.Header.MsgID != currentFatigueMsgID || fatigue.Header.Classification != 0 {
				t.Fatalf("fatigue projection/order: %+v trailing=%d", fatigue.Header, len(rest))
			}
			usage, rest := splitGameServerUpperPacket(t, rest)
			if usage.Header.MsgID != currentFatiguePotionUsageMsgID || usage.Header.Classification != 0 || len(rest) != 0 {
				t.Fatalf("usage projection/order: %+v trailing=%d", usage.Header, len(rest))
			}
			usageItemID := uint32(itemID)
			if want := []byte{1, byte(usageItemID), byte(usageItemID >> 8), byte(usageItemID >> 16), byte(usageItemID >> 24), 1, 0}; !bytes.Equal(usage.Body, want) {
				t.Fatalf("usage body=%x want=%x", usage.Body, want)
			}
			stored, _, err := repos.Character.Load(ctx, "17")
			if err != nil || stored.Stats["fatigue"] != definition.Points {
				t.Fatalf("fatigue=%d want=%d err=%v", stored.Stats["fatigue"], definition.Points, err)
			}
			bag, _, err := repos.Inventory.Load(ctx, "17")
			if err != nil || bag.Slots["0:87"].Count != count-1 {
				t.Fatalf("remaining=%d want=%d err=%v", bag.Slots["0:87"].Count, count-1, err)
			}
		})
	}
}

func TestFatiguePotionRejectsNonNativeActionBeforeMutation(t *testing.T) {
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	session := &gameSession{conn: &bufferConn{}, connID: "fatigue-action", selectedCharacterID: 17}
	handled, err := service.handleCurrentFatiguePotionUse(session, testQuestClearCouponRequestBody(87, 0x12345678))
	if err != nil || handled {
		t.Fatalf("non-native action handled=%t err=%v", handled, err)
	}
	if got := len(session.conn.(*bufferConn).write.Bytes()); got != 0 {
		t.Fatalf("non-native action wrote %d bytes", got)
	}
}

func TestFatiguePotionInvalidRequestUsesActionBearingFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]byte)
	}{
		{"reserved_3", func(body []byte) { body[3] = 1 }},
		{"reserved_11", func(body []byte) { body[11] = 1 }},
		{"reserved_15", func(body []byte) { body[15] = 1 }},
		{"list_type", func(body []byte) { body[2] = 1 }},
		{"missing_character", func([]byte) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
			session := &gameSession{conn: &bufferConn{}, connID: "fatigue-invalid", selectedCharacterID: 17}
			if tc.name == "missing_character" {
				session.selectedCharacterID = 0
			}
			body := testQuestClearCouponRequestBody(87, currentFatiguePotionAction)
			tc.mutate(body)
			handled, err := service.handleCurrentFatiguePotionUse(session, body)
			if err != nil || !handled {
				t.Fatalf("handled=%t err=%v", handled, err)
			}
			ack, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
			if len(rest) != 0 {
				t.Fatalf("unexpected trailing packets: %d", len(rest))
			}
			want := []byte{0, 1, 0x57, 0, body[2], byte(currentFatiguePotionAction)}
			if ack.Header.MsgID != uint16(dnfenum.CmdPacketUseStackableAction) || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, want) {
				t.Fatalf("failure header=%+v body=%x want=%x", ack.Header, ack.Body, want)
			}
		})
	}
}
