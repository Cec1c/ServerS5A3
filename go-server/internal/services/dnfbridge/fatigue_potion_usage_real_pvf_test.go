package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	dnffatigue "longheng.io/server/internal/modules/dnf/fatigue"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFFatiguePotion416RemainingCountLifecycle(t *testing.T) {
	testRealPVFFatiguePotionRemainingCountLifecycle(t, 416, 50)
}
func TestRealPVFFatiguePotionOnePointRemainingCountLifecycle(t *testing.T) {
	testRealPVFFatiguePotionRemainingCountLifecycle(t, 2683254, 1)
}
func testRealPVFFatiguePotionRemainingCountLifecycle(t *testing.T, itemID uint32, points int64) {
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
	item, err := catalog.ResolveItem(itemID)
	if err != nil {
		t.Fatal(err)
	}
	text, err := archive.ReadText(item.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse(item.PVFPath, text)
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := fatiguePotionDefinition(int64(itemID), item.PVFPath, doc)
	if !ok || definition.DailyLimit != 3 || definition.Points != points {
		t.Fatalf("unexpected item416 definition: %+v", definition)
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	account := dnfrepo.AccountRecord{AccountID: "fatigue-416"}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	zone := time.FixedZone("UTC+8", 8*3600)
	queue.now = time.Date(2026, 10, 22, 12, 0, 0, 0, zone)
	character := dnfrepo.CharacterRecord{CharacterID: "17", AccountID: account.AccountID, Level: 90, Stats: map[string]int64{"fatigue": 0, "fatigue_limit": 156}}
	dnffatigue.Refresh(&character, account, queue.now)
	for _, saveErr := range []error{
		repos.Account.Save(ctx, account),
		repos.Character.Save(ctx, character),
		repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "17", Slots: map[string]dnfrepo.ItemStack{
			"0:87": {ItemID: int64(itemID), Count: 4, RawEntry: make([]byte, currentItemListEntryWireSize), Extra: map[string]string{"item_kind": "stackable", "pvf_path": item.PVFPath}},
		}}),
	} {
		if saveErr != nil {
			t.Fatal(saveErr)
		}
	}
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}, initialEquipmentArchive: archive, pvfItemCatalog: catalog, gameplayTimers: queue, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "fatigue416", selectedCharacterID: 17, characterGeneration: 1, accountID: account.AccountID}
	checkUsage := func(data []byte, want byte) {
		t.Helper()
		packet, rest := splitGameServerUpperPacket(t, data)
		if packet.Header.MsgID != 541 || packet.Header.Classification != 0 || len(rest) != 0 || len(packet.Body) != 7 || packet.Body[0] != 1 || binary.LittleEndian.Uint32(packet.Body[1:5]) != itemID || packet.Body[5] != want || packet.Body[6] != 0 {
			t.Fatalf("usage header=%+v body=%x trailing=%d want used=%d", packet.Header, packet.Body, len(rest), want)
		}
	}
	for used := byte(1); used <= 3; used++ {
		conn.write.Reset()
		queue.now = queue.now.Add(2 * time.Second)
		handled, err := service.handleCurrentFatiguePotionUse(session, testQuestClearCouponRequestBody(87, 58))
		if !handled || err != nil {
			t.Fatalf("use%d handled=%v err=%v", used, handled, err)
		}
		data := conn.write.Bytes()
		for _, op := range []uint16{515, 14, currentFatigueMsgID} {
			packet, rest := splitGameServerUpperPacket(t, data)
			if packet.Header.MsgID != op {
				t.Fatalf("use%d got op%d want%d", used, packet.Header.MsgID, op)
			}
			data = rest
		}
		checkUsage(data, used)
	}
	conn.write.Reset()
	queue.now = queue.now.Add(2 * time.Second)
	if handled, err := service.handleCurrentFatiguePotionUse(session, testQuestClearCouponRequestBody(87, 58)); !handled || err != nil {
		t.Fatalf("fourth use handled=%v err=%v", handled, err)
	}
	rejected, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if rejected.Header.MsgID != 515 || rejected.Body[0] != 0 || len(rest) != 0 {
		t.Fatalf("fourth use not rejected: %+v body=%x rest=%d", rejected.Header, rejected.Body, len(rest))
	}
	stored, _, err := repos.Character.Load(ctx, "17")
	if err != nil || stored.Stats["fatigue_potion_used_"+definition.UsageGroup()] != 3 {
		t.Fatalf("durable count=%v err=%v", stored.Stats, err)
	}
	bag, _, err := repos.Inventory.Load(ctx, "17")
	if err != nil || bag.Slots["0:87"].Count != 1 {
		t.Fatalf("remaining stock=%d err=%v", bag.Slots["0:87"].Count, err)
	}
	// A replacement session reconstructs the same count from persisted state.
	conn.write.Reset()
	replacement := &gameSession{conn: conn, connID: "fatigue416-reload", selectedCharacterID: 17, characterGeneration: 2, accountID: account.AccountID}
	if err := service.sendCurrentFatiguePotionUsageForCharacter(ctx, replacement, stored, "test_reload"); err != nil {
		t.Fatal(err)
	}
	checkUsage(conn.write.Bytes(), 3)
	// The existing online service-day callback sends the zero-use projection.
	queue.now = time.Date(2026, 10, 23, 5, 59, 59, 0, zone)
	service.armCurrentFatigueDailyRefresh(replacement)
	task := queue.task(t, 0)
	conn.write.Reset()
	queue.now = queue.now.Add(time.Second)
	task.callback(queue.Now())
	fatigue, data := splitGameServerUpperPacket(t, conn.write.Bytes())
	if fatigue.Header.MsgID != currentFatigueMsgID {
		t.Fatalf("reset op=%d", fatigue.Header.MsgID)
	}
	checkUsage(data, 0)
	t.Logf("real item%d: three uses -> used1/2/3, fourth rejected without debit, reload3, six-AM reset0", itemID)
}
