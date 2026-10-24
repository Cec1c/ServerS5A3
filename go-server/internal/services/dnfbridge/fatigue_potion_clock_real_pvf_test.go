package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	fatigue "longheng.io/server/internal/modules/dnf/fatigue"
	doc "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type publicFatigueResources struct {
	archive *platformpvf.Archive
	catalog *pvfDungeonDropCatalog
}

func publicFatigueLoad(t *testing.T) publicFatigueResources {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the current real PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	return publicFatigueResources{a, c}
}
func (r publicFatigueResources) definition(t *testing.T, id int64) fatigue.PotionDefinition {
	t.Helper()
	item, err := r.catalog.ResolveItem(uint32(id))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.archive.ReadText(item.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	d, err := doc.Parse(item.PVFPath, raw)
	if err != nil {
		t.Fatal(err)
	}
	def, ok := fatiguePotionDefinition(id, item.PVFPath, d)
	if !ok {
		t.Fatal("not fatigue", id)
	}

	return def
}

type publicFatigueFixture struct {
	s        *Service
	p        *gameSession
	repos    repo.Group
	queue    *fakeCurrentDungeonDeathTimerQueue
	resource publicFatigueResources
	def      fatigue.PotionDefinition
	slot     int16
}

func publicFatigueNew(t *testing.T, r publicFatigueResources, id, count int64, now time.Time) *publicFatigueFixture {
	t.Helper()
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	a := repo.AccountRecord{AccountID: "fatigue-local"}
	c := repo.CharacterRecord{CharacterID: "17", AccountID: a.AccountID, Level: 90, Stats: map[string]int64{"fatigue": 0, "fatigue_limit": 156}}
	fatigue.Refresh(&c, a, now)
	if err := repos.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	def := r.definition(t, id)
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw, 87)
	binary.LittleEndian.PutUint32(raw[2:], uint32(id))
	binary.LittleEndian.PutUint32(raw[6:], uint32(count))
	raw[0x20] = 0x6a
	stack := repo.ItemStack{ItemID: id, Count: count, RawEntry: raw, Extra: map[string]string{"item_kind": "stackable", "pvf_path": def.PVFPath, "amount_or_count": strconv.FormatInt(count, 10)}}
	if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "17", Slots: map[string]repo.ItemStack{"0:87": stack}}); err != nil {
		t.Fatal(err)
	}
	q := newFakeCurrentDungeonDeathTimerQueue()
	q.now = now
	f := &publicFatigueFixture{repos: repos, queue: q, resource: r, def: def, slot: 87}
	f.reload()
	return f
}
func (f *publicFatigueFixture) reload() {
	f.s = &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}, initialEquipmentArchive: f.resource.archive, pvfItemCatalog: f.resource.catalog, gameplayTimers: f.queue, repositoryProvider: func() (repo.Group, bool) { return f.repos, true }}
	f.p = &gameSession{conn: &bufferConn{}, connID: "fatigue-local", selectedCharacterID: 17, accountID: "fatigue-local"}
}
func (f *publicFatigueFixture) spendFixtureFatigue(t *testing.T) {
	// Controlled dungeon-spend prerequisite for clock tests; not production data.
	t.Helper()
	ctx := context.Background()
	c, _, err := f.repos.Character.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	fatigue.Refresh(&c, repo.AccountRecord{AccountID: "fatigue-local"}, f.queue.now)
	c.Stats["fatigue"] = 0
	c.Stats["fatigue_used"] = 156
	c.Stats["fatigue_display_update"] = 156
	if err = f.repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
}
func (f *publicFatigueFixture) snapshot(t *testing.T) (repo.CharacterRecord, repo.InventoryRecord) {
	t.Helper()
	c, ok, err := f.repos.Character.Load(context.Background(), "17")
	if err != nil || !ok {
		t.Fatal(err)
	}
	b, ok, err := f.repos.Inventory.Load(context.Background(), "17")
	if err != nil || !ok {
		t.Fatal(err)
	}
	return c, b
}

func (f *publicFatigueFixture) splitSingleItemSlots(t *testing.T) {
	// 10005187 has real PVF stack limit 1. Stock multiple separate instances
	// for clock tests, so re-use after cooldown never depends on an oversized stack.
	t.Helper()
	_, bag := f.snapshot(t)
	source := bag.Slots["0:87"]
	for n := int64(0); n < source.Count; n++ {
		stack := source
		slot := int16(87 + n)
		stack.Count = 1
		stack.RawEntry = append([]byte(nil), source.RawEntry...)
		binary.LittleEndian.PutUint16(stack.RawEntry, uint16(slot))
		binary.LittleEndian.PutUint32(stack.RawEntry[6:], 1)
		stack.Extra = map[string]string{"item_kind": "stackable", "pvf_path": f.def.PVFPath, "amount_or_count": "1"}
		bag.Slots[fmt.Sprintf("0:%d", slot)] = stack
	}
	if err := f.repos.Inventory.Save(context.Background(), bag); err != nil {
		t.Fatal(err)
	}
}

func (f *publicFatigueFixture) use(t *testing.T, wantSuccess bool) []byte {
	t.Helper()
	f.p.conn.(*bufferConn).write.Reset()
	beforeC, beforeB := f.snapshot(t)
	handled, err := f.s.handleAlignedGameCommand(f.p, 1, uint16(dnfenum.CmdPacketUseStackableAction), testQuestClearCouponRequestBody(f.slot, 58))
	if err != nil || !handled {
		t.Fatalf("op515 dispatch handled=%v err=%v", handled, err)
	}
	wire := f.p.conn.(*bufferConn).write.Bytes()
	ack, rest := splitGameServerUpperPacket(t, wire)
	if ack.Header.MsgID != 515 || ack.Header.Classification != 1 {
		t.Fatalf("wrong ACK %+v", ack.Header)
	}
	if !wantSuccess {
		if !bytes.Equal(ack.Body, []byte{0, 1, byte(f.slot), byte(f.slot >> 8), 0, 58}) || len(rest) != 0 {
			t.Errorf("expected rejection only, ACK=%x trailing=%d", ack.Body, len(rest))
		}
		c, b := f.snapshot(t)
		if !reflect.DeepEqual(c, beforeC) || !reflect.DeepEqual(b, beforeB) {
			t.Error("rejection changed assets or usage/cooldown")
		}
		if t.Failed() {
			t.FailNow() // Stop this case before an unexpected debit changes later boundaries.
		}
		return nil
	}
	if !bytes.Equal(ack.Body, []byte{1, byte(f.slot), byte(f.slot >> 8), 0, 58, 0, 0, 0}) {
		t.Fatalf("expected action58 success, ACK=%x", ack.Body)
	}
	update, rest := splitGameServerUpperPacket(t, rest)
	if update.Header.MsgID != 14 || update.Header.Classification != 0 {
		t.Fatal("op14 missing", update.Header)
	}
	points, rest := splitGameServerUpperPacket(t, rest)
	if points.Header.MsgID != currentFatigueMsgID || points.Header.Classification != 0 {
		t.Fatal("fatigue projection/order", points.Header, len(rest))
	}
	if f.def.DailyLimit <= 0 {
		if len(rest) != 0 {
			t.Fatalf("uncapped potion unexpectedly projected usage: %d bytes", len(rest))
		}
		return update.Body
	}
	usage, rest := splitGameServerUpperPacket(t, rest)
	itemID := uint32(f.def.ItemID)
	c, _ := f.snapshot(t)
	wantUsed := byte(c.Stats["fatigue_potion_used_"+f.def.UsageGroup()])
	wantUsage := []byte{1, byte(itemID), byte(itemID >> 8), byte(itemID >> 16), byte(itemID >> 24), wantUsed, 0}
	if usage.Header.MsgID != currentFatiguePotionUsageMsgID || usage.Header.Classification != 0 || len(rest) != 0 || !bytes.Equal(usage.Body, wantUsage) {
		t.Fatalf("fatigue usage projection/order: %+v body=%x trailing=%d", usage.Header, usage.Body, len(rest))
	}
	if len(points.Body) != 10 || int64(binary.LittleEndian.Uint16(points.Body)) != c.Stats["fatigue_used"] || int64(binary.LittleEndian.Uint16(points.Body[2:])) != c.Stats["fatigue_limit"] || int64(binary.LittleEndian.Uint16(points.Body[6:])) != c.Stats["fatigue_display_update"] {
		t.Fatalf("fatigue notification does not match committed snapshot: %x", points.Body)
	}
	return update.Body
}
func TestPublicFatiguePotionContracts(t *testing.T) {
	r := publicFatigueLoad(t)
	for _, expected := range []struct{ id, points, daily, cool int64 }{{10005025, 30, 3, 0}, {416, 50, 3, 1000}, {10005096, 30, 3, 36000000}, {10005097, 50, 3, 36000000}, {10005098, 156, 3, 36000000}, {10005187, 156, 0, 79200000}, {490004968, 156, 1, 1000}} {
		d := r.definition(t, expected.id)
		if d.Points != expected.points || d.DailyLimit != expected.daily || d.CoolTimeMS != expected.cool {
			t.Fatalf("contract %+v", d)
		}
		t.Logf("real PVF %+v", d)
	}

}
func TestPublicFatiguePotionCountProjection(t *testing.T) {
	r := publicFatigueLoad(t)
	for _, id := range []int64{10005025, 416, 10005096, 10005097, 10005098, 10005187, 490004968} {
		for _, count := range []int64{1, 4} {
			t.Run(fmt.Sprintf("%d_count%d", id, count), func(t *testing.T) {
				if (id == 10005187 || id == 490004968) && count > 1 {
					t.Log("oversized stack is a defensive projection fixture; real PVF stack limit is 1")
				}
				f := publicFatigueNew(t, r, id, count, time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC))
				body := f.use(t, true)
				// Main-inventory op14 = list/u16 count/119-byte row; no equipment blobs.
				if len(body) != 3+currentItemListEntryWireSize || body[0] != 0 || binary.LittleEndian.Uint16(body[1:]) != 1 {
					t.Fatalf("bad update %x", body)
				}
				wantID := uint32(id)
				if count == 1 {
					wantID = ^uint32(0)
				}
				assertCurrentItemListTestRow(t, body[3:3+currentItemListEntryWireSize], 87, wantID, uint32(count-1))
				c, b := f.snapshot(t)
				if c.Stats["fatigue"] != f.def.Points || b.Slots["0:87"].Count != count-1 {
					t.Fatalf("durable fatigue/count mismatch: %v/%d", c.Stats, b.Slots["0:87"].Count)
				}
				if count > 1 {
					if b.Slots["0:87"].RawEntry[0x20] != 0x6a {
						t.Fatal("unrelated instance byte lost")
					}
					t.Logf("durable Count=%d raw amount=%d projected amount=%d", b.Slots["0:87"].Count, binary.LittleEndian.Uint32(b.Slots["0:87"].RawEntry[6:]), binary.LittleEndian.Uint32(body[9:]))
				}
				f.reload()
				cold, _, _, ok := f.s.buildCurrentItemListBodyForSession(context.Background(), f.p, 0)
				if !ok {
					t.Fatal("cold login inventory projection unavailable")
				}
				found := false
				for _, row := range decodeCurrentItemListTestRows(t, cold[5:]) {
					if binary.LittleEndian.Uint16(row) == 87 {
						found = true
						assertCurrentItemListTestRow(t, row, 87, uint32(id), uint32(count-1))
					}
				}
				if found != (count > 1) {
					t.Fatalf("cold login slot presence=%v count=%d", found, count)
				}
				c2, b2 := f.snapshot(t)
				if c2.Stats["fatigue"] != c.Stats["fatigue"] || b2.Slots["0:87"].Count != count-1 {
					t.Fatal("cold reload changed balance")
				}
			})
		}
	}
}
func TestPublicFatiguePotionDailyCount(t *testing.T) {
	r := publicFatigueLoad(t)
	for _, id := range []int64{10005025, 416} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			f := publicFatigueNew(t, r, id, 5, time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC))
			for n := int64(1); n <= 3; n++ {
				f.use(t, true)
				c, b := f.snapshot(t)
				if c.Stats["fatigue_potion_used_"+fmt.Sprint(id)] != n || b.Slots["0:87"].Count != 5-n {
					t.Fatal("daily and stack counts confused")
				}
				f.queue.now = f.queue.now.Add(time.Second)
				f.reload()
			}
			f.use(t, false)
			f.queue.now = time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC)
			f.spendFixtureFatigue(t)
			f.reload()
			f.use(t, true)
			c, b := f.snapshot(t)
			if c.Stats["fatigue_potion_used_"+fmt.Sprint(id)] != 1 || b.Slots["0:87"].Count != 1 {
				t.Fatal("daily reset/debit wrong")
			}
		})
	}
}
func TestPublicFatiguePotionClock(t *testing.T) {
	r := publicFatigueLoad(t)
	for _, id := range []int64{10005098, 10005187} {
		t.Run(fmt.Sprintf("%d_cross_six", id), func(t *testing.T) {
			start := time.Date(2026, 10, 21, 21, 59, 59, 0, time.UTC)
			f := publicFatigueNew(t, r, id, 4, start)
			if id == 10005187 {
				f.splitSingleItemSlots(t)
			}
			f.use(t, true)
			if id == 10005187 {
				f.slot = 88
			}
			f.queue.now = start.Add(time.Second)
			f.spendFixtureFatigue(t)
			f.reload()
			f.use(t, false)
			f.queue.now = start.Add(time.Duration(f.def.CoolTimeMS)*time.Millisecond - time.Millisecond)
			f.spendFixtureFatigue(t)
			f.reload()
			f.use(t, false)
			f.queue.now = start.Add(time.Duration(f.def.CoolTimeMS) * time.Millisecond)
			f.spendFixtureFatigue(t)
			f.reload()
			f.use(t, true)
		})
	}
	t.Run("156_no_daily_tag_can_repeat_after_22h_same_service_day", func(t *testing.T) {
		start := time.Date(2026, 10, 20, 22, 0, 0, 0, time.UTC)
		f := publicFatigueNew(t, r, 10005187, 3, start)
		f.splitSingleItemSlots(t)
		f.use(t, true)
		f.slot = 88
		f.queue.now = start.Add(22 * time.Hour)
		f.spendFixtureFatigue(t)
		f.reload()
		f.use(t, true)
		_, bag := f.snapshot(t)
		if _, present := bag.Slots["0:87"]; present {
			t.Fatal("first single-item potion not removed")
		}
		if _, present := bag.Slots["0:88"]; present {
			t.Fatal("second single-item potion not removed")
		}
		if bag.Slots["0:89"].Count != 1 {
			t.Fatal("unused third slot changed")
		}
	})
	t.Run("clock_rewind_rejects", func(t *testing.T) {
		f := publicFatigueNew(t, r, 10005098, 3, time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC))
		f.use(t, true)
		f.queue.now = f.queue.now.Add(-time.Minute)
		f.spendFixtureFatigue(t)
		f.reload()
		f.use(t, false)
	})
}

type publicFatigueFailCharacter struct{ repo.CharacterRepository }

func (f publicFatigueFailCharacter) Save(context.Context, repo.CharacterRecord) error {
	return errors.New("public fatigue injected character save after staged stack debit")
}

type publicFatigueFailAssets struct{ repo.CharacterAssetUnitOfWork }

func (f publicFatigueFailAssets) WithinCharacterAssets(ctx context.Context, id string, apply func(repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return f.CharacterAssetUnitOfWork.WithinCharacterAssets(ctx, id, func(c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		return apply(publicFatigueFailCharacter{c}, i, e)
	})
}
func TestPublicFatiguePotionGuards(t *testing.T) {
	r := publicFatigueLoad(t)
	for _, name := range []string{"foreign_account", "full_fatigue", "dungeon", "transaction_failure"} {
		t.Run(name, func(t *testing.T) {
			f := publicFatigueNew(t, r, 10005025, 4, time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC))
			switch name {
			case "foreign_account":
				c, _, _ := f.repos.Character.Load(context.Background(), "17")
				c.AccountID = "other"
				if err := f.repos.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
			case "full_fatigue":
				c, _, _ := f.repos.Character.Load(context.Background(), "17")
				c.Stats["fatigue"] = 156
				if err := f.repos.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
			case "dungeon":
				f.p.dungeon.runtime = &runtimeDungeonState{}
			case "transaction_failure":
				f.repos.CharacterAssets = publicFatigueFailAssets{f.repos.CharacterAssets}
			}
			f.use(t, false)
		})
	}
}
