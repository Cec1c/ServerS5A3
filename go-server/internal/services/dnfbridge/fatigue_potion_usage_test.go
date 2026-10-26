package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	dnffatigue "longheng.io/server/internal/modules/dnf/fatigue"
	dnfpremium "longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type fatiguePotionUsageTestCatalog map[uint32]dungeonDropItemDefinition

func buildCurrentFatiguePotionUsageBody(character dnfrepo.CharacterRecord, inventory dnfrepo.InventoryRecord, now time.Time, catalog fatiguePotionUsageCatalog, source fatiguePotionUsageSource) ([]byte, bool) {
	bodies, ok := buildCurrentFatiguePotionUsageBodies(character, inventory, now, catalog, source)
	if !ok || len(bodies) != 1 {
		return nil, false
	}
	return bodies[0], true
}

type fatiguePotionUsageFailConn struct{ bufferConn }

func (*fatiguePotionUsageFailConn) Write([]byte) (int, error) {
	return 0, errors.New("fatigue usage write failed")
}

func (c fatiguePotionUsageTestCatalog) ResolveItem(itemID uint32) (dungeonDropItemDefinition, error) {
	definition, found := c[itemID]
	if !found {
		return dungeonDropItemDefinition{}, errors.New("unknown test item")
	}
	return definition, nil
}

func fatiguePotionUsageTestDefinitions(ids ...uint32) (fatiguePotionUsageTestCatalog, dungeonDropCatalogTestSource) {
	catalog := make(fatiguePotionUsageTestCatalog, len(ids))
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
	}
	var lines []string
	for _, id := range ids {
		path := "stackable/test/" + strconv.FormatUint(uint64(id), 10) + ".stk"
		catalog[id] = dungeonDropItemDefinition{ItemID: id, Kind: dungeonDropItemStackable, PVFPath: path}
		source[path] = "[action type]\n`[add fatigue]` 50\n[/action type]\n[total usable count]\n3\n[cool time]\n1000\n"
		lines = append(lines, strconv.FormatUint(uint64(id), 10)+" `test/"+strconv.FormatUint(uint64(id), 10)+".stk`")
	}
	source["stackable/stackable.lst"] = strings.Join(lines, "\n") + "\n"
	return catalog, source
}

func fatiguePotionUsageTestAddSharedGroup(catalog fatiguePotionUsageTestCatalog, source dungeonDropCatalogTestSource, id uint32, group string) {
	definition := catalog[id]
	source[definition.PVFPath] += "[cooltime group]\n`" + group + "`\n"
}

func fatiguePotionUsageTestPlayer(now time.Time, stats map[string]int64, slots map[string]dnfrepo.ItemStack) (dnfrepo.CharacterRecord, *dnfrepo.InventoryRecord) {
	day := dnfpremium.ServiceDayID(now)
	if stats == nil {
		stats = make(map[string]int64)
	}
	stats[dnffatigue.DayKey] = day
	character := dnfrepo.CharacterRecord{CharacterID: "17", AccountID: "a", Level: 90, Stats: stats}
	inventory := &dnfrepo.InventoryRecord{CharacterID: "17", Slots: slots}
	return character, inventory
}

func TestCurrentFatiguePotionUsageUsesPersistedGroupCounts(t *testing.T) {
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	day := dnfpremium.ServiceDayID(now)
	catalog, source := fatiguePotionUsageTestDefinitions(416, 490010384, 601, 777)
	fatiguePotionUsageTestAddSharedGroup(catalog, source, 490010384, "shared")
	fatiguePotionUsageTestAddSharedGroup(catalog, source, 601, "900")
	source[catalog[777].PVFPath] = "[stackable type]\n`[material]`\n"
	character, inventory := fatiguePotionUsageTestPlayer(now, map[string]int64{
		"fatigue_potion_item_416":       1,
		"fatigue_potion_day_416":        day,
		"fatigue_potion_used_416":       2,
		"fatigue_potion_item_490010384": 1,
		"fatigue_potion_day_shared":     day,
		"fatigue_potion_used_shared":    2,
		"fatigue_potion_item_601":       1,
		"fatigue_potion_day_900":        day,
		"fatigue_potion_used_900":       1,
		"fatigue_potion_item_999":       1,
		"fatigue_potion_item_777":       1,
	}, map[string]dnfrepo.ItemStack{
		"0:1": {ItemID: 416, Count: 99},
		"0:2": {ItemID: 490010384, Count: 0},
		"0:3": {ItemID: 601, Count: 1},
		"0:4": {ItemID: 777, Count: 1},
	})

	body, ok := buildCurrentFatiguePotionUsageBody(character, *inventory, now, catalog, source)
	if !ok {
		t.Fatal("known persisted potion history was not projected")
	}
	want := []byte{
		3,
		0xa0, 0x01, 0x00, 0x00, 2,
		0x59, 0x02, 0x00, 0x00, 1,
		0x10, 0xf7, 0x34, 0x1d, 2,
		0,
	}
	if string(body) != string(want) {
		t.Fatalf("541 body=%x want=%x", body, want)
	}
	if got := binary.LittleEndian.Uint32(body[1:5]); got != 416 {
		t.Fatalf("first deterministic row=%d", got)
	}
	if got := body[5]; got != 2 {
		t.Fatalf("packet projected remaining stock instead of used count: %d", got)
	}
}

func TestCurrentFatiguePotionUsageClearsStaleDayAndFiltersUnknownDefinitions(t *testing.T) {
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	staleDay := dnfpremium.ServiceDayID(now.Add(-24 * time.Hour))
	catalog, source := fatiguePotionUsageTestDefinitions(416, 500, 501)
	catalog[500] = dungeonDropItemDefinition{ItemID: 500, Kind: dungeonDropItemStackable, PVFPath: catalog[500].PVFPath}
	source[catalog[500].PVFPath] = "[action type]\n`[add fatigue]` 50\n[/action type]\n[total usable count]\n0\n"
	catalog[501] = dungeonDropItemDefinition{ItemID: 501, Kind: dungeonDropItemStackable, PVFPath: catalog[501].PVFPath}
	source[catalog[501].PVFPath] = "[stackable type]\n`[material]`\n"
	character, inventory := fatiguePotionUsageTestPlayer(now, map[string]int64{
		"fatigue_potion_item_416": 1,
		"fatigue_potion_day_416":  staleDay,
		"fatigue_potion_used_416": 3,
		"fatigue_potion_item_500": 1,
		"fatigue_potion_item_501": 1,
		"fatigue_potion_item_502": 1,
	}, map[string]dnfrepo.ItemStack{
		"0:1": {ItemID: 500, Count: 1},
		"0:2": {ItemID: 501, Count: 1},
		"0:3": {ItemID: 502, Count: 1},
	})
	body, ok := buildCurrentFatiguePotionUsageBody(character, *inventory, now, catalog, source)
	if !ok {
		t.Fatal("stale-day row should reset to zero")
	}
	want := []byte{1, 0xa0, 0x01, 0, 0, 0, 0}
	if string(body) != string(want) {
		t.Fatalf("filtered body=%x want=%x", body, want)
	}
}

func TestCurrentFatiguePotionUsageDoesNotInventHistoryFromInventory(t *testing.T) {
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	catalog, source := fatiguePotionUsageTestDefinitions(416)
	character, inventory := fatiguePotionUsageTestPlayer(now, nil, map[string]dnfrepo.ItemStack{
		"0:1": {ItemID: 416, Count: 3},
	})
	if body, ok := buildCurrentFatiguePotionUsageBody(character, *inventory, now, catalog, source); ok || len(body) != 0 {
		t.Fatalf("unused inventory invented history: ok=%t body=%x", ok, body)
	}
}

func TestCurrentFatiguePotionUsageSendsClass0Op541(t *testing.T) {
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	_, source := fatiguePotionUsageTestDefinitions(416)
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = now
	character, inventory := fatiguePotionUsageTestPlayer(now, map[string]int64{
		"fatigue_potion_item_416": 1,
		"fatigue_potion_day_416":  dnfpremium.ServiceDayID(now),
		"fatigue_potion_used_416": 2,
	}, nil)
	service := &Service{
		options:        options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		pvfItemCatalog: catalog,
		gameplayTimers: queue,
	}
	session := &gameSession{conn: &bufferConn{}, connID: "fatigue-usage"}
	if err := service.sendCurrentFatiguePotionUsage(context.Background(), session, character, *inventory, "test"); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	if len(rest) != 0 || packet.Header.MsgID != currentFatiguePotionUsageMsgID || packet.Header.Classification != 0 {
		t.Fatalf("541 header=%+v rest=%d", packet.Header, len(rest))
	}
	want := []byte{1, 0xa0, 0x01, 0, 0, 2, 0}
	if string(packet.Body) != string(want) {
		t.Fatalf("541 body=%x want=%x", packet.Body, want)
	}

	noHistory := character
	noHistory.Stats = map[string]int64{}
	session.conn.(*bufferConn).write.Reset()
	if err := service.sendCurrentFatiguePotionUsage(context.Background(), session, noHistory, *inventory, "test"); err != nil {
		t.Fatal(err)
	}
	if got := session.conn.(*bufferConn).write.Len(); got != 0 {
		t.Fatalf("no-history character wrote %d bytes", got)
	}
}

func TestCurrentFatiguePotionUsageSelectionRestoresAfterAck(t *testing.T) {
	service, repos := newAccountRecordBootstrapFixture(t)
	service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	_, source := fatiguePotionUsageTestDefinitions(416)
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	service.pvfItemCatalog = catalog
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	service.gameplayTimers = queue
	ctx := context.Background()
	character, _, err := repos.Character.Load(ctx, "10")
	if err != nil {
		t.Fatal(err)
	}
	character.Stats["fatigue_potion_item_416"] = 1
	character.Stats[dnffatigue.DayKey] = dnfpremium.ServiceDayID(queue.Now())
	character.Stats["fatigue"] = 100
	character.Stats["fatigue_limit"] = 156
	character.Stats["fatigue_potion_day_416"] = dnfpremium.ServiceDayID(queue.Now())
	character.Stats["fatigue_potion_used_416"] = 2
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	// Inventory is empty: the last consumed bottle must still restore on login.
	for attempt := 0; attempt < 2; attempt++ {
		session := newAccountRecordBootstrapSession()
		if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
			t.Fatal(err)
		}
		packets := splitAllCurrentGameServerUpperPackets(t, session.conn.(*bufferConn).write.Bytes())
		ack, usage := -1, -1
		for index, packet := range packets {
			if packet.Header.MsgID == 4 && packet.Header.Classification == 1 {
				ack = index
			}
			if packet.Header.MsgID == 541 && packet.Header.Classification == 0 {
				if usage != -1 || !reflect.DeepEqual(packet.Body, []byte{1, 0xa0, 1, 0, 0, 2, 0}) {
					t.Fatalf("selection usage=%x duplicate=%t", packet.Body, usage != -1)
				}
				usage = index
			}
		}
		if ack < 0 || usage != ack+1 {
			t.Fatalf("attempt%d ack=%d usage=%d", attempt, ack, usage)
		}
	}
}

func TestCurrentFatiguePotionUsageUnavailableCatalogKeepsSelectionRecoverable(t *testing.T) {
	character := dnfrepo.CharacterRecord{CharacterID: "10", Stats: map[string]int64{"fatigue_potion_used_416": 1}}
	service := &Service{pvfItemCatalogLoadErr: errors.New("catalog unavailable")}
	session := &gameSession{conn: &bufferConn{}}
	if err := service.sendCurrentFatiguePotionUsage(context.Background(), session, character, dnfrepo.InventoryRecord{}, "select_character_after_ack"); err != nil {
		t.Fatal(err)
	}
	if session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("unavailable catalog emitted invented counts")
	}
}

func TestCurrentFatiguePotionUsageWriteFailurePreservesCommittedState(t *testing.T) {
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	_, source := fatiguePotionUsageTestDefinitions(416)
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	account := dnfrepo.AccountRecord{AccountID: "a"}
	character, inventory := fatiguePotionUsageTestPlayer(now, map[string]int64{
		"fatigue_potion_item_416": 1,
		"fatigue_potion_day_416":  dnfpremium.ServiceDayID(now),
		"fatigue_potion_used_416": 1,
	}, nil)
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, *inventory); err != nil {
		t.Fatal(err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = now
	service := &Service{
		options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		pvfItemCatalog:     catalog,
		gameplayTimers:     queue,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
	}
	failed := &gameSession{conn: &fatiguePotionUsageFailConn{}, connID: "fatigue-usage-failed"}
	if err := service.sendCurrentFatiguePotionUsageForCharacter(ctx, failed, character, "test"); err == nil {
		t.Fatal("failed usage send returned nil")
	}
	stored, found, err := repos.Character.Load(ctx, "17")
	if err != nil || !found || !reflect.DeepEqual(stored, character) {
		t.Fatalf("failed usage send changed committed character: found=%t err=%v stats=%v", found, err, stored.Stats)
	}

	replacement := &gameSession{conn: &bufferConn{}, connID: "fatigue-usage-retry"}
	if err := service.sendCurrentFatiguePotionUsageForCharacter(ctx, replacement, stored, "test"); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, replacement.conn.(*bufferConn).write.Bytes())
	if len(rest) != 0 || packet.Header.MsgID != currentFatiguePotionUsageMsgID || !reflect.DeepEqual(packet.Body, []byte{1, 0xa0, 1, 0, 0, 1, 0}) {
		t.Fatalf("retry header=%+v body=%x rest=%d", packet.Header, packet.Body, len(rest))
	}
}

func TestCurrentFatiguePotionUsagePagesAllRowsAndSaturatesUsed(t *testing.T) {
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	day := dnfpremium.ServiceDayID(now)
	ids := make([]uint32, 0, 260)
	stats := make(map[string]int64)
	for n := uint32(1); n <= 260; n++ {
		id := 100000 + n
		ids = append(ids, id)
		stats["fatigue_potion_item_"+strconv.FormatUint(uint64(id), 10)] = 1
		stats["fatigue_potion_day_"+strconv.FormatUint(uint64(id), 10)] = day
		stats["fatigue_potion_used_"+strconv.FormatUint(uint64(id), 10)] = 300
	}
	catalog, source := fatiguePotionUsageTestDefinitions(ids...)
	character, inventory := fatiguePotionUsageTestPlayer(now, stats, nil)
	bodies, ok := buildCurrentFatiguePotionUsageBodies(character, *inventory, now, catalog, source)
	if !ok || len(bodies) != 2 {
		t.Fatalf("pages=%d ok=%t", len(bodies), ok)
	}
	row := 0
	for page, body := range bodies {
		wantCount := []byte{255, 5}[page]
		if body[0] != wantCount || len(body) != 2+int(wantCount)*5 || body[len(body)-1] != 0 {
			t.Fatalf("page%d count=%d len=%d", page, body[0], len(body))
		}
		for offset := 1; offset < len(body)-1; offset += 5 {
			wantID := uint32(100001 + row)
			if got := binary.LittleEndian.Uint32(body[offset:]); got != wantID || body[offset+4] != 255 {
				t.Fatalf("row%d id=%d used=%d", row, got, body[offset+4])
			}
			row++
		}
	}
	if row != 260 {
		t.Fatalf("lost rows: %d", row)
	}
}
