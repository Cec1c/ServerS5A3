package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestCurrentAvatarTimeDeadlineBoundary(t *testing.T) {
	now := time.Unix(1791500000, 750000000).UTC()
	for _, tc := range []struct{ expire, want uint32 }{{0, 0}, {1791499999, 0}, {1791500000, 0}, {1791500001, 1}, {1791500123, 123}} {
		if got := currentAvatarRemainingSecondsAt(tc.expire, now); got != tc.want {
			t.Fatalf("expire=%d remaining=%d want=%d", tc.expire, got, tc.want)
		}
	}
}

func TestCurrentAvatarTimeOverridesStaleRawAndQualityWithoutMutation(t *testing.T) {
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], 3)
	binary.LittleEndian.PutUint32(raw[2:6], 40734)
	binary.LittleEndian.PutUint32(raw[6:10], 78750866)
	binary.LittleEndian.PutUint32(raw[0x0e:0x12], 40734)
	binary.LittleEndian.PutUint32(raw[0x38:0x3c], 1700000000)
	extra := map[string]string{"item_kind": "equipment", "equipment_type": "[coat avatar]", "quality_seed": "78750866", "mode1_v74": "999999998", "amount_or_count": "676594001"}
	stack := dnfrepo.ItemStack{ItemID: 40734, Count: 1, RawEntry: raw, Extra: extra}
	worn := dnfrepo.EquipmentEntry{SlotIndex: 3, ItemID: 40734, RawEntry: raw, Extra: extra}
	before := dnfrepo.CloneInventory(dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"1:0": stack}})
	bag := currentItemListEntryFromStack(1, 0, stack)
	equipped, ok := currentItemListEntryFromEquipment(worn)
	if !ok {
		t.Fatal("avatar equipment missing")
	}
	update := currentEquipmentUpdateEntryFromEquipment(worn)
	for name, row := range map[string][]byte{"bag": bag.data[:], "worn": equipped.data[:], "update": update.data[:]} {
		if got := binary.LittleEndian.Uint32(row[6:10]); got != 0 {
			t.Fatalf("%s time=%d", name, got)
		}
		if got := binary.LittleEndian.Uint32(row[0x0e:0x12]); got != 40734 {
			t.Fatalf("%s identity=%d", name, got)
		}
	}
	if binary.LittleEndian.Uint32(bag.data[0x38:0x3c]) != 0 || binary.LittleEndian.Uint32(equipped.data[0x38:0x3c]) != 0 {
		t.Fatal("stale raw deadline survived")
	}
	if currentMode1EquipmentGradeOrInstance(worn) != 0 {
		t.Fatal("scene quality override replaced permanent time")
	}
	if !reflect.DeepEqual(before.Slots["1:0"], stack) {
		t.Fatal("projection changed durable inventory")
	}
	gear := worn
	gear.SlotIndex = 14
	gear.Extra = map[string]string{"item_kind": "equipment", "equipment_type": "[weapon]", "quality_seed": "78750866", "mode1_v74": "123"}
	if currentItemListEquipmentInstance(gear) != 78750866 || currentMode1EquipmentGradeOrInstance(gear) != 123 {
		t.Fatal("ordinary equipment quality changed")
	}
	pet := dnfrepo.ItemStack{ItemID: 100000000, Count: 1, Extra: map[string]string{"creature_serial_or_handle": "19"}}
	if currentItemListStackAmount(7, pet) != 19 {
		t.Fatal("pet serial changed")
	}
	creature := dnfrepo.EquipmentEntry{SlotIndex: 26, ItemID: pet.ItemID, Extra: pet.Extra}
	if currentItemListEquipmentInstance(creature) != 19 || currentMode1EquipmentGradeOrInstance(creature) != 19 {
		t.Fatal("worn pet serial changed")
	}
}

func TestCurrentAvatarTimePVFLatePassAndScene(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst": "", "stackable/stackable.lst": "",
		"equipment/equipment.lst": "40734 `avatar.equ`\n",
		"equipment/avatar.equ":    "[equipment type] `[coat avatar]`\n[expiration date] `2028-08-16 06:00:00`\n[usable period] 3\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{pvfItemCatalog: catalog}
	avatar := dnfrepo.EquipmentEntry{SlotIndex: 3, ItemID: 40734, Extra: map[string]string{"equipment_type": "[coat avatar]", "quality_seed": "78750866"}}
	definition, err := catalog.ResolveItem(40734)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		expire   time.Time
		deadline uint32
	}{
		{"template_absolute", time.Time{}, currentPVFExpirationUnix(definition.ExpirationDate)},
		{"instance_live", now.Add(90 * time.Second), uint32(now.Unix() + 90)},
		{"instance_expired", now.Add(-time.Second), uint32(now.Unix() - 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := avatar
			item.ExpireAt = tc.expire
			entry, _ := currentItemListEntryFromEquipment(item)
			rows := []currentItemListEntry{entry}
			if _, err := svc.applyCurrentPVFUsePeriodsToEntriesWithCatalog(context.Background(), rows, catalog, now); err != nil {
				t.Fatal(err)
			}
			if got := binary.LittleEndian.Uint32(rows[0].data[6:10]); got != currentAvatarRemainingSecondsAt(tc.deadline, now) {
				t.Fatalf("late-pass scalar=%d", got)
			}
			if got := binary.LittleEndian.Uint32(rows[0].data[0x38:0x3c]); got != tc.deadline {
				t.Fatalf("deadline=%d want=%d", got, tc.deadline)
			}
		})
	}
	reader := csharpLegacyUserInfoReader{ctx: context.Background(), characterID: "31", service: svc}
	entry, _ := currentItemListEntryFromEquipment(avatar)
	before := time.Now().UTC()
	scalar := reader.currentMode1EquipmentTimeOrInstance(avatar, &entry)
	after := time.Now().UTC()
	deadline := currentPVFExpirationUnix(definition.ExpirationDate)
	if scalar < currentAvatarRemainingSecondsAt(deadline, after) || scalar > currentAvatarRemainingSecondsAt(deadline, before) {
		t.Fatalf("scene absolute deadline scalar=%d", scalar)
	}
	// Both incremental production send paths need the same template-only
	// deadline even though the repository row has no per-instance timestamp.
	repos := testRepositoryGroup()
	stack := dnfrepo.ItemStack{ItemID: avatar.ItemID, Count: 1, Extra: avatar.Extra}
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "31", Slots: map[string]dnfrepo.ItemStack{"1:0": stack}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{CharacterID: "31", Entries: map[string]dnfrepo.EquipmentEntry{"3": avatar}}); err != nil {
		t.Fatal(err)
	}
	svc.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	svc.options.gameUpperHeader = gameUpperHeaderServer16
	svc.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	conn := &bufferConn{}
	session := &gameSession{selectedCharacterID: 31, conn: conn}
	before = time.Now().UTC()
	if err := svc.sendSelectedIncrementalItemSlotRefreshes(session, "avatar_time_test", []alignedcmd.ItemSlotRefresh{{ListType: 1, SlotIndex: 0}}); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
	if len(rest) != 0 || packet.Body[0] != 1 {
		t.Fatalf("unexpected avatar refresh packet header=%+v body=%x rest=%d", packet.Header, packet.Body, len(rest))
	}
	got := binary.LittleEndian.Uint32(packet.Body[3+6 : 3+10])
	after = time.Now().UTC()
	if got < currentAvatarRemainingSecondsAt(deadline, after) || got > currentAvatarRemainingSecondsAt(deadline, before) {
		t.Fatalf("bag incremental deadline scalar=%d", got)
	}
	before = time.Now().UTC()
	body, _, n, ok := svc.buildCurrentEquipmentItemUpdateBodyForSession(context.Background(), session)
	if !ok || n != 1 {
		t.Fatal("missing worn incremental body")
	}
	got = binary.LittleEndian.Uint32(body[3+6 : 3+10])
	after = time.Now().UTC()
	if got < currentAvatarRemainingSecondsAt(deadline, after) || got > currentAvatarRemainingSecondsAt(deadline, before) {
		t.Fatalf("worn incremental deadline scalar=%d", got)
	}
	// Relative [usable period] never starts again during a read-only login.
	catalog.itemCache[40734] = dungeonDropItemDefinition{ItemID: 40734, Kind: dungeonDropItemEquipment, EquipmentType: "[coat avatar]", UsablePeriodDays: 3}
	entry, _ = currentItemListEntryFromEquipment(avatar)
	if got := reader.currentMode1EquipmentTimeOrInstance(avatar, &entry); got != 0 {
		t.Fatalf("relative deadline was invented: %d", got)
	}
	// Missing catalog keeps the already authoritative instance deadline, and
	// reports the fallback rather than inventing a relative/template deadline.
	reader.service = &Service{}
	avatar.ExpireAt = time.Now().UTC().Add(time.Minute)
	entry, _ = currentItemListEntryFromEquipment(avatar)
	before = time.Now().UTC()
	got = reader.currentMode1EquipmentTimeOrInstance(avatar, &entry)
	after = time.Now().UTC()
	deadline = currentPVFExpirationUnix(avatar.ExpireAt)
	if got < currentAvatarRemainingSecondsAt(deadline, after) || got > currentAvatarRemainingSecondsAt(deadline, before) {
		t.Fatalf("catalog fallback lost instance deadline: %d", got)
	}
}

// SQL641/642's eight owned permanent avatars, reconstructed with their real
// IDs, appearance slots, count, seeds and identity fields. Resolve each actual
// PVF type/path before exercising the repository-backed production builders.
func TestRealPVFAvatarTimeEightOwnedProductionPacketsDoNotMutateInventory(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	repos := testRepositoryGroup()
	ctx := context.Background()
	inv := dnfrepo.InventoryRecord{CharacterID: "31", Slots: map[string]dnfrepo.ItemStack{}}
	equipment := dnfrepo.EquipmentRecord{CharacterID: "31", Entries: map[string]dnfrepo.EquipmentEntry{}}
	for i, actual := range []struct {
		id, seed uint32
		slot     int16
	}{{40734, 78750866, 3}, {41909, 676594001, 5}, {41468, 774444052, 7}, {41133, 6039174, 4}, {40265, 474927217, 6}, {39869, 480417632, 2}, {39524, 454562339, 1}, {39085, 772069834, 0}} {
		definition, err := catalog.ResolveItem(actual.id)
		if err != nil {
			t.Fatal(err)
		}
		if !currentItemListStackExtraIsAvatar(map[string]string{"equipment_type": definition.EquipmentType}) || !definition.ExpirationDate.IsZero() || definition.UsablePeriodDays != 0 {
			t.Fatalf("actual permanent avatar definition=%+v", definition)
		}
		raw := make([]byte, currentItemListEntryWireSize)
		binary.LittleEndian.PutUint16(raw[:2], uint16(actual.slot))
		binary.LittleEndian.PutUint32(raw[2:6], actual.id)
		binary.LittleEndian.PutUint32(raw[6:10], actual.seed)
		binary.LittleEndian.PutUint32(raw[0x0e:0x12], actual.id)
		slot := strconv.Itoa(int(actual.slot))
		extra := map[string]string{"current_exe_equipment_type": slot, "current_exe_inventory_count": "1", "current_exe_runtime_move": "1", "equipment_slot": slot, "equipment_type": definition.EquipmentType, "equipped_slot": slot, "item_kind": "equipment", "pvf_path": definition.PVFPath, "quality_seed": strconv.FormatUint(uint64(actual.seed), 10), "raw_entry_hex": hex.EncodeToString(raw)}
		inv.Slots[fmt.Sprintf("1:%d", i)] = dnfrepo.ItemStack{ItemID: int64(actual.id), Count: 1, RawEntry: raw, Extra: extra}
		equipment.Entries[slot] = dnfrepo.EquipmentEntry{SlotIndex: actual.slot, ItemID: int64(actual.id), RawEntry: raw, Extra: extra}
	}
	if err := repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if err := repos.Equipment.Save(ctx, equipment); err != nil {
		t.Fatal(err)
	}
	beforeInv, _, _ := repos.Inventory.Load(ctx, "31")
	beforeEquipment, _, _ := repos.Equipment.Load(ctx, "31")
	svc := &Service{pvfItemCatalog: catalog, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{selectedCharacterID: 31}
	for _, list := range []byte{1, 3} {
		body, _, n, ok := svc.buildCurrentItemListBodyForSession(ctx, session, list)
		if !ok || n != 8 {
			t.Fatalf("list%d n=%d ok=%t", list, n, ok)
		}
		start, stride := 3, 119
		if list == 1 {
			start, stride = 5, 127
		}
		for i := 0; i < 8; i++ {
			row := body[start+i*stride : start+i*stride+119]
			if binary.LittleEndian.Uint32(row[6:10]) != 0 {
				t.Fatalf("list%d row%d not permanent", list, i)
			}
			if binary.LittleEndian.Uint32(row[0x0e:0x12]) != binary.LittleEndian.Uint32(row[2:6]) {
				t.Fatal("identity changed")
			}
		}
	}
	delta := buildCurrentItemUpdateBody(1, currentItemListEntriesFromMap(inv.Slots, 1))
	for i := 0; i < 8; i++ {
		if binary.LittleEndian.Uint32(delta[3+i*127+6:3+i*127+10]) != 0 {
			t.Fatal("bag update not permanent")
		}
	}
	reader := csharpLegacyUserInfoReader{ctx: ctx, characterID: "31", service: svc}
	rows := reader.currentMode1EquipmentObjectRows()
	if len(rows) != 8 {
		t.Fatalf("scene rows=%d", len(rows))
	}
	for _, row := range rows {
		var w packetWriter
		writeCurrentMode1EquipmentCreateRow(&w, row)
		if binary.LittleEndian.Uint32(w.bytes()[5:9]) != 0 {
			t.Fatal("scene not permanent")
		}
	}
	// A real PVF avatar can also own a genuine per-instance rental deadline.
	for _, expire := range []time.Time{time.Now().UTC().Add(180 * time.Second), time.Now().UTC().Add(-time.Minute)} {
		item := equipment.Entries["3"]
		item.ExpireAt = expire
		row, _ := currentItemListEntryFromEquipment(item)
		before := time.Now().UTC()
		got := reader.currentMode1EquipmentTimeOrInstance(item, &row)
		after := time.Now().UTC()
		deadline := currentPVFExpirationUnix(expire)
		if got < currentAvatarRemainingSecondsAt(deadline, after) || got > currentAvatarRemainingSecondsAt(deadline, before) {
			t.Fatalf("real rental deadline=%d scalar=%d", deadline, got)
		}
	}
	afterInv, _, _ := repos.Inventory.Load(ctx, "31")
	afterEquipment, _, _ := repos.Equipment.Load(ctx, "31")
	if !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeEquipment, afterEquipment) {
		t.Fatal("read-only production projection mutated inventory/equipment")
	}
}
