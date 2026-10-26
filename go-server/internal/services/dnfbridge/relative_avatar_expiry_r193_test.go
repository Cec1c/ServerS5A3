package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	relativeAvatarExpiryR193ItemID     uint32 = 108551075
	relativeAvatarExpiryR193RetainedID uint32 = 108551076
)

func relativeAvatarExpiryR193Fixture(t *testing.T, itemID uint32) (*loginWearAnchorFixture, *fakeCurrentDungeonDeathTimerQueue, uint16, dnfrepo.ItemStack, dungeonDropItemDefinition) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_RELATIVE_EXPIRY_PVF")
	if path == "" {
		t.Skip("set DNFBRIDGE_RELATIVE_EXPIRY_PVF to the approved real resource")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !knownRepairPVFChecksum(archive.Snapshot().Checksum) {
		t.Fatal("wrong PVF")
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := catalog.ResolveItem(itemID)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Kind != dungeonDropItemEquipment || definition.EquipmentType != "[hat avatar]" ||
		definition.UsablePeriodDays != 1 || !definition.ExpirationDate.IsZero() {
		t.Fatalf("unexpected real avatar definition: %+v", definition)
	}
	wantUsableExpired := itemID == relativeAvatarExpiryR193RetainedID
	if definition.UsableExpiredItem != wantUsableExpired {
		t.Fatalf("usable expired item=%t want=%t for %d", definition.UsableExpiredItem, wantUsableExpired, itemID)
	}

	issued := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	q := newFakeCurrentDungeonDeathTimerQueue()
	q.now = issued
	f := newLoginWearAnchorFixture(t, "19", 19, nil)
	f.service.gameplayTimers = q
	f.service.initialEquipmentArchive = archive
	f.service.pvfItemCatalog = catalog
	f.service.options.currentClientVersion = "official-24.9"
	f.session.characterGeneration = 3
	f.session.selectedItemListBootstrapCharacterID = 19
	t.Cleanup(func() { f.service.stopAvatarAppearanceCoalesce(f.session, "relative-avatar-r193-test-end") })
	if err := f.group.AccountInventory.Save(context.Background(), dnfrepo.AccountInventoryRecord{AccountID: "dnf:10011", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}

	grantedDefinition, err := currentCeraShopProductDefinitionForGrantAt(definition, currentCeraShopProduct{}, issued)
	if err != nil {
		t.Fatal(err)
	}
	inventory, found, err := f.group.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("inventory found=%t err=%v", found, err)
	}
	slot, err := grantCurrentCeraShopAvatar(&inventory, archive, grantedDefinition, currentCeraShopProduct{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.group.Inventory.Save(context.Background(), inventory); err != nil {
		t.Fatal(err)
	}
	stack, found := inventory.Slots[currentCeraShopInventorySlotKey(currentAvatarInventoryListType, slot)]
	if !found {
		t.Fatalf("real avatar grant did not land in list 1 slot %d: %+v", uint16(slot), inventory.Slots)
	}
	deadline := issued.Add(24 * time.Hour)
	wantUnix := strconv.FormatInt(deadline.Unix(), 10)
	if stack.Extra["expiration_source"] != "runtime_pvf_usable_period_grant" ||
		stack.Extra["usable_period_days"] != "1" ||
		stack.Extra["expire_time"] != wantUnix ||
		stack.Extra["expire_unix"] != wantUnix ||
		!stack.ExpireAt.Equal(deadline) {
		t.Fatalf("unexpected grant provenance/time: %+v expire=%s want=%s", stack.Extra, stack.ExpireAt, deadline)
	}
	return f, q, uint16(slot), stack, definition
}

func wearRelativeAvatarOp19R193(t *testing.T, f *loginWearAnchorFixture, inventorySlot uint16, itemID uint32) {
	t.Helper()
	body := flaggedBody(t, loginWearBridgeBody(currentAvatarInventoryListType, int16(inventorySlot), int32(itemID), 3, 0, 0), op19CurrentUIPlayerFlag)
	handled, err := f.service.handleAlignedGameCommand(f.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), body)
	if err != nil || !handled {
		t.Fatalf("op19 handled=%t err=%v", handled, err)
	}
	if f.session.hasOp19QuietPending() {
		t.Fatal("explicit avatar wear queued a quiet replay")
	}
	equipment, found, err := f.group.Equipment.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("equipment found=%t err=%v", found, err)
	}
	entry, found := equipment.Entries["0"]
	if !found || entry.ItemID != int64(itemID) {
		t.Fatalf("avatar was not worn at slot 0: %+v", equipment)
	}
	actorSlot, valid := currentEXEActorEquipmentSlot(entry)
	if !valid || actorSlot != 0 || entry.Extra["current_exe_runtime_move"] != "1" {
		t.Fatalf("avatar actor identity slot=%d valid=%t extra=%+v", actorSlot, valid, entry.Extra)
	}
	for _, key := range []string{"item_kind", "equipment_type", "expiration_source", "usable_period_days", "expire_time", "expire_unix", "pvf_path"} {
		if entry.Extra[key] == "" {
			t.Fatalf("op19 lost %s: %+v", key, entry.Extra)
		}
	}
	f.buffer.write.Reset()
}

func requireRelativeAvatarProjectionR193(t *testing.T, data []byte) {
	t.Helper()
	packets := splitAllCurrentGameServerUpperPackets(t, data)
	container := -1
	for i, packet := range packets {
		if packet.Header.Classification == 0 && len(packet.Body) > 0 && packet.Body[0] == currentDailyDeleteAvatarRefreshList {
			t.Fatal("avatar refresh marker leaked into a container packet")
		}
		if packet.Header.Classification == 0 && packet.Header.MsgID == 14 && len(packet.Body) >= 3+currentItemListEntryWireSize && packet.Body[0] == dnfrepo.MainInventoryListType {
			t.Fatal("avatar expiry reused a main-inventory clear packet")
		}
		if packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketLeaveParty) &&
			len(packet.Body) > 0 && packet.Body[0] == currentAvatarInventoryListType {
			container = i
			break
		}
	}
	if container < 0 {
		t.Fatalf("missing list-1 avatar container projection in %d packets", len(packets))
	}
	if container+1 >= len(packets) {
		t.Fatal("avatar container was the final packet; actor appearance refresh is missing")
	}
}

func TestRelativeAvatarR193RealGrantWearAndExpiry(t *testing.T) {
	f, q, slot, stack, _ := relativeAvatarExpiryR193Fixture(t, relativeAvatarExpiryR193ItemID)
	wearRelativeAvatarOp19R193(t, f, slot, relativeAvatarExpiryR193ItemID)

	q.now = stack.ExpireAt.Add(-time.Second)
	if _, removed, err := f.service.reconcileCurrentDailyDeleteWithRemoved(context.Background(), f.group, "19", "dnf:10011", f.service.pvfItemCatalog); err != nil || len(removed) != 0 {
		t.Fatalf("pre-deadline cleanup removed=%v err=%v", removed, err)
	}
	equipment, _, _ := f.group.Equipment.Load(context.Background(), "19")
	if equipment.Entries["0"].ItemID != int64(relativeAvatarExpiryR193ItemID) {
		t.Fatal("avatar disappeared before its recorded deadline")
	}

	f.service.armCurrentDailyDeleteClock(f.session)
	task := q.active["daily-delete:"+f.session.connID]
	if task == nil || task.delay != time.Second {
		t.Fatalf("avatar clock task=%+v want=1s", task)
	}
	q.now = stack.ExpireAt
	q.fire(task, false)
	equipment, _, _ = f.group.Equipment.Load(context.Background(), "19")
	if _, exists := equipment.Entries["0"]; exists {
		t.Fatal("expired worn avatar remained persisted")
	}
	if f.session.pendingItemExpiryProjection != nil {
		t.Fatalf("successful avatar projection remained pending: %+v", f.session.pendingItemExpiryProjection)
	}
	requireRelativeAvatarProjectionR193(t, f.buffer.write.Bytes())
	if _, removed, err := f.service.reconcileCurrentDailyDeleteWithRemoved(context.Background(), f.group, "19", "dnf:10011", f.service.pvfItemCatalog); err != nil || len(removed) != 0 {
		t.Fatalf("avatar cleanup replay removed=%v err=%v", removed, err)
	}
}

func TestRelativeAvatarR193UsableExpiredRetained(t *testing.T) {
	f, q, slot, stack, definition := relativeAvatarExpiryR193Fixture(t, relativeAvatarExpiryR193RetainedID)
	if !definition.UsableExpiredItem {
		t.Fatal("108551076 no longer exposes the unproven usable expired item field")
	}
	wearRelativeAvatarOp19R193(t, f, slot, relativeAvatarExpiryR193RetainedID)
	equipment, _, _ := f.group.Equipment.Load(context.Background(), "19")
	entry := equipment.Entries["0"]
	if currentPVFRelativeGrantExpired(dnfrepo.ItemStack{ItemID: entry.ItemID, ExpireAt: entry.ExpireAt, Extra: entry.Extra}, definition, stack.ExpireAt) {
		t.Fatal("usable expired item template was admitted by the relative predicate")
	}

	c := &relativeExpiryFailConn{}
	f.session.conn = c
	q.now = stack.ExpireAt
	f.service.armCurrentDailyDeleteClock(f.session)
	task := q.active["daily-delete:"+f.session.connID]
	if task == nil {
		t.Fatal("avatar expiry clock was not armed")
	}
	q.fire(task, false)
	equipment, _, _ = f.group.Equipment.Load(context.Background(), "19")
	if kept, exists := equipment.Entries["0"]; !exists || kept.ItemID != int64(relativeAvatarExpiryR193RetainedID) {
		t.Fatalf("usable expired avatar was removed: %+v", equipment)
	}
	if f.session.pendingItemExpiryProjection != nil {
		t.Fatalf("retained avatar queued a projection: %+v", f.session.pendingItemExpiryProjection)
	}
	if c.write.Len() != 0 {
		t.Fatal("retained avatar emitted a list-1 or appearance packet")
	}
	if _, removed, err := f.service.reconcileCurrentDailyDeleteWithRemoved(context.Background(), f.group, "19", "dnf:10011", f.service.pvfItemCatalog); err != nil || len(removed) != 0 {
		t.Fatalf("retained avatar reported removal=%v err=%v", removed, err)
	}
}

func TestRelativeAvatarR193FinalCommitRollsBack(t *testing.T) {
	f, q, slot, stack, _ := relativeAvatarExpiryR193Fixture(t, relativeAvatarExpiryR193ItemID)
	wearRelativeAvatarOp19R193(t, f, slot, relativeAvatarExpiryR193ItemID)
	inventory, _, _ := f.group.Inventory.Load(context.Background(), "19")
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	inventory.Slots["1:5"] = stack
	if err := f.group.Inventory.Save(context.Background(), inventory); err != nil {
		t.Fatal(err)
	}
	q.now = stack.ExpireAt
	before, _, _ := f.group.Equipment.Load(context.Background(), "19")
	beforeInventory, _, _ := f.group.Inventory.Load(context.Background(), "19")
	repositories := f.group
	repositories.AccountAssets = rejectRelativeExpiryCommit{repositories.AccountAssets}
	_, removed, err := f.service.reconcileCurrentDailyDeleteWithRemoved(context.Background(), repositories, "19", "dnf:10011", f.service.pvfItemCatalog)
	if err == nil || len(removed) != 0 {
		t.Fatalf("commit failure removed=%v err=%v", removed, err)
	}
	after, _, _ := f.group.Equipment.Load(context.Background(), "19")
	afterInventory, _, _ := f.group.Inventory.Load(context.Background(), "19")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeInventory, afterInventory) ||
		f.buffer.write.Len() != 0 || f.session.pendingItemExpiryProjection != nil {
		t.Fatal("failed avatar transaction changed assets or emitted projection")
	}
}

func TestRelativeAvatarR193ProjectionRetry(t *testing.T) {
	for _, mode := range []string{"write_failure", "actor_not_ready"} {
		t.Run(mode, func(t *testing.T) {
			f, q, slot, stack, _ := relativeAvatarExpiryR193Fixture(t, relativeAvatarExpiryR193ItemID)
			wearRelativeAvatarOp19R193(t, f, slot, relativeAvatarExpiryR193ItemID)
			c := &relativeExpiryFailConn{fail: mode == "write_failure"}
			f.session.conn = c
			if mode == "actor_not_ready" {
				f.session.townSceneReadyCharacterID = 0
			}
			q.now = stack.ExpireAt.Add(-time.Second)
			f.service.armCurrentDailyDeleteClock(f.session)
			task := q.active["daily-delete:"+f.session.connID]
			if task == nil {
				t.Fatal("avatar expiry clock was not armed")
			}
			q.now = stack.ExpireAt
			q.fire(task, false)
			equipment, _, _ := f.group.Equipment.Load(context.Background(), "19")
			if _, exists := equipment.Entries["0"]; exists {
				t.Fatal("avatar asset was not committed before projection failure")
			}
			if f.session.pendingItemExpiryProjection == nil || !f.session.pendingItemExpiryProjection.avatarRedraw {
				t.Fatalf("avatar projection retry intent was lost: %+v", f.session.pendingItemExpiryProjection)
			}

			c.fail = false
			f.session.townSceneReadyCharacterID = 19
			c.write.Reset()
			q.now = q.now.Add(time.Minute)
			retry := q.active["daily-delete:"+f.session.connID]
			if retry == nil {
				t.Fatal("avatar projection retry was not scheduled")
			}
			q.fire(retry, false)
			if f.session.pendingItemExpiryProjection != nil {
				t.Fatalf("avatar projection retry did not complete: %+v", f.session.pendingItemExpiryProjection)
			}
			requireRelativeAvatarProjectionR193(t, c.write.Bytes())
			if _, removed, err := f.service.reconcileCurrentDailyDeleteWithRemoved(context.Background(), f.group, "19", "dnf:10011", f.service.pvfItemCatalog); err != nil || len(removed) != 0 {
				t.Fatalf("retry repeated asset deletion removed=%v err=%v", removed, err)
			}
		})
	}
}

func TestRelativeAvatarR193IdentityGateAndOrdinarySlotControl(t *testing.T) {
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	deadline := now.Add(24 * time.Hour)
	extra := map[string]string{
		"item_kind":                  "avatar",
		"equipment_type":             "[hat avatar]",
		"pvf_path":                   "equipment/character/priest/avatar/cap/108551075.equ",
		"expiration_source":          "runtime_pvf_usable_period_grant",
		"usable_period_days":         "1",
		"expire_time":                strconv.FormatInt(deadline.Unix(), 10),
		"expire_unix":                strconv.FormatInt(deadline.Unix(), 10),
		"current_exe_equipment_type": "0",
		"current_exe_runtime_move":   "1",
	}
	avatarDefinition := dungeonDropItemDefinition{
		ItemID:           relativeAvatarExpiryR193ItemID,
		Kind:             dungeonDropItemEquipment,
		EquipmentType:    "[hat avatar]",
		PVFPath:          "equipment/character/priest/avatar/cap/108551075.equ",
		UsablePeriodDays: 1,
	}
	valid := dnfrepo.EquipmentEntry{SlotIndex: 0, ItemID: int64(relativeAvatarExpiryR193ItemID), ExpireAt: deadline, Extra: extra}
	if slot, ok := currentPVFRelativeAvatarEquipmentExpiry(avatarDefinition, valid, deadline); !ok || slot != 0 {
		t.Fatalf("valid avatar gate slot=%d ok=%t", slot, ok)
	}
	wornEquipmentKind := valid
	wornEquipmentKind.Extra = cloneRelativeAvatarExpiryR193Extra(extra)
	wornEquipmentKind.Extra["item_kind"] = "equipment"
	if slot, ok := currentPVFRelativeAvatarEquipmentExpiry(avatarDefinition, wornEquipmentKind, deadline); !ok || slot != 0 {
		t.Fatalf("worn avatar equipment-kind gate slot=%d ok=%t", slot, ok)
	}
	if currentPVFRelativeAvatarExpirySlotMax != 10 {
		t.Fatalf("avatar expiry slot ceiling=%d want=10", currentPVFRelativeAvatarExpirySlotMax)
	}

	for _, mode := range []string{"wrong_kind", "wrong_type", "wrong_path", "wrong_actor_slot", "aura_skin_slot_11", "future", "usable_expired"} {
		t.Run(mode, func(t *testing.T) {
			entry := valid
			entry.Extra = cloneRelativeAvatarExpiryR193Extra(extra)
			definition := avatarDefinition
			when := deadline
			switch mode {
			case "wrong_kind":
				entry.Extra["item_kind"] = "equipment"
				entry.Extra["equipment_type"] = "[coat]"
			case "wrong_type":
				definition.EquipmentType = "[coat]"
			case "wrong_path":
				entry.Extra["pvf_path"] = "equipment/character/priest/avatar/hair/108551075.equ"
			case "wrong_actor_slot":
				entry.Extra["current_exe_equipment_type"] = "1"
			case "aura_skin_slot_11":
				entry.Extra["equipment_type"] = "[weapon avatar]"
				definition.EquipmentType = "[weapon avatar]"
				definition.PVFPath = "equipment/character/priest/avatar/weapon/108551075.equ"
				entry.Extra["pvf_path"] = definition.PVFPath
				entry.Extra["current_exe_equipment_type"] = "11"
			case "future":
				when = deadline.Add(-time.Second)
			case "usable_expired":
				definition.UsableExpiredItem = true
			}
			if _, ok := currentPVFRelativeAvatarEquipmentExpiry(definition, entry, when); ok {
				t.Fatal("identity, slot or deadline gate admitted an ineligible entry")
			}
		})
	}

	ordinary := valid
	ordinary.SlotIndex = 14
	ordinary.Extra = cloneRelativeAvatarExpiryR193Extra(extra)
	delete(ordinary.Extra, "item_kind")
	ordinary.Extra["equipment_type"] = "[coat]"
	ordinary.Extra["current_exe_equipment_type"] = "14"
	if _, ok := currentPVFRelativeAvatarEquipmentExpiry(dungeonDropItemDefinition{ItemID: 100300063, Kind: dungeonDropItemEquipment, EquipmentType: "[coat]", UsablePeriodDays: 1}, ordinary, deadline); ok {
		t.Fatal("ordinary equipment was admitted through the avatar branch")
	}
}

func TestRelativeAvatarR193WrongPersistentKeyIsAtomic(t *testing.T) {
	f, q, slot, stack, _ := relativeAvatarExpiryR193Fixture(t, relativeAvatarExpiryR193ItemID)
	wearRelativeAvatarOp19R193(t, f, slot, relativeAvatarExpiryR193ItemID)
	equipment, _, _ := f.group.Equipment.Load(context.Background(), "19")
	equipment.Entries["1"] = equipment.Entries["0"]
	delete(equipment.Entries, "0")
	if err := f.group.Equipment.Save(context.Background(), equipment); err != nil {
		t.Fatal(err)
	}
	q.now = stack.ExpireAt
	_, removed, err := f.service.reconcileCurrentDailyDeleteWithRemoved(context.Background(), f.group, "19", "dnf:10011", f.service.pvfItemCatalog)
	if err == nil || len(removed) != 0 {
		t.Fatalf("wrong avatar key removed=%v err=%v", removed, err)
	}
	after, _, _ := f.group.Equipment.Load(context.Background(), "19")
	if kept, exists := after.Entries["1"]; !exists || kept.ItemID != int64(relativeAvatarExpiryR193ItemID) {
		t.Fatalf("wrong-key avatar transaction was not atomic: %+v", after)
	}
}

func cloneRelativeAvatarExpiryR193Extra(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
