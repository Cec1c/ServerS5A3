package dnfbridge

import (
	"context"
	"encoding/binary"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"math"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func dailyEquipmentFixture(t *testing.T) (*loginWearAnchorFixture, *fakeCurrentDungeonDeathTimerQueue, uint16, dnfrepo.ItemStack) {
	t.Helper()
	f := newLoginWearAnchorFixture(t, "19", 19, nil)
	path := os.Getenv("DNFBRIDGE_DAILY_EQUIPMENT_PVF")
	if path == "" {
		t.Skip("set DNFBRIDGE_DAILY_EQUIPMENT_PVF to approved real PVF")
	}
	a, e := platformpvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if a.Snapshot().Checksum != "25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348" {
		t.Fatal("PVF identity")
	}
	cat, e := newPVFDungeonDropCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	def, e := cat.ResolveItem(400300075)
	if e != nil {
		t.Fatal(e)
	}
	if def.UsablePeriodDays != 0 || def.EquipmentType != "[amulet]" || !def.ExpirationDate.IsZero() {
		t.Fatalf("definition %+v", def)
	}
	t.Logf("PVF path=%s type=%s period=%d slotrange=%d..%d", def.PVFPath, def.EquipmentType, def.UsablePeriodDays, def.SlotStart, def.SlotEnd)
	t.Logf("PVF attach=%s impossible=%v", def.AttachType, def.ImpossibleContents)
	q := newFakeCurrentDungeonDeathTimerQueue()
	q.now = time.Date(2030, 10, 21, 21, 59, 0, 0, time.UTC)
	f.service.gameplayTimers = q
	f.service.initialEquipmentArchive = a
	f.service.pvfItemCatalog = cat
	f.service.options.currentClientVersion = "official-24.9"
	f.session.characterGeneration = 3
	f.session.selectedItemListBootstrapCharacterID = 19
	if e = f.group.AccountInventory.Save(context.Background(), dnfrepo.AccountInventoryRecord{AccountID: "dnf:10011", Slots: map[string]dnfrepo.ItemStack{}}); e != nil {
		t.Fatal(e)
	}
	slot, _, e := grantCurrentDungeonPickupItem(context.Background(), f.group, "19", def, 1, q.now)
	if e != nil {
		t.Fatal(e)
	}
	inv, _, e := f.group.Inventory.Load(context.Background(), "19")
	if e != nil {
		t.Fatal(e)
	}
	stack := inv.Slots[currentDungeonPickupMainSlotKey(int16(slot))]
	if stack.Extra["expiration_source"] != "runtime_pvf_daily_equipment_grant" || !stack.ExpireAt.Equal(q.now.Add(time.Minute)) {
		t.Fatalf("grant provenance=%+v", stack)
	}
	if binary.LittleEndian.Uint32(stack.RawEntry[currentItemListExpireTimeOffset:]) != uint32(stack.ExpireAt.Unix()) {
		t.Fatal("grant raw expiry")
	}
	if got := binary.LittleEndian.Uint16(stack.RawEntry[0x0B:0x0D]); got != def.Durability {
		t.Fatalf("durability corrupted got=%d want=%d", got, def.Durability)
	}
	t.Logf("GRANT transaction bag=0:%d expiry=%s source=%s; no metadata seeded", slot, stack.ExpireAt, stack.Extra["expiration_source"])
	q.now = stack.ExpireAt.Add(-time.Minute)
	t.Logf("CARGO rule personal=%v account=%v", f.service.currentInventoryCargoInsert(stack, 2), f.service.currentInventoryCargoInsert(stack, 12))
	return f, q, slot, stack
}

func dailyEquipmentMove(t *testing.T, f *loginWearAnchorFixture, slot uint16, list byte, dst int16) {
	t.Helper()
	b := flaggedBody(t, loginWearBridgeBody(0, int16(slot), 400300075, list, dst, 0), op19CurrentUIPlayerFlag)
	handled, e := f.service.handleAlignedGameCommand(f.session, 1, 19, b)
	if e != nil || !handled {
		t.Fatalf("op19 handled=%t err=%v", handled, e)
	}
	if f.session.hasOp19QuietPending() {
		t.Fatal("explicit move queued")
	}
	t.Logf("OP19=%x wire_bytes=%d", b, f.buffer.write.Len())
}

func TestDailyEquipmentRealGrantWearAndExpiry(t *testing.T) {
	f, q, slot, stack := dailyEquipmentFixture(t)
	defer f.service.stopAvatarAppearanceCoalesce(f.session, "daily-equipment-end")
	dailyEquipmentMove(t, f, slot, 3, 19)
	eq, _, e := f.group.Equipment.Load(context.Background(), "19")
	if e != nil {
		t.Fatal(e)
	}
	entry, ok := eq.Entries["19"]
	if !ok || entry.ItemID != 400300075 {
		t.Fatalf("wear failed: %+v", eq)
	}
	for _, k := range []string{"expiration_source", "daily_delete_at", "expire_unix", "expire_time", "pvf_path"} {
		if entry.Extra[k] != stack.Extra[k] {
			t.Fatalf("lost %s: %q -> %q", k, stack.Extra[k], entry.Extra[k])
		}
	}
	if !entry.ExpireAt.Equal(stack.ExpireAt) {
		t.Fatal("expiry changed")
	}
	actor, ok := currentEXEActorEquipmentSlot(entry)
	if !ok || actor != 19 || entry.Extra["current_exe_runtime_move"] != "1" {
		t.Fatalf("actor slot=%d entry=%+v", actor, entry)
	}
	t.Logf("PERSIST EquipmentEntry key=19 slot=%d actor=%d runtime_move=%s", entry.SlotIndex, actor, entry.Extra["current_exe_runtime_move"])
	inv, _, _ := f.group.Inventory.Load(context.Background(), "19")
	if _, ok = inv.Slots[currentDungeonPickupMainSlotKey(int16(slot))]; ok {
		t.Fatal("source duplicated")
	}
	f.service.armCurrentDailyDeleteClock(f.session)
	task := q.active["daily-delete:"+f.session.connID]
	if task == nil || task.delay != time.Minute {
		t.Fatalf("deadline task=%+v", task)
	}
	f.buffer.write.Reset()
	q.now = stack.ExpireAt
	q.fire(task, false)
	eq, _, _ = f.group.Equipment.Load(context.Background(), "19")
	if len(eq.Entries) != 0 {
		t.Fatalf("expired worn persists %+v", eq)
	}
	if f.buffer.write.Len() == 0 || f.session.pendingItemExpiryProjection != nil {
		t.Fatalf("expiry output incomplete bytes=%d pending=%+v", f.buffer.write.Len(), f.session.pendingItemExpiryProjection)
	}
	seen := false
	for rest := f.buffer.write.Bytes(); len(rest) > 0; {
		p, trailing := splitLongHengGameServerUpperPacket(t, rest)
		rest = trailing
		if p.Header.MsgID == 14 && p.Header.Classification == 0 && len(p.Body) == 3+currentItemListEntryWireSize && p.Body[0] == 3 {
			if binary.LittleEndian.Uint16(p.Body[3:5]) != 19 || binary.LittleEndian.Uint32(p.Body[5:9]) != math.MaxUint32 {
				t.Fatal("invalid worn clear")
			}
			seen = true
		}
	}
	if !seen {
		t.Fatal("missing list3 op14 clear")
	}
	f.buffer.write.Reset()
	if _, removed, e := f.service.reconcileCurrentDailyDeleteWithRemoved(context.Background(), f.group, "19", "dnf:10011", f.service.pvfItemCatalog); e != nil || len(removed) != 0 {
		t.Fatalf("replay removed=%v err=%v", removed, e)
	}
	t.Log("GRANT -> explicit op19 -> recorded actor19 -> exact daily callback -> durable removal/list3 clear -> replay empty")
}

func TestDailyEquipmentRealPreservation(t *testing.T) {
	for _, mode := range []string{"before", "missing_source", "gm", "rental", "conflict", "missing_marker", "wrong_owner", "commit_failure", "stale_generation"} {
		t.Run(mode, func(t *testing.T) {
			f, q, slot, stack := dailyEquipmentFixture(t)
			defer f.service.stopAvatarAppearanceCoalesce(f.session, "daily-test-end")
			ctx := context.Background()
			switch mode {
			case "missing_source":
				delete(stack.Extra, "expiration_source")
			case "gm":
				stack.Extra["expiration_source"] = "gm_override"
			case "rental":
				stack.Extra["expiration_source"] = "rental"
			case "conflict":
				stack.Extra["expire_unix"] = "1"
			case "missing_marker":
				delete(stack.Extra, "daily_delete_at")
			}
			inv, _, _ := f.group.Inventory.Load(ctx, "19")
			inv.Slots[currentDungeonPickupMainSlotKey(int16(slot))] = stack
			if e := f.group.Inventory.Save(ctx, inv); e != nil {
				t.Fatal(e)
			}
			before, _, _ := f.group.Inventory.Load(ctx, "19")
			if mode == "stale_generation" {
				f.service.armCurrentDailyDeleteClock(f.session)
				task := q.active["daily-delete:"+f.session.connID]
				f.session.characterGeneration++
				q.now = stack.ExpireAt
				f.buffer.write.Reset()
				q.fire(task, true)
				after, _, _ := f.group.Inventory.Load(ctx, "19")
				if !reflect.DeepEqual(before, after) || f.buffer.write.Len() != 0 {
					t.Fatal("stale timer mutated current owner")
				}
				return
			}
			if mode != "before" {
				q.now = stack.ExpireAt
			}
			account := "dnf:10011"
			if mode == "wrong_owner" {
				account = "other"
			}
			repos := f.group
			if mode == "commit_failure" {
				repos.AccountAssets = rejectRelativeExpiryCommit{repos.AccountAssets}
			}
			_, removed, e := f.service.reconcileCurrentDailyDeleteWithRemoved(ctx, repos, "19", account, f.service.pvfItemCatalog)
			wantErr := mode == "wrong_owner" || mode == "commit_failure"
			if (e != nil) != wantErr {
				t.Fatalf("err=%v wantErr=%t", e, wantErr)
			}
			after, _, _ := f.group.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(before, after) || len(removed) != 0 {
				t.Fatalf("protected/aborted asset mutated mode=%s removed=%v", mode, removed)
			}
		})
	}
}

func TestDailyEquipmentRealCatalogControls(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_DAILY_EQUIPMENT_PVF")
	if path == "" {
		t.Skip("explicit real PVF required")
	}
	a, e := platformpvf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	c, e := newPVFDungeonDropCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []uint32{400300075, 400330115, 100322469} {
		t.Run(strconv.FormatUint(uint64(id), 10), func(t *testing.T) {
			d, e := c.ResolveItem(id)
			if e != nil {
				t.Fatal(e)
			}
			want := id == 400300075
			if d.DailyDeleteItem != want {
				t.Fatalf("real tag ignored/misread daily=%t want=%t", d.DailyDeleteItem, want)
			}
		})
	}
}
